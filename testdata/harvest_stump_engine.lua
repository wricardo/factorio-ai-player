-- Minimal Factorio runtime used by stump_corpse_test.go. harvestArea must mine
-- trees, destroy the collidable stump corpse each tree mine leaves behind
-- (Factorio 2.0: type corpse, mineable=false), and report any stump destroy
-- refused instead of claiming a clean corridor.
local surface = {name = "nauvis"}

local trees = {
  {
    valid = true, name = "tree-08", type = "tree",
    position = {x = 1.5, y = 1.5},
    bounding_box = {left_top = {x = 0.9, y = 0.9}, right_bottom = {x = 2.1, y = 2.1}},
  },
  {
    valid = true, name = "tree-09", type = "tree",
    position = {x = 4.5, y = 1.5},
    bounding_box = {left_top = {x = 3.9, y = 0.9}, right_bottom = {x = 5.1, y = 2.1}},
  },
}

local corpses = {}

local function in_area(position, area)
  return position.x >= area[1][1] and position.y >= area[1][2]
    and position.x <= area[2][1] and position.y <= area[2][2]
end

local function in_box(position, box)
  return position.x >= box.left_top.x and position.y >= box.left_top.y
    and position.x <= box.right_bottom.x and position.y <= box.right_bottom.y
end

function surface.find_entities_filtered(filter)
  local out = {}
  if filter.type == "corpse" then
    for _, corpse in ipairs(corpses) do
      if corpse.valid then
        if filter.area and in_area(corpse.position, filter.area) then out[#out + 1] = corpse end
        if filter.position and in_box(filter.position, corpse.bounding_box) then out[#out + 1] = corpse end
      end
    end
    return out
  end
  if type(filter.type) == "table" then
    for _, tree in ipairs(trees) do
      if tree.valid and filter.area and in_area(tree.position, filter.area) then out[#out + 1] = tree end
    end
  end
  return out
end

local inventory_items = {}
local inventory = {}
function inventory.get_contents()
  local out = {}
  for name, count in pairs(inventory_items) do
    if count > 0 then out[#out + 1] = {name = name, quality = "normal", count = count} end
  end
  return out
end

local function add_corpse(name, position, destroyable)
  corpses[#corpses + 1] = {
    valid = true, name = name, type = "corpse", position = position,
    bounding_box = {
      left_top = {x = position.x - 0.4, y = position.y - 0.4},
      right_bottom = {x = position.x + 0.4, y = position.y + 0.4},
    },
    destroy = function(self)
      if not destroyable then return false end
      self.valid = false
      return true
    end,
  }
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
  inventory_items["wood"] = (inventory_items["wood"] or 0) + 4
  if entity.name == "tree-08" then
    -- This stump can be cleared.
    add_corpse(entity.name .. "-stump", entity.position, true)
  else
    -- This stump destroy refuses, as a guard against a false clean report.
    add_corpse(entity.name .. "-stump", entity.position, false)
  end
  return true
end

game = {
  tick = 2630000,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 7 then return character end
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
  io.write(tostring(message), "\n")
end}
