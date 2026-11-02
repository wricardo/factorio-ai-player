-- Exercise controller state transitions with a deterministic character model.
local atan, atan2 = math.atan, math.atan2 or math.atan
math.atan = function(x) return atan(x) end -- Factorio's Lua atan ignores a second argument.
math.atan2 = function(y, x) return atan2(y, x) end
storage = {}
defines = {direction = {north = 0}, events = {on_tick = 1, on_script_path_request_finished = 2}}
local handlers, api = {}, nil
script = {
  on_init = function(f) handlers.init = f end,
  on_configuration_changed = function() end,
  on_event = function(event, f) handlers[event] = f end
}
remote = {add_interface = function(_, functions) api = functions end}
local request_id = 0
local requested_goals = {}
local requested_paths = {}
local surface = {name = 'nauvis', index = 1}
surface.request_path = function(request)
  request_id = request_id + 1
  requested_goals[request_id] = request.goal
  requested_paths[request_id] = request
  return request_id
end
-- 2.0.77 prototypes expose their mask as CollisionMask ({layers = {...}}).
local character_mask = {layers = {player = true, object = true, item = true}}
local character = {
  valid = true, type = 'character', unit_number = 1, surface = surface,
  force = {}, prototype = {collision_box = {}, collision_mask = character_mask},
  position = {x = 0, y = 0}
}
local other = {
  valid = true, type = 'character', unit_number = 2, surface = surface,
  force = {}, prototype = {collision_box = {}, collision_mask = {}},
  position = {x = 5, y = 0}
}
local characters = {[1] = character, [2] = other}
local test_entities = {character, other}
local blockers = {}
local snap_response = nil
local scan_filters = {}
local function within(position, center, radius)
  local dx, dy = position.x - center.x, position.y - center.y
  return dx * dx + dy * dy <= radius * radius
end
local function intersects(entity, area)
  local box = entity.bounding_box or {left_top = entity.position, right_bottom = entity.position}
  return box.right_bottom.x >= area.left_top.x and box.left_top.x <= area.right_bottom.x
    and box.right_bottom.y >= area.left_top.y and box.left_top.y <= area.right_bottom.y
end
-- Static lookup: named references search one tile-sized window, and the
-- collision-mask probe answers only from the explicit blocker list by box
-- overlap, mirroring the engine's area semantics.
surface.find_entities_filtered = function(filter)
  scan_filters[#scan_filters + 1] = filter
  local matches = {}
  local candidates
  if filter.name then
    candidates = test_entities
  elseif filter.collision_mask then
    candidates = blockers
  else
    candidates = {}
  end
  for _, entity in ipairs(candidates) do
    if entity.valid and (not filter.name or entity.name == filter.name) then
      local keep = true
      if filter.position then
        keep = within(entity.position, filter.position, filter.radius or 0.01)
      end
      if keep and filter.area then
        keep = intersects(entity, filter.area)
      end
      if keep then matches[#matches + 1] = entity end
    end
  end
  return matches
end
surface.find_non_colliding_position = function()
  return snap_response
end
local test_player
game = {
  tick = 0,
  get_entity_by_unit_number = function(id) return characters[id] end,
  get_player = function(index) if test_player and test_player.index == index then return test_player end end
}
package.path = 'mods/factorio_ai_navigation/?.lua;' .. package.path
dofile('mods/factorio_ai_navigation/control.lua')
handlers.init()
local frozen = false
local function tick()
  game.tick = game.tick + 1
  handlers[defines.events.on_tick]({tick = game.tick})
  local state = character.walking_state
  if state and state.walking and not frozen then
    local angle = state.direction * math.pi / 8
    character.position.x = character.position.x + math.sin(angle) * 0.1
    character.position.y = character.position.y - math.cos(angle) * 0.1
  end
end
for _, destination in ipairs({{x=3,y=0},{x=0,y=3},{x=-3,y=0},{x=0,y=-3}}) do
  character.position = {x=0,y=0}
  local job = api.start(1, destination.x, destination.y, 0.2, 600)
  assert(job.status == 'PATHFINDING')
  handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position=destination}}})
  for _ = 1, 100 do
    tick()
    if api.status(job.id).status ~= 'MOVING' then break end
  end
  local result = api.status(job.id)
  assert(result.status == 'ARRIVED', 'failed to reach destination: '..result.status)
  assert(not character.walking_state.walking, 'character must stop after arrival')
