-- Minimal Factorio runtime for remove_single_test.go. The surface is strict
-- about unknown keys the way the real LuaSurface is: reading a field it does
-- not have raises "LuaSurface doesn't contain key <name>". That is what exposes
-- a Lua chunk that glues a trailing `s.name` into the next statement as
-- `s.namelocal`, because a plain Lua table would silently return nil instead.
-- REMOVE_SCENARIO=noop makes mine_entity report success without changing the
-- world, which must fail the removal instead of reporting it.
local scenario = os.getenv("REMOVE_SCENARIO") or "ok"

local player_force = {name = "player"}

local world = {}

local surface = {name = "nauvis"}
function surface.find_entities_filtered(filter)
  local out = {}
  for _, entity in pairs(world) do
    local kept = true
    if filter.name and entity.name ~= filter.name then kept = false end
    if filter.type and entity.type ~= filter.type then kept = false end
    if kept and filter.position and filter.radius then
      local box = entity.bounding_box
      kept = box ~= nil
        and box.left_top.x <= filter.position.x + filter.radius
        and box.right_bottom.x >= filter.position.x - filter.radius
        and box.left_top.y <= filter.position.y + filter.radius
        and box.right_bottom.y >= filter.position.y - filter.radius
    end
    if kept then out[#out + 1] = entity end
  end
  return out
end
setmetatable(surface, {__index = function(_, key)
  error("LuaSurface doesn't contain key " .. tostring(key), 2)
end})

local belt = {
  valid = true,
  name = "transport-belt",
  type = "transport-belt",
  unit_number = 10610,
  position = {x = -31.5, y = 67.5},
  bounding_box = {left_top = {x = -32, y = 67}, right_bottom = {x = -31, y = 68}},
  force = player_force,
  surface = surface,
}
world[#world + 1] = belt

local inventory_items = {}
local inventory = {}
function inventory.get_contents()
  local out = {}
  for name, count in pairs(inventory_items) do
    if count > 0 then out[#out + 1] = {name = name, quality = "normal", count = count} end
  end
  return out
end

local character = {
  valid = true,
  type = "character",
  position = {x = -31.5, y = 68.5},
  surface = surface,
  force = player_force,
}
function character.get_main_inventory() return inventory end
function character.can_reach_entity() return true end
function character.mine_entity(entity, _)
  if not entity.valid then return false end
  if scenario == "noop" then return true end
  entity.valid = false
  inventory_items[entity.name] = (inventory_items[entity.name] or 0) + 1
  inventory_items["iron-plate"] = (inventory_items["iron-plate"] or 0) + 2
  return true
end

game = {
  tick = 2846000,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 10599 then return character end
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
    return '"' .. value:gsub('[%z\1-\31\\"]', function(byte)
      return string.format("\\u%04x", string.byte(byte))
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
  if scenario ~= "noop" then
    assert(belt.valid == false, "the removed belt must leave the world")
    assert(inventory_items["transport-belt"] == 1, "the mined belt must reach the actor")
    assert(inventory_items["iron-plate"] == 2, "the belt contents must reach the actor")
  end
end}
