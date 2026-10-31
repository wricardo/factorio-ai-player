-- Exercise the action job controller with a deterministic character model.
storage = {}
defines = {shooting = {not_shooting = 0, shooting_enemies = 1, shooting_selected = 2}}
local api = nil
remote = {add_interface = function(_, functions) api = functions end}
local character = {
  valid = true, type = 'character', unit_number = 7, surface = {name = 'nauvis'},
  shooting_state = {state = defines.shooting.not_shooting, position = {x = 0, y = 0}},
  player = {index = 1, connected = true}
}
character.player.character = character
local players = {[1] = character.player}
game = {
  tick = 100,
  get_entity_by_unit_number = function(id) if character.unit_number == id then return character end end,
  get_player = function(index) return players[index] end
}
package.path = 'mods/factorio_ai_navigation/?.lua;' .. package.path
local actions = require('actions')
actions.initialize()
local function tick()
  game.tick = game.tick + 1
  actions.on_tick({tick = game.tick})
end
local function check(condition, message)
  if not condition then error(message, 2) end
end

-- Validation failures must not start a job.
check(api.start_shoot(0, 1, 1, 10).error ~= nil, 'unit 0 must be rejected')
check(api.start_shoot(7, 0 / 0, 1, 10).error ~= nil, 'NaN target must be rejected')
check(api.start_shoot(7, 1, 1, 0).error ~= nil, 'zero ticks must be rejected')
check(api.start_shoot(99, 1, 1, 10).error ~= nil, 'missing character must be rejected')

local detached = {valid = true, type = 'character', unit_number = 8, shooting_state = {state = 0, position = {x = 0, y = 0}}}
local found = game.get_entity_by_unit_number
game.get_entity_by_unit_number = function(id) if id == 8 then return detached end return found(id) end
check(api.start_shoot(8, 1, 1, 10).error ~= nil, 'detached character must be rejected')
game.get_entity_by_unit_number = found

-- A started job holds the shooting state until its tick budget expires.
local job = api.start_shoot(7, 10, 5, 3)
check(job.error == nil, 'valid job must start: ' .. tostring(job.error))
check(character.shooting_state.state == defines.shooting.shooting_enemies, 'state must be set immediately')
check(job.remainingTicks == 3, 'remaining ticks must reflect the budget')
check(api.start_shoot(7, 10, 5, 3).error ~= nil, 'a second concurrent job must be rejected')
tick()
check(character.shooting_state.state == defines.shooting.shooting_enemies, 'state must persist while shooting')
tick()
check(api.status(job.id).remainingTicks == 1, 'remaining ticks must count down')
tick()
local done = api.status(job.id)
check(done.status == 'COMPLETE', 'job must complete after its ticks, got ' .. tostring(done.status))
check(character.shooting_state.state == defines.shooting.not_shooting, 'state must be cleared on completion')

-- Cancelling clears the state and frees the character.
local second = api.start_shoot(7, 10, 5, 100)
check(second.error == nil, 'second job must start after completion')
tick()
local cancelled = api.cancel(second.id)
check(cancelled.status == 'CANCELLED', 'cancel must report CANCELLED')
check(character.shooting_state.state == defines.shooting.not_shooting, 'cancel must clear the state')
local reuse = api.start_shoot(7, 10, 5, 10)
check(reuse.error == nil, 'character must be reusable after cancel')
check(api.cancel(reuse.id).status == 'CANCELLED', 'reused job must cancel')
check(api.status('missing').error ~= nil, 'unknown job must error')

-- Losing the controller cancels the job.
local third = api.start_shoot(7, 10, 5, 100)
check(third.error == nil, 'third job must start: ' .. tostring(third.error))
character.player.connected = false
tick()
check(api.status(third.id).status == 'CANCELLED', 'disconnect must cancel the job')
check(character.shooting_state.state == defines.shooting.not_shooting, 'disconnect must clear the state')

print('actions controller ok')