end

-- A running job reports its tolerance, budget, replans and remaining distance.
character.position = {x=0,y=0}
local progress_job = api.start(1, 3, 0, 0.2, 600)
local fresh = api.status(progress_job.id)
assert(fresh.tolerance == 0.2 and fresh.timeoutTicks == 600 and fresh.replans == 0,
  'the snapshot must expose tolerance, timeoutTicks and replans')
assert(fresh.waypointsRemaining == 0, 'pathfinding jobs hold no waypoints')
assert(math.abs(fresh.distanceRemaining - 3) < 1e-6, 'distanceRemaining must measure the remaining walk')
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=1,y=0}},{position={x=3,y=0}}}})
local moving = api.status(progress_job.id)
assert(moving.status == 'MOVING', 'delivered path should move the character')
assert(moving.waypointsRemaining == 2, 'waypointsRemaining must count the pending waypoints, got '..tostring(moving.waypointsRemaining))
assert(math.abs(moving.distanceRemaining - 3) < 1e-6, 'distanceRemaining must measure the remaining walk')
api.cancel(progress_job.id)

-- A lost start response is recoverable: the error names the job and active()
-- returns it.
character.position = {x=0,y=0}
local cancelled = api.start(1, 3, 0, 0.2, 600)
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=3,y=0}}}})
tick()
local busy = api.start(1, 4, 0, 0.2, 600)
assert(busy.error, 'a second concurrent job must be rejected')
assert(busy.error:find(cancelled.id, 1, true), 'the busy error must name the active job id, got '..tostring(busy.error))
assert(api.active(1).id == cancelled.id, 'active must return the running job')
assert(api.active(2).id == nil, 'active must be empty for an idle character')
assert(api.cancel(cancelled.id).status == 'CANCELLED')
assert(api.active(1).id == nil, 'active must be empty once the job is cancelled')
assert(not character.walking_state.walking)
local timed = api.start(1, 3, 0, 0.2, 1)
tick()
assert(api.status(timed.id).status == 'TIMED_OUT')
assert(not character.walking_state.walking)
character.position = {x=0,y=0}
test_player = {index=7, connected=false, character=character}
character.player = test_player
local request_before_rejection = request_id
local rejected = api.start(1, 3, 0, 0.2, 600)
assert(rejected.error and rejected.error:find("associated player must be connected"), 'disconnected player character should be rejected clearly')
assert(request_id == request_before_rejection, 'rejected navigation must not request a path')
test_player.connected = true
character.player = test_player
local player_job = api.start(1, 3, 0, 0.2, 600)
assert(player_job.status == 'PATHFINDING', 'connected player character should be navigable')
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=3,y=0}}}})
tick()
assert(character.walking_state.walking, 'navigation should steer connected character')
test_player.connected = false
tick()
assert(api.status(player_job.id).status == 'CANCELLED', 'disconnect should cancel player navigation')
assert(not character.walking_state.walking, 'disconnect should stop scripted movement')
print('navigation controller transitions passed')

-- A destination inside an entity is snapped to a free tile and reported instead
-- of failing with a generic NO_PATH.
character.player = nil
test_player = nil
character.position = {x = 0, y = 0}
local lab = {
  valid = true, type = 'assembling-machine', name = 'lab', unit_number = 9, surface = surface,
  position = {x = 30, y = 0},
  bounding_box = {left_top = {x = 28.5, y = -1.5}, right_bottom = {x = 31.5, y = 1.5}}
}
test_entities[#test_entities + 1] = lab
blockers[#blockers + 1] = lab
snap_response = {x = 32.5, y = 0}
local filters_before_snap = #scan_filters
local snapped = api.start(1, 30, 0, 0.5, 2000)
assert(not snapped.error, 'a snapped destination should start: '..tostring(snapped.error))
assert(snapped.destination.x == 32.5 and snapped.destination.y == 0,
  'the snapshot must report the snapped destination, got '..tostring(snapped.destination.x))
-- The engine rejects a CollisionMask wrapper in an entity search: the search
-- filter takes the plain layer dictionary, while request_path takes the mask.
local blocking_filter
for index = filters_before_snap + 1, #scan_filters do
  if scan_filters[index].collision_mask then blocking_filter = scan_filters[index] end
end
assert(blocking_filter, 'the occupied destination must be probed with a collision mask')
assert(blocking_filter.area, 'the occupied destination must be probed with a bounded area, not a tiny radius')
assert(blocking_filter.collision_mask.layers == nil, 'entity search filters take a layer dictionary, not a CollisionMask wrapper')
assert(blocking_filter.collision_mask.player == true, 'the search must use the character prototype layers')
assert(requested_paths[request_id].collision_mask == character_mask, 'request_path takes the prototype CollisionMask')
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=32.5,y=0}}}})
for _ = 1, 400 do
  tick()
  if api.status(snapped.id).status ~= 'MOVING' then break end
