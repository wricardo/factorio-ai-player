-- Minimal Factorio runtime used by drop_item_replay_test.go. It models the two
-- engine behaviours dropItem must report honestly:
--   * spill_item_stack without force creates items that are NOT marked for
--     deconstruction, and drop_full_stack keeps the stack in one entity;
--   * insert_at creates one belt stack per call, so inserting a whole stack at
--     once loses everything but the first belt stack.
-- DROP_SCENARIO selects the world:
--   ground        - clear ground, the spill succeeds
--   ground_belt   - the target tile holds a belt, so a ground drop is refused
--   belt          - a belt line that accepts exactly one insert at that position
--   belt_full     - a belt line that refuses every insert
local scenario = os.getenv("DROP_SCENARIO") or "ground"

local belt_tile = scenario == "ground_belt"
local surface = {name = "nauvis"}
-- Forward-declared: find_entities_filtered below returns this belt, and the
-- closure must see the local rather than a global.
local belt

function surface.find_entities_filtered(filter)
  if filter.type then
    if belt_tile then
      return {{type = "transport-belt", name = "transport-belt"}}
    end
    return {}
  end
  return {belt}
end

local spilled = {}
function surface.spill_item_stack(params)
  assert(params.force == nil, "spill_item_stack must not pass force: it marks items for deconstruction")
  assert(params.drop_full_stack == true, "a ground drop must spill the whole stack as one entity")
  assert(params.allow_belts == false, "allow_belts must stay false")
  assert(params.stack.count > 0, "spill needs items")
  spilled[#spilled + 1] = params
  return {{valid = true, position = {x = params.position.x, y = params.position.y}, stack = {count = params.stack.count}}}
end

local occupied = false
function surface.create_entity(_)
  error("dropItem must not create entities")
end

local line = {
  valid = true,
  can_insert_at = function(_) return not occupied end,
  insert_at = function(_, stack)
    assert(stack.count == 1, "insert_at must be called with a single item")
    occupied = true
    return true
  end,
}
if scenario == "belt_full" then
  line.can_insert_at = function(_) return true end
  line.insert_at = function(_, stack)
    assert(stack.count == 1, "insert_at must be called with a single item")
    return false
  end
end

belt = {
  type = "transport-belt",
  name = "transport-belt",
  valid = true,
  position = {x = 1.5, y = 2.5},
  get_item_insert_specification = function(_) return 1, 0.5 end,
  get_transport_line = function(_) return line end,
}

local inventory_items = {["iron-plate"] = 5}
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
  local have = inventory_items[filter.name] or 0
  inventory_items[filter.name] = have + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  position = {x = 1.5, y = 1.5},
  surface = surface,
  build_distance = 30,
  force = {name = "player"},
}
function character.get_main_inventory() return inventory end

game = {
  tick = 77,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then return character end
    return nil
  end,
  get_player = function() return nil end,
}

prototypes = {
  item = {["iron-plate"] = {}},
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
  -- The Lua under test prints exactly once, at the end; the engine state is
  -- asserted here because the fixture cannot run code after the command.
  if scenario == "ground" then
    assert(inventory_items["iron-plate"] == 0, "a ground drop must move the whole stack out of the inventory")
  elseif scenario == "belt" then
    assert(inventory_items["iron-plate"] == 4, "a belt drop must remove only the items the line accepted")
  elseif scenario == "belt_full" then
    assert(inventory_items["iron-plate"] == 5, "a refused belt drop must leave the inventory untouched")
  end
  
end}
