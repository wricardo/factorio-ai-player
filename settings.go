package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// This file implements the entity settings API: splitter filters and lane
// priorities, inserter filters, lamp colors, container bar limits and logistic
// requests, train stop limits, rocket silo launch settings, generic control
// behavior flags, and circuit wire connections. Every write resolves the acting
// character, enforces build reach, and returns one snapshot of the post-write
// state so callers can verify without a second round trip.

// settingsTarget addresses the entity a settings operation acts on.
type settingsTarget struct {
	Player     string
	Character  string
	Surface    string
	Entity     string
	UnitNumber string
	X, Y       float64
}

// settingsInventoryNames maps the GraphQL inventory keys to engine inventory
// constants. Only inventories that can carry a bar limit are listed.
var settingsInventoryNames = map[string]string{
	"chest":       "chest",
	"fuel":        "fuel",
	"input":       "crafter_input",
	"output":      "crafter_output",
	"modules":     "crafter_modules",
	"turret-ammo": "turret_ammo",
}

// settingsActorPrefix binds p (the actor) and c (its control entity) and sets
// actor_surface. Exactly one of player and character must be given.
func settingsActorPrefix(character uint64, player string) string {
	if character != 0 {
		return fmt.Sprintf(`local p=game.get_entity_by_unit_number(%d); if not p or p.type~="character" then fail("character not found"); return end
local c=p
`, character)
	}
	return fmt.Sprintf(`local p=game.players[%s]; if not p then fail("player not found"); return end
local c=p.character; if not c then fail("player has no character"); return end
`, luaString(player))
}

// lua prepares the actor, resolves the target into `e` and enforces build reach.
func (t settingsTarget) lua() (string, error) {
	surface := t.Surface
	if surface == "" {
		surface = "nauvis"
	}
	character, player, err := settingsActor(t.Character, t.Player)
	if err != nil {
		return "", err
	}
	name := t.Entity
	unit, err := entityTarget(name, t.UnitNumber, t.X, t.Y)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(settingsActorPrefix(character, player))
	if character != 0 {
		b.WriteString("local actor_surface=c.surface.name\n")
		b.WriteString(targetLocals(unit, name, t.X, t.Y, ""))
		b.WriteString("surface_name=actor_surface\n")
	} else {
		b.WriteString(targetLocals(unit, name, t.X, t.Y, surface))
	}
	b.WriteString(luaEntityResolve)
	b.WriteString(resolveTargetLua())
	b.WriteString("if " + reachViolation("c", "e") + ` then fail("entity is out of reach"); return end
`)
	return b.String(), nil
}

// settingsActor validates the actor fields and returns its unit number or name.
func settingsActor(character, player string) (uint64, string, error) {
	hasCharacter := character != ""
	hasPlayer := player != ""
	if hasCharacter == hasPlayer {
		return 0, "", errors.New("exactly one of player or character is required")
	}
	if hasCharacter {
		unit, err := strconv.ParseUint(character, 10, 32)
		if err != nil || unit == 0 {
			return 0, "", errors.New("character must be a character unit number from 1 to 4294967295")
		}
		return unit, "", nil
	}
	return 0, player, nil
}

// settingsLogisticSectionsLua reads one LuaLogisticPoint or
// LuaConstantCombinatorControlBehavior into sections. Both expose
// LuaLogisticSection, so container requests, actor requester points and constant
// combinator slots share this reader. It is self-contained so it can also be
// spliced into player snapshots that do not define the rest of settingsLua.
//
// Slot iteration is bounded by the engine's filters_count instead of stopping at
// the first empty slot, so a layout with gaps keeps every request after the gap,
// and an infinite request (the engine stores max nil) is reported with max
// absent instead of as 0.
const settingsLogisticSectionsLua = `local function settings_logistic_sections(logistic)
  local sections={}
  if not logistic then return sections end
  local section_count=20
  local ok_section_count,read_section_count=pcall(function() return logistic.sections_count end)
  if ok_section_count and type(read_section_count)=="number" then section_count=read_section_count end
  if section_count<1 then return sections end
  for index=1,section_count do
    local ok_section,section=pcall(function() return logistic.get_section(index) end)
    if not ok_section or not section then break end
    local slots={}
    local slot_count=30
    local ok_slot_count,read_slot_count=pcall(function() return section.filters_count end)
    if ok_slot_count and type(read_slot_count)=="number" then slot_count=read_slot_count end
    for slot=1,slot_count do
      local ok_filter,filter=pcall(function() return section.get_slot(slot) end)
      if ok_filter and filter then
        local value=filter.value
        local name,quality,signal_type
        if type(value)=="table" then
          name=value.name
          signal_type=value.type
          quality=value.quality
        else
          name=value
        end
        if type(quality)~="string" then quality=quality and quality.name or nil end
        if name~=nil then
          -- Normalize here rather than calling signal_type_name: this reader is
          -- also spliced into player snapshots that do not define the shared
          -- helper, and a raw engine type ("fluid") cannot decode into the
          -- SignalType enum.
          if signal_type==nil or signal_type=="" then signal_type="ITEM"
          else signal_type=string.upper((string.gsub(signal_type,"-","_"))) end
          slots[#slots+1]={index=slot,name=name,type=signal_type,quality=quality,min=filter.min or 0,max=filter.max}
        end
      end
    end
    local multiplier=section.multiplier
    if type(multiplier)~="number" then multiplier=1 end
    sections[#sections+1]={index=index,active=section.active~=false,multiplier=multiplier,group=section.group or "",slots=slots}
  end
  return sections
end
`

// luaSignalTypeName normalizes an engine SignalIDType name ("item",
// "space-location") to the GraphQL SignalType enum name ("ITEM",
// "SPACE_LOCATION"), so a signal read round-trips into SignalInput. Spliced into
// every chunk that serializes a SignalID or SignalType row.
const luaSignalTypeName = `local function signal_type_name(value)
  if value==nil or value=="" then return "ITEM" end
  return string.upper((string.gsub(value,"-","_")))
end
`

