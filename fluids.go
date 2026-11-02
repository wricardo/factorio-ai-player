package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// This file implements direct fluid manipulation (SET/ADD/CLEAR on one
// reachable entity) and a read-only aggregate of fluid contents across a
// surface. Both go through the engine's fluid box API so the reported amounts
// are exactly what the simulation holds, never a re-derivation from recipes.

// fluidWriteModes maps the GraphQL enum to the mode string reported by the
// engine-side write. Lookup also validates the enum.
var fluidWriteModes = map[FluidWriteMode]string{
	FluidWriteModeSet:   "SET",
	FluidWriteModeAdd:   "ADD",
	FluidWriteModeClear: "CLEAR",
}

// setFluid writes into one entity's fluid boxes. SET empties every box first so
// the engine clamps the requested amount to capacity; ADD reports the amount the
// engine actually accepted; CLEAR ignores the fluid name and empties the boxes.
func setFluid(r *Resolver, input SetFluidInput) (*FluidWriteResult, error) {
	mode, ok := fluidWriteModes[input.Mode]
	if !ok {
		return nil, errors.New("mode must be SET, ADD, or CLEAR")
	}
	fluid := stringValueOr(input.Fluid, "")
	amount := 0.0
	if input.Amount != nil {
		amount = *input.Amount
	}
	if mode != "CLEAR" {
		if strings.TrimSpace(fluid) == "" {
			return nil, errors.New("fluid is required for SET and ADD")
		}
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			return nil, errors.New("amount must be a finite number of zero or more")
		}
	}
	temperature := math.NaN()
	if input.Temperature != nil {
		if math.IsNaN(*input.Temperature) || math.IsInf(*input.Temperature, 0) {
			return nil, errors.New("temperature must be finite")
		}
		temperature = *input.Temperature
	}
	target, err := settingsTargetFromRef(input.Actor, input.Target)
	if err != nil {
		return nil, err
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	lua := setFluidLua(prefix, mode, fluid, amount, temperature)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[FluidWriteResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fluidWriteSummary(result))
	return result, nil
}

