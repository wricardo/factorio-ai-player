local belt_flow = require("belt_flow")
local actions = require("actions")

-- Jobs and outstanding path requests live in storage so saves retain navigation.
local MAX_REPLANS = 3
local STALL_TICKS = 120
local RETRY_DELAY = 30
local WAYPOINT_RADIUS = 0.2

local function initialize()
  storage.jobs = storage.jobs or {}
  storage.active = storage.active or {}
  storage.requests = storage.requests or {}
  storage.next_id = storage.next_id or 1
  belt_flow.initialize()
  actions.initialize()
end

script.on_init(initialize)
script.on_configuration_changed(initialize)

local function copy_position(position)
  return {x = position.x, y = position.y}
end

local function distance_squared(a, b)
  local x, y = a.x - b.x, a.y - b.y
  return x * x + y * y
end

-- Only characters, vehicles, trains, and a few other prototypes resolve through
-- get_entity_by_unit_number; most buildings need a surface scan.
local function entity_by_unit(unit, surface, filter)
  local entity = game.get_entity_by_unit_number(unit)
  if entity and entity.valid then
    return entity
  end
  if surface then
    for _, candidate in pairs(surface.find_entities_filtered(filter or {})) do
      if candidate.valid and candidate.unit_number == unit then
        return candidate
      end
    end
  end
end

-- Buildings do not move, so rescanning for a missing static target only needs
-- entities of its prototype near its recorded box instead of the whole surface.
local SCAN_PADDING = 2
local function target_scan_filter(job)
  local box = job.target_box
  if not job.target_name or not box then
    return nil
  end
  return {
    name = job.target_name,
    area = {
      left_top = {x = box.left_top.x - SCAN_PADDING, y = box.left_top.y - SCAN_PADDING},
      right_bottom = {x = box.right_bottom.x + SCAN_PADDING, y = box.right_bottom.y + SCAN_PADDING}
    }
  }
end

-- Finished jobs stay queryable for polling clients, but only the newest few
-- hundred are kept so storage does not grow with every navigation request.
local RETAINED_JOBS = 256
local function prune_jobs()
  local cutoff = storage.next_id - RETAINED_JOBS
  for id, job in pairs(storage.jobs) do
    if storage.active[job.unit] ~= id and tonumber(id) < cutoff then
      storage.jobs[id] = nil
    end
  end
end

-- A target entity's goal sits on its bounding box inflated by the tolerance, so
-- the pathfinder can reach a tile beside it instead of inside its collision box.
local function target_gap(position, box)
  local dx = math.max(box.left_top.x - position.x, position.x - box.right_bottom.x, 0)
  local dy = math.max(box.left_top.y - position.y, position.y - box.right_bottom.y, 0)
  return math.sqrt(dx * dx + dy * dy)
end

local function target_goal(position, box, tolerance)
  local left, top = box.left_top.x - tolerance, box.left_top.y - tolerance
  local right, bottom = box.right_bottom.x + tolerance, box.right_bottom.y + tolerance
  return {x = math.min(math.max(position.x, left), right), y = math.min(math.max(position.y, top), bottom)}
end

local function arrived(job, character)
  if job.target_box then
    return target_gap(character.position, job.target_box) <= job.tolerance
  end
  return distance_squared(character.position, job.destination) <= job.tolerance * job.tolerance
end

local function character_for(job)
  local character = game.get_entity_by_unit_number(job.unit)
  if character and character.valid and character.type == "character" then
    return character
  end
end

local function controller_loss_reason(job, character)
  if character.surface.index ~= job.surface_index then
    return "character moved to another surface"
  end
  local player = character.player
  if job.player_index then
    local owner = game.get_player(job.player_index)
    if not owner or not owner.connected or owner.character ~= character or not player or player.index ~= job.player_index then
      return "controlling player disconnected or changed character"
    end
  elseif player then
    return "detached character became player-controlled"
  end
end

local function target_loss_reason(job, character)
  if not job.target_unit then
    return
  end
  local target = game.get_entity_by_unit_number(job.target_unit)
  if not target or not target.valid then
    if not job.target_entity then
      return "target character no longer exists"
    end
    if job.target_scan_tick and game.tick < job.target_scan_tick then
      return
    end
    -- Scanning a surface costs more than a direct lookup, so re-check a static
    -- entity at most once a second while the job is otherwise healthy.
    job.target_scan_tick = game.tick + 60
    target = entity_by_unit(job.target_unit, character.surface, target_scan_filter(job))
    if not target then
      return "target entity no longer exists"
    end
  end
  if target.surface ~= character.surface then
    return job.target_entity and "target entity moved to another surface" or "target character moved to another surface"
  end