// settingsLua is shared by every settings read and write.
const settingsLua = luaSignalTypeName + luaInserterSettingsBody + settingsLogisticSectionsLua + `
local function settings_field(object,key)
  local ok,value=pcall(function() return object[key] end)
  if ok then return value end
  return nil
end
local function settings_field_any(object,keys)
  for _,key in ipairs(keys) do
    local ok,value=pcall(function() return object[key] end)
    if ok then return value end
  end
  return nil
end
local function settings_quality_name(quality)
  if type(quality)=="string" then return quality end
  return quality and quality.name
end
-- SignalID.type is nil for item signals in 2.0.77 and lowercase otherwise, so it
-- is normalized to the SignalType enum name: a read must round-trip into
-- SignalInput/SignalConditionInput.
local function settings_signal(signal)
  if not signal then return nil end
  return {name=signal.name,type=signal_type_name(signal.type),quality=settings_quality_name(signal.quality)}
end
local function settings_condition(condition)
  if not condition then return nil end
  if not condition.first_signal and not condition.second_signal then return nil end
  return {comparator=condition.comparator,constant=condition.constant,fulfilled=condition.fulfilled,
    firstSignal=settings_signal(condition.first_signal),secondSignal=settings_signal(condition.second_signal)}
end
local function settings_networks(selection)
  if not selection then return nil end
  return {red=selection.red,green=selection.green}
end
local function settings_color(color)
  if not color then return nil end
  return {r=color.r,g=color.g,b=color.b,a=color.a}
end
-- defines.control_behavior.lamp.color_mode is an enum table, so the engine value
-- is mapped back to its canonical enum name for the GraphQL enum.
local function settings_lamp_color_mode(control)
  local ok,value=pcall(function() return control.color_mode end)
  if not ok or value==nil then return nil end
  for name,id in pairs(defines.control_behavior.lamp.color_mode) do
    if id==value then return string.upper(name) end
  end
  return nil
end
local function settings_item_filter(filter)
  if not filter then return nil end
  local name=filter.name
  if type(name)=="table" then name=name.name end
  return {name=name,quality=settings_quality_name(filter.quality),comparator=filter.comparator}
end
local function settings_common(e)
  return {tick=game.tick,surface=e.surface.name,force=e.force.name,name=e.name,unitNumber=e.unit_number,position={x=e.position.x,y=e.position.y}}
end
local function settings_control(e)
  local ok,control=pcall(function() return e.get_control_behavior() end)
  if ok then return control end
  return nil
end
local function settings_circuit(e)
  local control=settings_control(e)
  if not control then return nil end
  local result={behaviorType=control.type}
  local disabled=settings_field(control,"disabled")
  if disabled~=nil then result.enabled=not disabled end
  result.circuitEnableDisable=settings_field(control,"circuit_enable_disable")
  result.circuitCondition=settings_condition(settings_field(control,"circuit_condition"))
  result.logisticCondition=settings_condition(settings_field(control,"logistic_condition"))
  result.readContents=settings_field_any(control,{"circuit_read_contents","read_contents"})
  result.setRecipe=settings_field_any(control,{"circuit_set_recipe"})
  result.readIngredients=settings_field_any(control,{"circuit_read_ingredients"})
  result.readWorking=settings_field_any(control,{"circuit_read_working"})
  result.readResources=settings_field_any(control,{"circuit_read_resources"})
  result.resourceReadMode=settings_field_any(control,{"resource_read_mode"})
  result.readAmmo=settings_field_any(control,{"read_ammo"})
  result.readLogistics=settings_field_any(control,{"read_logistics"})
  result.readRobotStats=settings_field_any(control,{"read_robot_stats"})
  result.readItemsMode=settings_field_any(control,{"read_items_mode"})
  result.setFilters=settings_field(control,"circuit_set_filters")
  result.readHandContents=settings_field(control,"circuit_read_hand_contents")
  result.handReadMode=settings_field(control,"circuit_hand_read_mode")
  result.stackControlSignal=settings_signal(settings_field(control,"circuit_stack_control_signal"))
  result.setStackSize=settings_field(control,"circuit_set_stack_size")
  result.connectToLogisticNetwork=settings_field(control,"connect_to_logistic_network")
  result.useColors=settings_field(control,"use_colors")
  result.colorMode=settings_lamp_color_mode(control)
  result.lampColor=settings_color(settings_field(control,"color"))
  result.redSignal=settings_signal(settings_field(control,"red_signal"))
  result.greenSignal=settings_signal(settings_field(control,"green_signal"))
  result.blueSignal=settings_signal(settings_field(control,"blue_signal"))
  result.rgbSignal=settings_signal(settings_field(control,"rgb_signal"))
  result.contentsReadMode=settings_field(control,"read_contents_mode")
  result.readRecipeFinished=settings_field(control,"circuit_read_recipe_finished")
  result.recipeFinishedSignal=settings_signal(settings_field(control,"circuit_recipe_finished_signal"))
  result.workingSignal=settings_signal(settings_field(control,"circuit_working_signal"))
  result.includeInCrafting=settings_field(control,"include_in_crafting")
  result.includeFuel=settings_field(control,"include_fuel")
  result.setFilter=settings_field(control,"set_filter")
  result.splitterInputSide=settings_field(control,"set_input_side")
  result.splitterOutputSide=settings_field(control,"set_output_side")
  result.inputLeftCondition=settings_condition(settings_field(control,"input_left_condition"))
  result.inputRightCondition=settings_condition(settings_field(control,"input_right_condition"))
  result.outputLeftCondition=settings_condition(settings_field(control,"output_left_condition"))
  result.outputRightCondition=settings_condition(settings_field(control,"output_right_condition"))
  result.sendToTrain=settings_field(control,"send_to_train")
  result.readFromTrain=settings_field(control,"read_from_train")
  result.readStoppedTrain=settings_field(control,"read_stopped_train")
  result.setTrainsLimit=settings_field(control,"set_trains_limit")
  result.readTrainsCount=settings_field(control,"read_trains_count")
  result.stoppedTrainSignal=settings_signal(settings_field(control,"stopped_train_signal"))
  result.trainsCountSignal=settings_signal(settings_field(control,"trains_count_signal"))
  result.trainsLimitSignal=settings_signal(settings_field(control,"trains_limit_signal"))
  result.setPriority=settings_field(control,"set_priority")
  result.prioritySignal=settings_signal(settings_field(control,"priority_signal"))
  result.siloReadMode=settings_field(control,"read_mode")
  result.circuitConditionEnabled=settings_field(control,"circuit_condition_enabled")
  result.exclusiveModeOfOperation=settings_field(control,"circuit_exclusive_mode_of_operation")
  return result
end
local function settings_inventory_index(name)
  local map={chest=defines.inventory.chest,fuel=defines.inventory.fuel,
    input=defines.inventory.crafter_input,output=defines.inventory.crafter_output,
    modules=defines.inventory.crafter_modules,["turret-ammo"]=defines.inventory.turret_ammo}
  return map[name]
end
-- get_inventory_bar is the index at which the red (blocked) area starts, so the
-- usable slot count is one less. Zero means the inventory has no limit, which
-- stays nil so that "no limit" and "zero usable slots" (bar 1) differ.
local function settings_container(e,inventory_name)
  local index=settings_inventory_index(inventory_name)
  if not index then return nil end
  local ok,supported=pcall(function() return e.inventory_supports_bar(index) end)
  if not ok or not supported then return nil end
  local okbar,bar=pcall(function() return e.get_inventory_bar(index) end)
  if not okbar or type(bar)~="number" then bar=0 end
  local inventory=e.get_inventory(index)
  local capacity=inventory and #inventory or 0
  if capacity>0 and bar>capacity+1 then bar=capacity+1 end
  local allowed_slots
  if bar>0 then
    allowed_slots=bar-1
    if capacity>0 and allowed_slots>capacity then allowed_slots=capacity end
  end
  return {inventory=inventory_name,bar=bar,capacity=capacity,allowedSlots=allowed_slots}
end
local function settings_infinity(e)
  if e.type=="infinity-pipe" then
    local out={filters={},fluidFilters={},removeUnfilteredItems=false}
    local ok_filter,filter=pcall(function() return e.get_infinity_pipe_filter() end)
    if ok_filter and filter then
      out.fluidFilters[#out.fluidFilters+1]={fluid=filter.name,percentage=filter.percentage,
        temperature=filter.temperature,mode=filter.mode or "at-least"}
    end
    return out
  end
  if e.type~="infinity-container" then return nil end
  local out={filters={},fluidFilters={},removeUnfilteredItems=false}
  local ok_filters,filters=pcall(function() return e.infinity_container_filters end)
  if not ok_filters or type(filters)~="table" then return out end
  for index,filter in ipairs(filters) do
    out.filters[#out.filters+1]={index=filter.index or index,name=filter.name,
      quality=filter.quality or "normal",count=filter.count or 0,mode=filter.mode or "at-least"}
  end
  local ok_remove,remove=pcall(function() return e.remove_unfiltered_items end)
  if ok_remove then out.removeUnfilteredItems=remove==true end
  return out
end
local function settings_logistic_point(point)
  if not point then return nil end
  return {sections=settings_logistic_sections(point)}
end
local function settings_logistic_member(e)
  if e.type=="rocket-silo" then return defines.logistic_member_index.rocket_silo_requester end
  return defines.logistic_member_index.logistic_container
end
local function settings_logistic(e)
  local index=settings_logistic_member(e)
  local ok,point=pcall(function() return e.get_logistic_point(index) end)
  if not ok or not point then return nil end
  return settings_logistic_point(point)
end
-- Constant combinators expose LuaLogisticSection through their control
-- behavior, where each slot's min is the emitted signal count.
local function settings_constant_combinator(e)
  if e.type~="constant-combinator" then return nil end
  local control=settings_control(e)
  if not control then return nil end
  local sections={}
  for _,section in ipairs(settings_logistic_sections(control)) do
    local slots={}
    for _,slot in ipairs(section.slots) do
      slots[#slots+1]={index=slot.index,count=slot.min,
        signal={type=signal_type_name(slot.type),name=slot.name,quality=slot.quality}}
    end
    sections[#sections+1]={index=section.index,active=section.active,
      multiplier=section.multiplier,group=section.group,slots=slots}
  end
  return {enabled=settings_field(control,"enabled") and true or false,sections=sections}
end
-- LuaEntity.trains_limit stores the uint32 maximum for an unlimited stop, which
-- does not fit in a GraphQL Int, so anything at or above 2^31-1 reads as null.
local function settings_train_stop(e)
  local limit=settings_field(e,"trains_limit")
  if type(limit)~="number" or limit>=2147483647 then limit=nil end
  return {backerName=settings_field(e,"backer_name") or "",
    trainsLimit=limit,
    priority=settings_field(e,"train_stop_priority") or 0,
    color=settings_color(settings_field(e,"color"))}
end
local function settings_silo(e)
  return {rocketParts=settings_field(e,"rocket_parts") or 0,
    rocketSiloStatus=settings_field(e,"rocket_silo_status") or 0,
    sendToOrbitAutomatically=settings_field(e,"send_to_orbit_automatically") or false}
end
local function settings_sections(e,inventory_name)
  local out=settings_common(e)
  if e.type=="splitter" then
    out.splitter={filter=settings_item_filter(settings_field(e,"splitter_filter")),
      inputPriority=settings_field(e,"splitter_input_priority") or "none",
      outputPriority=settings_field(e,"splitter_output_priority") or "none"}
  end
  if e.type=="inserter" then out.inserter=inserter_settings(e) end
  if e.type=="lamp" then
    local control=settings_control(e)
    out.lamp={color=settings_color(settings_field(e,"color")) or {r=0,g=0,b=0,a=0},
      alwaysOn=settings_field(e,"always_on") or false,
      useColors=(control and settings_field(control,"use_colors")) or false,
      colorMode=(control and settings_lamp_color_mode(control)) or "COLOR_MAPPING"}
  end
  if e.inventory_supports_bar then out.container=settings_container(e,inventory_name) end
  if e.type=="infinity-container" or e.type=="infinity-pipe" then out.infinity=settings_infinity(e) end
  if e.type=="logistic-container" or e.type=="infinity-container" or e.type=="rocket-silo" then out.logistic=settings_logistic(e) end
  if e.type=="constant-combinator" then out.constantCombinator=settings_constant_combinator(e) end
  if e.type=="train-stop" then out.trainStop=settings_train_stop(e) end
  if e.type=="rocket-silo" then out.rocketSilo=settings_silo(e) end
  out.circuit=settings_circuit(e)
  return out
end
local function settings_print(e,inventory_name)
  rcon.print(helpers.table_to_json(settings_sections(e,inventory_name)))
end
`

// settingsReadLua builds the Lua for EntityInspection.settings.
func settingsReadLua(entityLua, inventory string) (string, error) {
	if inventory == "" {
		inventory = "chest"
	}
	if _, ok := settingsInventoryNames[inventory]; !ok {
		return "", errors.New("inventory must be chest, fuel, input, output, modules, or turret-ammo")
	}
	return luaFail + entityLua + settingsLua + fmt.Sprintf("settings_print(e,%s)\n", luaString(inventory)), nil
}