// setFluidLua renders the engine program for a fluid write. prefix binds the
// actor and resolves the target into `e` with reach already enforced.
//
// Amount and capacity share one scope: a box that belongs to an engine fluid
// segment reports the whole segment through get_fluid_segment_contents and
// get_capacity (which 2.0.77 documents as the segment capacity), while a box
// outside any segment reports only its own share. scope names that choice, so a
// client never compares a per-box amount against a segment capacity.
func setFluidLua(prefix, mode, fluid string, amount, temperature float64) string {
	temperatureLua := "nil"
	if !math.IsNaN(temperature) {
		temperatureLua = formatFloat(temperature)
	}
	return luaFail + prefix + fmt.Sprintf(`local mode = %s
local fluid_name = %s
local amount, temperature = %s, %s
if fluid_name ~= "" and not prototypes.fluid[fluid_name] then fail("unknown fluid: "..fluid_name); return end
local function box_count()
  local ok, count = pcall(function() return e.fluids_count end)
  if ok and type(count) == "number" then return count end
  return 0
end
local count = box_count()
local function box_fluid(index)
  local ok, fluid = pcall(function() return e.fluidbox[index] end)
  if ok then return fluid end
  return nil
end
local function box_amount(index)
  local fluid = box_fluid(index)
  if fluid and type(fluid.amount) == "number" then return fluid.amount end
  return 0
end
local function box_segment_id(index)
  local ok, id = pcall(function() return e.fluidbox.get_fluid_segment_id(index) end)
  if ok and id ~= nil then return id end
  return nil
end
-- Build the write scopes: one per engine fluid segment, one per plain box.
local scopes, order = {}, {}
local segment_scopes, entity_scopes = 0, 0
for index = 1, count do
  local id = box_segment_id(index)
  local key, is_segment
  if id ~= nil then key = "segment:"..tostring(id); is_segment = true
  else key = "box:"..index; is_segment = false end
  local scope = scopes[key]
  if not scope then
    scope = {is_segment = is_segment, boxes = {}, capacity = 0}
    scopes[key] = scope
    order[#order+1] = scope
    local ok_capacity, capacity = pcall(function() return e.fluidbox.get_capacity(index) end)
    if ok_capacity and type(capacity) == "number" then scope.capacity = capacity end
    if is_segment then segment_scopes = segment_scopes + 1 else entity_scopes = entity_scopes + 1 end
  end
  scope.boxes[#scope.boxes+1] = index
end
local function scope_fluids(scope)
  local fluids = {}
  if scope.is_segment then
    local ok, contents = pcall(function() return e.fluidbox.get_fluid_segment_contents(scope.boxes[1]) end)
    if ok and type(contents) == "table" then
      for name, value in pairs(contents) do fluids[name] = value end
      return fluids
    end
  end
  local fluid = box_fluid(scope.boxes[1])
  if fluid and fluid.name and fluid.amount then fluids[fluid.name] = fluid.amount end
  return fluids
end
local function total()
  local sum = 0
  for _, scope in ipairs(order) do
    for name, value in pairs(scope_fluids(scope)) do
      if fluid_name == "" or name == fluid_name then sum = sum + value end
    end
  end
  return sum
end
local capacity = 0
for _, scope in ipairs(order) do capacity = capacity + scope.capacity end
local before_amounts = {}
for index = 1, count do before_amounts[index] = box_amount(index) end
local before = total()
local function clear_fluid()
  local ok, message = pcall(function() e.clear_fluid_inside() end)
  if not ok then fail("clear fluid: "..tostring(message)); return false end
  return true
end
local function insert_fluid()
  local params = {name=fluid_name, amount=amount}
  if temperature ~= nil then params.temperature = temperature end
  local ok, inserted = pcall(function() return e.insert_fluid(params) end)
  if not ok then fail("insert fluid: "..tostring(inserted)); return nil end
  if type(inserted) ~= "number" then return 0 end
  return inserted
end
local inserted = 0
if mode == "CLEAR" then
  if not clear_fluid() then return end
elseif mode == "SET" then
  if not clear_fluid() then return end
  inserted = insert_fluid()
  if inserted == nil then return end
else
  inserted = insert_fluid()
  if inserted == nil then return end
end
local after = total()
local boxes = 0
for index = 1, count do
  local current = box_amount(index)
  if (mode == "CLEAR" and before_amounts[index] > 0) or (mode ~= "CLEAR" and current ~= before_amounts[index]) then
    boxes = boxes + 1
  end
end
local scope_name
if segment_scopes > 0 and entity_scopes > 0 then scope_name = "MIXED"
elseif segment_scopes > 0 then scope_name = "SEGMENT"
else scope_name = "ENTITY" end
rcon.print(helpers.table_to_json({tick=game.tick, surface=e.surface.name, force=e.force.name, name=e.name, unitNumber=(e.unit_number and tostring(e.unit_number) or nil), position={x=e.position.x,y=e.position.y}, fluid=(fluid_name ~= "" and fluid_name or nil), before=before, after=after, capacity=capacity, mode=mode, scope=scope_name, boxesAffected=boxes, inserted=inserted}))`,
		luaString(mode), luaString(fluid), formatFloat(amount), temperatureLua)
}

// fluidWriteSummary renders the in-game notification for a fluid write.
func fluidWriteSummary(result *FluidWriteResult) string {
	if result == nil {
		return "fluid write failed"
	}
	if result.Fluid == nil || *result.Fluid == "" {
		return fmt.Sprintf("%s %s fluid boxes%s: %.0f to %.0f of %.0f", result.Mode, result.Name, positionText(result.Position), result.Before, result.After, result.Capacity)
	}
	return fmt.Sprintf("%s %s on %s%s: %.0f to %.0f of %.0f", result.Mode, *result.Fluid, result.Name, positionText(result.Position), result.Before, result.After, result.Capacity)
}

