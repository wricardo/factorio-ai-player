-- Minimal Factorio runtime used by place_tile_replay_test.go. It models what
-- set_tiles does not do for the caller: replacing a minable player tile
-- destroys its mined product, so placeTile must return it to the actor.
-- TILE_SCENARIO selects the world:
--   same    - the tile already matches, so nothing may be spent
--   replace - stone-path is replaced by concrete, which should return stone-brick
--   blocked - an entity collides with the new tile's mask
local scenario = os.getenv("TILE_SCENARIO") or "replace"

local surface = {name = "nauvis"}
local current = {}
if scenario == "same" then
  current.name = "concrete"
else
  current.name = "stone-path"
end
current.prototype = {
  mineable_properties = {
    minable = true,
    mining_time = 0.5,
    products = {{type = "item", name = "stone-brick", amount = 1}},
  },
}
function current.collides_with(_) return false end

function surface.get_tile(_) return current end

function surface.set_tiles(tiles)
  local tile = tiles[1]
  current = {name = tile.name, prototype = {mineable_properties = {minable = false}}}
  function current.collides_with(_) return false end
end

function surface.find_entities_filtered(filter)
  if filter.area and scenario == "blocked" then
    return {{valid = true, type = "container", name = "wooden-chest", prototype = {collision_mask = {layers = {ground_tile = true}}}}}
  end
  return {}
end

local inventory_items = {["concrete"] = 3, ["stone-brick"] = 0}
local inventory = {}
function inventory.get_item_count(filter) return inventory_items[filter.name] or 0 end
function inventory.remove(filter)
  local have = inventory_items[filter.name] or 0
  local count = filter.count or 1
  if have < count then return 0 end
  inventory_items[filter.name] = have - count
  return count
end
function inventory.insert(filter)
  local have = inventory_items[filter.name] or 0
  inventory_items[filter.name] = have + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  position = {x = 5.5, y = 5.5},
  surface = surface,
  build_distance = 30,
  force = {name = "player"},
}
function character.get_main_inventory() return inventory end

game = {
  tick = 91,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then return character end
    return nil
  end,
  get_player = function() return nil end,
}

prototypes = {
  item = {
    ["concrete"] = {
      place_as_tile_result = {
        result = {name = "concrete", collision_mask = {layers = {ground_tile = true}}},
        condition_size = 1,
        tile_condition = {{name = "stone-path"}, {name = "dirt"}},
      },
    },
  },
  quality = {normal = {}},
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
  if scenario == "replace" then
    assert(inventory_items["concrete"] == 2, "one concrete must be spent")
    assert(inventory_items["stone-brick"] == 1, "the replaced tile's product must go to the actor")
  elseif scenario == "same" then
    assert(inventory_items["concrete"] == 3, "an already matching tile must not spend an item")
  end
  
end}
