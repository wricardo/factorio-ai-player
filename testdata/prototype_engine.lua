-- Minimal Factorio prototype runtime used by prototype_replay_test.go. Inserter
-- pickup/drop offsets are returned as two-element arrays, the shape the engine
-- serializes for those Vector-valued prototype fields, which is what broke
-- prototype(name:inserter) decoding.
local function vectors(pickup, drop)
  return pickup, drop
end

local inserters = {}

local function add_inserter(name, pickup, drop, tile_width, tile_height)
  local pickup_position, drop_position = vectors(pickup, drop)
  inserters[name] = {
    name = name,
    type = "inserter",
    tile_width = tile_width or 1,
    tile_height = tile_height or 1,
    collision_box = {left_top = {x = -0.15, y = -0.15}, right_bottom = {x = 0.15, y = 0.15}},
    selection_box = {left_top = {x = -0.4, y = -0.4}, right_bottom = {x = 0.4, y = 0.4}},
    inserter_pickup_position = pickup_position,
    inserter_drop_position = drop_position,
    crafting_categories = {crafting = true},
    fluidbox_prototypes = {},
    mineable_properties = {minable = true},
    get_max_health = function(_)
      return 120
    end,
    get_max_wire_distance = function(_)
      return nil
    end,
    get_supply_area_distance = function(_)
      return nil
    end,
    get_crafting_speed = function(_)
      return nil
    end,
  }
end

add_inserter("inserter", {0, -1}, {0, 1.2})
add_inserter("burner-inserter", {0, -1}, {0, 1.2})
add_inserter("long-handed-inserter", {0, -2}, {0, 2.2})

local items = {}
for name in pairs(inserters) do
  items[name] = {
    name = name,
    stack_size = 50,
    stackable = true,
    fuel_category = nil,
    fuel_value = nil,
    place_result = {name = name},
  }
end

game = {tick = 9000}

prototypes = {item = items, entity = inserters}

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
  io.write(message, "\n")
end}
