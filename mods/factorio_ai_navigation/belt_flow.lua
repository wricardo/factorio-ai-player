-- Non-invasive transport-line sampling. Only observations of an ID on both
-- sides of a fixed midpoint count; inventory deltas are never used.
local M = {}
local MAX_ACTIVE, MAX_RETAINED, MAX_DURATION = 32, 128, 36000
local MAX_ITEMS_PER_LINE = 4096

local function finite(value)
  return type(value) == "number" and value == value and value ~= math.huge and value ~= -math.huge
end

function M.initialize()
  storage.belt_flow = storage.belt_flow or {jobs = {}, active = {}, next_id = 1}
end

local function copy_position(position)
  return {x = position.x, y = position.y}
end

local function same_position(a, b)
  return math.abs(a.x - b.x) < 0.000001 and math.abs(a.y - b.y) < 0.000001
end

local function contents(line)
  local observed = {}
  local detailed = line.get_detailed_contents()
  if #detailed > MAX_ITEMS_PER_LINE then error("transport line exceeds sampling item bound") end
  for _, item in ipairs(detailed) do
    local stack = item.stack
    if not finite(item.unique_id) or not finite(item.position) or not stack or not stack.valid_for_read then
      error("Factorio >= 2.0.76 detailed transport-line item API required")
    end
    observed[item.unique_id] = {
      position = item.position, count = stack.count, name = stack.name,
      quality = stack.quality.name
    }
  end
  return observed
end

local function rate(count, elapsed)
  if elapsed > 0 then return count * 3600 / elapsed end
end

local function snapshot(job)
  local elapsed = job.updatedTick - job.startedTick
  local lanes, total = {}, 0
  for index, lane in ipairs(job.lanes) do
    local items = {}
    for _, item in pairs(lane.counts) do
      items[#items + 1] = {
        name = item.name, quality = item.quality, itemsCrossed = item.count,
        itemsPerMinute = rate(item.count, elapsed)
      }
    end
    table.sort(items, function(a, b)
      if a.name ~= b.name then return a.name < b.name end
      return a.quality < b.quality
    end)
    lanes[index] = {
      lane = index, measurementPoint = lane.point, itemsCrossed = lane.crossed,
      itemsPerMinute = rate(lane.crossed, elapsed), crossedItems = items
    }
    total = total + lane.crossed
  end
  return {
    id = job.id, status = job.status, reason = job.reason,
    surface = job.surface, force = job.force, name = job.name,
    unitNumber = job.unit and tostring(job.unit) or nil, position = copy_position(job.position),
    startedTick = job.startedTick, updatedTick = job.updatedTick,
    durationTicks = job.duration, elapsedTicks = elapsed,
    itemsCrossed = total, itemsPerMinute = rate(total, elapsed), lanes = lanes
  }
end

local function finish(job, status, reason)
  job.status, job.reason = status, reason
  storage.belt_flow.active[job.id] = nil
  job.entity = nil
  for _, lane in ipairs(job.lanes) do lane.previous = nil end
end

local function prune(state)
  local count, oldest = 0, nil
  for _, job in pairs(state.jobs) do
    count = count + 1
    if job.status ~= "RUNNING" and (not oldest or tonumber(job.id) < tonumber(oldest.id)) then oldest = job end
  end
  if count >= MAX_RETAINED and oldest then state.jobs[oldest.id] = nil end
end

