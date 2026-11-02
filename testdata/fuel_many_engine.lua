-- Minimal Factorio runtime used by fuel_many_topup_replay_test.go. It models the
-- two fuelling modes: insert exactly countPerTarget, or fill the fuel inventory
-- up to topUpTo so a repeated call adds nothing.
-- FUEL_SCENARIO selects the starting fuel level of each target:
--   full    - every target already holds 10 coal, so topUpTo:10 adds nothing
--   empty   - every target holds no coal
local scenario = os.getenv("FUEL_SCENARIO") or "full"

local held = scenario == "full" and 10 or 0
local surface = {name = "nauvis"}
local force = {name = "player"}

local function furnace(unit)
  local entity = {
    valid = true,
    name = "stone-furnace",
    type = "furnace",
    unit_number = unit,
    position = {x = unit + 0.5, y = 0.5},
    surface = surface,
    force = force,
  }
  local fuel_items = held
  local fuel = {
    get_item_count = function(_) return fuel_items end,
    get_insertable_count = function(_) return 64 - fuel_items end,
    insert = function(filter)
      local count = math.min(filter.count or 1, 64 - fuel_items)
      fuel_items = fuel_items + count
      return count
    end,
  }
  entity.get_fuel_inventory = function() return fuel end
  return entity
end

local furnaces = {[61] = furnace(61), [62] = furnace(62)}
print_count = function() end

function surface.find_entities_filtered(filter)
  local out = {}
  for _, entity in pairs(furnaces) do
    if filter.name == entity.name then out[#out + 1] = entity end
  end
  return out
end

local inventory_items = {["coal"] = 20}
local inventory = {}
function inventory.get_item_count(filter)
  local count = inventory_items[filter.name] or 0
  if filter.count and count > filter.count then return filter.count end
  return count
end
function inventory.remove(filter)
  local have = inventory_items[filter.name] or 0
  local count = filter.count or 1
  if have < count then return 0 end
  inventory_items[filter.name] = have - count
  return count
end
function inventory.insert(filter)
  inventory_items[filter.name] = (inventory_items[filter.name] or 0) + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  position = {x = 0.5, y = 0.5},
  surface = surface,
  force = force,
  build_distance = 200,
}
function character.get_main_inventory() return inventory end

prototypes = {item = {["coal"] = {}}, quality = {normal = {}}}

game = {
  tick = 66,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then return character end
    return furnaces[unit]
  end,
  get_player = function() return nil end,
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
  if scenario == "full" then
    assert(inventory_items["coal"] == 20, "a target that already holds topUpTo must not consume fuel")
  end
  
end}
