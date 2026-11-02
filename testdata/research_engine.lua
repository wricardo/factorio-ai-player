-- Minimal Factorio runtime used by research_trigger_replay_test.go. In 2.0 base
-- the earliest technologies carry a research_trigger, and add_research returns
-- false for them, so start-research must name the trigger instead of reporting
-- an opaque failure.
-- RESEARCH_SCENARIO selects the technology:
--   trigger  - a trigger technology that add_research refuses
--   ok       - a normal technology that starts
local scenario = os.getenv("RESEARCH_SCENARIO") or "trigger"

local technology = {name = "automation-science-pack", researched = false, enabled = true, prerequisites = {}}
if scenario == "trigger" then
  technology.prototype = {research_trigger = {type = "craft-item", item = "automation-science-pack"}}
else
  technology.prototype = {}
end

local force = {
  name = "player",
  technologies = {["automation-science-pack"] = technology},
  research_queue = {},
  current_research = nil,
  add_research = function(_) return false end,
}

game = {
  tick = 3,
  forces = {player = force},
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
