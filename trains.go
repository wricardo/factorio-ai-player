package main

import (
	"errors"
	"fmt"
	"math"
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
        result[#result+1]={name=entry.name,count=entry.count or entry.amount,quality=trains_quality_name(entry.quality)}
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
    result[#result+1]={name=trains_key_name(key),count=count,quality=quality}
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
    local wait_seconds=nil
    local condition_json=nil
    local fallback=nil
    local conditions=trains_field(record,"wait_conditions") or {}
    for _,condition in ipairs(conditions) do
      local kind=trains_field(condition,"type")
      if kind=="time" then
        wait_seconds=(tonumber(trains_field(condition,"ticks")) or 0)/60
        break
      end
      if not fallback then fallback=condition end
    end
    if wait_seconds==nil and fallback then
      local ok,text=pcall(function() return helpers.table_to_json(fallback) end)
      if ok then condition_json=text end
    end
    result[#result+1]={station=station or "",wait_seconds=wait_seconds,wait_condition_json=condition_json}
  end
  return result
end
local function trains_info(train)
  local carriages=trains_field(train,"carriages") or {}
  local locomotives=trains_field(train,"locomotives") or {}
  local wagons=trains_field(train,"cargo_wagons")
  local wagon_count
  if wagons then wagon_count=#wagons else wagon_count=#carriages-#locomotives end
  if wagon_count<0 then wagon_count=0 end
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
  return {
    id=tostring(id),
    state=state,
    status_name=train_states[state],
    manual_mode=trains_field(train,"manual_mode") and true or false,
    speed=speed,
    locomotives=#locomotives,
    wagons=wagon_count,
    front_position=trains_position(train),
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
  table.sort(matches,function(a,b) return (tonumber(a.id) or 0)<(tonumber(b.id) or 0) end)
  local page={}
  local first=offset+1
  local last=offset+limit
  if last>#matches then last=#matches end
  for i=first,last do page[#page+1]=matches[i] end
  return {tick=game.tick,surface=surface.name,total=#matches,limit=limit,offset=offset,trains=page}
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
	if limit < 1 || limit > 1000 {
		return "", errors.New("limit must be between 1 and 1000")
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

// trainsTarget builds the actor binding and entity resolution for a train
// mutation. The target may be a locomotive or any carriage.
func trainsTarget(player, character, surface, entity, unitNumber *string, x, y *float64) (settingsTarget, error) {
	target, err := settingsTargetFrom(player, character, surface, entity, unitNumber, x, y)
	if err != nil {
		return settingsTarget{}, err
	}
	if math.IsInf(target.X, 0) || math.IsInf(target.Y, 0) {
		return settingsTarget{}, errors.New("entity x and y must be finite")
	}
	return target, nil
}

// trainsReadLua resolves the target train and prints its post-write snapshot.
// body must perform the write and end with a newline.
func trainsReadLua(target settingsTarget, body string) (string, error) {
	prefix, err := target.lua()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(luaFail)
	b.WriteString(trainsLua)
	b.WriteString(prefix)
	b.WriteString(`local train
local ok,train_value=pcall(function() return e.train end)
if ok then train=train_value end
if not train then fail("entity does not belong to a train"); return end
`)
	b.WriteString(body)
	b.WriteString(`
rcon.print(helpers.table_to_json(trains_info(train)))`)
	return b.String(), nil
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
	if len(input.Records) > 100 {
		return "", errors.New("at most 100 schedule records are allowed")
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
			if math.IsNaN(record.WaitSeconds) || math.IsInf(record.WaitSeconds, 0) || record.WaitSeconds < 0 {
				return "", fmt.Errorf("schedule record %q waitSeconds must be a finite number of seconds no less than 0", record.Station)
			}
			fmt.Fprintf(&body, "  {station=%s,wait_conditions={{type=%s,ticks=math.floor(%s*60)}}},\n",
				luaString(record.Station), luaString("time"), formatFloat(record.WaitSeconds))
		}
		body.WriteString(`}
local ok_schedule,schedule_message=pcall(function() train.schedule={current=1,records=records} end)
if not ok_schedule then fail(tostring(schedule_message)); return end
`)
	}
	target, err := trainsTarget(input.Player, input.Character, input.Surface, input.Entity, input.UnitNumber, input.X, input.Y)
	if err != nil {
		return "", err
	}
	return trainsReadLua(target, body.String())
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
	summary := fmt.Sprintf("cleared the schedule of train %s", result.ID)
	if !input.Clear {
		summary = fmt.Sprintf("set the schedule of train %s: %d record(s)", result.ID, len(result.Schedule))
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
	target, err := trainsTarget(input.Player, input.Character, input.Surface, input.Entity, input.UnitNumber, input.X, input.Y)
	if err != nil {
		return "", err
	}
	return trainsReadLua(target, body.String())
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
	summary := "train " + result.ID
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
// Engine limitation on 2.0.77: assigning e.direction is silently ignored for
// prototypes that are placed with a direction code (transport belts, straight
// rails), so the write is always verified by readback. A one-quarter-turn
// rotate{} fallback covers both clockwise and counter-clockwise neighbours; when
// neither the write nor a rotation reaches the requested value the call fails
// instead of reporting a change that did not happen.
func setDirectionLua(input SetDirectionInput) (string, error) {
	if input.Direction < 0 || input.Direction > 15 {
		return "", errors.New("direction must be between 0 and 15")
	}
	target, err := trainsTarget(input.Player, input.Character, input.Surface, input.Entity, input.UnitNumber, input.X, input.Y)
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
if not applied then
  local clockwise=(requested-before)%%16
  local counter=(before-requested)%%16
  if clockwise==4 or counter==4 then
    local ok_rotate,rotate_message=pcall(function() return e.rotate{reverse=(counter==4)} end)
    if not ok_rotate then fail(tostring(rotate_message)); return end
    applied=(e.direction==requested)
  end
end
if not applied then
  fail("the engine ignored the scripted direction change for this prototype; it does not allow scripted direction changes here, place it with directionCode instead")
  return
end
rcon.print(helpers.table_to_json({name=e.name,position=e.position,surface=s.name,before=before,direction=tostring(e.direction),unit_number=tostring(e.unit_number)}))`, input.Direction), nil
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
	announceMutation(r, input.Message, "set the direction of "+stringValue(result.Name)+" to "+stringValue(result.Direction)+" (was "+intValue(result.Before)+")"+positionText(result.Position))
	return result, nil
}
