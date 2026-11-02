-- Minimal Factorio runtime used by transfer_many_reason_replay_test.go. A chest
-- that rejects every item looks identical for a limited bar, a deconstruction
-- mark and a slot filter, so transferMany has to name the cause.
-- TRANSFER_SCENARIO selects the destination:
--   bar      - the chest bar is 0, so no slot is usable
--   filtered - a slot filter rejects iron-plate
--   decon    - the chest is marked for deconstruction
--   source   - the actor has no iron-plate at all
local scenario = os.getenv("TRANSFER_SCENARIO") or "bar"

local surface = {name = "nauvis"}
-- Forward-declared: find_entities_filtered below returns this chest, and the
-- closure must see the local rather than a global.
local chest
function surface.find_entities_filtered(filter)
  if filter.name == "wooden-chest" then return {chest} end
  return {}
end
local force = {name = "player"}

local actor_items = 10
if scenario == "source" then actor_items = 0 end
local destination_items = 0

local slots = {}
for index = 1, 8 do
  slots[index] = {
    transfer_stack = function(stack, count)
      local moved = math.min(count, stack.count)
      stack.count = stack.count - moved
      if stack.count == 0 then stack.valid_for_read = false end
      destination_items = destination_items + moved
      return moved
    end,
  }
end

-- The inventory is array-like (one entry per slot) plus its methods, so the
-- shared transfer loop can index it the way LuaInventory does.
local chest_inventory = {}
for index = 1, 8 do chest_inventory[index] = slots[index] end
chest_inventory.get_item_count = function(_) return destination_items end
chest_inventory.get_insertable_count = function(_) return 0 end
chest_inventory.supports_bar = function() return scenario == "bar" end
chest_inventory.get_bar = function() return 1 end
chest_inventory.supports_filters = function() return scenario == "filtered" end
chest_inventory.is_filtered = function() return scenario == "filtered" end

chest = {
  valid = true,
  name = "wooden-chest",
  type = "container",
  unit_number = 71,
  position = {x = 3.5, y = 3.5},
  surface = surface,
  force = force,
  to_be_deconstructed = function() return scenario == "decon" end,
  get_inventory = function(_) return chest_inventory end,
  get_fuel_inventory = function() return nil end,
}

local player_slots = {
  {
    valid_for_read = actor_items > 0,
    name = "iron-plate",
    quality = {name = "normal"},
    count = actor_items,
  },
}
local player_inventory = {
  get_item_count = function(_) return player_slots[1].valid_for_read and player_slots[1].count or 0 end,
  get_insertable_count = function(_) return 0 end,
  supports_bar = function() return false end,
}

local character = {
  type = "character",
  object_name = "LuaEntity",
  position = {x = 0.5, y = 0.5},
  surface = surface,
  force = force,
  build_distance = 200,
}
function character.get_main_inventory() return player_inventory end

game = {
  tick = 88,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then return character end
    if unit == 71 then return chest end
    return nil
  end,
  get_player = function() return nil end,
}

defines = {inventory = {chest = 1}}
prototypes = {item = {["iron-plate"] = {}}, quality = {normal = {}}}

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
  assert(destination_items == 0, "no scenario may move items")
  
end}
