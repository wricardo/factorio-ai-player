package main

import (
	"errors"
	"fmt"
	"strings"
)

// This file implements the actor gear API: reading a character's armor
// equipment grid and its own logistic requester point, plus moving items
// between the main inventory and the equipment grid and replacing the actor's
// logistic request sections. The reads are part of the player snapshot; the
// writes are mutations that resolve the acting character.

// luaActorGear appends the actor's equipment grid contents and its own logistic
// requester sections to an existing `subject` table. It is concatenated into
// the player snapshot so both the player-with-character and the detached
// character paths report them (c is the character in both cases). Every engine
// access is wrapped in pcall so an armorless character or a character without a
// requester point simply omits the fields instead of failing the query.
const luaActorGear = `
if c then
  local gear_ok,gear_grid=pcall(function() return c.grid end)
  if gear_ok and gear_grid then
    local gear_contents_ok,gear_contents=pcall(function() return gear_grid.get_contents() end)
    if gear_contents_ok and gear_contents then subject.equipment_grid=gear_contents end
  end
  local gear_point_ok,gear_point=pcall(function() return c.get_requester_point() end)
  if gear_point_ok and gear_point then
    local gear_sections={}
    for gear_index=1,20 do
      local gear_section_ok,gear_section=pcall(function() return gear_point.get_section(gear_index) end)
      if not gear_section_ok or not gear_section then break end
      local gear_slots={}
      for gear_slot=1,30 do
        local gear_slot_ok,gear_filter=pcall(function() return gear_section.get_slot(gear_slot) end)
        if not gear_slot_ok or not gear_filter then break end
        local gear_value=gear_filter.value
        local gear_name,gear_quality
        if type(gear_value)=="table" then
          gear_name=gear_value.name
          gear_quality=type(gear_value.quality)=="string" and gear_value.quality or (gear_value.quality and gear_value.quality.name)
        else
          gear_name=gear_value
        end
        if gear_name==nil then break end
        gear_slots[#gear_slots+1]={index=gear_slot,name=gear_name,quality=gear_quality,min=gear_filter.min or 0,max=gear_filter.max or 0}
      end
      gear_sections[#gear_sections+1]={index=gear_index,slots=gear_slots}
    end
    subject.logistic_requests={sections=gear_sections}
  end
end
`

// equipmentItemLua reads the grid contents and counts one item with its
// quality inside the grid.
const equipmentItemLua = `
local function gear_entry_quality(entry)
  local quality=entry.quality
  if type(quality)=="string" then return quality end
  if quality then return quality.name end
  return "normal"
end
local function gear_grid_count()
  local total=0
  for _,entry in ipairs(grid.get_contents()) do
    if entry.name==gear_item and gear_entry_quality(entry)==gear_quality then total=total+(entry.count or 0) end
  end
  return total
end
local function gear_count()
  local ok,count=pcall(function() return grid.count{name=gear_item,quality=gear_quality} end)
  if ok and type(count)=="number" then return count end
  return gear_grid_count()
end
`

