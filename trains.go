package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This file implements the train API: a paginated train snapshot with schedule,
// cargo and fluid contents, schedule replacement, manual-mode and station
// routing control, and raw engine direction writes. Every mutation resolves the
// acting character, enforces build reach, and returns the post-write state read
// in the same game tick.

// trainsLua holds the Lua helpers shared by the train query and mutations. Every
// engine access is defensive: Factorio returns nil for members a prototype does
// not have, and a train's schedule may be a LuaSchedule or a plain table.
const trainsLua = `
local train_states={}
local ok_train_states,state_defs=pcall(function() return defines.train_state end)
if ok_train_states and state_defs then
  for name,value in pairs(state_defs) do train_states[value]=name end
end
local function trains_field(object,key)
  if object==nil then return nil end
  local ok,value=pcall(function() return object[key] end)
  if ok then return value end
  return nil
end
local function trains_quality_name(quality)
  if type(quality)=="string" then return quality end
  return quality and quality.name
end
local function trains_key_name(key)
  if type(key)=="table" then return key.name end
  return key
end
local function trains_key_quality(key)
  if type(key)=="table" then return trains_quality_name(key.quality) end
  return nil
end
local function trains_sorted_keys(contents)
  local keys={}
  for key in pairs(contents) do keys[#keys+1]=key end
  table.sort(keys,function(a,b) return tostring(trains_key_name(a))<tostring(trains_key_name(b)) end)
  return keys
end
local function trains_contents(contents)
  if not contents then return {} end
  local result={}
  if contents[1]~=nil then
    for _,entry in ipairs(contents) do
      if type(entry)=="table" and entry.name then
        result[#result+1]={name=entry.name,count=entry.count or entry.amount,quality=trains_quality_name(entry.quality) or "normal"}
      end
    end
    table.sort(result,function(a,b) return tostring(a.name)<tostring(b.name) end)
    return result
  end
  for _,key in ipairs(trains_sorted_keys(contents)) do
    local value=contents[key]
    local count=value
    local quality=trains_key_quality(key)
    if type(value)=="table" then
      count=value.count or value.amount
      if quality==nil then quality=trains_quality_name(value.quality) end
    end
    result[#result+1]={name=trains_key_name(key),count=count,quality=quality or "normal"}
  end
  return result
end
local function trains_fluid_contents(contents)
  if not contents then return {} end
  local result={}
  if contents[1]~=nil then
    for _,entry in ipairs(contents) do
      if type(entry)=="table" and entry.name then
        result[#result+1]={name=entry.name,amount=entry.amount}
      end
    end
    table.sort(result,function(a,b) return tostring(a.name)<tostring(b.name) end)
    return result
  end
  for _,key in ipairs(trains_sorted_keys(contents)) do
    local value=contents[key]
    local amount=value
    if type(value)=="table" then amount=value.amount end
    result[#result+1]={name=trains_key_name(key),amount=amount}
  end
  return result
end
local function trains_position(train)
  local front=trains_field(train,"front_stock")
  if front then
    local ok,pos=pcall(function() return front.position end)
    if ok and pos then return pos end
  end
  local carriages=trains_field(train,"carriages") or {}
  for _,carriage in ipairs(carriages) do
    local ok,pos=pcall(function() return carriage.position end)
    if ok and pos then return pos end
  end
  return nil
end
-- SignalID.type is nil for item signals in 2.0.77 and lowercase otherwise, so it
-- is normalized to the SignalType enum name exactly like the settings API: a
-- train wait condition read must round-trip into SignalConditionInput.
local function trains_signal_type(value)
  if value==nil or value=="" then return "ITEM" end
  return string.upper((string.gsub(value,"-","_")))
end
local function trains_signal(signal)
  if not signal then return nil end
  return {name=signal.name,type=trains_signal_type(signal.type),quality=trains_quality_name(signal.quality)}
end
local function trains_circuit_condition(condition)
  if not condition then return nil end
  local first=trains_field(condition,"first_signal")
  local second=trains_field(condition,"second_signal")
  if not first and not second then return nil end
  return {comparator=trains_field(condition,"comparator"),constant=trains_field(condition,"constant"),
    fulfilled=trains_field(condition,"fulfilled"),firstSignal=trains_signal(first),secondSignal=trains_signal(second)}
end
local function trains_wait_condition(condition)
  if not condition then return nil end
  local raw_type=trains_field(condition,"type") or "other"
  local known={time="TIME",full="FULL",empty="EMPTY",not_empty="NOT_EMPTY",item_count="ITEM_COUNT",circuit="CIRCUIT",inactivity="INACTIVITY",robots_inactive="ROBOTS_INACTIVE",fluid_count="FLUID_COUNT",passenger_present="PASSENGER_PRESENT",passenger_not_present="PASSENGER_NOT_PRESENT",fuel_item_count_all="FUEL_ITEM_COUNT_ALL",fuel_item_count_any="FUEL_ITEM_COUNT_ANY",fuel_full="FUEL_FULL",destination_full_or_no_path="DESTINATION_FULL_OR_NO_PATH",request_satisfied="REQUEST_SATISFIED",request_not_satisfied="REQUEST_NOT_SATISFIED",all_requests_satisfied="ALL_REQUESTS_SATISFIED",any_request_not_satisfied="ANY_REQUEST_NOT_SATISFIED",any_request_zero="ANY_REQUEST_ZERO",any_planet_import_zero="ANY_PLANET_IMPORT_ZERO",specific_destination_full="SPECIFIC_DESTINATION_FULL",specific_destination_not_full="SPECIFIC_DESTINATION_NOT_FULL",at_station="AT_STATION",not_at_station="NOT_AT_STATION",damage_taken="DAMAGE_TAKEN"}
  local result={
    type=known[raw_type] or "OTHER",
    raw_type=raw_type,
    compare_type=string.upper(trains_field(condition,"compare_type") or "or"),
    ticks=trains_field(condition,"ticks"),
    station=trains_field(condition,"station"),
    damage=trains_field(condition,"damage"),
    condition=trains_circuit_condition(trains_field(condition,"condition"))
  }
  return result
end
local function trains_schedule_records(train)
  local schedule=trains_field(train,"schedule")
  if not schedule then return {} end
  local records=trains_field(schedule,"records")
  if not records and type(schedule)=="table" and schedule[1] then records=schedule end
  if not records then return {} end
  local result={}
  for _,record in ipairs(records) do
    local station=trains_field(record,"station")
    if type(station)=="table" then station=station.name end
    local conditions=trains_field(record,"wait_conditions") or {}
    local raw_json=nil
    local ok,text=pcall(function() return helpers.table_to_json(conditions) end)
    if ok then raw_json=text end
    local decoded={}
    for _,condition in ipairs(conditions) do decoded[#decoded+1]=trains_wait_condition(condition) end
    result[#result+1]={station=station or "",wait_conditions=decoded,wait_condition_raw_json=raw_json}
  end
  return result
end
-- Categories follow the full 2.0.77 defines.train_state list: a train braking
-- or held at a red signal is BLOCKED, arrive_signal is RUNNING, and a
-- destination without a path or with full stops is BLOCKED.
local function trains_status(state,name)
  local category="OTHER"
  if name=="on_the_path" or name=="arrive_signal" then category="RUNNING"
  elseif name=="wait_signal" or name=="no_path" or name=="no_schedule" or name=="destination_full" then category="BLOCKED"
  elseif name=="arrive_station" or name=="wait_station" or name=="manual_control" or name=="manual_control_stop" then category="IDLE" end
  return {category=category,raw_name=name,raw_code=state,terminal=false}
end
local function trains_info(train)
  local carriages=trains_field(train,"carriages") or {}
  local locomotives=trains_field(train,"locomotives")
  local locomotive_count=0
  if type(locomotives)=="table" then
    locomotive_count=#(trains_field(locomotives,"front_movers") or {})+#(trains_field(locomotives,"back_movers") or {})
    if locomotive_count==0 and #locomotives>0 then locomotive_count=#locomotives end
  end
  local fluid_wagons=trains_field(train,"fluid_wagons") or {}
  local wagon_count=#carriages-locomotive_count
  if wagon_count<0 then wagon_count=0 end
  local carriage_rows={}
  for _,carriage in ipairs(carriages) do
    local ok_position,position=pcall(function() return carriage.position end)
    carriage_rows[#carriage_rows+1]={unit_number=(trains_field(carriage,"unit_number") and tostring(trains_field(carriage,"unit_number")) or nil),
      name=carriage.name,type=carriage.type,position=(ok_position and position or nil),direction=trains_field(carriage,"direction")}
  end
  local station=trains_field(train,"station")
  local station_name=nil
  if station then
    local ok,value=pcall(function() return station.backer_name end)
    if ok then station_name=value end
  end
  local path_end_stop=trains_field(train,"path_end_stop")
  local path_end_stop_name=nil
  if path_end_stop then
    local ok,value=pcall(function() return path_end_stop.backer_name end)
    if ok then path_end_stop_name=value end
  end
  local schedule=trains_field(train,"schedule")
  local schedule_index=trains_field(schedule,"current")
  if type(schedule_index)~="number" then schedule_index=nil end
  local destination=nil
  if schedule and schedule_index then
    local records=trains_field(schedule,"records")
    local record=records and records[schedule_index]
    if record then
      local record_station=trains_field(record,"station")
      if type(record_station)=="table" then destination=record_station.name else destination=record_station end
    end
  end
  local state=trains_field(train,"state")
  if state==nil then state=0 end
  local id=trains_field(train,"id")
  if id==nil then id=trains_field(train,"unit_number") end
  if id==nil then id=0 end
  local speed=trains_field(train,"speed")
  if speed==nil then speed=0 end
  local cargo={}
  local ok_contents,contents=pcall(function() return train.get_contents() end)
  if ok_contents then cargo=trains_contents(contents) end
  local fluids={}
  local ok_fluids,fluid_contents=pcall(function() return train.get_fluid_contents() end)
  if ok_fluids then fluids=trains_fluid_contents(fluid_contents) end
  local state_name=train_states[state]
  local representative=carriages[1] or trains_field(train,"front_stock")
  local surface_name,force_name
  if representative then
    local ok_surface,value_surface=pcall(function() return representative.surface.name end)
    if ok_surface then surface_name=value_surface end
    local ok_force,value_force=pcall(function() return representative.force.name end)
    if ok_force then force_name=value_force end
  end
  return {
    tick=game.tick,
    surface=surface_name,
    force=force_name,
    train_id=tostring(id),
    status=trains_status(state,state_name),
    manual_mode=trains_field(train,"manual_mode") and true or false,
    speed=speed,
    locomotives=locomotive_count,
    wagons=wagon_count,
    fluid_wagons=#fluid_wagons,
    front_position=trains_position(train),
    station=station_name,
    path_end_stop=path_end_stop_name,
    schedule_index=schedule_index,
    destination=destination,
    carriages=carriage_rows,
    schedule=trains_schedule_records(train),
    cargo=cargo,
    fluids=fluids,
  }
end
local function trains_on_surface(train,surface_name)
  local carriages=trains_field(train,"carriages") or {}
  if #carriages==0 then
    local front=trains_field(train,"front_stock")
    if front then
      local ok,name=pcall(function() return front.surface.name end)
      if ok and name then return name==surface_name end
    end
    return true
  end
  for _,carriage in ipairs(carriages) do
    local ok,name=pcall(function() return carriage.surface.name end)
    if ok and name==surface_name then return true end
  end
  return false
end
local function trains_on_force(train,force_name)
  local carriages=trains_field(train,"carriages") or {}
  for _,carriage in ipairs(carriages) do
    local ok,name=pcall(function() return carriage.force.name end)
    if ok and name==force_name then return true end
  end
  return #carriages==0
end
local function trains_point_in_area(area,position)
  if not position then return false end
  return position.x>=area[1][1] and position.x<=area[2][1] and position.y>=area[1][2] and position.y<=area[2][2]
end
local function trains_in_area(train,area)
  if trains_point_in_area(area,trains_position(train)) then return true end
  local carriages=trains_field(train,"carriages") or {}
  for _,carriage in ipairs(carriages) do
    if trains_point_in_area(area,trains_field(carriage,"position")) then return true end
  end
  return false
end
local function trains_fetch(surface,force)
  local ok,trains=pcall(function() return game.train_manager.get_trains({surface=surface,force=force.name}) end)
  if not ok then return nil,tostring(trains) end
  return trains, nil
end
local function trains_page(surface,force,station,area,limit,offset)
  local trains,message=trains_fetch(surface,force)
  if not trains then return nil,message or "could not list trains on this surface" end
  local matches={}
  for _,train in ipairs(trains) do
    if trains_on_surface(train,surface.name) and trains_on_force(train,force.name) then
      local info=trains_info(train)
      local include=true
      if station then
        include=false
        for _,record in ipairs(info.schedule) do
          if record.station==station then include=true break end
        end
      end
      if include and area and not trains_in_area(train,area) then include=false end
      if include then matches[#matches+1]=info end
    end
  end
  table.sort(matches,function(a,b) return (tonumber(a.train_id) or 0)<(tonumber(b.train_id) or 0) end)
  local page={}
  local first=offset+1
  local last=offset+limit
  if last>#matches then last=#matches end
  for i=first,last do page[#page+1]=matches[i] end
  return {tick=game.tick,surface=surface.name,force=force.name,page_info={total=#matches,limit=limit,offset=offset,has_more=#matches>offset+limit},trains=page}
end
`