// fluidStockQuery aggregates fluid contents of every fluid box on a surface for
// one force (plus the neutral force) and returns independently paginated
// locations and totals.
func fluidStockQuery(r *Resolver, input *FluidStockInput) (*FluidStockPage, error) {
	if input == nil {
		input = &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 100, TotalsLimit: 100}
	}
	if strings.TrimSpace(input.Surface) == "" {
		return nil, errors.New("surface must not be empty")
	}
	if strings.TrimSpace(input.Force) == "" {
		return nil, errors.New("force must not be empty")
	}
	if input.Limit < 1 || input.Limit > maxPageSize || input.TotalsLimit < 1 || input.TotalsLimit > maxPageSize {
		return nil, pageLimitError("limit and totalsLimit")
	}
	if input.Offset < 0 || input.TotalsOffset < 0 {
		return nil, errors.New("offset and totalsOffset must be non-negative")
	}
	fluid := stringValueOr(input.Fluid, "")
	if input.Fluid != nil && strings.TrimSpace(fluid) == "" {
		return nil, errors.New("fluid must not be empty")
	}
	area := "nil"
	if input.Area != nil {
		bounds := areaBounds{input.Area.LeftTopX, input.Area.LeftTopY, input.Area.RightBottomX, input.Area.RightBottomY}
		if err := bounds.validate(); err != nil {
			return nil, err
		}
		area = bounds.lua()
	}
	lua := fluidStockLua(input.Surface, input.Force, fluid, area, input.Limit, input.Offset, input.TotalsLimit, input.TotalsOffset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[FluidStockPage](response)
}