// equipGridLua validates the input and builds the mutation Lua.
func equipGridLua(in EquipGridInput) (string, error) {
	unit, player, err := settingsActorFrom(in.Actor)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Item) == "" {
		return "", errors.New("item must not be empty")
	}
	if in.Count < 1 || int64(in.Count) > 4294967295 {
		return "", errors.New("count must be between 1 and 4294967295")
	}
	if in.Direction != EquipmentDirectionToGrid && in.Direction != EquipmentDirectionToMain {
		return "", errors.New("direction must be TO_GRID or TO_MAIN")
	}
	var body strings.Builder
	body.WriteString(luaFail)
	body.WriteString(settingsActorPrefix(unit, player))
	body.WriteString(`local gear_ok,grid=pcall(function() return c.grid end)
if not gear_ok or not grid then fail("character has no equipment grid; wear armor with an equipment grid"); return end
local main=c.get_main_inventory()
if not main then fail("character has no main inventory"); return end
`)
	fmt.Fprintf(&body, "local gear_item=%s\nlocal gear_quality=\"normal\"\nlocal gear_requested=%d\n", luaString(in.Item), in.Count)
	body.WriteString(equipmentItemLua)
	if in.Direction == EquipmentDirectionToGrid {
		body.WriteString(`local gear_available=main.get_item_count{name=gear_item,quality=gear_quality}
if gear_available<gear_requested then fail("main inventory has only "..gear_available.." "..gear_item); return end
-- LuaEquipmentGrid has no capacity query; a full grid is detected when put returns nil below.
local gear_before=gear_count()
local gear_removed=main.remove{name=gear_item,quality=gear_quality,count=gear_requested}
if not gear_removed or gear_removed<gear_requested then
  main.insert{name=gear_item,quality=gear_quality,count=(gear_removed or 0)}
  fail("could not take "..gear_item.." from the main inventory"); return
end
-- 2.0.77 LuaEquipmentGrid.put inserts exactly one equipment, so insert one by one.
local gear_put_error=nil
for gear_index=1,gear_requested do
  local gear_put_ok,gear_put_result=pcall(function() return grid.put{name=gear_item,quality=gear_quality} end)
  if not gear_put_ok then gear_put_error=gear_put_result; break end
  if not gear_put_result then break end
end
local gear_moved=gear_count()-gear_before
if gear_moved<gear_requested then main.insert{name=gear_item,quality=gear_quality,count=gear_requested-gear_moved} end
if gear_moved<1 then fail("equipment grid put failed: "..tostring(gear_put_error or "no equipment inserted")); return end
`)
	} else {
		body.WriteString(`local gear_before=gear_count()
if gear_before<=0 then fail("equipment grid has no "..gear_item); return end
-- 2.0.77 LuaEquipmentGrid.take removes exactly one equipment found by grid.find.
local gear_take_limit=math.min(gear_requested,gear_before)
local gear_moved=0
for gear_index=1,gear_take_limit do
  local gear_find_ok,gear_equipment=pcall(function() return grid.find{name=gear_item,quality=gear_quality} end)
  if not gear_find_ok or not gear_equipment then break end
  local gear_take_ok=pcall(function() return grid.take{equipment=gear_equipment} end)
  if not gear_take_ok then break end
  local gear_accepted=main.insert{name=gear_item,quality=gear_quality,count=1}
  if not gear_accepted or gear_accepted<1 then
    pcall(function() return grid.put{name=gear_item,quality=gear_quality} end)
    break
  end
  gear_moved=gear_moved+1
end
if gear_moved<1 then fail("main inventory cannot accept "..gear_item); return end
`)
	}
	body.WriteString(`local gear_out={tick=game.tick,surface=c.surface.name,force=c.force.name,moved=gear_moved,items=grid.get_contents()}
`)
	if unit != 0 {
		fmt.Fprintf(&body, "gear_out.character=%d\n", unit)
	} else {
		fmt.Fprintf(&body, "gear_out.player=%s\n", luaString(player))
	}
	body.WriteString("rcon.print(helpers.table_to_json(gear_out))\n")
	return body.String(), nil
}

func equipGrid(r *Resolver, in EquipGridInput) (*EquipmentGridResult, error) {
	lua, err := equipGridLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[EquipmentGridResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, fmt.Sprintf("moved %s %s %s; %d stack(s) in the grid",
		numberValue(&result.Moved), in.Item, string(in.Direction), len(result.Items)))
	return result, nil
}

// actorLogisticSectionWrites emits the section replacement statements for the
// actor requester point, using the same slot shape as the container path.
func actorLogisticSectionWrites(sections []*LogisticSectionInput) (check, writes string, err error) {
	return logisticSectionStatements(sections, "gear_point", "gear_section")
}

// actorLogisticRequestsLua validates the input and builds the mutation Lua.
func actorLogisticRequestsLua(in ActorLogisticRequestsInput) (string, error) {
	unit, player, err := settingsActorFrom(in.Actor)
	if err != nil {
		return "", err
	}
	if in.Clear && in.Sections != nil {
		return "", errors.New("clear cannot be combined with sections")
	}
	if !in.Clear && in.Sections == nil {
		return "", errors.New("set sections or clear")
	}
	check, writes, err := actorLogisticSectionWrites(in.Sections)
	if err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString(luaFail)
	body.WriteString(settingsLua)
	body.WriteString(settingsActorPrefix(unit, player))
	body.WriteString(`local gear_point_ok,gear_point=pcall(function() return c.get_requester_point() end)
if not gear_point_ok or not gear_point then fail("actor has no logistic requester point"); return end
`)
	body.WriteString(check)
	body.WriteString("for gear_index=20,1,-1 do gear_point.remove_section(gear_index) end\n")
	body.WriteString(writes)
	body.WriteString(`local gear_logistic=settings_logistic_point(gear_point)
local gear_out={tick=game.tick,surface=c.surface.name,force=c.force.name,sections=(gear_logistic and gear_logistic.sections) or {}}
`)
	if unit != 0 {
		fmt.Fprintf(&body, "gear_out.character=%d\n", unit)
	} else {
		fmt.Fprintf(&body, "gear_out.player=%s\n", luaString(player))
	}
	body.WriteString("rcon.print(helpers.table_to_json(gear_out))\n")
	return body.String(), nil
}

func setActorLogisticRequests(r *Resolver, in ActorLogisticRequestsInput) (*ActorLogisticResult, error) {
	lua, err := actorLogisticRequestsLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ActorLogisticResult](response)
	if err != nil {
		return nil, err
	}
	slots := 0
	for _, section := range result.Sections {
		slots += len(section.Slots)
	}
	announceMutation(r, in.Message, fmt.Sprintf("%d logistic section(s), %d slot(s)", len(result.Sections), slots))
	return result, nil
}