// settingsMutationLua composes a write body with the shared settings readers and
// the post-write print. body must end after the last write statement.
func settingsMutationLua(body, inventory string) string {
	if inventory == "" {
		inventory = "chest"
	}
	return luaFail + settingsLua + body + fmt.Sprintf("settings_print(e,%s)\n", luaString(inventory))
}

func executeSettings(r *Resolver, lua string) (*SettingsResult, error) {
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[SettingsResult](response)
}

// entitySettings reads the settings snapshot for an already inspected entity.
func entitySettings(r *Resolver, obj *EntityInspection, inventory *string) (*SettingsResult, error) {
	if obj == nil {
		return nil, errors.New("settings require an inspection")
	}
	entityLua, err := diagnosticsEntityLua(obj.Surface, stringValueOr(obj.Name, ""), nil, obj.Position, obj.Force)
	if err != nil {
		return nil, err
	}
	name := ""
	if inventory != nil {
		name = *inventory
	}
	lua, err := settingsReadLua(entityLua, name)
	if err != nil {
		return nil, err
	}
	return executeSettings(r, lua)
}

func setSplitterSettings(r *Resolver, in SplitterSettingsInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	if in.InputPriority != nil && !validSplitterPriority(*in.InputPriority) {
		return nil, errors.New("inputPriority must be none, left, or right")
	}
	if in.OutputPriority != nil && !validSplitterPriority(*in.OutputPriority) {
		return nil, errors.New("outputPriority must be none, left, or right")
	}
	if in.Filter != nil && in.Filter.Name == "" {
		return nil, errors.New("filter name must not be empty")
	}
	if in.Filter != nil && in.OutputPriority != nil && *in.OutputPriority == "none" {
		return nil, errors.New("outputPriority none makes a splitter filter inert; set left or right")
	}
	if in.Filter != nil && in.OutputPriority == nil {
		left := "left"
		in.OutputPriority = &left
	}
	clear := in.Clear != nil && *in.Clear
	if clear && (in.Filter != nil || in.InputPriority != nil || in.OutputPriority != nil) {
		return nil, errors.New("clear cannot be combined with filter or priorities")
	}
	if in.Filter == nil && in.InputPriority == nil && in.OutputPriority == nil && !clear {
		return nil, errors.New("set at least one of filter, inputPriority, outputPriority, or clear")
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`if e.type~="splitter" then fail("entity is not a splitter"); return end
`)
	if clear {
		body.WriteString(`e.splitter_filter=nil
e.splitter_input_priority="none"
e.splitter_output_priority="none"
`)
	}
	if in.Filter != nil {
		quality := "normal"
		if in.Filter.Quality != nil && *in.Filter.Quality != "" {
			quality = *in.Filter.Quality
		}
		filterLua := fmt.Sprintf("{name=%s,quality=%s", luaString(in.Filter.Name), luaString(quality))
		if in.Filter.Comparator != nil && *in.Filter.Comparator != "" {
			filterLua += ",comparator=" + luaString(*in.Filter.Comparator)
		}
		filterLua += "}"
		fmt.Fprintf(&body, "e.splitter_filter=%s\n", filterLua)
	}
	if in.InputPriority != nil {
		fmt.Fprintf(&body, "e.splitter_input_priority=%s\n", luaString(*in.InputPriority))
	}
	if in.OutputPriority != nil {
		fmt.Fprintf(&body, "e.splitter_output_priority=%s\n", luaString(*in.OutputPriority))
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, splitterSettingsSummary(result))
	return result, nil
}

func setInserterSettings(r *Resolver, in InserterSettingsInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	clear := in.Clear != nil && *in.Clear
	if clear && (in.Filters != nil || in.FilterMode != nil || in.StackSizeOverride != nil) {
		return nil, errors.New("clear cannot be combined with filters, filterMode, or stackSizeOverride")
	}
	if in.FilterMode != nil && *in.FilterMode != "whitelist" && *in.FilterMode != "blacklist" {
		return nil, errors.New("filterMode must be whitelist or blacklist")
	}
	if in.StackSizeOverride != nil && (*in.StackSizeOverride < 0 || *in.StackSizeOverride > 12) {
		return nil, errors.New("stackSizeOverride must be between 0 and 12")
	}
	if clear && (in.PickupFromLeftLane != nil || in.PickupFromRightLane != nil) {
		return nil, errors.New("clear cannot be combined with pickupFromLeftLane or pickupFromRightLane")
	}
	// An empty but non-nil list means "remove every filter", so it counts as a
	// request even though it writes no slot.
	if in.Filters == nil && in.FilterMode == nil && in.StackSizeOverride == nil && in.PickupFromLeftLane == nil && in.PickupFromRightLane == nil && !clear {
		return nil, errors.New("set at least one of filters, filterMode, stackSizeOverride, pickupFromLeftLane, pickupFromRightLane, or clear")
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`if e.type~="inserter" then fail("entity is not an inserter"); return end
local function check_index(index)
  if index<1 or index>e.filter_slot_count then fail("filter index "..index.." is outside 1..."..e.filter_slot_count); return false end
  return true
end
`)
	if clear {
		body.WriteString(`for index=1,e.filter_slot_count do e.set_filter(index,nil) end
e.use_filters=false
e.inserter_filter_mode="whitelist"
e.inserter_stack_size_override=0
`)
	}
	if in.Filters != nil {
		body.WriteString(`for index=1,e.filter_slot_count do e.set_filter(index,nil) end
`)
		written := 0
		for _, filter := range in.Filters {
			if filter == nil {
				continue
			}
			if filter.Index < 1 {
				return nil, errors.New("filter index must be at least 1")
			}
			if filter.Name == "" {
				return nil, errors.New("filter name must not be empty")
			}
			quality := "normal"
			if filter.Quality != nil && *filter.Quality != "" {
				quality = *filter.Quality
			}
			filterLua := fmt.Sprintf("{name=%s,quality=%s", luaString(filter.Name), luaString(quality))
			if filter.Comparator != nil && *filter.Comparator != "" {
				filterLua += ",comparator=" + luaString(*filter.Comparator)
			}
			fmt.Fprintf(&body, "if not check_index(%d) then return end\ne.set_filter(%d,%s})\n",
				filter.Index, filter.Index, filterLua)
			written++
		}
		// Enabling filter use with no slots would leave a whitelist inserter
		// inert, which is never what "remove every filter" means.
		fmt.Fprintf(&body, "e.use_filters=%t\n", written > 0)
	}
	if in.FilterMode != nil {
		fmt.Fprintf(&body, "e.inserter_filter_mode=%s\n", luaString(*in.FilterMode))
	}
	if in.StackSizeOverride != nil {
		fmt.Fprintf(&body, "e.inserter_stack_size_override=%d\n", *in.StackSizeOverride)
	}
	if in.PickupFromLeftLane != nil {
		fmt.Fprintf(&body, "e.pickup_from_left_lane=%t\n", *in.PickupFromLeftLane)
	}
	if in.PickupFromRightLane != nil {
		fmt.Fprintf(&body, "e.pickup_from_right_lane=%t\n", *in.PickupFromRightLane)
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, inserterSettingsSummary(result))
	return result, nil
}

func setLampSettings(r *Resolver, in LampSettingsInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	if in.Color == nil && in.AlwaysOn == nil && in.UseColors == nil && in.ColorMode == nil {
		return nil, errors.New("set at least one of color, alwaysOn, useColors, or colorMode")
	}
	if in.Color != nil {
		if err := validateColor(*in.Color); err != nil {
			return nil, err
		}
	}
	lampColorMode, err := lampColorModeName(in.ColorMode)
	if err != nil {
		return nil, err
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`if e.type~="lamp" then fail("entity is not a lamp"); return end
`)
	if in.Color != nil {
		alpha := 1.0
		if in.Color.A != nil {
			alpha = *in.Color.A
		}
		fmt.Fprintf(&body, "e.color={r=%s,g=%s,b=%s,a=%s}\n",
			formatFloat(in.Color.R), formatFloat(in.Color.G), formatFloat(in.Color.B), formatFloat(alpha))
	}
	if in.AlwaysOn != nil {
		fmt.Fprintf(&body, "e.always_on=%t\n", *in.AlwaysOn)
	}
	if in.UseColors != nil || lampColorMode != "" {
		body.WriteString("local control=e.get_or_create_control_behavior()\nif not control then fail(\"lamp has no control behavior\"); return end\n")
		if in.UseColors != nil {
			fmt.Fprintf(&body, "control.use_colors=%t\n", *in.UseColors)
		}
		if lampColorMode != "" {
			fmt.Fprintf(&body, "control.color_mode=defines.control_behavior.lamp.color_mode[%s]\n", luaString(lampColorMode))
		}
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, lampSettingsSummary(result))
	return result, nil
}