end

local function stop(character)
  if character and character.valid then
    character.walking_state = {walking = false, direction = defines.direction.north}
  end
end

local function snapshot(job)
  return {
    id = job.id, character = tostring(job.unit), surface = job.surface,
    targetCharacter = job.target_unit and not job.target_entity and tostring(job.target_unit) or nil,
    targetEntity = job.target_entity and job.target_unit and tostring(job.target_unit) or nil,
    destination = copy_position(job.destination),
    position = job.position and copy_position(job.position) or nil,
    status = job.status, reason = job.reason,
    startedTick = job.startedTick, updatedTick = job.updatedTick
  }
end

local function finish(job, status, reason, character)
  if character and character.valid then
    job.position = copy_position(character.position)
    stop(character)
  end
  if job.request then
    storage.requests[job.request] = nil
    job.request = nil
  end
  storage.active[job.unit] = nil
  job.path = nil
  job.retry_tick = nil
  job.status = status
  job.reason = reason
  job.updatedTick = game.tick
end

local CHASE_GOAL_RINGS = 3
local CHASE_GOAL_STEP = 0.75
local CHASE_GOAL_DIRECTIONS = 8

-- A chase goal sits on the target, which the pathfinder cannot reach while the
-- target occupies it, so probe a ring of points one tolerance out. A ring point
-- can land inside a neighbouring entity's collision box, so candidates are
-- ordered by distance from the walker and tried in turn.
local function chase_goals(job, character)
  local origin = job.destination
  local x, y = character.position.x, character.position.y
  local candidates = {}
  for ring = 0, CHASE_GOAL_RINGS - 1 do
    local radius = job.tolerance + ring * CHASE_GOAL_STEP
    for step = 0, CHASE_GOAL_DIRECTIONS - 1 do
      local angle = step * math.pi / 4
      local goal = {x = origin.x + math.cos(angle) * radius, y = origin.y + math.sin(angle) * radius}
      candidates[#candidates + 1] = {
        goal = goal,
        ring = ring,
        order = #candidates + 1,
        distance = distance_squared(goal, {x = x, y = y})
      }
    end
  end
  table.sort(candidates, function(a, b)
    if a.ring ~= b.ring then
      return a.ring < b.ring
    end
    if a.distance ~= b.distance then
      return a.distance < b.distance
    end
    return a.order < b.order
  end)
  local goals = {}
  for index, candidate in ipairs(candidates) do
    goals[index] = candidate.goal
  end
  return goals
end

local function path_goal(job, character)
  if job.target_box then
    return target_goal(character.position, job.target_box, job.tolerance)
  end
  if job.target_unit then
    local goals = job.goal_candidates
    local index = job.goal_index or 1
    if goals and goals[index] then
      return goals[index]
    end
  end
  return job.destination
end

local function request_path(job, character, keep_goals)
  stop(character)
  job.status = "PATHFINDING"
  job.updatedTick = game.tick
  job.path = nil
  job.path_goal = copy_position(job.destination)
  job.retry_tick = nil
  if job.target_unit and not job.target_entity and not keep_goals then
    job.goal_candidates = chase_goals(job, character)
    job.goal_index = 1
  end
  local id = character.surface.request_path{
    bounding_box = character.prototype.collision_box,
    collision_mask = character.prototype.collision_mask,
    start = character.position,
    goal = path_goal(job, character),
    force = character.force,
    radius = math.max(0, job.tolerance - WAYPOINT_RADIUS),
    pathfind_flags = {cache = false},
    can_open_gates = true,
    entity_to_ignore = character
  }
  job.request = id
  storage.requests[id] = job.id
end

-- Try the next chase candidate goal before declaring the chase unreachable.
local function advance_goal(job, character)
  local goals = job.goal_candidates
  local index = job.goal_index or 1
  if goals and index < #goals then
    job.goal_index = index + 1
    request_path(job, character, true)
    return true
  end
  return false
end

local function needs_destroy(path)
  for _, waypoint in ipairs(path) do
    if waypoint.needs_destroy_to_reach then
      return true
    end
  end
  return false
end

