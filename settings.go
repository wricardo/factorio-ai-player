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
	b.WriteString(`if not c.can_reach_entity(e) then fail("entity is out of reach"); return end
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

// settingsLua is shared by every settings read and write.
const settingsLua = luaInserterSettingsBody + `
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
local function settings_signal(signal)
  if not signal then return nil end
  return {name=signal.name,type=signal.type or "item",quality=settings_quality_name(signal.quality)}
end
local function settings_condition(condition)
  if not condition then return nil end
  if not condition.first_signal and not condition.second_signal then return nil end
  return {comparator=condition.comparator,constant=condition.constant,fulfilled=condition.fulfilled,
    firstSignal=settings_signal(condition.first_signal),secondSignal=settings_signal(condition.second_signal)}
end
local function settings_color(color)
  if not color then return nil end
  return {r=color.r,g=color.g,b=color.b,a=color.a}
end
local function settings_item_filter(filter)
  if not filter then return nil end
  local name=filter.name
  if type(name)=="table" then name=name.name end
  return {name=name,quality=settings_quality_name(filter.quality),comparator=filter.comparator}
end
local function settings_common(e)
  return {tick=game.tick,surface=e.surface.name,name=e.name,unitNumber=e.unit_number,position={x=e.position.x,y=e.position.y}}
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
  result.setStackSize=settings_field(control,"circuit_set_stack_size")
  result.connectToLogisticNetwork=settings_field(control,"connect_to_logistic_network")
  result.useColors=settings_field(control,"use_colors")
  result.colorMode=settings_field(control,"color_mode")
  result.lampColor=settings_color(settings_field(control,"color"))
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
  result.trainsLimitSignal=settings_signal(settings_field(control,"trains_limit_signal"))
  result.prioritySignal=settings_signal(settings_field(control,"priority_signal"))
  result.siloReadMode=settings_field(control,"read_mode")
  return result
end
local function settings_inventory_index(name)
  local map={chest=defines.inventory.chest,fuel=defines.inventory.fuel,
    input=defines.inventory.crafter_input,output=defines.inventory.crafter_output,
    modules=defines.inventory.crafter_modules,["turret-ammo"]=defines.inventory.turret_ammo}
  return map[name]
end
local function settings_container(e,inventory_name)
  local index=settings_inventory_index(inventory_name)
  if not index then return nil end
  local ok,supported=pcall(function() return e.inventory_supports_bar(index) end)
  if not ok or not supported then return nil end
  local okbar,bar=pcall(function() return e.get_inventory_bar(index) end)
  local inventory=e.get_inventory(index)
  local capacity=inventory and #inventory or 0
  bar=(okbar and bar) or 0
  if capacity>0 and bar>capacity then bar=0 end
  return {inventory=inventory_name,bar=bar,capacity=capacity}
end
local function settings_logistic(e)
  local ok,point=pcall(function() return e.get_logistic_point(defines.logistic_member_index.logistic_container) end)
  if not ok or not point then return nil end
  local sections={}
  for index=1,20 do
    local oksection,section=pcall(function() return point.get_section(index) end)
    if not oksection or not section then break end
    local slots={}
    for slot=1,30 do
      local okslot,filter=pcall(function() return section.get_slot(slot) end)
      if not okslot or not filter then break end
      local value=filter.value
      local name,quality
      if type(value)=="table" then name=value.name; quality=settings_quality_name(value.quality) else name=value end
      if name==nil then break end
      slots[#slots+1]={index=slot,name=name,quality=quality,min=filter.min or 0,max=filter.max or 0}
    end
    sections[#sections+1]={index=index,slots=slots}
  end
  return {sections=sections}
end
local function settings_train_stop(e)
  return {backerName=settings_field(e,"backer_name") or "",
    trainsLimit=settings_field(e,"trains_limit") or 0,
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
      useColors=control and settings_field(control,"use_colors") or false,
      colorMode=control and settings_field(control,"color_mode") or 0}
  end
  if e.inventory_supports_bar then out.container=settings_container(e,inventory_name) end
  if e.type=="logistic-container" or e.type=="infinity-container" then out.logistic=settings_logistic(e) end
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
	entityLua, err := diagnosticsEntityLua(stringValueOr(obj.Surface, "nauvis"), stringValueOr(obj.Name, ""), nil, obj.Position, stringValueOr(obj.Force, ""))
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
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
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
		comparator := ""
		if in.Filter.Comparator != nil {
			comparator = *in.Filter.Comparator
		}
		fmt.Fprintf(&body, "e.splitter_filter={name=%s,quality=%s,comparator=%s}\n",
			luaString(in.Filter.Name), luaString(quality), luaString(comparator))
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
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
	if err != nil {
		return nil, err
	}
	clear := in.Clear != nil && *in.Clear
	if clear && (len(in.Filters) > 0 || in.FilterMode != nil || in.StackSizeOverride != nil) {
		return nil, errors.New("clear cannot be combined with filters, filterMode, or stackSizeOverride")
	}
	if in.FilterMode != nil && *in.FilterMode != "whitelist" && *in.FilterMode != "blacklist" {
		return nil, errors.New("filterMode must be whitelist or blacklist")
	}
	if in.StackSizeOverride != nil && (*in.StackSizeOverride < 0 || *in.StackSizeOverride > 12) {
		return nil, errors.New("stackSizeOverride must be between 0 and 12")
	}
	if len(in.Filters) == 0 && in.FilterMode == nil && in.StackSizeOverride == nil && !clear {
		return nil, errors.New("set at least one of filters, filterMode, stackSizeOverride, or clear")
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
		}
		body.WriteString("e.use_filters=true\n")
	}
	if in.FilterMode != nil {
		fmt.Fprintf(&body, "e.inserter_filter_mode=%s\n", luaString(*in.FilterMode))
	}
	if in.StackSizeOverride != nil {
		fmt.Fprintf(&body, "e.inserter_stack_size_override=%d\n", *in.StackSizeOverride)
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, inserterSettingsSummary(result))
	return result, nil
}

func setLampSettings(r *Resolver, in LampSettingsInput) (*SettingsResult, error) {
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
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
	if in.ColorMode != nil && (*in.ColorMode < 0 || *in.ColorMode > 2) {
		return nil, errors.New("colorMode must be 0, 1, or 2")
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
	if in.UseColors != nil || in.ColorMode != nil {
		body.WriteString("local control=e.get_or_create_control_behavior()\nif not control then fail(\"lamp has no control behavior\"); return end\n")
		if in.UseColors != nil {
			fmt.Fprintf(&body, "control.use_colors=%t\n", *in.UseColors)
		}
		if in.ColorMode != nil {
			fmt.Fprintf(&body, "control.color_mode=%d\n", *in.ColorMode)
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
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
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
	if in.Bar < 0 || in.Bar > 65536 {
		return nil, errors.New("bar must be zero or between 1 and 65536")
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	barCall := fmt.Sprintf("e.set_inventory_bar(index,%d)", in.Bar)
	if in.Bar == 0 {
		barCall = "e.set_inventory_bar(index,nil)"
	}
	var body strings.Builder
	body.WriteString(prefix)
	fmt.Fprintf(&body, `local index=settings_inventory_index(%s)
if not index then fail("inventory is not supported"); return end
local ok,supported=pcall(function() return e.inventory_supports_bar(index) end)
if not ok or not supported then fail("entity inventory does not support a bar"); return end
local inventory=e.get_inventory(index)
if not inventory then fail("entity does not have that inventory"); return end
if %d>#inventory then fail("bar %d exceeds inventory size "..#inventory); return end
%s
`, luaString(inventory), in.Bar, in.Bar, barCall)
	result, err := executeSettings(r, settingsMutationLua(body.String(), inventory))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, containerSettingsSummary(result))
	return result, nil
}

func setLogisticRequests(r *Resolver, in LogisticRequestsInput) (*SettingsResult, error) {
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
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
	body.WriteString(`local ok,point=pcall(function() return e.get_logistic_point(defines.logistic_member_index.logistic_container) end)
if not ok or not point then fail("entity has no logistic container behavior"); return end
for index=20,1,-1 do point.remove_section(index) end
`)
	if in.Sections != nil {
		for _, section := range in.Sections {
			body.WriteString(`local section=point.add_section()
if not section then fail("could not add a logistic section"); return end
`)
			if section == nil {
				continue
			}
			for _, slot := range section.Slots {
				if slot == nil {
					continue
				}
				if slot.Index < 1 || slot.Name == "" {
					return nil, errors.New("each request needs a positive index and a name")
				}
				min := 0
				if slot.Min != nil {
					min = *slot.Min
				}
				if slot.Max < 0 || min < 0 || slot.Max < min {
					return nil, errors.New("each request needs 0 <= min <= max")
				}
				quality := "normal"
				if slot.Quality != nil && *slot.Quality != "" {
					quality = *slot.Quality
				}
				fmt.Fprintf(&body, "section.set_slot(%d,{value={name=%s,quality=%s},min=%d,max=%d})\n",
					slot.Index, luaString(slot.Name), luaString(quality), min, slot.Max)
			}
		}
	}
	result, err := executeSettings(r, settingsMutationLua(body.String(), "chest"))
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, logisticSettingsSummary(result))
	return result, nil
}

func setTrainStop(r *Resolver, in TrainStopInput) (*SettingsResult, error) {
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
	if err != nil {
		return nil, err
	}
	if in.Name == nil && in.TrainsLimit == nil && in.Priority == nil && in.Color == nil {
		return nil, errors.New("set at least one of name, trainsLimit, priority, or color")
	}
	if in.TrainsLimit != nil && (*in.TrainsLimit < 0 || *in.TrainsLimit > 1000) {
		return nil, errors.New("trainsLimit must be between 0 and 1000")
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
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
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
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
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
	announceMutation(r, in.Message, fmt.Sprintf("launched rocket from %s at %s", result.Name, positionText(result.Position)))
	return result, nil
}

func setCircuitBehavior(r *Resolver, in CircuitBehaviorInput) (*SettingsResult, error) {
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
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
		"setFilter":                in.SetFilter,
		"splitterInputSide":        in.SplitterInputSide,
		"splitterOutputSide":       in.SplitterOutputSide,
		"sendToTrain":              in.SendToTrain,
		"readFromTrain":            in.ReadFromTrain,
		"readStoppedTrain":         in.ReadStoppedTrain,
	}
	intValues := map[string]*int{
		"colorMode":        in.ColorMode,
		"siloReadMode":     in.SiloReadMode,
		"resourceReadMode": in.ResourceReadMode,
		"readItemsMode":    in.ReadItemsMode,
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
		for _, values := range []map[string]any{} {
			_ = values
		}
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
		for name, value := range conditionValues {
			if value != nil {
				return nil, fmt.Errorf("clearConditions cannot be combined with %s", name)
			}
		}
	}
	if err := validateCircuitInts(intValues); err != nil {
		return nil, err
	}
	var assignments []string
	if in.Enabled != nil {
		assignments = append(assignments, circuitApplyLua("enabled", circuitFieldKeys["enabled"], fmt.Sprintf("%t", !*in.Enabled)))
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
local function apply(name,keys,value)
  for _,key in ipairs(keys) do
    local ok=pcall(function() control[key]=value end)
    if ok then return true end
  end
  fail(name.." is not supported by this control behavior"); return false
end
local function reset(keys,value)
  for _,key in ipairs(keys) do pcall(function() control[key]=value end) end
end
`)
	if clear {
		body.WriteString("pcall(function() control.disabled=false end)\n")
		for _, field := range circuitBooleanFields {
			fmt.Fprintf(&body, "reset(%s,false)\n", luaKeys(field.keys))
		}
		for _, field := range circuitIntFields {
			fmt.Fprintf(&body, "reset(%s,0)\n", luaKeys(field.keys))
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
	{"setFilter", []string{"set_filter"}},
	{"splitterInputSide", []string{"set_input_side"}},
	{"splitterOutputSide", []string{"set_output_side"}},
	{"sendToTrain", []string{"send_to_train"}},
	{"readFromTrain", []string{"read_from_train"}},
	{"readStoppedTrain", []string{"read_stopped_train"}},
}

var circuitIntFields = []circuitField{
	{"colorMode", []string{"color_mode"}},
	{"siloReadMode", []string{"read_mode"}},
	{"resourceReadMode", []string{"resource_read_mode"}},
	{"readItemsMode", []string{"read_items_mode"}},
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
	keys := map[string][]string{"enabled": {"disabled"}}
	for _, field := range circuitBooleanFields {
		keys[field.name] = field.keys
	}
	return keys
}()

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
		switch name {
		case "colorMode":
			if *value < 0 || *value > 2 {
				return errors.New("colorMode must be 0, 1, or 2")
			}
		default:
			if *value < 0 {
				return fmt.Errorf("%s must be zero or positive", name)
			}
		}
	}
	return nil
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
	signalType := "item"
	if in.Type != nil && *in.Type != "" {
		signalType = *in.Type
	}
	b := "{type=" + luaString(signalType) + ",name=" + luaString(in.Name)
	if in.Quality != nil && *in.Quality != "" {
		b += ",quality=" + luaString(*in.Quality)
	}
	return b + "}", nil
}

func connectWire(r *Resolver, in WireInput, connect bool) (*WireResult, error) {
	surface := "nauvis"
	if in.Surface != nil && *in.Surface != "" {
		surface = *in.Surface
	}
	character, player, err := settingsActor(stringValueOr(in.Character, ""), stringValueOr(in.Player, ""))
	if err != nil {
		return nil, err
	}
	color := "red"
	if in.Color != nil && *in.Color != "" {
		color = *in.Color
	}
	if color != "red" && color != "green" {
		return nil, errors.New("color must be red or green")
	}
	if in.From == nil || in.To == nil {
		return nil, errors.New("from and to endpoints are required")
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
	fromUnit, err := entityTarget(stringValueOr(in.From.Entity, ""), stringValueOr(in.From.UnitNumber, ""), floatValueOr(in.From.X), floatValueOr(in.From.Y))
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	toUnit, err := entityTarget(stringValueOr(in.To.Entity, ""), stringValueOr(in.To.UnitNumber, ""), floatValueOr(in.To.X), floatValueOr(in.To.Y))
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	for _, endpoint := range []struct {
		tag  string
		unit uint64
		name string
		x, y float64
	}{
		{"from", fromUnit, stringValueOr(in.From.Entity, ""), floatValueOr(in.From.X), floatValueOr(in.From.Y)},
		{"to", toUnit, stringValueOr(in.To.Entity, ""), floatValueOr(in.To.X), floatValueOr(in.To.Y)},
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
	b.WriteString(`if not c.can_reach_entity(from_entity) then fail("from entity is out of reach"); return end
if not c.can_reach_entity(to_entity) then fail("to entity is out of reach"); return end
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
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface_name,color=%s,connected=from_connector.is_connected_to(to_connector),
  from={name=from_entity.name,unitNumber=from_entity.unit_number,position={x=from_entity.position.x,y=from_entity.position.y}},
  to={name=to_entity.name,unitNumber=to_entity.unit_number,position={x=to_entity.position.x,y=to_entity.position.y}}}))
`, call, luaString(color))
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
	return fmt.Sprintf("splitter at %s: filter %s, input %s, output %s", positionText(result.Position), filter,
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
	return fmt.Sprintf("inserter at %s: %d filter slot(s) set, mode %s, stack override %d", positionText(result.Position), filled,
		stringValue(result.Inserter.FilterMode), result.Inserter.StackSizeOverride)
}

func lampSettingsSummary(result *SettingsResult) string {
	if result.Lamp == nil {
		return "updated lamp settings"
	}
	return fmt.Sprintf("lamp at %s: color %.2f/%.2f/%.2f, always on %t", positionText(result.Position),
		result.Lamp.Color.R, result.Lamp.Color.G, result.Lamp.Color.B, result.Lamp.AlwaysOn)
}

func containerSettingsSummary(result *SettingsResult) string {
	if result.Container == nil {
		return "updated container bar"
	}
	return fmt.Sprintf("container at %s: %s bar %d of %d", positionText(result.Position), result.Container.Inventory,
		result.Container.Bar, result.Container.Capacity)
}

func logisticSettingsSummary(result *SettingsResult) string {
	if result.Logistic == nil {
		return "updated logistic requests"
	}
	slots := 0
	for _, section := range result.Logistic.Sections {
		slots += len(section.Slots)
	}
	return fmt.Sprintf("container at %s: %d logistic section(s), %d slot(s)", positionText(result.Position),
		len(result.Logistic.Sections), slots)
}

func trainStopSettingsSummary(result *SettingsResult) string {
	if result.TrainStop == nil {
		return "updated train stop"
	}
	return fmt.Sprintf("train stop %q at %s: limit %d, priority %d", result.TrainStop.BackerName,
		positionText(result.Position), result.TrainStop.TrainsLimit, result.TrainStop.Priority)
}

func siloSettingsSummary(result *SettingsResult) string {
	if result.RocketSilo == nil {
		return "updated rocket silo"
	}
	return fmt.Sprintf("rocket silo at %s: %d rocket parts, auto launch %t", positionText(result.Position),
		result.RocketSilo.RocketParts, result.RocketSilo.SendToOrbitAutomatically)
}

func circuitSettingsSummary(result *SettingsResult) string {
	if result.Circuit == nil {
		return "updated control behavior"
	}
	return fmt.Sprintf("control behavior %d at %s updated", result.Circuit.BehaviorType, positionText(result.Position))
}