// trainsQueryLua validates the input and builds the Lua for Query.trains. One
// snapshot of the filtered surface is paginated inside the same Lua call.
func trainsQueryLua(input *TrainsInput) (string, error) {
	surface := "nauvis"
	force := "player"
	limit := 100
	offset := 0
	station := "nil"
	areaLua := "nil"
	if input != nil {
		surface = input.Surface
		force = input.Force
		limit = input.Limit
		offset = input.Offset
		if input.Station != nil {
			if *input.Station == "" {
				return "", errors.New("station must not be empty")
			}
			station = luaString(*input.Station)
		}
		if input.Area != nil {
			bounds := areaBounds{
				LeftTopX:     input.Area.LeftTopX,
				LeftTopY:     input.Area.LeftTopY,
				RightBottomX: input.Area.RightBottomX,
				RightBottomY: input.Area.RightBottomY,
			}
			if err := bounds.validate(); err != nil {
				return "", err
			}
			areaLua = bounds.lua()
		}
	}
	if surface == "" {
		return "", errors.New("surface must not be empty")
	}
	if force == "" {
		return "", errors.New("force must not be empty")
	}
	if limit < 1 || limit > maxPageSize {
		return "", pageLimitError("limit")
	}
	if offset < 0 {
		return "", errors.New("offset must be non-negative")
	}
	return luaFail + trainsLua + fmt.Sprintf(`
local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local f=game.forces[%s]; if not f then fail("force not found"); return end
local page,message=trains_page(s,f,%s,%s,%d,%d)
if not page then fail(message or "could not list trains"); return end
rcon.print(helpers.table_to_json(page))`, luaString(surface), luaString(force), station, areaLua, limit, offset), nil
}

