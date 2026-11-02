-- Minimal Factorio runtime stub used by place_many_replay_test.go. It models the
-- engine behaviour that placeMany must not misreport: can_place_entity rejects a
-- box that straddles four tiles, while create_entity snaps the entity to the
-- nearest tile centre and succeeds. PLACE_MANY_SCENARIO selects the world:
--   snap         - empty ground; integer positions are accepted after the engine snap
--   blocked      - one tile is genuinely occupied by another entity
--   engine       - create_entity raises a scripted engine error
--   replace      - the tiled belt run is fast-replaceable
--   underground_userdata / underground_array - neighbours as userdata / array
--   unreplaceable - the tile holds an entity that cannot be fast-replaced
--   commit_then_raise - create_entity commits the entity, then raises; placeMany
--                 must report it as placed and must not refund the item
--   ghost_corpse - the tile holds our own entity-ghost plus a tree corpse, and
--                 another corpse sits elsewhere on the route: corpses only
--                 collide with tiles, so the placement must succeed
--                 (factorio-ai-player-sth)
--   stray_corpse - a search that returns an entity whose box does not cover the
--                 requested tile must never turn that entity into the occupant
--   neighbour    - an entity in the next tile clips this tile with its box; the
--                 requested point is outside that box, the tile is not
--   belt_occupied - the tile holds a transport belt that genuinely blocks a pipe
local scenario = os.getenv("PLACE_MANY_SCENARIO") or "snap"
local explode = scenario == "engine"
local commit_then_raise = scenario == "commit_then_raise"
-- Entities the engine created before it raised, keyed by tile.
local committed = {}
local player_force = {name = "player"}
local returned = 0

local function is_centre(v)
  return math.abs(v - math.floor(v) - 0.5) < 1e-6
end

local function centre(v)
  return math.floor(v) + 0.5
end

local function tile_key(position)
  return string.format("%d,%d", math.floor(centre(position.x)), math.floor(centre(position.y)))
end

-- Collision masks come from the base game. A pipe collides with built objects;
-- a corpse collides with tiles only, which is why the engine builds over a tree
-- stump and the occupancy guard must not name one as an occupant.
local MASKS = {
  ["building"] = {layers = {item = true, meltable = true, object = true, player = true, water_tile = true, is_object = true, is_lower_object = true}},
  ["belt"] = {layers = {floor = true, meltable = true, object = true, transport_belt = true, water_tile = true}},
  ["corpse"] = {layers = {doodad = true}, colliding_with_tiles_only = true},
}

local function entity_at(name, kind, position, box, mask)
  return {
    valid = true,
    name = name,
    type = kind,
    position = position,
    bounding_box = {left_top = {x = position.x - box / 2, y = position.y - box / 2}, right_bottom = {x = position.x + box / 2, y = position.y + box / 2}},
    prototype = {collision_mask = MASKS[mask]},
  }
end

local function tile_box(position)
  local left, top = math.floor(position.x), math.floor(position.y)
  return {left_top = {x = left, y = top}, right_bottom = {x = left + 1, y = top + 1}}
end

local occupied = {}
if scenario == "blocked" or scenario == "unreplaceable" or scenario == "replace" or scenario == "stack" or scenario == "belt_occupied" then
  occupied["9,9"] = true
