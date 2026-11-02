-- Minimal Factorio runtime used by blueprint_place_replay_test.go. build_blueprint
-- returns only the ghosts it created, so the blueprint's own entity count is the
-- only way to tell a partial placement from a full one.
-- BP_SCENARIO selects the world:
--   full - a ten-entity blueprint of which the engine places seven ghosts
--   book - a blueprint book string, which makes is_blueprint_setup raise
local scenario = os.getenv("BP_SCENARIO") or "full"

local inventory = {}
function inventory.destroy() inventory.destroyed = true end

local stack = {}
function stack.set_stack(_) return true end
function stack.import_stack(value)
  assert(value ~= nil and #value > 0, "import_stack needs the blueprint string")
  return 0
end
function stack.is_blueprint_setup()
  if scenario == "book" then error("Not a blueprint") end
  return true
end
function stack.get_blueprint_entity_count() return 10 end
function stack.build_blueprint(params)
  assert(params.direction % 4 == 0, "blueprints only rotate in 90 degree steps")
  assert(params.build_mode ~= nil, "build_blueprint needs a build mode")
  local ghosts = {}
  for index = 1, 7 do
    ghosts[index] = {name = "transport-belt", type = "transport-belt", position = {x = index, y = 0}, unit_number = index}
  end
  return ghosts
end
inventory[1] = stack

game = {
  tick = 12,
  surfaces = {nauvis = {name = "nauvis"}},
  forces = {player = {name = "player"}},
  create_inventory = function() return inventory end,
}

defines = {build_mode = {normal = 1}}

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
  assert(inventory.destroyed == true, "the temporary script inventory must be destroyed")
  
end}
