-- Tick-driven action jobs for the AI player.
--
-- The engine exposes continuous control states rather than one-shot actions for
-- shooting (LuaControl.shooting_state), so a single RCON call cannot fire a
-- burst. This module keeps the state set for a bounded number of ticks and
-- clears it afterwards; GraphQL mutations start a job and poll its status.
--
-- Rendering and AI navigation live in control.lua; this module only owns the
-- action jobs (currently shooting).

local MAX_TICKS = 3600
local RETAINED_JOBS = 256

local function initialize()
  storage.action_jobs = storage.action_jobs or {}
  storage.action_active = storage.action_active or {}
  storage.next_action_id = storage.next_action_id or 1
end

local function finite(value)
  return type(value) == "number" and value == value and value ~= math.huge and value ~= -math.huge
end

local function clear_shooting(character, position)
  if character and character.valid and position then
    pcall(function()
      character.shooting_state = {state = defines.shooting.not_shooting, position = position}
    end)
  end
end

local function snapshot(job)
  return {
    id = job.id,
    kind = job.kind,
    character = tostring(job.unit),
    status = job.status,
    reason = job.reason,
    startedTick = job.startedTick,
    updatedTick = job.updatedTick,
    ticks = job.ticks,
    remainingTicks = job.status == "SHOOTING" and math.max(0, job.endTick - game.tick) or 0,
    position = {x = job.position.x, y = job.position.y}
  }
end

local function finish(job, status, reason)
  local character = game.get_entity_by_unit_number(job.unit)
  clear_shooting(character, job.position)
  storage.action_active[job.unit] = nil
  job.status = status
  job.reason = reason
  job.updatedTick = game.tick
end

-- Finished jobs stay pollable, but only the newest few hundred are kept so
-- storage does not grow with every shooting request.
local function prune_jobs()
  local cutoff = storage.next_action_id - RETAINED_JOBS
  for id, job in pairs(storage.action_jobs) do
    if storage.action_active[job.unit] ~= id and tonumber(id) < cutoff then
      storage.action_jobs[id] = nil
    end
  end
end

local function start_shoot(unit, x, y, ticks)
  -- Storage keys are created lazily so a save from before the action jobs
  -- existed keeps working without waiting for on_configuration_changed.
  initialize()
  if not finite(unit) or unit < 1 or unit > 4294967295 or unit % 1 ~= 0 then
    return {error = "character must be a positive uint32"}
  end
  if not finite(x) or not finite(y) then
    return {error = "target must contain finite map coordinates"}
  end
  if not finite(ticks) or ticks < 1 or ticks > MAX_TICKS or ticks % 1 ~= 0 then
    return {error = "ticks must be an integer from 1 to " .. MAX_TICKS}
  end
  local character = game.get_entity_by_unit_number(unit)
  if not character or not character.valid or character.type ~= "character" then
    return {error = "character not found"}
  end
  local player = character.player
  if not player or not player.connected or player.character ~= character then
    return {error = "shooting requires a connected player controlling this character"}
  end
  if storage.action_active[unit] then
    return {error = "character already has an active action job"}
  end
  local id = tostring(storage.next_action_id)
  storage.next_action_id = storage.next_action_id + 1
  prune_jobs()
  local job = {
    id = id, kind = "shoot", unit = unit, position = {x = x, y = y},
    startedTick = game.tick, updatedTick = game.tick, ticks = ticks,
    endTick = game.tick + ticks, status = "SHOOTING",
    player_index = player.index
  }
  storage.action_jobs[id] = job
  storage.action_active[unit] = id
  local ok, error = pcall(function()
    character.shooting_state = {state = defines.shooting.shooting_selected, position = job.position}
  end)
  if not ok then
    storage.action_jobs[id] = nil
    storage.action_active[unit] = nil
    return {error = tostring(error)}
  end
  return snapshot(job)
end

local function on_tick(event)
  if not storage.action_active then
    return
  end
  for unit, id in pairs(storage.action_active) do
    local job = storage.action_jobs[id]
    if not job then
      storage.action_active[unit] = nil
    else
      local character = game.get_entity_by_unit_number(unit)
      if not character or not character.valid or character.type ~= "character" then
        finish(job, "MISSING", "character no longer exists")
      else
        local player = character.player
        if not player or not player.connected or player.character ~= character or player.index ~= job.player_index then
          finish(job, "CANCELLED", "controlling player disconnected or changed character")
        elseif event.tick >= job.endTick then
          finish(job, "COMPLETE", nil)
        else
          local ok, error = pcall(function()
            character.shooting_state = {state = defines.shooting.shooting_selected, position = job.position}
          end)
          if not ok then
            finish(job, "FAILED", tostring(error))
          else
            job.updatedTick = event.tick
          end
        end
      end
    end
  end
end

remote.add_interface("factorio_ai_actions", {
  start_shoot = start_shoot,
  status = function(id)
    initialize()
    local job = type(id) == "string" and storage.action_jobs[id] or nil
    if not job then
      return {error = "action job not found"}
    end
    return snapshot(job)
  end,
  cancel = function(id)
    initialize()
    local job = type(id) == "string" and storage.action_jobs[id] or nil
    if not job then
      return {error = "action job not found"}
    end
    if storage.action_active[job.unit] == job.id then
      finish(job, "CANCELLED", "cancelled by request")
    end
    return snapshot(job)
  end
})

return {initialize = initialize, on_tick = on_tick}