end
local replaceable = scenario == "replace"
-- Entities already standing in the world, whatever their state.
local world = {}
-- The entity already standing on the occupied tile, used by the occupancy guard.
local occupant = nil
if occupied["9,9"] then
  local inserter = scenario == "blocked" or scenario == "stack"
  local belt = scenario == "belt_occupied"
  if belt then
    occupant = entity_at("transport-belt", "transport-belt", {x = 9.5, y = 9.5}, 0.8, "belt")
  elseif inserter then
    occupant = entity_at("burner-inserter", "inserter", {x = 9.5, y = 9.5}, 1, "building")
  else
    occupant = entity_at("wooden-chest", "container", {x = 9.5, y = 9.5}, 1, "building")
  end
  world[#world + 1] = occupant
end
-- Ore is not a placement obstacle: belts are legal over resources and
-- can_place_entity agrees, so the occupancy guard must ignore resources
-- (factorio-ai-player-y18).
if scenario == "resource" then
  occupant = entity_at("iron-ore", "resource", {x = 9.5, y = 9.5}, 1, "building")
  occupant.amount = 500
  world[#world + 1] = occupant
end
-- A planned pipe (our own ghost) plus tree corpses. The corpse on the tile has
-- the real 0.8 collision box of tree-09-stump and still blocks nothing.
if scenario == "ghost_corpse" then
  world[#world + 1] = entity_at("entity-ghost", "entity-ghost", {x = 9.5, y = 9.5}, 1, "building")
  world[#world + 1] = entity_at("tree-09-stump", "corpse", {x = 9.5, y = 9.5}, 0.8, "corpse")
  world[#world + 1] = entity_at("tree-09-stump", "corpse", {x = 15.5, y = 15.5}, 0.8, "corpse")
end
-- A search result that ignores the requested position (the field report's
-- "an unrelated nearby corpse is the occupant"). The guard must drop it because
-- its box does not overlap the tile.
local stray = nil
if scenario == "stray_corpse" then
  stray = entity_at("tree-09-stump", "corpse", {x = 15.5, y = 15.5}, 0.8, "corpse")
end
-- A 2x2 machine in the next tile whose collision box clips this tile by 0.4,
-- while the requested tile centre stays outside the box.
if scenario == "neighbour" then
  local machine = entity_at("assembling-machine-2", "assembling-machine", {x = 10.6, y = 10.6}, 2, "building")
  machine.bounding_box = {left_top = {x = 9.6, y = 9.6}, right_bottom = {x = 11.6, y = 11.6}}
  world[#world + 1] = machine
end

local surface = {name = "nauvis"}

local function box_contains(box, position)
  return position.x >= box.left_top.x and position.x <= box.right_bottom.x and position.y >= box.left_top.y and position.y <= box.right_bottom.y
end

local function box_overlaps(box, area)
  return box.left_top.x < area[2][1] and box.right_bottom.x > area[1][1] and box.left_top.y < area[2][2] and box.right_bottom.y > area[1][2]
end

local function all_entities()
  local list = {}
  for _, entity in pairs(committed) do
    list[#list + 1] = entity
  end
  for _, entity in ipairs(world) do
    list[#list + 1] = entity
  end
  return list
end

function surface.find_entities_filtered(spec)
  local found = {}
  if stray then
    -- Models a search that returns an entity from elsewhere on the surface.
    found[1] = stray
  end
  for _, entity in ipairs(all_entities()) do
    local keep = true
    if spec.type and entity.type ~= spec.type then
      keep = false
    end
    if keep and spec.position and not box_contains(entity.bounding_box, spec.position) then
      keep = false
    end
    if keep and spec.area and not box_overlaps(entity.bounding_box, spec.area) then
      keep = false
    end
    if keep then
      found[#found + 1] = entity
    end
  end
  return found
end

function surface.can_place_entity(params)
  if explode then
    return false
  end
  if not is_centre(params.position.x) or not is_centre(params.position.y) then
    return false
  end
  return occupied[tile_key(params.position)] ~= true
end

function surface.can_fast_replace(params)
  assert(params.name ~= nil and params.position ~= nil, "can_fast_replace needs a name and position")
  return replaceable and occupied[tile_key(params.position)] == true
end

function surface.create_entity(params)
  if explode then
    error("scripted engine failure")
  end
  local key = tile_key(params.position)
  if occupied[key] then
    if params.fast_replace then
      if not replaceable then
        return nil
      end
    else
      -- The live engine ignores collision boxes when no fast replace is asked
      -- for and stacks a second entity on the same tile.
      error("engine stacked a second entity on tile " .. key)
    end
  end
  if params.fast_replace then
    -- The engine returns the replaced entity to an actor only when one is given.
    -- character is declared after this closure, so identify it by shape.
    assert(params.character ~= nil and params.character.type == "character" and params.character.position.x == 10.5,
      "a fast replace must pass the acting character, not spill")
    if params.player then assert(params.player.name == "wricardo", "a fast replace by a player must pass that player") end
    returned = returned + 1
  end
  local x, y = centre(params.position.x), centre(params.position.y)
  local entity = {
    valid = true,
    name = params.name,
    type = "container",
    position = {x = x, y = y},
    direction = params.direction,
    unit_number = math.floor(x) * 1000 + math.floor(y),
    bounding_box = {left_top = {x = x - 0.5, y = y - 0.5}, right_bottom = {x = x + 0.5, y = y + 0.5}},
    prototype = {collision_mask = MASKS.building},
  }
  if commit_then_raise then
    -- The engine can raise after the entity exists; placeMany must detect it
    -- instead of refunding the item and reporting a failure.
    committed[key] = entity
    error("scripted engine failure after commit")
  end
  if params.name == "underground-belt" then
    entity.type = "underground-belt"
    entity.belt_to_ground_type = params.type or "input"
    -- The engine links a half to a facing half of the same tier that already
    -- exists, wherever and whenever it was built. neighbours is not always a
    -- plain table: it can be an array-like LuaCustomTable (indexed by length) or
    -- a userdata handle where pairs raises.
    if scenario == "underground_userdata" then
      entity.neighbours = io.stdout
    elseif scenario == "underground_array" then
      entity.neighbours = {{valid = true, unit_number = 4242}}
    else
      entity.neighbours = {[4] = {valid = true, unit_number = 4242}}
    end
  end
  return entity
end

local inventory = {count = 100}

function inventory.get_item_count(_)
  return inventory.count
end

function inventory.remove(filter)
  local count = filter.count or 1
  if inventory.count < count then
    return 0
  end
  inventory.count = inventory.count - count
  return count
end

function inventory.insert(filter)
  if next(committed) ~= nil then
    error("item refunded after a committed creation")
  end
  inventory.count = inventory.count + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  position = {x = 10.5, y = 9.5},
  build_distance = 20,
  surface = surface,
  force = player_force,
}

function character.get_main_inventory()
  return inventory
end

game = {
  tick = 4242,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then
      return character
    end
    return nil
  end,
  get_player = function(name)
    if name == "wricardo" then
      -- surface/force are read by the snapshot prelude executeGameOperation adds.
      return {name = name, character = character, force = player_force, surface = surface, get_main_inventory = function() return inventory end}
    end
    return nil
  end,
}

defines = {build_check_type = {manual = 1, script = 2, script_ghost = 3}}

prototypes = {
  item = {
    ["iron-chest"] = {place_result = {name = "iron-chest"}},
    ["transport-belt"] = {place_result = {name = "transport-belt"}},
    ["underground-belt"] = {place_result = {name = "underground-belt"}},
    pipe = {place_result = {name = "pipe"}},
    inserter = {place_result = {name = "inserter"}},
  },
  entity = {
    ["iron-chest"] = {type = "container", collision_mask = MASKS.building},
    ["transport-belt"] = {type = "transport-belt", collision_mask = MASKS.belt},
    ["underground-belt"] = {type = "underground-belt", collision_mask = MASKS.belt},
    pipe = {type = "pipe", collision_mask = MASKS.building},
    inserter = {type = "inserter", collision_mask = MASKS.building},
  },
  quality = {normal = {}, rare = {}},
}

local function encode(value)
  local kind = type(value)
  if kind == "nil" then
    return "null"
  elseif kind == "boolean" then
    return tostring(value)
  elseif kind == "number" then
    if value == math.floor(value) and math.abs(value) < 1e15 then
      return string.format("%d", value)
    end
    return string.format("%.14g", value)
  elseif kind == "string" then
    return '"' .. value:gsub('[%z\1-\31\\"]', function(c)
      return string.format("\\u%04x", string.byte(c))
    end) .. '"'
  elseif kind == "table" then
    if next(value) == nil then
      return "[]"
    end
    if #value > 0 then
      local parts = {}
      for index = 1, #value do
        parts[index] = encode(value[index])
      end
      return "[" .. table.concat(parts, ",") .. "]"
    end
    local parts = {}
    for key, item in pairs(value) do
      parts[#parts + 1] = encode(key) .. ":" .. encode(item)
    end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  error("cannot encode " .. kind)
end

helpers = {table_to_json = encode}
rcon = {print = function(message)
  local message = message
  io.write(tostring(message), "\n")
  
end}