func setContainerBar(r *Resolver, in ContainerBarInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	inventory := "chest"
	if in.Inventory != nil && *in.Inventory != "" {
		inventory = *in.Inventory
	}
	if _, ok := settingsInventoryNames[inventory]; !ok {
		return nil, errors.New("inventory must be chest, fuel, input, output, modules, or turret-ammo")
	}
	// The engine stores the index at which the blocked area starts, so the
	// usable slot count is one less: allowedSlots+1 keeps "no limit" (nil) and
	// "zero usable slots" (bar 1) distinguishable.
	allowedSlots, barValue := "nil", "nil"
	if in.AllowedSlots != nil {
		if *in.AllowedSlots < 0 {
			return nil, errors.New("allowedSlots must be zero or greater")
		}
		if *in.AllowedSlots > 65536 {
			return nil, errors.New("allowedSlots must not exceed 65536")
		}
		allowedSlots = strconv.Itoa(*in.AllowedSlots)
		barValue = fmt.Sprintf("(%d+1)", *in.AllowedSlots)
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	fmt.Fprintf(&body, `local index=settings_inventory_index(%s)
if not index then fail("inventory is not supported"); return end
local ok,supported=pcall(function() return e.inventory_supports_bar(index) end)
if not ok or not supported then fail("entity inventory does not support a bar"); return end
local inventory=e.get_inventory(index)
if not inventory then fail("entity does not have that inventory"); return end
local capacity=#inventory
local allowed_slots=%s
if allowed_slots~=nil and allowed_slots>capacity then fail("allowedSlots "..allowed_slots.." exceeds inventory size "..capacity); return end
e.set_inventory_bar(index,%s)
`, luaString(inventory), allowedSlots, barValue)
	result, err := executeSettings(r, settingsMutationLua(body.String(), inventory))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, containerSettingsSummary(result))
	return result, nil
}

// infinityFilterModeName maps the GraphQL enum to the engine policy string
// accepted by InfinityInventoryFilter.mode.
func infinityFilterModeName(mode InfinityFilterMode) (string, error) {
	switch mode {
	case InfinityFilterModeAtLeast:
		return "at-least", nil
	case InfinityFilterModeAtMost:
		return "at-most", nil
	case InfinityFilterModeExactly:
		return "exactly", nil
	}
	return "", fmt.Errorf("unsupported infinity filter mode %q", mode)
}

// infinityPipeFilterModeName maps the GraphQL enum to the engine policy string
// accepted by InfinityPipeFilter.mode.
func infinityPipeFilterModeName(mode InfinityPipeFilterMode) (string, error) {
	switch mode {
	case InfinityPipeFilterModeAtLeast:
		return "at-least", nil
	case InfinityPipeFilterModeAtMost:
		return "at-most", nil
	case InfinityPipeFilterModeExactly:
		return "exactly", nil
	case InfinityPipeFilterModeAdd:
		return "add", nil
	case InfinityPipeFilterModeRemove:
		return "remove", nil
	}
	return "", fmt.Errorf("unsupported infinity pipe filter mode %q", mode)
}

// configureInfinityContainer replaces every infinity-container filter so a
// sandbox save can expose an item source without an inventory-write shortcut.
// The engine's setter takes one filter index at a time; existing filters are
// cleared after every prototype has been validated, so a bad entry cannot leave
// the container empty.
func configureInfinityContainer(r *Resolver, in ConfigureInfinityContainerInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	type filterWrite struct {
		item, quality, mode, count string
	}
	writes := make([]filterWrite, 0, len(in.Filters))
	var checks strings.Builder
	for i, filter := range in.Filters {
		if filter == nil {
			return nil, fmt.Errorf("filter %d is null", i+1)
		}
		item := strings.TrimSpace(filter.Item)
		if item == "" {
			return nil, fmt.Errorf("filter %d needs a non-empty item", i+1)
		}
		if math.IsNaN(filter.Count) || math.IsInf(filter.Count, 0) || filter.Count < 0 || filter.Count > 4294967295 || math.Trunc(filter.Count) != filter.Count {
			return nil, fmt.Errorf("filter %d count must be a whole number from 0 through 4294967295", i+1)
		}
		quality := "normal"
		if filter.Quality != nil && strings.TrimSpace(*filter.Quality) != "" {
			quality = strings.TrimSpace(*filter.Quality)
		}
		mode, err := infinityFilterModeName(filter.Mode)
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", i+1, err)
		}
		fmt.Fprintf(&checks, "if not prototypes.item[%s] then fail(\"item not found: \"..%s); return end\n",
			luaString(item), luaString(item))
		fmt.Fprintf(&checks, "if not prototypes.quality[%s] then fail(\"quality not found: \"..%s); return end\n",
			luaString(quality), luaString(quality))
		writes = append(writes, filterWrite{item: item, quality: quality, mode: mode, count: formatFloat(filter.Count)})
	}
	type fluidWrite struct {
		fluid, mode             string
		percentage, temperature *float64
	}
	fluids := make([]fluidWrite, 0, len(in.FluidFilters))
	for i, filter := range in.FluidFilters {
		if filter == nil {
			return nil, fmt.Errorf("fluid filter %d is null", i+1)
		}
		fluid := strings.TrimSpace(filter.Fluid)
		if fluid == "" {
			return nil, fmt.Errorf("fluid filter %d needs a non-empty fluid", i+1)
		}
		if filter.Percentage != nil {
			value := *filter.Percentage
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
				return nil, fmt.Errorf("fluid filter %d percentage must be from 0 through 1", i+1)
			}
		}
		if filter.Temperature != nil {
			value := *filter.Temperature
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return nil, fmt.Errorf("fluid filter %d temperature must be a finite non-negative number", i+1)
			}
		}
		mode, err := infinityPipeFilterModeName(filter.Mode)
		if err != nil {
			return nil, fmt.Errorf("fluid filter %d: %w", i+1, err)
		}
		fluids = append(fluids, fluidWrite{fluid: fluid, mode: mode, percentage: filter.Percentage, temperature: filter.Temperature})
	}
	if len(fluids) > 1 {
		return nil, errors.New("an infinity pipe takes exactly one fluid filter")
	}
	if len(fluids) > 0 && len(writes) > 0 {
		return nil, errors.New("fluidFilters cannot be combined with item filters")
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`if e.type=="infinity-pipe" then
`)
	if len(fluids) == 0 {
		body.WriteString(`fail("infinity pipe needs exactly one fluid filter"); return
`)
	} else {
		write := fluids[0]
		fmt.Fprintf(&body, "if not prototypes.fluid[%s] then fail(\"fluid not found: \"..%s); return end\n",
			luaString(write.fluid), luaString(write.fluid))
		fmt.Fprintf(&body, "local filter={name=%s,mode=%s}\n", luaString(write.fluid), luaString(write.mode))
		if write.percentage != nil {
			fmt.Fprintf(&body, "filter.percentage=%s\n", formatFloat(*write.percentage))
		}
		if write.temperature != nil {
			fmt.Fprintf(&body, "filter.temperature=%s\n", formatFloat(*write.temperature))
		}
		body.WriteString(`e.set_infinity_pipe_filter(filter)
local applied=e.get_infinity_pipe_filter()
if not applied or applied.name~=filter.name then fail("engine did not apply the infinity pipe filter"); return end
`)
	}
	body.WriteString(`elseif e.type=="infinity-container" then
`)
	if len(fluids) > 0 {
		body.WriteString(`fail("fluidFilters only apply to infinity pipes"); do return end
`)
	}
	body.WriteString(checks.String())
	// The engine exposes per-index setters, but clearing then setting can leave a
	// container half-rewritten when a later setter rejects. Build the whole array
	// and assign it once so the container keeps either the old or the new filters.
	body.WriteString("local filters={}\n")
	for index, write := range writes {
		// Assignment through infinity_container_filters expects the index even
		// though set_infinity_container_filter does not.
		fmt.Fprintf(&body, "filters[#filters+1]={index=%d,name=%s,quality=%s,count=%s,mode=%s}\n",
			index+1, luaString(write.item), luaString(write.quality), write.count, luaString(write.mode))
	}
	body.WriteString(`local capacity=0
local inventory=e.get_inventory(defines.inventory.chest)
if inventory then capacity=#inventory end
local used=0
for _,filter in ipairs(filters) do
  local prototype=prototypes.item[filter.name]
  local stack=prototype and prototype.stack_size or 1
  local count=filter.count or 0
  -- At-most filters do not require stock, so they consume no reserved slots.
  if count>0 and stack>0 and filter.mode~="at-most" then used=used+math.ceil(count/stack) end
end
if used>capacity then fail("infinity filters need "..used.." slots but the container has "..capacity); return end
e.infinity_container_filters=filters
`)
	if in.RemoveUnfilteredItems != nil {
		fmt.Fprintf(&body, "e.remove_unfiltered_items=%t\n", *in.RemoveUnfilteredItems)
	}
	body.WriteString(`else
fail("entity is not an infinity container or infinity pipe"); return
end
`)
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, infinityContainerSummary(result))
	return result, nil
}

func setLogisticRequests(r *Resolver, in LogisticRequestsInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	clear := in.Clear != nil && *in.Clear
	if clear && in.Sections != nil {
		return nil, errors.New("clear cannot be combined with sections")
	}
	if !clear && in.Sections == nil {
		return nil, errors.New("set sections or clear")
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`local member_index=settings_logistic_member(e)
local ok,point=pcall(function() return e.get_logistic_point(member_index) end)
if not ok or not point then fail("entity has no logistic container behavior"); return end
`)
	if in.Sections != nil {
		check, writes, err := logisticSectionStatements(in.Sections, "point", "section")
		if err != nil {
			return nil, err
		}
		// Prototype checks run before the clear so a bad name cannot leave the container emptied.
		body.WriteString(check)
		body.WriteString("for index=20,1,-1 do point.remove_section(index) end\n")
		body.WriteString(writes)
	} else {
		body.WriteString("for index=20,1,-1 do point.remove_section(index) end\n")
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, logisticSettingsSummary(result))
	return result, nil
}

