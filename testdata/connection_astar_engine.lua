-- Minimal Factorio runtime used by connection_astar_replay_test.go for the
-- connectEntities placement loop. The A* plan is computed in Go, so this file
-- only models the apply phase. can_place_entity reports blocked even for tiles
-- the engine accepts, which is the disagreement the placement loop must not
-- trust. CONNECTION_ASTAR_SCENARIO selects the world:
--   snap    - can_place_entity lies; create_entity accepts and snaps
--   blocked - create_entity refuses (genuine collision)
--   engine  - create_entity raises a scripted engine error
local mode = os.getenv("CONNECTION_ASTAR_SCENARIO") or "snap"

local function centre(value)
  return math.floor(value) + 0.5
end

local surface = {name = "nauvis"}

function surface.can_place_entity(_)
  return false
end

function surface.find_entities_filtered(_)
  return {}
end

function surface.create_entity(params)
  if mode == "engine" then
    error("scripted engine failure")
  end
  if mode == "blocked" then
    return nil
  end
  return {
    name = params.name,
    type = "transport-belt",
    position = {x = centre(params.position.x), y = centre(params.position.y)},
    direction = params.direction,
    unit_number = math.floor(params.position.x) * 100 + math.floor(params.position.y),
  }
end

local inventory = {count = 10}

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
  inventory.count = inventory.count + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  position = {x = 0.5, y = 2.5},
  build_distance = 20,
  surface = surface,
  force = {name = "player"},
}

function character.get_main_inventory()
  return inventory
end

game = {
  tick = 6000,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then
      return character
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
  item = {["transport-belt"] = {place_result = {name = "transport-belt", type = "transport-belt"}}},
  entity = {["transport-belt"] = {type = "transport-belt"}},
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