// trainsQuery returns one filtered, paginated snapshot of the trains on a
// surface. Surface and force must exist; the area lookup matches any carriage.
func trainsQuery(r *Resolver, input *TrainsInput) (*TrainPage, error) {
	lua, err := trainsQueryLua(input)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[TrainPage](response)
}

// trainsTargetLua binds the actor and resolves the addressed LuaTrain into
// `train`. The train comes either from an entity target (locomotive or any
// carriage) or from a LuaTrain id; exactly one is required, and a train id needs
// no entity scan or reach check.
func trainsTargetLua(actor *ActorInput, target *EntityRefInput, trainID *string) (string, error) {
	id := ""
	if trainID != nil {
		id = strings.TrimSpace(*trainID)
	}
	hasID := id != ""
	if (target == nil) == !hasID {
		return "", errors.New("give exactly one of target or trainId")
	}
	character, player, err := settingsActorFrom(actor)
	if err != nil {
		return "", err
	}
	actorLua := settingsActorPrefix(character, player)
	if hasID {
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return "", errors.New("trainId must be a numeric LuaTrain id")
		}
		return actorLua + fmt.Sprintf(`local train=game.train_manager.get_train_by_id(%s)
if not train then fail("train not found"); return end
`, id), nil
	}
	resolved, err := settingsTargetFromRef(actor, target)
	if err != nil {
		return "", err
	}
	targetLua, err := resolved.lua()
	if err != nil {
		return "", err
	}
	return targetLua + `local train
local ok,train_value=pcall(function() return e.train end)
if ok then train=train_value end
if not train then fail("entity does not belong to a train"); return end
`, nil
}

