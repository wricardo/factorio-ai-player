-- Minimal Factorio runtime used by set_direction_replay_test.go. It models the
-- three engine behaviours setDirection has to tell apart, and the readback that
-- proves which one happened:
--   writable - assigning e.direction applies (inserters, assembling machines,
--              pipes, splitters): the write is enough and no rotation is needed
--   belted   - the assignment is silently ignored and rotate{} moves exactly one
--              quarter turn (transport belts, straight rails), so a 180-degree
--              request needs two rotations
--   fixed    - the assignment is ignored and rotate{} changes nothing (an entity
--              the engine will not turn at all): the call must fail instead of
--              reporting a direction it never reached
-- The entity starts at direction 8, the value the field report used.
local scenario = os.getenv("SET_DIRECTION_SCENARIO") or "writable"
local tick = 4242

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
      return "{}"
    end
    local parts = {}
    for key, item in pairs(value) do
      parts[#parts + 1] = encode(tostring(key)) .. ":" .. encode(item)
    end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  return "null"
end

-- rcon.print is how the backend hands its result back; fail() is defined by the
-- Go side and prints an error table the same way.
-- rcon.print writes the JSON back to the replay server on stdout, exactly like
-- the live console would.
rcon = {print = function(value)
  io.write(tostring(value), "\n")
end}
helpers = {table_to_json = encode}

local force = {name = "player"}
local surface = {name = "nauvis"}

-- A LuaEntity stand-in whose direction only changes when the scenario allows it.
local function make_entity(name, direction, kind)
  local fields = {
    name = name,
    direction = direction,
    type = kind,
    position = {x = 10.5, y = 10.5},
    unit_number = 77,
    force = force,
    surface = surface,
  }
  -- rotate{} is the only scripted path for a prototype that ignores the direct
  -- assignment. It returns whether the turn happened, exactly like the engine.
  local function rotate(params)
    if scenario == "fixed" then
      return false
    end
    fields.direction = (fields.direction + (params.reverse and 12 or 4)) % 16
    return true
  end
  return setmetatable({}, {
    __index = function(_, key)
      if key == "rotate" then
        return rotate
      end
      return fields[key]
    end,
    __newindex = function(_, key, value)
      if key == "direction" and scenario ~= "writable" then
        -- The live engine accepts this assignment and does nothing with it.
        return
      end
      fields[key] = value
    end,
  })
end

local target = make_entity("inserter", 8, "inserter")

local character = {
  type = "character",
  position = {x = 10.5, y = 11.5},
  surface = surface,
  force = force,
  can_reach_entity = function()
    return true
  end,
}

local player = {
  name = "wricardo",
  force = force,
  surface = surface,
  character = character,
}

game = {
  tick = tick,
  players = {wricardo = player},
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 77 then
      return target
    end
    return nil
  end,
}