// fluidStockLua renders the read-only fluid aggregation program. area is either
// "nil" (whole surface) or a bounds constructor.
//
// Every row is one fluidbox index, not one entity: get_fluid_contents only
// returns amounts and loses temperature, while the fluidbox holds name, amount
// and temperature per box. Rows also carry the engine box identity an agent
// needs to address a box: unit number, box index, production type, filter and
// engine fluid segment id.
func fluidStockLua(surface, force, fluid, area string, limit, offset, totalsLimit, totalsOffset int) string {
	fluidTypes := luaKeys([]string{
		"pipe", "pipe-to-ground", "storage-tank", "pump", "boiler", "generator",
		"assembling-machine", "furnace", "fluid-wagon", "mining-drill",
		"offshore-pump", "fluid-turret",
	})
	return luaFail + fmt.Sprintf(`local surface_name, force_name, fluid_name = %s, %s, %s
local area, limit, offset, totals_limit, totals_offset = %s, %d, %d, %d, %d
local fluid_types = %s
local surface = game.surfaces[surface_name]
if not surface then fail("surface not found: "..surface_name); return end
local force = game.forces[force_name]
if not force then fail("force not found: "..force_name); return end
if fluid_name ~= "" and not prototypes.fluid[fluid_name] then fail("unknown fluid: "..fluid_name); return end
local function box_count(entity)
  local ok, count = pcall(function() return entity.fluids_count end)
  if ok and type(count) == "number" then return count end
  return 0
end
-- A merged crafting-machine fluidbox reports several prototypes; the first one
-- carries the box role used for classification.
local function box_prototype(fb, index)
  local ok, proto = pcall(function() return fb.get_prototype(index) end)
  if not ok or not proto then return nil end
  if type(proto) == "table" and proto[1] ~= nil then return proto[1] end
  return proto
end
local function box_segment_id(entity, index)
  local ok, id = pcall(function() return entity.fluidbox.get_fluid_segment_id(index) end)
  if ok and id ~= nil then return tostring(entity.surface.index)..":segment:"..tostring(id) end
  return nil
end
local function box_filter(fb, index)
  local ok, filter = pcall(function() return fb.get_filter(index) end)
  if not ok or not filter then return nil end
  if type(filter) == "table" then return filter.name end
  return nil
end
local entries, totals_by_key = {}, {}
local scan_forces = {force_name}
if force_name ~= "neutral" then scan_forces[#scan_forces+1] = "neutral" end
for _, name in ipairs(scan_forces) do
  local scan_force = game.forces[name]
  if scan_force then
    for _, entity in ipairs(surface.find_entities_filtered{force=scan_force, area=area, type=fluid_types}) do
      if entity.valid then
        local count = box_count(entity)
        local ok_fb, fb = pcall(function() return entity.fluidbox end)
        if ok_fb and fb and count > 0 then
          local unit = entity.unit_number and tostring(entity.unit_number) or nil
          for index = 1, count do
            local ok_fluid, box = pcall(function() return fb[index] end)
            if ok_fluid and box and box.name and (box.amount or 0) > 0 and (fluid_name == "" or box.name == fluid_name) then
              local proto = box_prototype(fb, index)
              local ok_capacity, capacity = pcall(function() return fb.get_capacity(index) end)
              if not ok_capacity or type(capacity) ~= "number" then capacity = nil end
              local key = box.name.."\0stored"
              local total = totals_by_key[key]
              if not total then
                total = {fluid=box.name, category="stored", amount=0}
                totals_by_key[key] = total
              end
              total.amount = total.amount + box.amount
              entries[#entries+1] = {category="stored", entityName=entity.name, entityType=entity.type,
                position={x=entity.position.x, y=entity.position.y}, inventoryKind="fluid",
                unitNumber=unit, boxIndex=index, productionType=(proto and proto.production_type or nil),
                filter=box_filter(fb, index), segmentId=box_segment_id(entity, index),
                fluid=box.name, amount=box.amount, temperature=box.temperature, capacity=capacity}
            end
          end
        end
      end
    end
  end
end
table.sort(entries, function(a, b)
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  if a.fluid ~= b.fluid then return a.fluid < b.fluid end
  if a.entityName ~= b.entityName then return a.entityName < b.entityName end
  if a.entityType ~= b.entityType then return a.entityType < b.entityType end
  return (a.boxIndex or 0) < (b.boxIndex or 0)
end)
local totals = {}
for _, total in pairs(totals_by_key) do totals[#totals+1] = total end
table.sort(totals, function(a, b)
  if a.fluid ~= b.fluid then return a.fluid < b.fluid end
  return a.category < b.category
end)
local page, totals_page = {}, {}
for index = offset+1, math.min(#entries, offset+limit) do page[#page+1] = entries[index] end
for index = totals_offset+1, math.min(#totals, totals_offset+totals_limit) do totals_page[#totals_page+1] = totals[index] end
rcon.print(helpers.table_to_json({tick=game.tick, surface=surface.name, force=force.name, location_page_info={total=#entries,limit=limit,offset=offset,has_more=#entries>offset+limit}, total_page_info={total=#totals,limit=totals_limit,offset=totals_offset,has_more=#totals>totals_offset+totals_limit}, locations=page, totals=totals_page}))`,
		luaString(surface), luaString(force), luaString(fluid), area, limit, offset, totalsLimit, totalsOffset, fluidTypes)
}