local function replan(job, character, exhausted_status, reason)
  if job.replans >= MAX_REPLANS then
    finish(job, exhausted_status, reason, character)
    return
  end
  job.replans = job.replans + 1
  request_path(job, character)
end

local function finite(value)
  return type(value) == "number" and value == value and value ~= math.huge and value ~= -math.huge
end

remote.add_interface("factorio_ai_navigation", {
  start = function(character_number, x, y, tolerance, timeout_ticks, target_character, target_entity)
    if not finite(character_number) or character_number < 1 or character_number > 4294967295 or character_number % 1 ~= 0 then
      return {error = "character_number must be a positive uint32"}
    end
    if not finite(tolerance) or tolerance <= 0 or tolerance > 1000 then
      return {error = "tolerance must be positive and at most 1000"}
    end
    if not finite(timeout_ticks) or timeout_ticks < 1 or timeout_ticks > 4294967295 or timeout_ticks % 1 ~= 0 then
      return {error = "timeout_ticks must be a positive uint32"}
    end
    local character = game.get_entity_by_unit_number(character_number)
    if not character or not character.valid or character.type ~= "character" then
      return {error = "character not found"}
    end
    local player = character.player
    if player and (not player.connected or player.character ~= character) then
      return {error = "associated player must be connected and controlling this character"}
    end
    local destination, target_box, target_name
    if target_character ~= nil then
      if x ~= nil or y ~= nil or target_entity ~= nil then return {error = "provide either destination coordinates, target_character, or target_entity"} end
      if not finite(target_character) or target_character < 1 or target_character > 4294967295 or target_character % 1 ~= 0 then
        return {error = "target_character must be a positive uint32"}
      end
      if target_character == character_number then
        return {error = "target_character must differ from character_number"}
      end
      if tolerance < 1 then
        return {error = "tolerance must be at least 1 when chasing a character"}
      end
      local target = game.get_entity_by_unit_number(target_character)
      if not target or not target.valid or target.type ~= "character" then
        return {error = "target character not found"}
      end
      if target.surface ~= character.surface then
        return {error = "target character is on another surface"}
      end
      destination = copy_position(target.position)
    elseif target_entity ~= nil then
      if x ~= nil or y ~= nil then return {error = "provide either destination coordinates, target_character, or target_entity"} end
      if not finite(target_entity) or target_entity < 1 or target_entity > 4294967295 or target_entity % 1 ~= 0 then
        return {error = "target_entity must be a positive uint32"}
      end
      if target_entity == character_number then
        return {error = "target_entity must differ from character_number"}
      end
      local target = entity_by_unit(target_entity, character.surface)
      if not target then return {error = "target entity not found"} end
      if target.type == "character" then return {error = "use target_character to chase a character"} end
      if target.surface ~= character.surface then return {error = "target entity is on another surface"} end
      target_box = target.bounding_box and {left_top = target.bounding_box.left_top, right_bottom = target.bounding_box.right_bottom} or nil
      destination = copy_position(target.position)
      target_name = target.name
    else
      if not finite(x) or not finite(y) or math.abs(x) > 1000000 or math.abs(y) > 1000000 then
        return {error = "destination must contain finite map coordinates"}
      end
      destination = {x = x, y = y}
    end
    if storage.active[character_number] then return {error = "character already has an active navigation job"} end
    local id = tostring(storage.next_id)
    storage.next_id = storage.next_id + 1
    prune_jobs()
    local job = {
      id = id, unit = character_number, surface = character.surface.name,
      surface_index = character.surface.index, destination = destination,
      target_unit = target_character or target_entity,
      target_entity = target_entity and true or nil,
      target_box = target_box,
      position = copy_position(character.position), tolerance = tolerance,
      player_index = player and player.index or nil,
      timeout = timeout_ticks, replans = 0, status = "PATHFINDING",
      startedTick = game.tick, updatedTick = game.tick,
      target_name = target_name
    }
    storage.jobs[id] = job
    storage.active[character_number] = id
    if arrived(job, character) then
      finish(job, "ARRIVED", nil, character)
    else
      local ok, request_error = pcall(request_path, job, character)
      if not ok then
        finish(job, "NO_PATH", tostring(request_error), character)
        return {error = tostring(request_error)}
      end
    end
    return snapshot(job)
  end,
  status = function(id)
    local job = type(id) == "string" and storage.jobs[id] or nil
    if not job then return {error = "navigation job not found"} end
    return snapshot(job)
  end,
  cancel = function(id)
    local job = type(id) == "string" and storage.jobs[id] or nil
    if not job then return {error = "navigation job not found"} end
    if storage.active[job.unit] == job.id then
      finish(job, "CANCELLED", "cancelled by request", character_for(job))
    end
    return snapshot(job)
  end
})