func setTrainStop(r *Resolver, in TrainStopInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	clearLimit := in.ClearTrainsLimit != nil && *in.ClearTrainsLimit
	if clearLimit && in.TrainsLimit != nil {
		return nil, errors.New("clearTrainsLimit cannot be combined with trainsLimit")
	}
	if in.Name == nil && in.TrainsLimit == nil && !clearLimit && in.Priority == nil && in.Color == nil {
		return nil, errors.New("set at least one of name, trainsLimit, clearTrainsLimit, priority, or color")
	}
	if in.TrainsLimit != nil && (*in.TrainsLimit < 0 || *in.TrainsLimit > 1000) {
		return nil, errors.New("trainsLimit must be between 0 and 1000; use clearTrainsLimit to remove the limit")
	}
	if in.Priority != nil && (*in.Priority < 0 || *in.Priority > 255) {
		return nil, errors.New("priority must be between 0 and 255")
	}
	if in.Color != nil {
		if err := validateColor(*in.Color); err != nil {
			return nil, err
		}
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`if e.type~="train-stop" then fail("entity is not a train stop"); return end
`)
	if in.Name != nil {
		if *in.Name == "" {
			return nil, errors.New("name must not be empty")
		}
		fmt.Fprintf(&body, "e.backer_name=%s\n", luaString(*in.Name))
	}
	if in.TrainsLimit != nil {
		fmt.Fprintf(&body, "e.trains_limit=%d\n", *in.TrainsLimit)
	}
	if clearLimit {
		body.WriteString("e.trains_limit=nil\n")
	}
	if in.Priority != nil {
		fmt.Fprintf(&body, "e.train_stop_priority=%d\n", *in.Priority)
	}
	if in.Color != nil {
		alpha := 1.0
		if in.Color.A != nil {
			alpha = *in.Color.A
		}
		fmt.Fprintf(&body, "e.color={r=%s,g=%s,b=%s,a=%s}\n",
			formatFloat(in.Color.R), formatFloat(in.Color.G), formatFloat(in.Color.B), formatFloat(alpha))
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, trainStopSettingsSummary(result))
	return result, nil
}