local function start(surface_name, x, y, duration)
  M.initialize()
  if type(surface_name) ~= "string" or surface_name == "" then return {error = "surface is required"} end
  if not finite(x) or not finite(y) or math.abs(x) > 1000000 or math.abs(y) > 1000000 then
    return {error = "belt coordinates must be finite and within +/-1000000"}
  end
  if not finite(duration) or duration % 1 ~= 0 or duration < 1 or duration > MAX_DURATION then
    return {error = "durationTicks must be from 1 to 36000"}
  end
  local state = storage.belt_flow
  local active = 0
  for _ in pairs(state.active) do active = active + 1 end
  if active >= MAX_ACTIVE then return {error = "maximum 32 active belt measurements reached"} end
  local surface = game.surfaces[surface_name]
  if not surface then return {error = "surface not found"} end
  local found = surface.find_entities_filtered{position = {x = x, y = y}, type = "transport-belt"}
  if #found ~= 1 then return {error = "select exactly one normal transport-belt tile; splitters, underground belts and loaders are unsupported"} end
  local entity, lanes = found[1], {}
  local ok, err = pcall(function()
    for index = 1, 2 do
      local line = entity.get_transport_line(index)
      if not line or not line.valid or not finite(line.line_length) or line.line_length <= 0 then
        error("normal belt has no valid transport line")
      end
      -- At most half a lane per tick: otherwise an ID could traverse the
      -- measurement point and leave this tile between consecutive samples.
      if entity.prototype.belt_speed >= line.line_length / 2 then
        error("belt speed is too high for lossless per-tick midpoint sampling")
      end
      local point = line.line_length / 2
      lanes[index] = {
        point = point, length = line.line_length, location = copy_position(line.get_line_item_position(point)),
        previous = contents(line), crossed = 0, counts = {}
      }
    end
  end)
  if not ok then return {error = "cannot measure belt: " .. tostring(err)} end
  prune(state)
  local id = tostring(state.next_id)
  state.next_id = state.next_id + 1
  local job = {
    id = id, status = "RUNNING", entity = entity, unit = entity.unit_number,
    surface = surface.name, surface_index = surface.index, force = entity.force.name,
    name = entity.name, position = copy_position(entity.position), direction = entity.direction,
    duration = duration, startedTick = game.tick, updatedTick = game.tick, lanes = lanes
  }
  state.jobs[id], state.active[id] = job, true
  return snapshot(job)
end

local function sample(job)
  local entity = job.entity
  if not entity or not entity.valid or entity.type ~= "transport-belt" or entity.name ~= job.name
    or entity.unit_number ~= job.unit or entity.surface.index ~= job.surface_index
    or entity.force.name ~= job.force or entity.direction ~= job.direction or not same_position(entity.position, job.position) then
    finish(job, "INVALIDATED", "belt removed, replaced, rotated, relocated, or changed force/surface")
    return
  end
  local observed = {}
  -- Validate both lanes before applying either lane's counts, so an invalid
  -- sample never advances one lane without the other.
  for index, lane in ipairs(job.lanes) do
    local line = entity.get_transport_line(index)
    if not line or not line.valid or line.line_length ~= lane.length
      or not same_position(line.get_line_item_position(lane.point), lane.location) then
      finish(job, "INVALIDATED", "belt lane geometry changed")
      return
    end
    observed[index] = contents(line)
  end
  for index, lane in ipairs(job.lanes) do
    for id, item in pairs(observed[index]) do
      local previous = lane.previous[id]
      if previous and not previous.counted then
        -- Normal belts move monotonically. Accept either coordinate orientation
        -- without assuming lane index implies a global left/right direction.
        local crossed = (previous.position < lane.point and item.position >= lane.point)
          or (previous.position > lane.point and item.position <= lane.point)
        if crossed then
          lane.crossed = lane.crossed + item.count
          local key = item.name .. "\0" .. item.quality
          local total = lane.counts[key]
          if not total then total = {name = item.name, quality = item.quality, count = 0}; lane.counts[key] = total end
          total.count = total.count + item.count
          item.counted = true
        end
      elseif previous then
        item.counted = previous.counted
      end
    end
    lane.previous = observed[index]
  end
  job.updatedTick = game.tick
  if game.tick - job.startedTick >= job.duration then finish(job, "COMPLETE") end
end

function M.on_tick(event)
  local state = storage.belt_flow
  if not state then return end
  for id in pairs(state.active) do
    local job = state.jobs[id]
    if event.tick > job.updatedTick then
      local ok, err = pcall(sample, job)
      if not ok then finish(job, "INVALIDATED", "belt sampling failed: " .. tostring(err)) end
    end
  end
end

remote.add_interface("factorio_ai_belt_flow", {
  version = function() return 1 end,
  start = start,
  status = function(id)
    M.initialize()
    local job = type(id) == "string" and storage.belt_flow.jobs[id] or nil
    if not job then return {error = "belt measurement not found or evicted (128-record retention bound)"} end
    return snapshot(job)
  end
})

return M