// trainsReadLua resolves the target train (prefix must bind `train`) and prints
// its post-write snapshot. body must perform the write and end with a newline.
func trainsReadLua(prefix, body string) (string, error) {
	var b strings.Builder
	b.WriteString(luaFail)
	b.WriteString(trainsLua)
	b.WriteString(prefix)
	b.WriteString(body)
	b.WriteString(`
rcon.print(helpers.table_to_json(trains_info(train)))`)
	return b.String(), nil
}

func trainWaitConditionLua(input *TrainWaitConditionInput) (string, error) {
	if input == nil {
		return "", errors.New("wait condition must not be null")
	}
	if input.Type == TrainWaitConditionTypeOther {
		return "", errors.New("wait condition type OTHER is read-only")
	}
	compareType := TrainWaitCompareTypeOr
	if input.CompareType != nil {
		compareType = *input.CompareType
	}
	parts := []string{
		"type=" + luaString(strings.ToLower(input.Type.String())),
		"compare_type=" + luaString(strings.ToLower(compareType.String())),
	}
	if input.Ticks != nil {
		if math.IsNaN(*input.Ticks) || math.IsInf(*input.Ticks, 0) || *input.Ticks < 0 {
			return "", errors.New("wait condition ticks must be finite and no less than 0")
		}
		parts = append(parts, "ticks="+formatFloat(*input.Ticks))
	}
	if input.Station != nil {
		station := strings.TrimSpace(*input.Station)
		if station == "" {
			return "", errors.New("wait condition station must not be empty")
		}
		parts = append(parts, "station="+luaString(station))
	}
	if input.Damage != nil {
		if *input.Damage < 0 {
			return "", errors.New("wait condition damage must be no less than 0")
		}
		parts = append(parts, "damage="+strconv.Itoa(*input.Damage))
	}
	if input.Condition != nil {
		value, err := conditionLua(*input.Condition)
		if err != nil {
			return "", err
		}
		parts = append(parts, "condition="+value)
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// setTrainScheduleLua validates the schedule input and builds the write plus
// post-write read.
func setTrainScheduleLua(input SetTrainScheduleInput) (string, error) {
	if input.Clear && input.Records != nil {
		return "", errors.New("give either records or clear, not both")
	}
	if !input.Clear && len(input.Records) == 0 {
		return "", errors.New("records are required unless clear is true")
	}
	if len(input.Records) > maxTrainScheduleRecords {
		return "", fmt.Errorf("at most %d schedule records are allowed", maxTrainScheduleRecords)
	}
	var body strings.Builder
	if input.Clear {
		body.WriteString(`local ok_clear,clear_message=pcall(function() train.schedule=nil end)
if not ok_clear then fail(tostring(clear_message)); return end
`)
	} else {
		body.WriteString("local records={\n")
		for _, record := range input.Records {
			if record == nil {
				return "", errors.New("schedule records must not be null")
			}
			if record.Station == "" {
				return "", errors.New("schedule record station must not be empty")
			}
			if len(record.WaitConditions) == 0 {
				return "", fmt.Errorf("schedule record %q needs at least one wait condition", record.Station)
			}
			conditions := make([]string, 0, len(record.WaitConditions))
			for index, condition := range record.WaitConditions {
				waitCondition, err := trainWaitConditionLua(condition)
				if err != nil {
					return "", fmt.Errorf("schedule record %q wait condition %d: %w", record.Station, index+1, err)
				}
				conditions = append(conditions, waitCondition)
			}
			fmt.Fprintf(&body, "  {station=%s,wait_conditions={%s}},\n", luaString(record.Station), strings.Join(conditions, ","))
		}
		body.WriteString(`}
local ok_schedule,schedule_message=pcall(function() train.schedule={current=1,records=records} end)
if not ok_schedule then fail(tostring(schedule_message)); return end
`)
	}
	prefix, err := trainsTargetLua(input.Actor, input.Target, input.TrainID)
	if err != nil {
		return "", err
	}
	return trainsReadLua(prefix, body.String())
}

func setTrainSchedule(r *Resolver, input SetTrainScheduleInput) (*TrainInfo, error) {
	lua, err := setTrainScheduleLua(input)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[TrainInfo](response)
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("cleared the schedule of train %s", result.TrainID)
	if !input.Clear {
		summary = fmt.Sprintf("set the schedule of train %s: %d record(s)", result.TrainID, len(result.Schedule))
	}
	announceMutation(r, input.Message, summary)
	return result, nil
}

// trainControlLua validates the control input and builds the write plus
// post-write read.
func trainControlLua(input TrainControlInput) (string, error) {
	if input.ManualMode == nil && input.GoToStation == nil {
		return "", errors.New("manualMode or goToStation is required")
	}
	station := ""
	if input.GoToStation != nil {
		station = *input.GoToStation
		if station == "" {
			return "", errors.New("goToStation must not be empty")
		}
	}
	var body strings.Builder
	if input.ManualMode != nil {
		fmt.Fprintf(&body, `local ok_manual,manual_message=pcall(function() train.manual_mode=%t end)
if not ok_manual then fail(tostring(manual_message)); return end
`, *input.ManualMode)
	}
	if input.GoToStation != nil {
		fmt.Fprintf(&body, `local index=nil
for i,record in ipairs(trains_schedule_records(train)) do
  if record.station==%s then index=i break end
end
if not index then fail("station not found in train schedule: "..%s); return end
local ok_go,go_result=pcall(function() return train.go_to_station(index) end)
if not ok_go then fail(tostring(go_result)); return end
if go_result==false then fail("train could not go to station: "..%s); return end
`, luaString(station), luaString(station), luaString(station))
	}
	prefix, err := trainsTargetLua(input.Actor, input.Target, input.TrainID)
	if err != nil {
		return "", err
	}
	return trainsReadLua(prefix, body.String())
}

func trainControl(r *Resolver, input TrainControlInput) (*TrainInfo, error) {
	lua, err := trainControlLua(input)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[TrainInfo](response)
	if err != nil {
		return nil, err
	}
	summary := "train " + result.TrainID
	if input.ManualMode != nil {
		summary += fmt.Sprintf(": manual mode %t", *input.ManualMode)
	}
	if input.GoToStation != nil {
		summary += ": sent to station " + *input.GoToStation
	}
	announceMutation(r, input.Message, summary)
	return result, nil
}

// setDirectionLua validates the direction input and builds the write plus
// post-write read.
//
// Two engine facts drive this command, both re-verified on the live save
// (Factorio 2.0.77):
//
//   - Direction is the side an entity faces, not the side it acts on. For an
//     inserter that is its *pickup* side: direction 0 picks up one tile north
//     (0=north, 4=east, 8=south, 12=west) and drops on the opposite tile, so an
//     inserter that must drain a machine into a chest faces the machine.
//   - Assigning e.direction is silently ignored for prototypes whose direction
//     is fixed at placement (transport belts, straight rails), so the write is
//     always verified by readback. rotate{} is the only scripted path for those
//     and it moves one quarter turn per call, so the fallback walks the shorter
//     way round (up to three turns) and stops as soon as a turn changes
//     nothing. When neither the write nor the rotations reach the requested
//     value the call fails instead of reporting a change that did not happen.
//
// The response carries game.tick so a caller can tell a real write from an ack
// that happened in no tick at all.
func setDirectionLua(input SetDirectionInput) (string, error) {
	if input.Direction < 0 || input.Direction > 15 {
		return "", errors.New("direction must be between 0 and 15")
	}
	target, err := settingsTargetFromRef(input.Actor, input.Target)
	if err != nil {
		return "", err
	}
	prefix, err := target.lua()
	if err != nil {
		return "", err
	}
	return luaFail + prefix + fmt.Sprintf(`local before=e.direction
local requested=%d
local ok_direction,direction_message=pcall(function() e.direction=requested end)
if not ok_direction then fail(tostring(direction_message)); return end
local applied=(e.direction==requested)
local turns=0
if not applied then
  local clockwise=(requested-before)%%16
  local counter=(before-requested)%%16
  if clockwise%%4~=0 then
    fail(string.format("cannot rotate %%s to direction %%d: the engine ignored the direct write and rotate only steps a quarter turn at a time, so only 0, 4, 8 and 12 are reachable", tostring(e.name), requested))
    return
  end
  local reverse=counter<clockwise
  local steps=math.floor((reverse and counter or clockwise)/4)
  for index=1,steps do
    local previous=e.direction
    local ok_rotate,rotate_message=pcall(function() return e.rotate{reverse=reverse} end)
    if not ok_rotate then fail(tostring(rotate_message)); return end
    turns=index
    if e.direction==previous then break end
  end
  applied=(e.direction==requested)
end
if not applied then
  fail(string.format("the engine refused the direction change on %%s: the direct write was ignored and rotate left it at direction %%d instead of %%d, so no scripted path reaches that direction; remove it and re-place it with the requested direction", tostring(e.name), e.direction, requested))
  return
end
rcon.print(helpers.table_to_json({tick=game.tick,name=e.name,position=e.position,surface=s.name,before=before,requested=requested,direction=e.direction,turns=turns,unit_number=e.unit_number and tostring(e.unit_number) or nil}))`, input.Direction), nil
}

func setDirection(r *Resolver, input SetDirectionInput) (*ActionResult, error) {
	lua, err := setDirectionLua(input)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ActionResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, "set the direction of "+stringValue(result.Name)+" to "+intValue(result.Direction)+" (was "+intValue(result.Before)+")"+positionText(result.Position))
	return result, nil
}

// trainStopsQuery lists the train stops of one force on one surface. The engine
// stores its maximum uint32 value for an unlimited limit, so that value is
// reported as null rather than a meaningless number.
func trainStopsQuery(r *Resolver, surface, force string) ([]*TrainStop, error) {
	if strings.TrimSpace(surface) == "" {
		return nil, errors.New("surface must not be empty")
	}
	if strings.TrimSpace(force) == "" {
		return nil, errors.New("force must not be empty")
	}
	response, _, err := executeLua(r.addr, r.password, trainStopsLua(surface, force))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[struct {
		TrainStops []*TrainStop `json:"trainStops"`
	}](response)
	if err != nil {
		return nil, err
	}
	if result.TrainStops == nil {
		return []*TrainStop{}, nil
	}
	return result.TrainStops, nil
}

// trainStopsLua renders the read-only train stop listing program.
func trainStopsLua(surface, force string) string {
	return luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local f=game.forces[force_name]; if not f then fail("force not found"); return end
local rows={}
for _,e in ipairs(s.find_entities_filtered{force=f,type="train-stop"}) do
  if e.valid then
    -- A uint32 maximum means the engine limit is disabled.
    local trains_limit=nil
    local ok_limit,limit=pcall(function() return e.trains_limit end)
    if ok_limit and type(limit)=="number" and limit<4294967295 then trains_limit=limit end
    local trains_count,priority,assigned=0,0,0
    local ok_count,count=pcall(function() return e.trains_count end)
    if ok_count and type(count)=="number" then trains_count=count end
    local ok_priority,value=pcall(function() return e.priority end)
    if ok_priority and type(value)=="number" then priority=value end
    local ok_assigned,trains=pcall(function() return e.get_train_stop_trains() end)
    if ok_assigned and type(trains)=="table" then assigned=#trains end
    rows[#rows+1]={unitNumber=(e.unit_number and tostring(e.unit_number) or nil),backerName=e.backer_name,
      position={x=e.position.x,y=e.position.y},trainsLimit=trains_limit,trainsCount=trains_count,
      priority=priority,assignedTrains=assigned}
  end
end
table.sort(rows,function(a,b) return (tonumber(a.unitNumber) or 0)<(tonumber(b.unitNumber) or 0) end)
rcon.print(helpers.table_to_json({trainStops=rows}))`, luaString(surface), luaString(force))
}
