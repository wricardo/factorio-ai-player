-- Minimal Factorio runtime for resources_addressing_test.go. The world holds one
-- neutral-force stone rock whose entity centre is off the tile the caller names,
-- which is how a rock that is not on the tile grid reports. RESOURCE_SCENARIO
-- selects what mine_entity does: ok removes it, noop returns true without
-- changing the world, partial only shrinks the resource amount.
local scenario = os.getenv("RESOURCE_SCENARIO") or "ok"

local player_force = {name = "player"}
local neutral_force = {name = "neutral"}

local min_x = math.min
local max_x = math.max

local rock = {
  valid = true,
  name = "stone",
  type = "resource",
  position = {x = -12.2, y = 53.3},
  bounding_box = {left_top = {x = -12.7, y = 52.8}, right_bottom = {x = -11.7, y = 53.8}},
  force = neutral_force,
  amount = 149,
}
local world = {rock}

local surface = {name = "nauvis"}
rock.surface = surface
function surface.find_entities_filtered(filter)
  local out = {}
  for _, entity in pairs(world) do
    local kept = true
    if filter.name and entity.name ~= filter.name then kept = false end
    if filter.type and entity.type ~= filter.type then kept = false end
    if kept and filter.force and entity.force ~= filter.force then kept = false end
    if kept and filter.position and filter.radius then
      local box = entity.bounding_box
      kept = box.left_top.x <= filter.position.x + filter.radius
        and box.right_bottom.x >= filter.position.x - filter.radius
        and box.left_top.y <= filter.position.y + filter.radius
        and box.right_bottom.y >= filter.position.y - filter.radius
    end
    if kept then out[#out + 1] = entity end
  end
  return out
end

local inventory_items = {stone = 0}
local inventory = {}
function inventory.get_item_count()
  local total = 0
  for _, count in pairs(inventory_items) do total = total + count end
  return total
end
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
  position = {x = -13.5, y = 54.5},
  surface = surface,
  force = player_force,
}
function character.get_main_inventory() return inventory end
function character.mine_entity(entity, force)
  if not entity.valid then return false end
  if scenario == "noop" then return true end
  if scenario == "partial" then
    entity.amount = (entity.amount or 0) - 5
    inventory_items[entity.name] = (inventory_items[entity.name] or 0) + 5
    return true
  end
  entity.valid = false
  inventory_items[entity.name] = (inventory_items[entity.name] or 0) + (entity.amount or 1)
  return true
end

game = {
  tick = 777,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 2223 then return character end
    return nil
  end,
  get_player = function() return nil end,
}

defines = {controllers = {character = 1, remote = 2}}

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
  assert(min_x and max_x, "sanity")
end}