script.on_event(defines.events.on_script_path_request_finished, function(event)
  local id = storage.requests[event.id]
  if not id then return end
  storage.requests[event.id] = nil
  local job = storage.jobs[id]
  if not job or job.request ~= event.id then return end
  job.request = nil
  local character = character_for(job)
  if not character then
    finish(job, "CHARACTER_MISSING", "character no longer exists")
  elseif controller_loss_reason(job, character) then
    finish(job, "CANCELLED", controller_loss_reason(job, character), character)
  elseif game.tick - job.startedTick >= job.timeout then
    finish(job, "TIMED_OUT", "navigation timed out", character)
  elseif arrived(job, character) then
    finish(job, "ARRIVED", nil, character)
  elseif event.try_again_later then
    if job.replans >= MAX_REPLANS then
      finish(job, "NO_PATH", "pathfinder remained busy", character)
    else
      job.replans = job.replans + 1
      job.retry_tick = game.tick + RETRY_DELAY
      job.updatedTick = game.tick
    end
  elseif not event.path or #event.path == 0 then
    if not advance_goal(job, character) then
      finish(job, "NO_PATH", "pathfinder found no path", character)
    end
  elseif needs_destroy(event.path) then
    if not advance_goal(job, character) then
      finish(job, "NO_PATH", "path requires destroying an obstacle", character)
    end
  else
    job.path = event.path
    job.waypoint = 1
    job.status = "MOVING"
    job.updatedTick = game.tick
    job.progress_position = copy_position(character.position)
    job.progress_tick = game.tick
  end
end)

script.on_event(defines.events.on_tick, function(event)
  belt_flow.on_tick(event)
  actions.on_tick(event)
  for unit, id in pairs(storage.active) do
    local job = storage.jobs[id]
    local character = character_for(job)
    local controller_reason = character and controller_loss_reason(job, character)
    -- Evaluated once: a static-target scan is throttled, so a second call in the
    -- same tick would return nil and drop the reason.
    local target_reason = character and not controller_reason and target_loss_reason(job, character)
    if not character then
      finish(job, "CHARACTER_MISSING", "character no longer exists")
    elseif controller_reason then
      finish(job, "CANCELLED", controller_reason, character)
    elseif target_reason then
      finish(job, "TARGET_MISSING", target_reason, character)
    else
      if job.target_unit and not job.target_entity then
        job.destination = copy_position(game.get_entity_by_unit_number(job.target_unit).position)
      end
      local position = character.position
      if distance_squared(position, job.position) > 0 then
        job.position = copy_position(position)
        job.updatedTick = event.tick
      end
      if event.tick - job.startedTick >= job.timeout then
        finish(job, "TIMED_OUT", "navigation timed out", character)
      elseif arrived(job, character) then
        finish(job, "ARRIVED", nil, character)
      elseif job.retry_tick and event.tick >= job.retry_tick then
        request_path(job, character, job.goal_candidates ~= nil)
      elseif job.status == "MOVING" then
        if job.target_unit and not job.target_entity and distance_squared(job.destination, job.path_goal) > 9 then
          request_path(job, character)
        else
          while job.waypoint <= #job.path and distance_squared(position, job.path[job.waypoint].position) <= WAYPOINT_RADIUS * WAYPOINT_RADIUS do
            job.waypoint = job.waypoint + 1
          end
          local target = job.waypoint <= #job.path and job.path[job.waypoint].position or job.destination
          if event.tick - job.progress_tick >= STALL_TICKS then
            if distance_squared(position, job.progress_position) < 0.01 then
              replan(job, character, "STUCK", "character could not make progress")
            else
              job.progress_position = copy_position(position)
              job.progress_tick = event.tick
            end
          end
          if job.status == "MOVING" then
            local angle = math.atan2(target.x - position.x, position.y - target.y)
            local direction = (math.floor(angle / (math.pi / 4) + 0.5) % 8) * 2
            character.walking_state = {walking = true, direction = direction}
          end
        end
      end
    end
  end
end)