// fluidSegmentsQuery reads fluid segments and their classification problems over
// an area in one call, so contamination, blocked outputs and filter mismatches
// are visible without one inspection per entity.
func fluidSegmentsQuery(r *Resolver, input *FluidSegmentsInput) (*FluidSegmentsReport, error) {
	if input == nil {
		return nil, errors.New("input is required")
	}
	if strings.TrimSpace(input.Surface) == "" {
		return nil, errors.New("surface must not be empty")
	}
	if strings.TrimSpace(input.Force) == "" {
		return nil, errors.New("force must not be empty")
	}
	if input.MaxBoxes == 0 {
		input.MaxBoxes = 500
	}
	if input.MaxBoxes < 1 || input.MaxBoxes > maxFluidBoxes {
		return nil, limitError("maxBoxes", maxFluidBoxes)
	}
	area, responseArea, err := worldAreaBoundsLua(input.Area, maxAreaTiles)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, fluidSegmentsLua(input.Surface, input.Force, area, responseArea, input.MaxBoxes))
	if err != nil {
		return nil, err
	}
	return factoryDecode[FluidSegmentsReport](response)
}

// fluidSegmentsLua renders the fluid segment and problem classification program.
// Every visited box is grouped by engine fluid segment id, and a box outside a
// segment forms its own single-box segment. Capacity is the engine segment
// capacity, while OUTPUT_FULL and INPUT_EMPTY compare the box share against the
// box's own prototype volume.
func fluidSegmentsLua(surface, force, area, responseArea string, maxBoxes int) string {
	fluidTypes := luaKeys([]string{
		"pipe", "pipe-to-ground", "storage-tank", "pump", "boiler", "generator",
		"assembling-machine", "furnace", "fluid-wagon", "mining-drill",
		"offshore-pump", "fluid-turret",
	})
	return luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s
local area=%s
local response_area=%s
local max_boxes=%d
local fluid_types=%s
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found: "..surface_name); return end
local force=game.forces[force_name]
if not force then fail("force not found: "..force_name); return end
local segments,order,problems={},{},{}
local visited,truncated=0,false
local function box_prototype(fb,index)
  local ok,proto=pcall(function() return fb.get_prototype(index) end)
  if not ok or not proto then return nil end
  if type(proto)=="table" and proto[1]~=nil then return proto[1] end
  return proto
end
local function box_filter(fb,index)
  local ok,filter=pcall(function() return fb.get_filter(index) end)
  if not ok or not filter then return nil end
  if type(filter)=="table" then return filter.name end
  return nil
end
local function sorted_fluid_names(fluids)
  local names={}
  for name in pairs(fluids) do names[#names+1]=name end
  table.sort(names)
  return names
end
local function problem(kind,entity,position,index,detail)
  problems[#problems+1]={kind=kind,entity=entity.name,
    unitNumber=(entity.unit_number and tostring(entity.unit_number) or nil),
    position=position,boxIndex=index,detail=detail}
end
local function visit_box(entity,fb,index)
  local ok_box,box=pcall(function() return fb[index] end)
  if not ok_box then box=nil end
  local proto=box_prototype(fb,index)
  local filter=box_filter(fb,index)
  local volume=proto and proto.volume or nil
  local ok_id,engine_id=pcall(function() return fb.get_fluid_segment_id(index) end)
  local is_segment=ok_id and engine_id~=nil
  local key
  if is_segment then
    key=tostring(surface.index)..":segment:"..tostring(engine_id)
  else
    key=tostring(surface.index)..":box:"..tostring(entity.unit_number or (entity.position.x..":"..entity.position.y))..":"..tostring(index)
  end
  local segment=segments[key]
  if not segment then
    segment={id=key,engineSegmentId=(is_segment and tostring(engine_id) or nil),capacity=0,
      temperature=nil,amount=0,fluids={},members={},contents_read=false}
    segments[key]=segment
    order[#order+1]=segment
  end
  -- Pipe boxes report their whole segment through get_capacity while machine
  -- boxes report only their own volume, so the largest reported value is the
  -- segment capacity when any pipe is a member.
  local ok_capacity,capacity=pcall(function() return fb.get_capacity(index) end)
  if ok_capacity and type(capacity)=="number" and capacity>segment.capacity then segment.capacity=capacity end
  if is_segment then
    if not segment.contents_read then
      local ok_contents,contents=pcall(function() return fb.get_fluid_segment_contents(index) end)
      if ok_contents and type(contents)=="table" then
        for name,value in pairs(contents) do
          segment.fluids[name]=(segment.fluids[name] or 0)+value
          segment.amount=segment.amount+value
        end
      end
      segment.contents_read=true
    end
  elseif box and box.name and box.amount then
    segment.fluids[box.name]=(segment.fluids[box.name] or 0)+box.amount
    segment.amount=segment.amount+box.amount
  end
  if box and box.temperature~=nil then
    if segment.temperature==nil or box.temperature>segment.temperature then segment.temperature=box.temperature end
  end
  segment.members[#segment.members+1]={unitNumber=(entity.unit_number and tostring(entity.unit_number) or nil),
    name=entity.name,type=entity.type,position={x=entity.position.x,y=entity.position.y},boxIndex=index,
    productionType=(proto and proto.production_type or nil),filter=filter}
  local position={x=entity.position.x,y=entity.position.y}
  local amount=box and box.amount or 0
  if filter then
    local held=nil
    if is_segment then
      local names=sorted_fluid_names(segment.fluids)
      held=names[1]
    elseif box then
      held=box.name
    end
    if held and held~=filter then
      problem("FILTER_MISMATCH",entity,position,index,"box filter expects "..filter.." but holds "..held)
    end
  end
  local production=proto and proto.production_type or nil
  if production=="output" then
    if amount>0 and volume and volume>0 and amount>=volume-0.000001 then
      problem("OUTPUT_FULL",entity,position,index,string.format("output box is full: %%.2f of %%.2f",amount,volume))
    end
  elseif production=="input" then
    if amount<=0 then
      problem("INPUT_EMPTY",entity,position,index,"input box holds no fluid")
    end
  end
end
local function scan_force(scan_force)
  for _,entity in ipairs(surface.find_entities_filtered{force=scan_force,area=area,type=fluid_types}) do
    if truncated then return end
    if entity.valid then
      local ok_count,count=pcall(function() return entity.fluids_count end)
      local ok_fb,fb=pcall(function() return entity.fluidbox end)
      if ok_count and ok_fb and fb and type(count)=="number" then
        for index=1,count do
          if visited>=max_boxes then truncated=true; return end
          visit_box(entity,fb,index)
          visited=visited+1
        end
      end
    end
  end
end
scan_force(force)
if force.name~="neutral" and game.forces.neutral then scan_force(game.forces.neutral) end
local segment_rows={}
for _,segment in ipairs(order) do
  local names=sorted_fluid_names(segment.fluids)
  if #names>1 and #segment.members>0 then
    local first=segment.members[1]
    problems[#problems+1]={kind="MIXED",entity=first.name,unitNumber=first.unitNumber,
      position=first.position,boxIndex=first.boxIndex,
      detail="segment holds mixed fluids: "..table.concat(names,", ")}
  end
  local fluids={}
  for _,name in ipairs(names) do fluids[#fluids+1]={name=name,amount=segment.fluids[name]} end
  table.sort(segment.members,function(a,b)
    local ua,ub=a.unitNumber or "",b.unitNumber or ""
    if ua~=ub then return ua<ub end
    return a.boxIndex<b.boxIndex
  end)
  segment_rows[#segment_rows+1]={id=segment.id,engineSegmentId=segment.engineSegmentId,capacity=segment.capacity,
    temperature=segment.temperature,amount=segment.amount,fluids=fluids,members=segment.members}
end
table.sort(segment_rows,function(a,b) return a.id<b.id end)
table.sort(problems,function(a,b)
  if a.kind~=b.kind then return a.kind<b.kind end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  return a.boxIndex<b.boxIndex
end)
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,area=response_area,
  visitedBoxes=visited,truncated=truncated,segments=segment_rows,problems=problems}))`,
		luaString(surface), luaString(force), area, responseArea, maxBoxes, fluidTypes)
}
