-- Minimal Factorio runtime for factory_alerts_bar_replay_test.go. An inserter
-- waiting for space names no cause in its status; a drop target whose bar leaves
-- no usable slot must be reported as DESTINATION_BAR_LIMITED.
-- ALERT_SCENARIO selects the drop target:
--   bar      - chest bar 1, resolved through the inserter's drop_target
--   ok       - chest bar 0 with free slots
--   fallback - drop_target is absent, so the drop position must find the chest
local scenario = os.getenv("ALERT_SCENARIO") or "bar"

local bar = (scenario == "bar" or scenario == "fallback") and 1 or 0
local slots = {}
for index = 1, 32 do
  slots[index] = {valid_for_read = false}
end
local chest_inventory = {}
for index = 1, 32 do chest_inventory[index] = slots[index] end
chest_inventory.supports_bar = function() return true end
chest_inventory.get_bar = function() return bar end

local chest = {
  valid = true,
  name = "wooden-chest",
  type = "container",
  unit_number = 903,
  position = {x = 11.5, y = 5.5},
  get_inventory = function(_) return chest_inventory end,
}

local inserter = {
  valid = true,
  name = "inserter",
  type = "inserter",
  unit_number = 902,
  position = {x = 10.5, y = 5.5},
  direction = 4,
  status = 6,
  drop_position = {x = 11.5, y = 5.5},
}
if scenario ~= "fallback" then inserter.drop_target = chest end

local surface = {name = "nauvis"}
function surface.find_entities_filtered(filter)
  if filter and filter.position then return {chest} end
  return {inserter}
end
function surface.get_tile(_x, _y)
  return {valid = true, name = "grass-1"}
end

local force = {
  name = "player",
  get_spawn_position = function() return {x = 0, y = 0} end,
}

game = {
  tick = 1234,
  surfaces = {nauvis = surface},
  forces = {player = force},
}

defines = {
  entity_status = {working = 1, normal = 2, waiting_for_space_in_destination = 6},
  inventory = {chest = 1},
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
  io.write(tostring(message), "\n")
end}