func setRocketSilo(r *Resolver, in RocketSiloInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	body := prefix + fmt.Sprintf(`if e.type~="rocket-silo" then fail("entity is not a rocket silo"); return end
e.send_to_orbit_automatically=%t
`, in.SendToOrbitAutomatically)
	result, err := executeSettings(r, settingsMutationLua(body, "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, siloSettingsSummary(result))
	return result, nil
}

func launchRocket(r *Resolver, in RocketLaunchInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	body := prefix + `if e.type~="rocket-silo" then fail("entity is not a rocket silo"); return end
local ready=defines.entity_status.waiting_to_launch_rocket
if e.status~=ready then
  local required=settings_field(e.prototype,"rocket_parts_required") or 100
  local parts=e.rocket_parts or 0
  fail("rocket silo is not ready to launch: status "..tostring(e.status).." (waiting_to_launch_rocket is "..tostring(ready).."), "..parts.." of "..required.." rocket parts")
  return
end
if not e.launch_rocket() then fail("rocket silo refused the launch at status "..tostring(e.status)); return end
`
	result, err := executeSettings(r, settingsMutationLua(body, "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "launched rocket from "+result.Name+positionText(result.Position))
	return result, nil
}

func setCircuitBehavior(r *Resolver, in CircuitBehaviorInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	clear := in.ClearConditions != nil && *in.ClearConditions
	booleanValues := map[string]*bool{
		"circuitEnableDisable":     in.CircuitEnableDisable,
		"readContents":             in.ReadContents,
		"setRecipe":                in.SetRecipe,
		"readIngredients":          in.ReadIngredients,
		"readWorking":              in.ReadWorking,
		"readResources":            in.ReadResources,
		"readAmmo":                 in.ReadAmmo,
		"readLogistics":            in.ReadLogistics,
		"readRobotStats":           in.ReadRobotStats,
		"setFilters":               in.SetFilters,
		"readHandContents":         in.ReadHandContents,
		"setStackSize":             in.SetStackSize,
		"connectToLogisticNetwork": in.ConnectToLogisticNetwork,
		"useColors":                in.UseColors,
		"readRecipeFinished":       in.ReadRecipeFinished,
		"includeInCrafting":        in.IncludeInCrafting,
		"includeFuel":              in.IncludeFuel,
		"setFilter":                in.SetFilter,
		"splitterInputSide":        in.SplitterInputSide,
		"splitterOutputSide":       in.SplitterOutputSide,
		"sendToTrain":              in.SendToTrain,
		"readFromTrain":            in.ReadFromTrain,
		"readStoppedTrain":         in.ReadStoppedTrain,
		"setTrainsLimit":           in.SetTrainsLimit,
		"readTrainsCount":          in.ReadTrainsCount,
		"setPriority":              in.SetPriority,
		"circuitConditionEnabled":  in.CircuitConditionEnabled,
	}
	intValues := map[string]*int{
		"siloReadMode":             in.SiloReadMode,
		"resourceReadMode":         in.ResourceReadMode,
		"readItemsMode":            in.ReadItemsMode,
		"handReadMode":             in.HandReadMode,
		"contentsReadMode":         in.ContentsReadMode,
		"exclusiveModeOfOperation": in.ExclusiveModeOfOperation,
	}
	signalValues := map[string]*SignalInput{
		"stackControlSignal":   in.StackControlSignal,
		"redSignal":            in.RedSignal,
		"greenSignal":          in.GreenSignal,
		"blueSignal":           in.BlueSignal,
		"rgbSignal":            in.RgbSignal,
		"recipeFinishedSignal": in.RecipeFinishedSignal,
		"workingSignal":        in.WorkingSignal,
		"stoppedTrainSignal":   in.StoppedTrainSignal,
		"trainsCountSignal":    in.TrainsCountSignal,
		"trainsLimitSignal":    in.TrainsLimitSignal,
		"prioritySignal":       in.PrioritySignal,
	}
	conditionValues := map[string]*SignalConditionInput{
		"circuitCondition":     in.CircuitCondition,
		"logisticCondition":    in.LogisticCondition,
		"inputLeftCondition":   in.InputLeftCondition,
		"inputRightCondition":  in.InputRightCondition,
		"outputLeftCondition":  in.OutputLeftCondition,
		"outputRightCondition": in.OutputRightCondition,
	}
	if clear {
		for name, value := range booleanValues {
			if value != nil {
				return nil, fmt.Errorf("clearConditions cannot be combined with %s", name)
			}
		}
		for name, value := range intValues {
			if value != nil {
				return nil, fmt.Errorf("clearConditions cannot be combined with %s", name)
			}
		}
		for name, value := range signalValues {
			if value != nil {
				return nil, fmt.Errorf("clearConditions cannot be combined with %s", name)
			}
		}
		for name, value := range conditionValues {
			if value != nil {
				return nil, fmt.Errorf("clearConditions cannot be combined with %s", name)
			}
		}
		if in.ColorMode != nil {
			return nil, errors.New("clearConditions cannot be combined with colorMode")
		}
	}
	if err := validateCircuitInts(intValues); err != nil {
		return nil, err
	}
	colorMode, err := lampColorModeName(in.ColorMode)
	if err != nil {
		return nil, err
	}
	var assignments []string
	if in.Enabled != nil {
		return nil, errors.New("enabled is read-only in Factorio 2.0 (LuaGenericOnOffControlBehavior.disabled reports the current state); set circuitEnableDisable with circuitCondition or logisticCondition instead")
	}
	for _, field := range circuitBooleanFields {
		if value := booleanValues[field.name]; value != nil {
			assignments = append(assignments, circuitApplyLua(field.name, field.keys, fmt.Sprintf("%t", *value)))
		}
	}
	for _, field := range circuitIntFields {
		if value := intValues[field.name]; value != nil {
			assignments = append(assignments, circuitApplyLua(field.name, field.keys, strconv.Itoa(*value)))
		}
	}
	if colorMode != "" {
		assignments = append(assignments, circuitApplyLua("colorMode", circuitFieldKeys["colorMode"],
			"defines.control_behavior.lamp.color_mode["+luaString(colorMode)+"]"))
	}
	for _, field := range circuitSignalFields {
		if value := signalValues[field.name]; value != nil {
			signal, err := signalLua(*value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", field.name, err)
			}
			assignments = append(assignments, circuitApplyLua(field.name, field.keys, signal))
		}
	}
	for _, field := range circuitConditionFields {
		if value := conditionValues[field.name]; value != nil {
			lua, err := conditionLua(*value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", field.name, err)
			}
			assignments = append(assignments, circuitApplyLua(field.name, field.keys, lua))
		}
	}
	if len(assignments) == 0 && !clear {
		return nil, errors.New("set at least one circuit behavior field or clearConditions")
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`local control=e.get_or_create_control_behavior()
if not control then fail("entity has no control behavior"); return end
` + circuitControlApplyLua)
	if clear {
		for _, field := range circuitBooleanFields {
			fmt.Fprintf(&body, "reset(%s,false)\n", luaKeys(field.keys))
		}
		for _, field := range circuitIntFields {
			fmt.Fprintf(&body, "reset(%s,0)\n", luaKeys(field.keys))
		}
		for _, field := range circuitSignalFields {
			fmt.Fprintf(&body, "reset(%s,nil)\n", luaKeys(field.keys))
		}
		for _, field := range circuitConditionFields {
			fmt.Fprintf(&body, "reset(%s,nil)\n", luaKeys(field.keys))
		}
	}
	for _, assignment := range assignments {
		body.WriteString(assignment)
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, circuitSettingsSummary(result))
	return result, nil
}

// circuitField is one writable control behavior setting with its engine key
// candidates. Factorio renamed several flags in 2.0, so a field tries each
// candidate key and fails with the engine message when none exist.
type circuitField struct {
	name string
	keys []string
}

var circuitBooleanFields = []circuitField{
	{"circuitEnableDisable", []string{"circuit_enable_disable"}},
	{"readContents", []string{"circuit_read_contents", "read_contents"}},
	{"setRecipe", []string{"circuit_set_recipe"}},
	{"readIngredients", []string{"circuit_read_ingredients"}},
	{"readWorking", []string{"circuit_read_working"}},
	{"readResources", []string{"circuit_read_resources"}},
	{"readAmmo", []string{"read_ammo"}},
	{"readLogistics", []string{"read_logistics"}},
	{"readRobotStats", []string{"read_robot_stats"}},
	{"setFilters", []string{"circuit_set_filters"}},
	{"readHandContents", []string{"circuit_read_hand_contents"}},
	{"setStackSize", []string{"circuit_set_stack_size"}},
	{"connectToLogisticNetwork", []string{"connect_to_logistic_network"}},
	{"useColors", []string{"use_colors"}},
	{"readRecipeFinished", []string{"circuit_read_recipe_finished"}},
	{"includeInCrafting", []string{"include_in_crafting"}},
	{"includeFuel", []string{"include_fuel"}},
	{"setFilter", []string{"set_filter"}},
	{"splitterInputSide", []string{"set_input_side"}},
	{"splitterOutputSide", []string{"set_output_side"}},
	{"sendToTrain", []string{"send_to_train"}},
	{"readFromTrain", []string{"read_from_train"}},
	{"readStoppedTrain", []string{"read_stopped_train"}},
	{"setTrainsLimit", []string{"set_trains_limit"}},
	{"readTrainsCount", []string{"read_trains_count"}},
	{"setPriority", []string{"set_priority"}},
	{"circuitConditionEnabled", []string{"circuit_condition_enabled", "circuit_enable_disable"}},
}

var circuitIntFields = []circuitField{
	{"siloReadMode", []string{"read_mode"}},
	{"resourceReadMode", []string{"resource_read_mode"}},
	{"readItemsMode", []string{"read_items_mode"}},
	{"handReadMode", []string{"circuit_hand_read_mode"}},
	{"contentsReadMode", []string{"read_contents_mode"}},
	{"exclusiveModeOfOperation", []string{"circuit_exclusive_mode_of_operation", "circuit_mode_of_operation"}},
}

var circuitSignalFields = []circuitField{
	{"stackControlSignal", []string{"circuit_stack_control_signal"}},
	{"redSignal", []string{"red_signal"}},
	{"greenSignal", []string{"green_signal"}},
	{"blueSignal", []string{"blue_signal"}},
	{"rgbSignal", []string{"rgb_signal"}},
	{"recipeFinishedSignal", []string{"circuit_recipe_finished_signal"}},
	{"workingSignal", []string{"circuit_working_signal"}},
	{"stoppedTrainSignal", []string{"stopped_train_signal"}},
	{"trainsCountSignal", []string{"trains_count_signal"}},
	{"trainsLimitSignal", []string{"trains_limit_signal"}},
	{"prioritySignal", []string{"priority_signal"}},
}

var circuitConditionFields = []circuitField{
	{"circuitCondition", []string{"circuit_condition"}},
	{"logisticCondition", []string{"logistic_condition"}},
	{"inputLeftCondition", []string{"input_left_condition"}},
	{"inputRightCondition", []string{"input_right_condition"}},
	{"outputLeftCondition", []string{"output_left_condition"}},
	{"outputRightCondition", []string{"output_right_condition"}},
}

var circuitFieldKeys = func() map[string][]string {
	keys := map[string][]string{"colorMode": {"color_mode"}}
	for _, fields := range [][]circuitField{circuitBooleanFields, circuitIntFields, circuitSignalFields, circuitConditionFields} {
		for _, field := range fields {
			keys[field.name] = field.keys
		}
	}
	return keys
}()

// circuitControlApplyLua is spliced into every setCircuitBehavior write after
// `control` exists.
//
// The engine throws on an invalid value (unknown signal type or name, bad
// comparator, out-of-range mode) exactly like it throws when the key does not
// exist, so the key is probed first: an assignment failure is then a real value
// error and is reported with the engine message, while the
// "not supported by this control behavior" wording is reserved for a key the
// behavior does not expose at all.
const circuitControlApplyLua = `local function field_exists(key)
  local ok=pcall(function() return control[key] end)
  return ok
end
local function apply(name,keys,value)
  for _,key in ipairs(keys) do
    if field_exists(key) then
      local ok,err=pcall(function() control[key]=value end)
      if not ok then fail(name..": "..tostring(err)) end
      return true
    end
  end
  fail(name.." is not supported by this control behavior"); return false
end
local function reset(keys,value)
  for _,key in ipairs(keys) do pcall(function() control[key]=value end) end
end
`

// luaKeys renders engine key candidates as a Lua array constructor.
func luaKeys(keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, luaString(key))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func circuitApplyLua(name string, keys []string, value string) string {
	return fmt.Sprintf("if not apply(%s,%s,%s) then return end\n", luaString(name), luaKeys(keys), value)
}

func validateCircuitInts(values map[string]*int) error {
	for name, value := range values {
		if value == nil {
			continue
		}
		if *value < 0 {
			return fmt.Errorf("%s must be zero or positive", name)
		}
	}
	return nil
}

// maxLogisticSections is the section count the read-back and clear loops
// cover; more sections would be silently ignored or left behind.
const maxLogisticSections = 20

// constantCombinatorSectionStatements validates constant combinator sections and
// builds Lua that adds them. Constant combinators reuse LuaLogisticSection, where
// each slot's min is the emitted signal count, so the value keeps its signal
// type instead of being forced to an item.
func constantCombinatorSectionStatements(sections []*ConstantCombinatorSectionInput) (check, writes string, err error) {
	var checkBody, writeBody strings.Builder
	count := 0
	for _, input := range sections {
		if input == nil {
			continue
		}
		count++
		if count > maxLogisticSections {
			return "", "", fmt.Errorf("at most %d sections are supported", maxLogisticSections)
		}
		writeBody.WriteString("do local section=behavior.add_section()\nif not section then fail(\"could not add a constant combinator section\"); return end\n")
		if input.Active != nil {
			fmt.Fprintf(&writeBody, "section.active=%t\n", *input.Active)
		}
		if input.Multiplier != nil {
			if math.IsNaN(*input.Multiplier) || math.IsInf(*input.Multiplier, 0) {
				return "", "", errors.New("section multiplier must be finite")
			}
			if *input.Multiplier < 0 {
				return "", "", errors.New("section multiplier must be zero or positive")
			}
			fmt.Fprintf(&writeBody, "section.multiplier=%s\n", formatFloat(*input.Multiplier))
		}
		if input.Group != nil && *input.Group != "" {
			fmt.Fprintf(&writeBody, "section.group=%s\n", luaString(*input.Group))
		}
		for _, slot := range input.Slots {
			if slot == nil {
				continue
			}
			if slot.Index < 1 {
				return "", "", errors.New("each slot needs a positive index")
			}
			if slot.Signal == nil || strings.TrimSpace(slot.Signal.Name) == "" {
				return "", "", errors.New("each slot needs a signal name")
			}
			if slot.Count < 0 {
				return "", "", errors.New("each slot count must be zero or positive")
			}
			if int64(slot.Count) > math.MaxInt32 {
				return "", "", errors.New("each slot count must not exceed 2147483647")
			}
			signalType, err := signalTypeName(slot.Signal.Type)
			if err != nil {
				return "", "", fmt.Errorf("slot %d: %w", slot.Index, err)
			}
			quality := "normal"
			if slot.Signal.Quality != nil && *slot.Signal.Quality != "" {
				quality = *slot.Signal.Quality
			}
			fmt.Fprintf(&checkBody, "if not prototypes.quality[%s] then fail(\"quality not found: \"..%s); return end\n",
				luaString(quality), luaString(quality))
			filter := fmt.Sprintf("{value={type=%s,name=%s,quality=%s},min=%d}",
				luaString(signalType), luaString(slot.Signal.Name), luaString(quality), slot.Count)
			// The engine message carries the real cause (unknown prototype, bad
			// index), so surface it instead of a generic failure.
			fmt.Fprintf(&writeBody, "local ok_slot,slot_error=pcall(function() section.set_slot(%d,%s) end)\nif not ok_slot then fail(\"slot %d: \"..tostring(slot_error)); return end\n",
				slot.Index, filter, slot.Index)
		}
		writeBody.WriteString("end\n")
	}
	return checkBody.String(), writeBody.String(), nil
}

// setConstantCombinator replaces a constant combinator's enabled flag and signal
// sections and returns the post-write snapshot.
func setConstantCombinator(r *Resolver, in SetConstantCombinatorInput) (*SettingsResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	clear := in.ClearSections != nil && *in.ClearSections
	if clear && in.Sections != nil {
		return nil, errors.New("clearSections cannot be combined with sections")
	}
	if in.Enabled == nil && in.Sections == nil && !clear {
		return nil, errors.New("set at least one of enabled, sections, or clearSections")
	}
	check, writes, err := constantCombinatorSectionStatements(in.Sections)
	if err != nil {
		return nil, err
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(prefix)
	body.WriteString(`if e.type~="constant-combinator" then fail("entity is not a constant combinator"); return end
local ok_behavior,behavior=pcall(function() return e.get_or_create_control_behavior() end)
if not ok_behavior or not behavior then fail("entity has no control behavior"); return end
`)
	if in.Enabled != nil {
		fmt.Fprintf(&body, "behavior.enabled=%t\n", *in.Enabled)
	}
	// Prototype checks run before the clear so a bad entry cannot leave the
	// combinator without signals.
	body.WriteString(check)
	if in.Sections != nil || clear {
		body.WriteString("for index=behavior.sections_count,1,-1 do behavior.remove_section(index) end\n")
	}
	body.WriteString(writes)
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, constantCombinatorSummary(result))
	return result, nil
}

// entitySettingsQuery reads the settings snapshot of every matching entity in
// one Lua chunk instead of one RCON round trip per entity.
func entitySettingsQuery(r *Resolver, in EntitySettingsInput) (*EntitySettingsPage, error) {
	if strings.TrimSpace(in.Surface) == "" {
		return nil, errors.New("surface must not be empty")
	}
	if strings.TrimSpace(in.Force) == "" {
		return nil, errors.New("force must not be empty")
	}
	if in.Area == nil {
		return nil, errors.New("area is required")
	}
	area, err := chartTagAreaLua(in.Area)
	if err != nil {
		return nil, err
	}
	if in.Limit < 1 || in.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	inventory := "chest"
	if in.Inventory != nil && *in.Inventory != "" {
		inventory = *in.Inventory
	}
	if _, ok := settingsInventoryNames[inventory]; !ok {
		return nil, errors.New("inventory must be chest, fuel, input, output, modules, or turret-ammo")
	}
	for _, name := range in.Names {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("names must not contain an empty prototype name")
		}
	}
	for _, kind := range in.Types {
		if strings.TrimSpace(kind) == "" {
			return nil, errors.New("types must not contain an empty entity type")
		}
	}
	lua := luaFail + settingsLua + fmt.Sprintf(`local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local force=game.forces[%s]; if not force then fail("force not found"); return end
local filter={area=%s,force=force}
local names=%s
if names then filter.name=names end
local types=%s
if types then filter.type=types end
local found=surface.find_entities_filtered(filter)
local matched=#found
table.sort(found,function(a,b)
  local au=a.unit_number or 0
  local bu=b.unit_number or 0
  if au~=bu then return au<bu end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  return a.position.y<b.position.y
end)
local rows={}
for index=1,math.min(#found,%d) do
  rows[#rows+1]=settings_sections(found[index],%s)
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,matched=matched,entities=rows}))
`, luaString(in.Surface), luaString(in.Force), area, luaStringList(in.Names), luaStringList(in.Types), in.Limit, luaString(inventory))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	page, err := factoryDecode[EntitySettingsPage](response)
	if err != nil {
		return nil, err
	}
	if page.Entities == nil {
		page.Entities = []*SettingsResult{}
	}
	return page, nil
}

// luaStringList renders a string slice as a Lua array constructor, or nil so the
// caller can omit the engine filter entirely.
func luaStringList(values []string) string {
	if len(values) == 0 {
		return "nil"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, luaString(value))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// logisticSectionStatements validates sections and builds Lua that first checks
// every item and quality prototype (check) and then adds the sections (writes).
// Each section lives in its own do-block so many sections cannot exhaust the
// Lua 200-active-locals limit.
//
// A slot without max writes no engine max, which Factorio stores as an infinite
// request: that keeps a read (max absent for infinite) round-tripping into a
// write instead of forcing a bogus max of 0.
func logisticSectionStatements(sections []*LogisticSectionInput, point, section string) (check, writes string, err error) {
	var checkBody, writeBody strings.Builder
	count := 0
	for _, input := range sections {
		if input == nil {
			continue
		}
		count++
		if count > maxLogisticSections {
			return "", "", fmt.Errorf("at most %d sections are supported", maxLogisticSections)
		}
		fmt.Fprintf(&writeBody, "do local %s=%s.add_section()\nif not %s then fail(\"could not add a logistic section\"); return end\n", section, point, section)
		for _, slot := range input.Slots {
			if slot == nil {
				continue
			}
			if slot.Index < 1 || slot.Name == "" {
				return "", "", errors.New("each request needs a positive index and a name")
			}
			min := 0
			if slot.Min != nil {
				min = *slot.Min
			}
			if min < 0 {
				return "", "", errors.New("each request needs a non-negative min")
			}
			if slot.Max != nil && *slot.Max < min {
				return "", "", errors.New("each request needs min <= max when a max is given; omit max for infinite")
			}
			quality := "normal"
			if slot.Quality != nil && *slot.Quality != "" {
				quality = *slot.Quality
			}
			filter := fmt.Sprintf("{value={name=%s,quality=%s},min=%d", luaString(slot.Name), luaString(quality), min)
			if slot.Max != nil {
				filter += fmt.Sprintf(",max=%d", *slot.Max)
			}
			filter += "}"
			fmt.Fprintf(&checkBody, "if not prototypes.item[%s] then fail(\"item prototype not found: \"..%s); return end\nif not prototypes.quality[%s] then fail(\"quality not found: \"..%s); return end\n",
				luaString(slot.Name), luaString(slot.Name), luaString(quality), luaString(quality))
			// The engine message carries the real cause, so surface it instead of
			// replacing it with a generic failure.
			fmt.Fprintf(&writeBody, "local ok_slot,slot_error=pcall(function() %s.set_slot(%d,%s) end)\nif not ok_slot then fail(\"slot %d: \"..tostring(slot_error)); return end\n",
				section, slot.Index, filter, slot.Index)
		}
		writeBody.WriteString("end\n")
	}
	return checkBody.String(), writeBody.String(), nil
}

// conditionLua renders a circuit condition as a Lua constructor.
func conditionLua(in SignalConditionInput) (string, error) {
	if in.Comparator == "" {
		return "", errors.New("comparator is required")
	}
	var b strings.Builder
	b.WriteString("{comparator=")
	b.WriteString(luaString(in.Comparator))
	if in.Constant != nil {
		fmt.Fprintf(&b, ",constant=%s", formatFloat(*in.Constant))
	}
	if in.FirstSignal != nil {
		signal, err := signalLua(*in.FirstSignal)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, ",first_signal=%s", signal)
	}
	if in.SecondSignal != nil {
		signal, err := signalLua(*in.SecondSignal)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, ",second_signal=%s", signal)
	}
	b.WriteString("}")
	return b.String(), nil
}

func signalLua(in SignalInput) (string, error) {
	if in.Name == "" {
		return "", errors.New("signal name is required")
	}
	signalType, err := signalTypeName(in.Type)
	if err != nil {
		return "", err
	}
	b := "{type=" + luaString(signalType) + ",name=" + luaString(in.Name)
	if in.Quality != nil && *in.Quality != "" {
		b += ",quality=" + luaString(*in.Quality)
	}
	return b + "}", nil
}

// signalTypeNames maps the GraphQL SignalType enum to the engine's SignalIDType
// names. Factorio has exactly eight signal types, and sending anything else
// (notably the invalid "virtual-signal" the old docs suggested) makes the engine
// reject the containing assignment.
var signalTypeNames = map[SignalType]string{
	SignalTypeItem:          "item",
	SignalTypeFluid:         "fluid",
	SignalTypeVirtual:       "virtual",
	SignalTypeEntity:        "entity",
	SignalTypeRecipe:        "recipe",
	SignalTypeSpaceLocation: "space-location",
	SignalTypeAsteroidChunk: "asteroid-chunk",
	SignalTypeQuality:       "quality",
}

// signalTypeName returns the engine name for one signal type, defaulting to item.
func signalTypeName(kind *SignalType) (string, error) {
	if kind == nil {
		return "item", nil
	}
	name, ok := signalTypeNames[*kind]
	if !ok {
		return "", fmt.Errorf("signal type %q is not supported; use item, fluid, virtual, entity, recipe, space-location, asteroid-chunk or quality", string(*kind))
	}
	return name, nil
}

// lampColorModeNames maps the GraphQL enum to defines.control_behavior.lamp.color_mode.
var lampColorModeNames = map[LampColorMode]string{
	LampColorModeColorMapping: "color_mapping",
	LampColorModeComponents:   "components",
	LampColorModePackedRgb:    "packed_rgb",
}

// lampColorModeName returns the engine enum name, or "" when no mode is set.
func lampColorModeName(mode *LampColorMode) (string, error) {
	if mode == nil {
		return "", nil
	}
	name, ok := lampColorModeNames[*mode]
	if !ok {
		return "", fmt.Errorf("colorMode %q is not supported; use COLOR_MAPPING, COMPONENTS or PACKED_RGB", string(*mode))
	}
	return name, nil
}

func connectWire(r *Resolver, in WireInput, connect bool) (*WireResult, error) {
	if in.From == nil || in.To == nil || in.From.Target == nil || in.To.Target == nil {
		return nil, errors.New("from and to endpoints are required")
	}
	surface := stringValueOr(in.From.Target.Surface, "nauvis")
	if stringValueOr(in.To.Target.Surface, "nauvis") != surface {
		return nil, errors.New("wire endpoints must be on the same surface")
	}
	character, player, err := settingsActorFrom(in.Actor)
	if err != nil {
		return nil, err
	}
	color := strings.ToLower(string(in.Color))
	if color != "red" && color != "green" {
		return nil, errors.New("color must be RED or GREEN")
	}
	var b strings.Builder
	b.WriteString(luaFail)
	b.WriteString(settingsActorPrefix(character, player))
	actorSurface := luaString(surface)
	if character != 0 {
		b.WriteString("local actor_surface=c.surface.name\n")
		actorSurface = "actor_surface"
	}
	fromConnector := stringValueOr(in.From.Connector, "")
	toConnector := stringValueOr(in.To.Connector, "")
	_, fromEntity, fromUnitNumber, fromX, fromY := entityRefFields(in.From.Target)
	_, toEntity, toUnitNumber, toX, toY := entityRefFields(in.To.Target)
	fromUnit, err := entityTarget(stringValueOr(fromEntity, ""), stringValueOr(fromUnitNumber, ""), floatValueOr(fromX), floatValueOr(fromY))
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	toUnit, err := entityTarget(stringValueOr(toEntity, ""), stringValueOr(toUnitNumber, ""), floatValueOr(toX), floatValueOr(toY))
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	for _, endpoint := range []struct {
		tag  string
		unit uint64
		name string
		x, y float64
	}{
		{"from", fromUnit, stringValueOr(fromEntity, ""), floatValueOr(fromX), floatValueOr(fromY)},
		{"to", toUnit, stringValueOr(toEntity, ""), floatValueOr(toX), floatValueOr(toY)},
	} {
		fmt.Fprintf(&b, "local %s_unit=%d\nlocal %s_name=%s\nlocal %s_position={x=%s,y=%s}\n",
			endpoint.tag, endpoint.unit, endpoint.tag, luaString(endpoint.name), endpoint.tag,
			luaCoordinate(endpoint.x), luaCoordinate(endpoint.y))
	}
	fmt.Fprintf(&b, `local surface_name=%s
`, actorSurface)
	b.WriteString(luaEntityResolve)
	b.WriteString("local from_entity=resolve_entity(from_unit,from_name,from_position,surface_name,p); if not from_entity then return end\n")
	b.WriteString("local to_entity=resolve_entity(to_unit,to_name,to_position,surface_name,p); if not to_entity then return end\n")
	b.WriteString("if " + reachViolation("c", "from_entity") + ` then fail("from entity is out of reach"); return end
if ` + reachViolation("c", "to_entity") + ` then fail("to entity is out of reach"); return end
`)
	fmt.Fprintf(&b, `local function connector_id(name)
  if name~="" then
    local id=defines.wire_connector_id[name]
    if not id then fail("unknown wire connector "..name); return nil end
    return id
  end
  local id=defines.wire_connector_id["circuit_"..%s]
  if not id then fail("wire color is not supported"); return nil end
  return id
end
local from_id=connector_id(%s)
if not from_id then return end
local to_id=connector_id(%s)
if not to_id then return end
local from_connector=from_entity.get_wire_connector(from_id,true)
local to_connector=to_entity.get_wire_connector(to_id,true)
if not from_connector then fail("from entity has no matching circuit connector"); return end
if not to_connector then fail("to entity has no matching circuit connector"); return end
`, luaString(color), luaString(fromConnector), luaString(toConnector))
	call := "from_connector.disconnect_from(to_connector)"
	if connect {
		call = "from_connector.connect_to(to_connector)"
	}
	fmt.Fprintf(&b, `local ok,result=pcall(function() return %s end)
if not ok then fail("wire operation failed: "..tostring(result)); return end
if not result then fail("wire operation was rejected; entities may be out of wire reach"); return end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface_name,force=from_entity.force.name,color=%s,connected=from_connector.is_connected_to(to_connector),
  from={name=from_entity.name,unitNumber=from_entity.unit_number,position={x=from_entity.position.x,y=from_entity.position.y}},
  to={name=to_entity.name,unitNumber=to_entity.unit_number,position={x=to_entity.position.x,y=to_entity.position.y}}}))
`, call, luaString(strings.ToUpper(color)))
	response, _, err := executeLua(r.addr, r.password, b.String())
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[WireResult](response)
	if err != nil {
		return nil, err
	}
	verb := "disconnected"
	if connect {
		verb = "connected"
	}
	announceMutation(r, in.Message, fmt.Sprintf("%s %s wire between %s and %s", verb, result.Color, result.From.Name, result.To.Name))
	return result, nil
}

func settingsTargetFrom(player, character, surface, entity, unitNumber *string, x, y *float64) (settingsTarget, error) {
	target := settingsTarget{
		Player:     stringValueOr(player, ""),
		Character:  stringValueOr(character, ""),
		Surface:    stringValueOr(surface, "nauvis"),
		Entity:     stringValueOr(entity, ""),
		UnitNumber: stringValueOr(unitNumber, ""),
		X:          math.NaN(),
		Y:          math.NaN(),
	}
	if x != nil {
		target.X = *x
	}
	if y != nil {
		target.Y = *y
	}
	return target, nil
}

func validSplitterPriority(value string) bool {
	return value == "none" || value == "left" || value == "right"
}

func validateColor(in ColorInput) error {
	for _, component := range []float64{in.R, in.G, in.B} {
		if math.IsNaN(component) || math.IsInf(component, 0) || component < 0 || component > 1 {
			return errors.New("color components must be between 0 and 1")
		}
	}
	if in.A != nil && (*in.A < 0 || *in.A > 1) {
		return errors.New("color alpha must be between 0 and 1")
	}
	return nil
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func stringValueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func floatValueOr(value *float64) float64 {
	if value == nil {
		return math.NaN()
	}
	return *value
}

func splitterSettingsSummary(result *SettingsResult) string {
	if result.Splitter == nil {
		return "updated splitter settings"
	}
	filter := "none"
	if result.Splitter.Filter != nil {
		filter = result.Splitter.Filter.Name
	}
	return fmt.Sprintf("splitter%s: filter %s, input %s, output %s", positionText(result.Position), filter,
		result.Splitter.InputPriority, result.Splitter.OutputPriority)
}

func inserterSettingsSummary(result *SettingsResult) string {
	if result.Inserter == nil {
		return "updated inserter settings"
	}
	filled := 0
	for _, slot := range result.Inserter.FilterSlots {
		if slot.Name != nil {
			filled++
		}
	}
	return fmt.Sprintf("inserter%s: %d filter slot(s) set, mode %s, stack override %d", positionText(result.Position), filled,
		stringValue(result.Inserter.FilterMode), result.Inserter.StackSizeOverride)
}

func lampSettingsSummary(result *SettingsResult) string {
	if result.Lamp == nil {
		return "updated lamp settings"
	}
	return fmt.Sprintf("lamp%s: color %.2f/%.2f/%.2f, always on %t", positionText(result.Position),
		result.Lamp.Color.R, result.Lamp.Color.G, result.Lamp.Color.B, result.Lamp.AlwaysOn)
}

func containerSettingsSummary(result *SettingsResult) string {
	if result.Container == nil {
		return "updated container bar"
	}
	allowed := "no limit"
	if result.Container.AllowedSlots != nil {
		allowed = fmt.Sprintf("%d allowed", *result.Container.AllowedSlots)
	}
	return fmt.Sprintf("container%s: %s bar %d of %d, %s", positionText(result.Position), result.Container.Inventory,
		result.Container.Bar, result.Container.Capacity, allowed)
}

func infinityContainerSummary(result *SettingsResult) string {
	if result.Infinity == nil {
		return "updated infinity container"
	}
	return fmt.Sprintf("infinity container%s: %d filter(s), remove unfiltered %t", positionText(result.Position),
		len(result.Infinity.Filters), result.Infinity.RemoveUnfilteredItems)
}

func logisticSettingsSummary(result *SettingsResult) string {
	if result.Logistic == nil {
		return "updated logistic requests"
	}
	slots := 0
	for _, section := range result.Logistic.Sections {
		slots += len(section.Slots)
	}
	return fmt.Sprintf("container%s: %d logistic section(s), %d slot(s)", positionText(result.Position),
		len(result.Logistic.Sections), slots)
}

func trainStopSettingsSummary(result *SettingsResult) string {
	if result.TrainStop == nil {
		return "updated train stop"
	}
	limit := "unlimited"
	if result.TrainStop.TrainsLimit != nil {
		limit = fmt.Sprintf("limit %d", *result.TrainStop.TrainsLimit)
	}
	return fmt.Sprintf("train stop %q%s: %s, priority %d", result.TrainStop.BackerName,
		positionText(result.Position), limit, result.TrainStop.Priority)
}

func constantCombinatorSummary(result *SettingsResult) string {
	if result.ConstantCombinator == nil {
		return "updated constant combinator"
	}
	slots := 0
	for _, section := range result.ConstantCombinator.Sections {
		slots += len(section.Slots)
	}
	return fmt.Sprintf("constant combinator%s: enabled %t, %d section(s), %d signal(s)",
		positionText(result.Position), result.ConstantCombinator.Enabled,
		len(result.ConstantCombinator.Sections), slots)
}

func siloSettingsSummary(result *SettingsResult) string {
	if result.RocketSilo == nil {
		return "updated rocket silo"
	}
	return fmt.Sprintf("rocket silo%s: %d rocket parts, auto launch %t", positionText(result.Position),
		result.RocketSilo.RocketParts, result.RocketSilo.SendToOrbitAutomatically)
}

func circuitSettingsSummary(result *SettingsResult) string {
	if result.Circuit == nil {
		return "updated control behavior"
	}
	return fmt.Sprintf("control behavior %d%s updated", result.Circuit.BehaviorType, positionText(result.Position))
}
