-- Minimal Factorio runtime used by stump_corpse_test.go. A tree stump corpse
-- sits inside the probe area overlapping one sampled tile, and a transport belt
-- occupies another. Base-game collision masks: a corpse is {layers={doodad},
-- colliding_with_tiles_only}, so it never obstructs a pipe; a belt carries the
-- object/transport_belt layers, so it does. probePlacement must report the belt
-- tile blocked with its name and leave the corpse tile placeable.
local surface = {name = "nauvis"}

local entities = {
  {
    valid = true, name = "tree-08-stump", type = "corpse",
    position = {x = 2.2, y = 1.5},
    bounding_box = {left_top = {x = 1.8, y = 1.1}, right_bottom = {x = 2.6, y = 1.9}},
    prototype = {collision_mask = {layers = {doodad = true}, colliding_with_tiles_only = true}},
  },
  {
    valid = true, name = "transport-belt", type = "transport-belt",
    position = {x = 3.5, y = 3.5},
    bounding_box = {left_top = {x = 3, y = 3}, right_bottom = {x = 4, y = 4}},
    prototype = {collision_mask = {layers = {floor = true, object = true, transport_belt = true}}},
  },
}

local function in_box(position, box)
  return position.x >= box.left_top.x and position.x <= box.right_bottom.x
    and position.y >= box.left_top.y and position.y <= box.right_bottom.y
end

local function boxes_overlap(box, area)
  -- area is {{x1,y1},{x2,y2}} throughout the API.
  return box.left_top.x < area[2][1] and box.right_bottom.x > area[1][1]
    and box.left_top.y < area[2][2] and box.right_bottom.y > area[1][2]
end

local function masks_intersect(wanted, actual)
  if not wanted then
    return true
  end
  local wanted_layers = wanted.layers
  local actual_layers = actual and actual.layers
  if not wanted_layers or not actual_layers then
    return false
  end
  for layer in pairs(wanted_layers) do
    if actual_layers[layer] then
      return true
    end
  end
  return false
end

function surface.find_entities_filtered(filter)
  local out = {}
  for _, entity in ipairs(entities) do
    local keep = true
    if filter.type and entity.type ~= filter.type then
      keep = false
    end
    if keep and filter.position and not in_box(filter.position, entity.bounding_box) then
      keep = false
    end
    if keep and filter.area and not boxes_overlap(entity.bounding_box, filter.area) then
      keep = false
    end
    if keep and filter.collision_mask and not masks_intersect(filter.collision_mask, entity.prototype.collision_mask) then
      keep = false
    end
    if keep then
      out[#out + 1] = entity
    end
  end
  return out
end

function surface.find_tiles_filtered(_) return {} end

function surface.can_place_entity(params)
  -- A belt is a real obstacle for the engine too; the corpse is not.
  return not (params.position.x == 3.5 and params.position.y == 3.5)
end

function surface.get_tile(x, y)
  return {valid = true, name = "grass-1", position = {x = x, y = y}}
end

game = {
  tick = 2630000,
  surfaces = {nauvis = surface},
  forces = {player = {name = "player"}},
}

prototypes = {
  entity = {
    pipe = {name = "pipe", tile_width = 1, tile_height = 1, collision_mask = {layers = {object = true, player = true, water_tile = true}}},
  },
}

defines = {build_check_type = {manual = 1}}

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