end
assert(api.status(snapped.id).status == 'ARRIVED', 'the walker should reach the snapped tile, got '..api.status(snapped.id).status)
snap_response = nil
character.position = {x = 0, y = 0}
local trapped = api.start(1, 30, 0, 0.5, 600)
assert(trapped.error and trapped.error:find('destination is inside lab 9', 1, true),
  'an unreachable occupied destination must name the blocking entity, got '..tostring(trapped.error))
-- A rounded coordinate still lands inside the lab's box even though it is not
-- near the entity centre; the box test must catch it.
local offset_trap = api.start(1, 29.6, 0.4, 0.5, 600)
assert(offset_trap.error and offset_trap.error:find('destination is inside lab 9', 1, true),
  'an off-centre destination inside the box must name the blocker, got '..tostring(offset_trap.error))
assert(api.active(1).id == nil, 'a failed start must not leave an active job')
print('navigation destination snapping passed')

-- Chase a stationary character by unit number.
character.position = {x = 0, y = 0}
other.position = {x = 5, y = 0}
local chase = api.start(1, nil, nil, 1, 600, 2)
assert(not chase.error, 'chase should start: '..tostring(chase.error))
assert(chase.targetCharacter == '2', 'chase snapshot must expose the target character')
assert(chase.status == 'PATHFINDING', 'chase should begin pathfinding')
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=5,y=0}}}})
for _ = 1, 200 do
  tick()
  if api.status(chase.id).status ~= 'MOVING' then break end
end
local chased = api.status(chase.id)
assert(chased.status == 'ARRIVED', 'chase should arrive: '..chased.status..' '..tostring(chased.reason))
assert(not character.walking_state.walking, 'chase should stop after arrival')

-- A chasing walker keeps moving while a re-path for a drifted target is pending.
character.position = {x = 0, y = 0}
other.position = {x = 10, y = 0}
local moving = api.start(1, nil, nil, 1, 600, 2)
assert(not moving.error, 'moving chase should start: '..tostring(moving.error))
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=10,y=0}}}})
tick()
local requests_before_drift = request_id
other.position = {x = 20, y = 0}
tick()
assert(request_id == requests_before_drift + 1, 'drifted target should trigger a re-path request')
assert(api.status(moving.id).status == 'MOVING', 'a chase re-path must not stop the walker')
assert(character.walking_state.walking, 'the chaser must keep walking while the new path is pending')
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=20,y=0}}}})
assert(api.status(moving.id).status == 'MOVING', 'the delivered path must keep the chase moving')
api.cancel(moving.id)

-- Losing the chased character ends the job without touching the walker.
character.position = {x = 0, y = 0}
other.position = {x = 5, y = 0}
local lost = api.start(1, nil, nil, 1, 600, 2)
assert(not lost.error, 'loss chase should start: '..tostring(lost.error))
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=5,y=0}}}})
other.valid = false
tick()
assert(api.status(lost.id).status == 'TARGET_MISSING', 'missing target should end the job')
assert(not character.walking_state.walking, 'missing target should stop the walker')
other.valid = true

-- Chase argument validation.
assert(api.start(1, 3, 0, 1, 600, 2).error, 'coordinates plus target must be rejected')
assert(api.start(1, nil, nil, 0.5, 600, 2).error, 'chase tolerance below 1 must be rejected')
assert(api.start(1, nil, nil, 1, 600, 1).error, 'chasing itself must be rejected')
assert(api.start(1, nil, nil, 1, 600, 99).error, 'unknown target must be rejected')
assert(api.start(1).error, 'missing destination must be rejected')
print('navigation chase transitions passed')

