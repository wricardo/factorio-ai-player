-- Minimal Factorio runtime used by remove_many_replay_test.go. removeMany must
-- report one row per removed target with the items that target returned, plus
-- the aggregate gain, instead of only counts.
-- Units 51 and 52 exist; unit 53 does not, so one target fails.
local surface = {name = "nauvis"}
function surface.find_entities_filtered(filter)
  if filter.name == "wooden-chest" then
    return {
      {valid = true, name = "wooden-chest", type = "container", unit_number = 51, position = {x = 1.5, y = 1.5}, surface = surface, force = player_force},
      {valid = true, name = "wooden-chest", type = "container", unit_number = 52, position = {x = 2.5, y = 1.5}, surface = surface, force = player_force},
    }
  end
  return {}
end

local contents = {
  [51] = {{name = "iron-plate", quality = "normal", count = 3}, {name = "wooden-chest", quality = "normal", count = 1}},
  [52] = {{name = "copper-plate", quality = "normal", count = 2}},
}

local inventory_items = {}
local inventory = {}
function inventory.get_contents()
  local out = {}
  for name, count in pairs(inventory_items) do
    if count > 0 then out[#out + 1] = {name = name, quality = "normal", count = count} end
  end
  return out
end
function inventory.insert(filter)
  inventory_items[filter.name] = (inventory_items[filter.name] or 0) + (filter.count or 1)
  return filter.count or 1
end

player_force = {name = "player"}
local character = {
  type = "character",
  position = {x = 1.5, y = 1.5},
  surface = surface,
  force = player_force,
}
function character.get_main_inventory() return inventory end
function character.mine_entity(entity, _)
  assert(entity.valid, "only a valid entity can be mined")
  entity.valid = false
  for _, stack in pairs(contents[entity.unit_number] or {}) do
    inventory.insert(stack)
  end
  return true
end

game = {
  tick = 55,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then return character end
    return nil
  end,
  get_player = function() return nil end,
}

defines = {controllers = {character = 1}}

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
  assert(inventory_items["iron-plate"] == 3, "the chest contents must reach the actor")
  assert(inventory_items["copper-plate"] == 2, "the second chest contents must reach the actor")
  
end}
