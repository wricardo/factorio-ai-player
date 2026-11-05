-- Loads topology.lua under a stub engine and exercises every remote operation.
-- Run from the repository root: lua mods/factorio_ai_navigation/topology_test.lua
storage = {}
defines = {entity_status = {}, transport_line = {left_line = 1, right_line = 2, secondary_left_line = 3, secondary_right_line = 4}}
prototypes = {entity = {}}
local api = nil
remote = {add_interface = function(name, functions)
  assert(name == 'factorio_ai_topology', name)
  api = functions
end}

dofile('mods/factorio_ai_navigation/topology.lua')
assert(api ~= nil, 'topology.lua must register factorio_ai_topology')

local surface = {name = 'nauvis', index = 1}
local world = {}

local function box(cx, cy, size)
  local half = (size or 1) / 2
  return {left_top = {x = cx - half, y = cy - half}, right_bottom = {x = cx + half, y = cy + half}}
end

local function in_area(spec, e)
  local area = spec.area
  if not area then return true end
  local x1, y1, x2, y2
  if area.left_top then
    x1, y1, x2, y2 = area.left_top.x, area.left_top.y, area.right_bottom.x, area.right_bottom.y
  else
    x1, y1, x2, y2 = area[1][1], area[1][2], area[2][1], area[2][2]
  end
  local bb = e.bounding_box
  return bb.left_top.x <= x2 and bb.right_bottom.x >= x1 and bb.left_top.y <= y2 and bb.right_bottom.y >= y1
end

local function matches_type(filter, e)
  if not filter then return true end
  if type(filter) == 'string' then return e.type == filter end
  for _, candidate in ipairs(filter) do
    if e.type == candidate then return true end
  end
  return false
end

