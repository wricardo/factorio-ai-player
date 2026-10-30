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
local surface = {name = 'nauvis', index = 1}
surface.request_path = function()
  request_id = request_id + 1
  return request_id
end
local character = {
  valid = true, type = 'character', unit_number = 1, surface = surface,
  force = {}, prototype = {collision_box = {}, collision_mask = {}},
  position = {x = 0, y = 0}
}
local other = {
  valid = true, type = 'character', unit_number = 2, surface = surface,
  force = {}, prototype = {collision_box = {}, collision_mask = {}},
  position = {x = 5, y = 0}
}
local characters = {[1] = character, [2] = other}
local test_player
game = {
  tick = 0,
  get_entity_by_unit_number = function(id) return characters[id] end,
  get_player = function(index) if test_player and test_player.index == index then return test_player end end
}
package.path = 'mods/factorio_ai_navigation/?.lua;' .. package.path
dofile('mods/factorio_ai_navigation/control.lua')
handlers.init()
local function tick()
  game.tick = game.tick + 1
  handlers[defines.events.on_tick]({tick = game.tick})
  local state = character.walking_state
  if state and state.walking then
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
character.position = {x=0,y=0}
local cancelled = api.start(1, 3, 0, 0.2, 600)
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=3,y=0}}}})
tick()
assert(api.start(1, 4, 0, 0.2, 600).error)
assert(api.cancel(cancelled.id).status == 'CANCELLED')
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

-- Chase a stationary character by unit number.
character.player = nil
test_player = nil
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

-- A target that moves far enough must trigger a fresh path request.
character.position = {x = 0, y = 0}
other.position = {x = 10, y = 0}
local moving = api.start(1, nil, nil, 1, 600, 2)
assert(not moving.error, 'moving chase should start: '..tostring(moving.error))
handlers[defines.events.on_script_path_request_finished]({id=request_id, path={{position={x=10,y=0}}}})
local requests_before_drift = request_id
other.position = {x = 20, y = 0}
tick()
assert(request_id == requests_before_drift + 1, 'drifted target should trigger a re-path request')
assert(api.status(moving.id).status == 'PATHFINDING', 'drifted chase should return to pathfinding')
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

-- Approach a static entity by unit number; most buildings need a surface scan.
local chest = {
  valid = true, type = 'container', name = 'wooden-chest', unit_number = 3, surface = surface,
  position = {x = 10, y = 0},
  bounding_box = {left_top = {x = 9.5, y = -0.5}, right_bottom = {x = 10.5, y = 0.5}}
}
surface.find_entities_filtered = function() return {character, other, chest} end
character.position = {x = 0, y = 0}
local approach = api.start(1, nil, nil, 1, 600, nil, 3)
assert(not approach.error, 'entity approach should start: ' .. tostring(approach.error))
assert(approach.targetEntity == '3', 'entity snapshot must expose the target entity')
assert(not approach.targetCharacter, 'entity target must not report a target character')
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

-- A target destroyed mid-job ends the job instead of walking to an empty tile.
character.position = {x = 0, y = 0}
local stale = api.start(1, nil, nil, 1, 600, nil, 3)
assert(not stale.error, 'entity approach should start: ' .. tostring(stale.error))
handlers[defines.events.on_script_path_request_finished]({id = request_id, path = {{position = {x = 8.5, y = 0}}}})
tick()
chest.valid = false
for _ = 1, 120 do
  tick()
  if api.status(stale.id).status ~= 'MOVING' then break end
end
assert(api.status(stale.id).status == 'TARGET_MISSING', 'destroyed target should end the job, got ' .. api.status(stale.id).status)
assert(not character.walking_state.walking, 'destroyed target should stop the walker')
chest.valid = true
assert(api.start(1, nil, nil, 1, 600, nil, 99).error, 'unknown entity target must be rejected')

-- Entity target argument validation.
assert(api.start(1, 3, 0, 1, 600, nil, 3).error, 'coordinates plus target entity must be rejected')
assert(api.start(1, nil, nil, 1, 600, 2, 3).error, 'character plus entity target must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, 1).error, 'targeting itself must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, 2).error, 'a character entity target must be rejected')
assert(api.start(1, nil, nil, 1, 600, nil, 'chest').error, 'non-numeric entity target must be rejected')
print('navigation entity-target transitions passed')
