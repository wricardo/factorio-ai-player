-- Minimal Factorio runtime used by belt_test.go and entity_query_test.go.
-- BELT_SCENARIO selects the world; the fixture asserts the lane-level outcome
-- of the command it just ran, so a replay proves which transport line changed
-- instead of trusting the caller's counters.
local scenario = os.getenv("BELT_SCENARIO") or "belts"

local player_force = {name = "player", get_item_production_statistics = function() return nil end}
local surface = {name = "nauvis"}
local belts = {}
local lane_counts = {}

local function in_area(position, area)
  if not area then return true end
  return position.x >= area[1][1] and position.y >= area[1][2]
    and position.x <= area[2][1] and position.y <= area[2][2]
end

-- One engine transport line. counts is a name -> count table so a test can read
-- the leftover contents after the command ran.
local function make_line(counts)
  local line = {}
  function line.get_contents()
    local out = {}
    for name, count in pairs(counts) do
      if count > 0 then out[#out + 1] = {name = name, count = count, quality = "normal"} end
    end
    table.sort(out, function(a, b) return a.name < b.name end)
    return out
  end
  function line.remove_item(filter)
    if (filter.quality or "normal") ~= "normal" then return 0 end
    local have = counts[filter.name] or 0
    local taken = math.min(have, filter.count or 0)
    counts[filter.name] = have - taken
    return taken
  end
  return line
end

local function add_belt(unit, position, lanes)
  local belt = {
    name = "transport-belt",
    type = "transport-belt",
    unit_number = unit,
    position = {x = position[1], y = position[2]},
    surface = surface,
    force = player_force,
  }
  local lines = {}
  lane_counts[unit] = {}
  for index, counts in ipairs(lanes) do
    lines[index] = make_line(counts)
    lane_counts[unit][index] = counts
  end
  function belt.get_max_transport_line_index() return #lines end
  function belt.get_transport_line(index) return lines[index] end
  function belt.get_fluid_contents() return {} end
  function belt.get_inventory() return {get_contents = function() return {} end} end
  belts[#belts + 1] = belt
  return belt
end

function surface.find_entities_filtered(filter)
  local out = {}
  for _, belt in ipairs(belts) do
    local match = true
    if filter.type ~= nil and belt.type ~= filter.type then match = false end
    if filter.name ~= nil and belt.name ~= filter.name then match = false end
    if not in_area(belt.position, filter.area) then match = false end
    if filter.position ~= nil and filter.radius ~= nil then
      local dx = belt.position.x - filter.position.x
      local dy = belt.position.y - filter.position.y
      match = match and (dx * dx + dy * dy) <= filter.radius * filter.radius
    end
    if match then out[#out + 1] = belt end
  end
  return out
end

local stored = {}
local inventory = {}
function inventory.get_insertable_count(_) return 1000000 end
function inventory.insert(filter)
  stored[filter.name] = (stored[filter.name] or 0) + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  unit_number = 4,
  position = {x = 0.5, y = 0.5},
  surface = surface,
  force = player_force,
}
function character.get_main_inventory() return inventory end

game = {
  tick = 777,
  surfaces = {nauvis = surface},
  players = {},
  forces = {player = player_force},
  get_player = function() return nil end,
  get_entity_by_unit_number = function(unit)
    if unit == 4 then return character end
    for _, belt in ipairs(belts) do
      if belt.unit_number == unit then return belt end
    end
    return nil
  end,
}

defines = {entity_status = {}, transport_line = {left_line = 1, right_line = 2}}
prototypes = {quality = {normal = {}}, item = {}}

if scenario == "belts" then
  -- One mixed lane and one single-item lane, so the read test can prove both
  -- the mixed flag and the per-line identity.
  add_belt(700, {0.5, 0.5}, {{["iron-plate"] = 4, ["copper-plate"] = 1}, {["copper-plate"] = 3}})
elseif scenario == "take-left" or scenario == "take-both" then
  add_belt(700, {0.5, 0.5}, {{["iron-plate"] = 4}, {["copper-plate"] = 3}})
elseif scenario == "take-order" then
  -- The same item on both lanes, so the take order decides which lane empties.
  add_belt(700, {0.5, 0.5}, {{["iron-plate"] = 4}, {["iron-plate"] = 3}})
elseif scenario == "take-invalid-lane" then
  -- A belt that exposes only its left line, so a right-lane request must be
  -- rejected instead of silently draining line 1 again.
  add_belt(701, {0.5, 0.5}, {{["iron-plate"] = 2}})
elseif scenario == "purge-left" or scenario == "purge-limit" or scenario == "purge-target" then
  add_belt(710, {0.5, 0.5}, {{["iron-plate"] = 5, ["copper-plate"] = 1}, {["iron-plate"] = 2}})
  add_belt(711, {1.5, 0.5}, {{["iron-plate"] = 3}, {["copper-plate"] = 2}})
elseif scenario == "purge-missing-lane" then
  add_belt(710, {0.5, 0.5}, {{["iron-plate"] = 5, ["copper-plate"] = 1}, {["iron-plate"] = 2}})
  add_belt(711, {1.5, 0.5}, {{["iron-plate"] = 3}, {["copper-plate"] = 2}})
  add_belt(712, {2.5, 0.5}, {{["iron-plate"] = 4}})
end

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
  local function lane(unit, index, name)
    return lane_counts[unit][index][name] or 0
  end
  if scenario == "take-left" then
    assert(lane(700, 1, "iron-plate") == 0, "the left lane must be emptied")
    assert(lane(700, 2, "copper-plate") == 3, "the right lane must be untouched")
  elseif scenario == "take-both" then
    assert(lane(700, 2, "copper-plate") == 0, "the right lane is the default when lanes are omitted")
    assert(lane(700, 1, "iron-plate") == 4, "an item on the other lane must be untouched")
  elseif scenario == "take-order" then
    assert(lane(700, 1, "iron-plate") == 0, "the left lane must be drained first")
    assert(lane(700, 2, "iron-plate") == 3, "the right lane must keep its items when the left lane satisfies the count")
  elseif scenario == "take-invalid-lane" then
    assert(lane(701, 1, "iron-plate") == 2, "a rejected lane request must not remove items")
  elseif scenario == "purge-left" then
    assert(lane(710, 1, "iron-plate") == 0, "the left lane must lose the item")
    assert(lane(710, 1, "copper-plate") == 1, "other items on that lane must stay")
    assert(lane(710, 2, "iron-plate") == 2, "the unselected right lane must stay")
    assert(lane(711, 1, "iron-plate") == 0, "the sweep covers every belt in the area")
    assert(lane(711, 2, "copper-plate") == 2, "other items must stay")
  elseif scenario == "purge-limit" then
    assert(lane(710, 1, "iron-plate") == 0, "the first belt must be purged")
    assert(lane(711, 1, "iron-plate") == 3, "limit 1 must stop before the second belt")
  elseif scenario == "purge-target" then
    assert(lane(711, 1, "iron-plate") == 0, "the addressed belt must be purged")
    assert(lane(710, 1, "iron-plate") == 5, "other belts must stay")
  elseif scenario == "purge-missing-lane" then
    assert(lane(710, 2, "iron-plate") == 0, "the selected lane must be purged")
    assert(lane(712, 1, "iron-plate") == 4, "a belt without the requested lane must stay intact")
  end
end}