surface.find_entities_filtered = function(spec)
  local out = {}
  for _, e in ipairs(world) do
    local ok = true
    if not in_area(spec, e) then ok = false end
    if ok and spec.name and e.name ~= spec.name then ok = false end
    if ok and not matches_type(spec.type, e) then ok = false end
    if ok and spec.force and e.force ~= spec.force then ok = false end
    if ok and spec.position then
      local bb = e.bounding_box
      -- Engine position filters match the entity whose bounding box covers the
      -- point, not merely a nearby entity.
      ok = spec.position.x >= bb.left_top.x and spec.position.x <= bb.right_bottom.x
        and spec.position.y >= bb.left_top.y and spec.position.y <= bb.right_bottom.y
    end
    if ok then out[#out + 1] = e end
  end
  return out
end
surface.can_place_entity = function() return true end
surface.get_tile = function() return {valid = true, name = 'grass-1'} end

local force = {name = 'player'}
game = {
  tick = 42,
  surfaces = {nauvis = surface},
  forces = {player = force},
  get_entity_by_unit_number = function(id)
    for _, e in ipairs(world) do if e.unit_number == id then return e end end
  end
}

local next_unit = 0
local function entity(kind, name, cx, cy, size)
  next_unit = next_unit + 1
  local e = {
    valid = true, unit_number = next_unit, type = kind, name = name or kind,
    surface = surface, force = force, position = {x = cx, y = cy}, direction = 4,
    bounding_box = box(cx, cy, size),
  }
  world[#world + 1] = e
  return e
end

local function belt(kind, cx, cy, direction, line_items)
  local e = entity(kind, kind, cx, cy)
  e.direction = direction
  e.belt_neighbours = {inputs = {}, outputs = {}}
  e.lines = {}
  for i, items in ipairs(line_items) do
    e.lines[i] = {owner = e, input_lines = {}, output_lines = {}, get_contents = function() return items end}
  end
  e.get_max_transport_line_index = function() return #e.lines end
  e.get_transport_line = function(index) return e.lines[index] end
  return e
end

local function connect(a, b)
  a.belt_neighbours.outputs[#a.belt_neighbours.outputs + 1] = b
  b.belt_neighbours.inputs[#b.belt_neighbours.inputs + 1] = a
end

-- Two connected belts in transit, one inserter that drains the first into a chest.
local first = belt('transport-belt', 0.5, 0.5, 4, {{}, {}})
local second = belt('transport-belt', 1.5, 0.5, 4, {{}})
connect(first, second)
local chest = entity('container', 'iron-chest', 3.5, 0.5)
chest.get_max_inventory_index = function() return 1 end
local inserter = entity('inserter', 'inserter', 0.5, 2.5)
inserter.pickup_position = {x = 0.5, y = 1.0}
inserter.drop_position = {x = 0.5, y = 2.0}
inserter.pickup_target = nil
inserter.drop_target = chest

local run = api.belt_run('nauvis', 0.5, 0.5, 10, false)
assert(type(run) == 'table', 'belt_run must return a payload')
assert(run.error == nil, 'belt_run error: ' .. tostring(run.error))
assert(#run.nodes == 2, 'expected two nodes, got ' .. #run.nodes)
assert(#run.edges == 1, 'expected one structural edge, got ' .. #run.edges)
assert(run.edges[1].kind == 'belt', 'edge kind ' .. tostring(run.edges[1].kind))
assert(#run.inserters == 1, 'the inserter draining the run must be found')
assert(run.sources[1].unitNumber ~= nil and #run.sinks == 1)

local with_lines = api.belt_run('nauvis', 0.5, 0.5, 10, true)
assert(#with_lines.nodes == 2, 'line links must not change traversal')

local stretch = api.belt_stretch('nauvis', 0.5, 0.5, 10)
assert(stretch.error == nil, 'belt_stretch error: ' .. tostring(stretch.error))
assert(#stretch.nodes == 2, 'stretch nodes ' .. #stretch.nodes)
assert(#stretch.ends == 2, 'stretch ends ' .. #stretch.ends)
assert(#stretch.inserters == 1, 'stretch inserters ' .. #stretch.inserters)
assert(stretch.inserters[1].inserter.status == nil, 'stub inserters carry no status')
assert(stretch.left and stretch.right and stretch.unknown, 'every side total must exist')

local graph = api.factory_graph('nauvis', 'player', {{-4, -4}, {4, 4}}, false)
assert(graph.error == nil, 'factory_graph error: ' .. tostring(graph.error))
assert(#graph.nodes >= 3, 'factory_graph nodes ' .. #graph.nodes)
assert(#graph.inserters == 1, 'factory_graph inserters ' .. #graph.inserters)

local compact = api.factory_graph('nauvis', 'player', {{-4, -4}, {4, 4}}, true)
assert(compact.error == nil, 'compact factory_graph error: ' .. tostring(compact.error))
assert(#compact.nodes >= 3, 'compact nodes ' .. #compact.nodes)
assert(#compact.machines == 0 and #compact.belts == 0, 'compact mode must not repeat entity lists')
assert(#compact.inserters == 0 and #compact.edges == 0, 'compact mode must not repeat edges or inserters')
assert(#compact.compactInserters == 1, 'compact inserters ' .. #compact.compactInserters)

local leaf = api.graph_leaf('nauvis', 'player', {{-4, -4}, {4, 4}}, 3, false, {'character'})
assert(leaf.error == nil, 'graph_leaf error: ' .. tostring(leaf.error))
assert(#leaf.nodes == 4, 'graph_leaf nodes ' .. #(leaf.nodes or {}))
local kinds = {}
for _, edge in ipairs(leaf.edges) do kinds[edge.kind] = (kinds[edge.kind] or 0) + 1 end
-- Both belts own the same link, so it arrives twice; the Go side deduplicates.
assert(kinds.belt == 2 and kinds.pickup == 1 and kinds.drop == 1, 'graph_leaf edge kinds')
-- Ownership is by centre in a half-open rectangle: the belt at x=1.5 belongs
-- to the neighbouring leaf even though the scan pad reaches it.
local half = api.graph_leaf('nauvis', 'player', {{0, 0}, {1, 1}}, 3, false, {})
assert(#half.nodes == 1 and half.nodes[1].id == 'nauvis:' .. first.unit_number, 'half-open leaf ownership')
local mobile = api.graph_mobile('nauvis', 'player', {'character'})
assert(mobile.error == nil and mobile.nodes == nil, 'graph_mobile without vehicles')
-- A splitter's two outputs are ports: each outgoing belt must carry the side of
-- the splitter it leaves from, resolved from the two bounding boxes. Placed far
-- from the areas the earlier assertions counted.
local splitter = entity('splitter', 'splitter', 100.5, 101.0, 2)
splitter.direction = 4
splitter.belt_neighbours = {inputs = {}, outputs = {}}
local up = entity('transport-belt', 'transport-belt', 101.5, 100.5)
local down = entity('transport-belt', 'transport-belt', 101.5, 101.5)
for _, b in ipairs({up, down}) do
  b.belt_neighbours = {inputs = {splitter}, outputs = {}}
  splitter.belt_neighbours.outputs[#splitter.belt_neighbours.outputs + 1] = b
end
splitter.splitter_output_priority = 'right'
splitter.splitter_input_priority = 'none'
local ports = api.graph_leaf('nauvis', 'player', {{100, 100}, {102, 102}}, 3, false, {})
local sides, splitterNode = {}, nil
for _, node in ipairs(ports.nodes) do if node.type == 'splitter' then splitterNode = node end end
for _, edge in ipairs(ports.edges) do
  -- The neighbouring belt reports the same link back without a port; Go merges
  -- the port onto that edge, so only the splitter-owned copy carries one.
  if edge.kind == 'belt' and edge.from == 'nauvis:' .. splitter.unit_number and edge.fromPort then
    sides[edge.fromPort] = edge.to
  end
end
assert(splitterNode and splitterNode.outputPriority == 'right' and splitterNode.inputPriority == 'none', 'splitter priorities')
assert(sides.left == 'nauvis:' .. up.unit_number and sides.right == 'nauvis:' .. down.unit_number,
  'splitter ports: left=' .. tostring(sides.left) .. ' right=' .. tostring(sides.right))
assert(compact.compactInserters[1].drop == 'nauvis:' .. tostring(chest.unit_number), compact.compactInserters[1].drop)
for _, edge in ipairs(compact.compactEdges or {}) do
  assert(type(edge.from) == 'string' and type(edge.to) == 'string', 'compact edges carry keys')
end

local components = api.belt_components('nauvis', 'player', {{-4, -4}, {4, 4}}, 100, 10, 0, false, false)
assert(components.error == nil, 'belt_components error: ' .. tostring(components.error))
assert(components.beltTotal == 2, 'beltTotal ' .. components.beltTotal)
assert(#components.components == 1, 'components ' .. #components.components)
local component = components.components[1]
assert(component.dead == false, 'a fed component must not report dead')
assert(component.belts == 2, 'component belts ' .. component.belts)
assert(#component.inserters == 1, 'component inserters ' .. #component.inserters)
assert(#component.sinks == 1, 'component sinks ' .. #component.sinks)

-- The padded scan is what proves a component alive from outside the area.
local outside = api.belt_components('nauvis', 'player', {{-4, -4}, {1, 1}}, 100, 10, 0, false, false)
assert(outside.error == nil, 'outer belt_components error: ' .. tostring(outside.error))
assert(#outside.components >= 1, 'outer components ' .. #outside.components)
assert(outside.components[1].dead == false, 'the padded scan must prove the outer component alive')
assert(#outside.components[1].inserters == 1, 'the outside inserter must count, got ' .. #outside.components[1].inserters)

local adjacency = api.adjacency('nauvis', 'player', {{-4, -4}, {4, 4}}, 0.5, 100, 100)
assert(adjacency.error == nil, 'adjacency error: ' .. tostring(adjacency.error))
assert(#adjacency.nodes >= 3, 'adjacency nodes ' .. #adjacency.nodes)
local paired = false
for _, pair in ipairs(adjacency.pairs) do
  if pair.connection == 'belt' then paired = true end
end
assert(paired, 'touching connected belts must pair as belt')

local inspection = api.inspection_logistics('nauvis', 'player', 'inserter', {x = 0.5, y = 2.5})
assert(inspection.error == nil, 'inspection error: ' .. tostring(inspection.error))
assert(#inspection.inserters == 1, 'an inspected inserter must report itself')
assert(inspection.inserters[1].inserter.unitNumber == inserter.unit_number, 'the inspected inserter must be the result')
assert(inspection.inserters[1].drop.name == 'iron-chest', 'the inspected inserter keeps its drop')

-- The prelude is compiled once at mod load, so its memo tables must not leak
-- across interface calls: a rotated inserter must report its live direction and
-- its live pickup/drop pair instead of the pre-rotation snapshot.
assert(inspection.inserters[1].inserter.direction == 4, 'baseline direction ' .. tostring(inspection.inserters[1].inserter.direction))
inserter.direction = 12
inserter.pickup_position = {x = 0.5, y = 3.0}
inserter.drop_position = {x = 0.5, y = 1.0}
inserter.pickup_target = chest
inserter.drop_target = first
local rotated = api.inspection_logistics('nauvis', 'player', 'inserter', {x = 0.5, y = 2.5})
assert(rotated.error == nil, 'rotated inspection error: ' .. tostring(rotated.error))
rotated = rotated.inserters[1]
assert(rotated.inserter.direction == 12, 'a rotated inserter must report its new direction, got ' .. tostring(rotated.inserter.direction))
assert(rotated.inserter.pickupPosition.y == 3.0 and rotated.inserter.dropPosition.y == 1.0, 'rotated endpoints must be live')
assert(rotated.pickup ~= nil and rotated.pickup.unitNumber == chest.unit_number, 'rotated pickup must be the live target')
assert(rotated.drop ~= nil and rotated.drop.unitNumber == first.unit_number, 'rotated drop must be the live target')

-- The same leak was visible on the neighbour path: inspecting a machine lists
-- the arms wired to it, and that wiring must follow a rotation too.
local second_chest = entity('container', 'iron-chest', 6.5, 6.5)
second_chest.get_max_inventory_index = function() return 1 end
local neighbour = entity('inserter', 'inserter', 6.5, 5.5)
neighbour.pickup_position = {x = 6.5, y = 5.0}
neighbour.drop_position = {x = 6.5, y = 6.0}
neighbour.pickup_target = nil
neighbour.drop_target = second_chest

local function arm_for_chest()
  local payload = api.inspection_logistics('nauvis', 'player', 'iron-chest', {x = 6.5, y = 6.5})
  assert(payload.error == nil, 'chest inspection error: ' .. tostring(payload.error))
  for _, entry in ipairs(payload.inserters) do
    if entry.inserter.unitNumber == neighbour.unit_number then return entry end
  end
  return nil
end

local wired = arm_for_chest()
assert(wired ~= nil and wired.drop.unitNumber == second_chest.unit_number, 'an arm dropping into the chest must be listed')
neighbour.direction = 12
neighbour.pickup_position = {x = 6.5, y = 6.0}
neighbour.drop_position = {x = 6.5, y = 5.0}
neighbour.pickup_target = second_chest
neighbour.drop_target = nil

wired = arm_for_chest()
assert(wired ~= nil, 'a rotated arm still feeding the chest must be listed')
assert(wired.inserter.direction == 12, 'neighbour direction must follow the rotation, got ' .. tostring(wired.inserter.direction))
assert(wired.pickup ~= nil and wired.pickup.unitNumber == second_chest.unit_number, 'neighbour pickup must be the live target')
assert(wired.drop == nil or wired.drop.unitNumber ~= second_chest.unit_number, 'neighbour drop must not stay on the old target')

local missing = api.belt_run('nauvis', 40.5, 40.5, 10, false)
assert(missing.error ~= nil, 'a missing seed must return an error payload')

local bad_force = api.adjacency('nauvis', 'nobody', {{0, 0}, {1, 1}}, 0.5, 10, 10)
assert(bad_force.error ~= nil, 'an unknown force must return an error payload')

print('topology harness ok')