-- A chase goal that the pathfinder cannot reach must fall back to another ring
-- point instead of failing the job.
character.position = {x = 0, y = 0}
other.position = {x = 5, y = 0}
local fallback = api.start(1, nil, nil, 1, 600, 2)
assert(not fallback.error, 'fallback chase should start: ' .. tostring(fallback.error))
local blocked = request_id
local blocked_goal = requested_goals[blocked]
assert(math.abs(blocked_goal.x - 4) < 1e-6 and math.abs(blocked_goal.y) < 1e-6,
  'first chase goal should be the nearest tolerance ring point, got ' .. blocked_goal.x .. ',' .. blocked_goal.y)
handlers[defines.events.on_script_path_request_finished]({id = blocked, path = nil})
assert(api.status(fallback.id).status == 'PATHFINDING', 'blocked chase goal should retry before giving up')
assert(request_id == blocked + 1, 'blocked chase goal should request the next candidate')
local retry_goal = requested_goals[request_id]
assert(retry_goal.x ~= blocked_goal.x or retry_goal.y ~= blocked_goal.y,
  'fallback candidate must differ from the blocked goal')
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = {{position = {x = 4, y = 0}}}})
tick()
assert(api.status(fallback.id).status == 'MOVING', 'fallback candidate should move the walker')
api.cancel(fallback.id)

-- Exhausting every candidate is the only case that reports NO_PATH.
character.position = {x = 0, y = 0}
other.position = {x = 5, y = 0}
local doomed = api.start(1, nil, nil, 1, 600, 2)
assert(not doomed.error, 'doomed chase should start: ' .. tostring(doomed.error))
local attempts = 0
while api.status(doomed.id).status == 'PATHFINDING' and attempts < 200 do
  handlers[defines.events.on_script_path_request_finished]({id = request_id, path = nil})
  attempts = attempts + 1
end
assert(attempts == 24, 'every chase candidate should be tried once, got ' .. attempts)
local exhausted = api.status(doomed.id)
assert(exhausted.status == 'NO_PATH', 'exhausted candidates should report NO_PATH, got ' .. exhausted.status)
assert(not character.walking_state.walking, 'failed chase should stop the walker')

-- Replans reset after real progress, so separate short stalls do not end a long
-- walk that is still making headway.
character.position = {x = 0, y = 0}
frozen = true
local long_walk = api.start(1, 100, 0, 0.5, 20000)
assert(not long_walk.error, 'long walk should start: ' .. tostring(long_walk.error))
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = {{position = {x = 100, y = 0}}}})
for _ = 1, 121 do tick() end
local stalled = api.status(long_walk.id)
assert(stalled.status == 'PATHFINDING' and stalled.replans == 1,
  'a stalled walk should replan once, got ' .. stalled.status .. ' replans ' .. tostring(stalled.replans))
frozen = false
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = {{position = {x = 100, y = 0}}}})
for _ = 1, 121 do tick() end
assert(api.status(long_walk.id).replans == 0,
  'progress must reset replans, got ' .. tostring(api.status(long_walk.id).replans))
api.cancel(long_walk.id)
print('navigation replan accounting passed')

-- Approach a static entity by name and position; no call may scan the surface.
character.position = {x = 0, y = 0}
local chest = {
  valid = true, type = 'container', name = 'wooden-chest', unit_number = 3, surface = surface,
  position = {x = 10, y = 0},
  bounding_box = {left_top = {x = 9.5, y = -0.5}, right_bottom = {x = 10.5, y = 0.5}}
}
test_entities[#test_entities + 1] = chest
local filters_before_start = #scan_filters
local approach = api.start(1, nil, nil, 1, 600, nil, {name = 'wooden-chest', x = 10, y = 0})
assert(not approach.error, 'entity approach should start: ' .. tostring(approach.error))
assert(approach.targetEntity == '3', 'entity snapshot must expose the target entity')
assert(not approach.targetCharacter, 'entity target must not report a target character')
for index = filters_before_start + 1, #scan_filters do
  local filter = scan_filters[index]
  assert(filter.name and filter.area, 'an entity start must not filter the whole surface')
end
local chest_goal = requested_goals[request_id]
assert(chest_goal.x == 8.5 or chest_goal.x == 11.5 or chest_goal.y == -1.5 or chest_goal.y == 1.5,
  'the entity goal must sit on the inflated box, got ' .. tostring(chest_goal.x) .. ',' .. tostring(chest_goal.y))
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = {{position = {x = 8.5, y = 0}}}})
for _ = 1, 200 do
  tick()
  if api.status(approach.id).status ~= 'MOVING' then break end
