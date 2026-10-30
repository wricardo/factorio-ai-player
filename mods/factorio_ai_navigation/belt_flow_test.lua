-- Deterministic transport observations drive the production counting state
-- machine. No inventory-count deltas or precomputed API responses are used.
storage = {}
local api
remote = {add_interface = function(name, functions)
  assert(name == 'factorio_ai_belt_flow')
  api = functions
end}
local module = dofile('mods/factorio_ai_navigation/belt_flow.lua')
local surface = {name = 'nauvis', index = 1}
local observations = {{}, {}}
local locations = {{x = 0.5, y = 0.25}, {x = 0.5, y = 0.75}}
local lines = {}
for index = 1, 2 do
  lines[index] = {
    valid = true, line_length = 1,
    get_detailed_contents = function() return observations[index] end,
    get_line_item_position = function() return locations[index] end
  }
end
local entity = {
  valid = true, type = 'transport-belt', name = 'transport-belt', unit_number = 42,
  position = {x = 0.5, y = 0.5}, direction = 4, surface = surface,
  force = {name = 'player'}, prototype = {belt_speed = 0.03125},
  get_transport_line = function(index) return lines[index] end
}
surface.find_entities_filtered = function() return {entity} end
game = {tick = 0, surfaces = {nauvis = surface}}
local function item(id, position, count, name, quality)
  return {unique_id = id, position = position,
    stack = {valid_for_read = true, count = count or 1, name = name or 'iron-plate', quality = {name = quality or 'normal'}}}
end
local function tick(first, second)
  if first then observations[1] = first end
  if second then observations[2] = second end
  game.tick = game.tick + 1
  module.on_tick({tick = game.tick})
end
local function start(duration)
  local result = api.start('nauvis', 0.5, 0.5, duration or 2)
  assert(not result.error, result.error)
  assert(result.status == 'RUNNING' and result.itemsPerMinute == nil)
  return result.id
end
module.initialize()
assert(api.version() == 1)

-- Empty and stopped belts both measure zero. Populated buffers are not flow.
local empty = start()
tick({}, {})
tick({}, {})
assert(api.status(empty).status == 'COMPLETE')
assert(api.status(empty).itemsCrossed == 0 and api.status(empty).itemsPerMinute == 0)
observations = {{item(1, 0.25, 4)}, {item(2, 0.75, 4)}}
local stopped = start(3)
for _ = 1, 3 do tick() end
assert(api.status(stopped).itemsCrossed == 0 and api.status(stopped).elapsedTicks == 3)

-- Constant occupancy, moving IDs: real crossings with stacked counts and
-- independent lanes/quality. Coordinate direction is deliberately opposite.
observations = {{item(10, 0.45, 4)}, {item(20, 0.55, 2, 'copper-plate', 'rare')}}
local moving = start(4)
tick({item(10, 0.5, 4)}, {item(20, 0.5, 2, 'copper-plate', 'rare')})
assert(api.status(moving).itemsCrossed == 6)
tick({item(10, 0.55, 4)}, {item(20, 0.45, 2, 'copper-plate', 'rare')})
assert(api.status(moving).itemsCrossed == 6, 'one crossing must not count again while same ID remains visible')
tick({item(11, 0.45, 3)}, {item(21, 0.8)})
tick({item(11, 0.55, 3)}, {item(21, 0.8)})
local result = api.status(moving)
assert(result.status == 'COMPLETE' and result.itemsCrossed == 9 and result.itemsPerMinute == 8100)
assert(result.lanes[1].itemsCrossed == 7 and result.lanes[2].itemsCrossed == 2)
assert(result.lanes[2].crossedItems[1].quality == 'rare')
assert(result.lanes[1].crossedItems[1].itemsCrossed == 7)

-- Downstream insertion, disappearing items, and stationary midpoint IDs are
-- not crossings; baseline midpoint items are excluded.
observations = {{item(30, 0.5)}, {item(31, 0.4)}}
local insertion = start(3)
tick({item(30, 0.6), item(32, 0.8)}, {})
tick({item(32, 0.9)}, {})
tick({}, {})
assert(api.status(insertion).itemsCrossed == 0)

-- Changed stack count is sampled at crossing, not inferred from occupancy.
observations = {{item(40, 0.4, 4)}, {}}
local stack = start(1)
tick({item(40, 0.6, 2)}, {})
assert(api.status(stack).itemsCrossed == 2)

-- Removing selected entity invalidates, even when a replacement occupies its
-- tile. No further elapsed ticks are included after the last valid sample.
observations = {{item(50, 0.4)}, {}}
local removed = start(10)
entity.valid = false
tick({item(50, 0.6)}, {})
assert(api.status(removed).status == 'INVALIDATED')
assert(api.status(removed).itemsCrossed == 0 and api.status(removed).elapsedTicks == 0)
entity.valid = true
local rotated = start(10)
entity.direction = 8
tick()
assert(api.status(rotated).status == 'INVALIDATED')
entity.direction = 4
local geometry = start(10)
locations[2].x = 0.7
tick()
assert(api.status(geometry).status == 'INVALIDATED')
locations[2].x = 0.5

-- Save/load and configuration initialization preserve baseline identities,
-- elapsed time, and counters, without needing an on_load storage write.
observations = {{item(60, 0.4)}, {}}
local persisted = start(2)
module = dofile('mods/factorio_ai_navigation/belt_flow.lua')
module.initialize()
tick({item(60, 0.6)}, {})
tick({}, {})
assert(api.status(persisted).itemsCrossed == 1 and api.status(persisted).status == 'COMPLETE')

-- ID may return after travelling around a loop; each physical pass counts.
observations = {{item(70, 0.4)}, {}}
local loop = start(4)
tick({item(70, 0.6)}, {})
tick({}, {})
tick({item(70, 0.4)}, {})
tick({item(70, 0.6)}, {})
assert(api.status(loop).itemsCrossed == 2)

assert(api.start('nauvis', 0 / 0, 0.5, 2).error)
assert(api.start('nauvis', 0.5, 0.5, 36001).error)
entity.prototype.belt_speed = 0.5
assert(api.start('nauvis', 0.5, 0.5, 2).error)
entity.prototype.belt_speed = 0.03125
surface.find_entities_filtered = function() return {} end
assert(api.start('nauvis', 0.5, 0.5, 2).error)
surface.find_entities_filtered = function() return {entity} end

observations = {{}, {}}
for _ = 1, 32 do start(1) end
assert(api.start('nauvis', 0.5, 0.5, 1).error, 'active watches must be bounded')
tick()
for _ = 1, 140 do start(1); tick() end
local retained = 0
for _ in pairs(storage.belt_flow.jobs) do retained = retained + 1 end
assert(retained == 128, 'completed measurement retention must stay bounded')
assert(api.status(empty).error, 'old terminal measurement must be explicitly evicted')
print('belt throughput counting transitions passed')
