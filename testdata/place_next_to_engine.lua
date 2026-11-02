-- Minimal Factorio runtime used by place_next_to_replay_test.go. can_place_entity
-- reports blocked even for tiles the engine accepts, the disagreement that made
-- placeNextTo refuse a legal spot; create_entity is the authority.
-- PLACE_NEXT_TO_SCENARIO selects the world:
--   snap    - the check lies; the engine accepts and snaps the position
--   blocked - every candidate is genuinely occupied
--   engine  - create_entity raises a scripted engine error
--   occupied - the first candidate tile already holds an entity, so the
--              placement must skip it instead of stacking a second entity
--   commit_then_raise - create_entity commits the entity, then raises
local scenario = os.getenv("PLACE_NEXT_TO_SCENARIO") or "snap"
local explode = scenario == "engine"
local blocked = scenario == "blocked"
local occupied = scenario == "occupied"
local commit_then_raise = scenario == "commit_then_raise"
-- Entity the engine created before raising, so the mutation's probe can see it.
local committed_entity = nil
local occupant = {
  valid = true,
  name = "small-lamp",
  type = "lamp",
  unit_number = 900,
  bounding_box = {left_top = {x = 3, y = 0}, right_bottom = {x = 4, y = 1}},
}

local function on_occupant_tile(position)
  return occupied and position.x == 3.5 and position.y == 0.5
end

local function centre(value)
  return math.floor(value) + 0.5
end

local surface = {name = "nauvis"}

function surface.can_place_entity(_)
  return false
end

function surface.find_entities_filtered(spec)
  if committed_entity then
    return {committed_entity}
  end
  local box = occupant.bounding_box
  if spec.position then
    if on_occupant_tile(spec.position) then
      return {occupant}
    end
    return {}
  end
  if spec.area then
    -- The occupancy guard searches the whole tile, not the tile centre, so the
    -- stub maps the searched area back to the tile under it.
    local left_top, right_bottom = spec.area[1], spec.area[2]
    local tile = {x = (left_top[1] + right_bottom[1]) / 2, y = (left_top[2] + right_bottom[2]) / 2}
    if on_occupant_tile(tile) and box.left_top.x < right_bottom[1] and box.right_bottom.x > left_top[1] and box.left_top.y < right_bottom[2] and box.right_bottom.y > left_top[2] then
      return {occupant}
    end
    return {}
  end
  return {}
end

function surface.create_entity(params)
  if explode then
    error("scripted engine failure")
  end
  if on_occupant_tile(params.position) then
    -- The live engine ignores collision boxes without fast replace and would
    -- stack a second entity on the occupied tile.
    error("engine stacked a second entity on an occupied tile")
  end
  if blocked then
    return nil
  end
  local entity = {
    valid = true,
    name = params.name,
    type = "ammo-turret",
    position = {x = centre(params.position.x), y = centre(params.position.y)},
    direction = params.direction,
    unit_number = 700,
  }
  if commit_then_raise then
    committed_entity = entity
    error("scripted engine failure after commit")
  end
  return entity
end

local inventory = {count = 5}

function inventory.get_item_count(filter)
  if filter.quality ~= "normal" then
    return 0
  end
  return inventory.count
end

function inventory.remove(filter)
  local count = filter.count or 1
  if inventory.count < count then
    return 0
  end
  inventory.count = inventory.count - count
  return count
end

function inventory.insert(filter)
  if committed_entity then
    error("item refunded after a committed creation")
  end
  inventory.count = inventory.count + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  position = {x = 6.5, y = 0.5},
  build_distance = 20,
  surface = surface,
  force = {name = "player"},
}

function character.get_main_inventory()
  return inventory
end

local furnace = {
  name = "stone-furnace",
  type = "furnace",
  valid = true,
  unit_number = 42,
  position = {x = 0, y = 0},
  bounding_box = {left_top = {x = -1, y = -1}, right_bottom = {x = 1, y = 1}},
  force = {name = "player"},
}

game = {
  tick = 5150,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then
      return character
    end
    if unit == 42 then
      return furnace
    end
    return nil
  end,
  get_player = function(name)
    if name == "wricardo" then
      return {name = name, character = character}
    end
    return nil
  end,
}

defines = {build_check_type = {manual = 1, script = 2, script_ghost = 3}}

prototypes = {
  item = {["gun-turret"] = {place_result = {name = "gun-turret", type = "ammo-turret", tile_width = 2, tile_height = 2}}},
  entity = {["gun-turret"] = {type = "ammo-turret"}},
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
  io.write(message, "\n")
end}