end
local approached = api.status(approach.id)
assert(approached.status == 'ARRIVED', 'entity approach should arrive: ' .. approached.status .. ' ' .. tostring(approached.reason))
assert(character.position.x >= 8.5 and character.position.x <= 9.6,
  'entity approach should stop beside the bounding box, got x=' .. character.position.x)
assert(not character.walking_state.walking, 'entity approach should stop after arrival')

-- A blocked side of the box falls back to another side instead of NO_PATH.
character.position = {x = 0, y = 0}
local rack = {
  valid = true, type = 'container', name = 'wooden-chest', unit_number = 4, surface = surface,
  position = {x = 20, y = 0},
  bounding_box = {left_top = {x = 19.5, y = -0.5}, right_bottom = {x = 20.5, y = 0.5}}
}
test_entities[#test_entities + 1] = rack
local box_job = api.start(1, nil, nil, 1, 600, nil, {name = 'wooden-chest', x = 20, y = 0, unit = 4})
assert(not box_job.error, 'box approach should start: ' .. tostring(box_job.error))
assert(box_job.targetEntity == '4', 'the unit-numbered reference must resolve the right entity')
local blocked_goal = requested_goals[request_id]
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = nil})
assert(api.status(box_job.id).status == 'PATHFINDING', 'a blocked box side must retry another candidate')
local fallback_goal = requested_goals[request_id]
assert(fallback_goal.x ~= blocked_goal.x or fallback_goal.y ~= blocked_goal.y,
  'the box fallback candidate must differ from the blocked goal')
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = {{position = fallback_goal}}})
tick()
assert(api.status(box_job.id).status == 'MOVING', 'the fallback box candidate should move the walker')
api.cancel(box_job.id)

-- A target destroyed mid-job ends the job instead of walking to an empty tile.
character.position = {x = 0, y = 0}
local stale = api.start(1, nil, nil, 1, 600, nil, {name = 'wooden-chest', x = 10, y = 0})
assert(not stale.error, 'entity approach should start: ' .. tostring(stale.error))
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = {{position = {x = 8.5, y = 0}}}})
tick()
chest.valid = false
for _ = 1, 120 do
  tick()
  if api.status(stale.id).status ~= 'MOVING' then break end
end
assert(api.status(stale.id).status == 'TARGET_MISSING', 'destroyed target should end the job, got ' .. api.status(stale.id).status)
assert(api.status(stale.id).reason == 'target entity no longer exists', 'destroyed target must report its reason, got ' .. tostring(api.status(stale.id).reason))
assert(not character.walking_state.walking, 'destroyed target should stop the walker')
chest.valid = true

-- Entity target argument validation.
assert(api.start(1, 3, 0, 1, 600, nil, {name = 'wooden-chest', x = 10, y = 0}).error, 'coordinates plus target entity must be rejected')
assert(api.start(1, nil, nil, 1, 600, 2, {name = 'wooden-chest', x = 10, y = 0}).error, 'character plus entity target must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, {unit = 1}).error, 'targeting itself must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, {unit = 2}).error, 'a character entity target must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, 'chest').error, 'a bare unit number must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, {name = 'wooden-chest'}).error, 'a name without a position must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, {name = 'wooden-chest', x = 1, y = 1}).error, 'an entity that is not there must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, {unit = 99}).error, 'an unresolvable unit number must be rejected')
print('navigation entity-target transitions passed')

-- Finished jobs are evicted once newer ones exceed the retention bound.
character.position = {x = 0, y = 0}
local first = api.start(1, 3, 0, 0.2, 600)
assert(api.cancel(first.id).status == 'CANCELLED')
local last
for _ = 1, 300 do
  last = api.start(1, 3, 0, 0.2, 600)
  assert(not last.error, 'retention loop start failed: ' .. tostring(last.error))
  api.cancel(last.id)
end
assert(api.status(first.id).error, 'old finished navigation job must be evicted')
assert(api.status(last.id).status == 'CANCELLED', 'newest finished navigation job must stay pollable')
print('navigation job retention passed')
