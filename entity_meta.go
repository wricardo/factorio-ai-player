package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// This file implements entity metadata reads and writes: unit/name/tags,
// typed arithmetic and decider combinator parameters, raw fallback parameters,
// and neighbour entities. Unsupported reads are omitted instead of failing
// inspection because most prototypes expose only a subset.

// entityMetaFieldsLua defines entity_meta_fields(entity), which collects the
// metadata an entity exposes. Callers spawn it in the same chunk as the print
// statement so one RCON round trip returns the whole snapshot.
//
// EntityInspection.undergroundNeighbour reports the other end of an underground
// belt: in 2.0.77 LuaEntity.neighbours returns that entity directly for
// underground belts (and nil when the belt is not paired), while
// belt_neighbours never contains it. Belt neighbours otherwise come from
// LuaEntity.belt_neighbours, whose entries are BeltConnectableNeighbour tables
// that hold the entity in their .entity field rather than exposing it directly.
const entityMetaFieldsLua = luaSignalTypeName + `local function entity_meta_neighbour(value)
  if value==nil then return nil end
  -- BeltConnectableNeighbour and fluidbox records wrap the entity in .entity.
  local ok_unwrap,unwrapped=pcall(function() return value.entity end)
  if ok_unwrap and unwrapped~=nil and unwrapped~=value then return entity_meta_neighbour(unwrapped) end
  local entity=value
  local ok_valid,valid=pcall(function() return entity.valid end)
  if ok_valid and valid==false then return nil end
  local ok_name,name=pcall(function() return entity.name end)
  if not ok_name or type(name)~="string" then return nil end
  local item={name=name}
  local ok_type,kind=pcall(function() return entity.type end)
  if ok_type and type(kind)=="string" then item.type=kind end
  local ok_position,position=pcall(function() return entity.position end)
  if ok_position and type(position)=="table" then item.position={x=position.x,y=position.y} end
  local ok_unit,unit=pcall(function() return entity.unit_number end)
  if ok_unit and unit then item.unit_number=unit end
  return item
end
local function entity_meta_collect(list,seen,entity)
  local item=entity_meta_neighbour(entity)
  if not item then return end
  local position=item.position or {x=0,y=0}
  local key=item.name.."|"..tostring(item.unit_number).."|"..tostring(position.x).."|"..tostring(position.y)
  if seen[key] then return end
  seen[key]=true
  list[#list+1]=item
end
local function entity_meta_collect_tree(list,seen,value,depth)
  if value==nil or depth>3 then return end
  if entity_meta_neighbour(value)~=nil then entity_meta_collect(list,seen,value); return end
  if type(value)~="table" then return end
  pcall(function()
    for _,child in pairs(value) do entity_meta_collect_tree(list,seen,child,depth+1) end
  end)
end
local function entity_meta_fields(entity)
  local meta={tick=game.tick,surface=entity.surface.name,force=entity.force.name,name=entity.name,position={x=entity.position.x,y=entity.position.y}}
  local ok_unit,unit=pcall(function() return entity.unit_number end)
  if ok_unit and unit then meta.unit_number=unit end
  local ok_name_tag,name_tag=pcall(function() return entity.name_tag end)
  if ok_name_tag and type(name_tag)=="string" and name_tag~="" then meta.name_tag=name_tag end
  local ok_tags,tags=pcall(function() return helpers.table_to_json(entity.tags) end)
  if ok_tags and tags then meta.tags_json=tags end
  local function entity_meta_parameters(entity)
    local ok_behavior,behavior=pcall(function() return entity.get_control_behavior() end)
    if ok_behavior and behavior~=nil then
      local ok_read,parameters=pcall(function() return behavior.parameters end)
      if ok_read and parameters~=nil then return parameters end
    end
    local ok_fallback,fallback=pcall(function() return entity.parameters end)
    if ok_fallback and fallback~=nil then return fallback end
    return nil
  end
  -- SignalID.type is nil for item signals when reading and lowercase otherwise,
  -- so it is normalized to the SignalType enum name here: a parameter read then
  -- round-trips into setCombinatorParameters, which takes a SignalInput.
  local function entity_meta_signal(signal)
    if not signal then return nil end
    local quality=signal.quality
    if type(quality)~="string" then quality=quality and quality.name or nil end
    return {name=signal.name,type=signal_type_name(signal.type),quality=quality}
  end
  local function entity_meta_networks(selection)
    if not selection then return nil end
    return {red=selection.red,green=selection.green}
  end
  local function entity_meta_arithmetic(parameters)
    return {firstSignal=entity_meta_signal(parameters.first_signal),
      firstSignalNetworks=entity_meta_networks(parameters.first_signal_networks),
      firstConstant=parameters.first_constant,
      secondSignal=entity_meta_signal(parameters.second_signal),
      secondSignalNetworks=entity_meta_networks(parameters.second_signal_networks),
      secondConstant=parameters.second_constant,
      operation=parameters.operation,
      outputSignal=entity_meta_signal(parameters.output_signal)}
  end
  local function entity_meta_decider(parameters)
    local conditions={}
    for _,condition in ipairs(parameters.conditions or {}) do
      conditions[#conditions+1]={firstSignal=entity_meta_signal(condition.first_signal),
        firstSignalNetworks=entity_meta_networks(condition.first_signal_networks),
        secondSignal=entity_meta_signal(condition.second_signal),
        secondSignalNetworks=entity_meta_networks(condition.second_signal_networks),
        constant=condition.constant,comparator=condition.comparator,
        compareType=condition.compare_type}
    end
    local outputs={}
    for _,output in ipairs(parameters.outputs or {}) do
      outputs[#outputs+1]={signal=entity_meta_signal(output.signal),
        copyCountFromInput=output.copy_count_from_input~=false,
        constant=output.constant,networks=entity_meta_networks(output.networks)}
    end
    return {conditions=conditions,outputs=outputs}
  end
  local parameters=entity_meta_parameters(entity)
  if parameters~=nil then
    if entity.type=="arithmetic-combinator" then
      meta.combinator_parameters={arithmetic=entity_meta_arithmetic(parameters)}
    elseif entity.type=="decider-combinator" then
      meta.combinator_parameters={decider=entity_meta_decider(parameters)}
    else
      local ok_json,encoded=pcall(helpers.table_to_json,parameters)
      if ok_json and encoded then meta.parameters_raw_json=encoded end
    end
  end
  if entity.type=="underground-belt" then
    local ok_underground,underground=pcall(function() return entity.neighbours end)
    if ok_underground and underground~=nil then meta.underground_neighbour=entity_meta_neighbour(underground) end
  end
  local ok_belt,belt=pcall(function() return entity.belt_neighbours end)
  if ok_belt and type(belt)=="table" then
    local seen={}; local list={}
    entity_meta_collect_tree(list,seen,belt,1)
    if #list>0 then meta.belt_neighbours=list end
  end
  local ok_fluidbox,fluidbox=pcall(function() return entity.fluidbox end)
  if ok_fluidbox and fluidbox then
    local seen={}; local list={}
    for i=1,10 do
      local ok_connections,connections=pcall(function() return fluidbox:get_connections(i) end)
      if not ok_connections then break end
      if type(connections)=="table" then
        for _,box in pairs(connections) do
          local ok_owner,owner=pcall(function() return box.owner end)
          if ok_owner and owner~=nil then entity_meta_collect(list,seen,owner) end
        end
      end
    end
    if #list>0 then meta.fluidbox_neighbours=list end
  end
  return meta
end
`

// validateTagsJSON rejects anything the engine cannot read as a tag table
// before a single game command runs.
func validateTagsJSON(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("tagsJSON must be a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("tagsJSON must be a JSON object: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return errors.New("tagsJSON must be a JSON object")
	}
	if decoder.More() {
		return errors.New("tagsJSON must be a single JSON object")
	}
	return nil
}
func combinatorSignalLua(signal *SignalInput) (string, error) {
	if signal == nil || strings.TrimSpace(signal.Name) == "" {
		return "", errors.New("signal name must not be empty")
	}
	kind, err := signalTypeName(signal.Type)
	if err != nil {
		return "", err
	}
	result := "{type=" + luaString(kind) + ",name=" + luaString(signal.Name)
	if signal.Quality != nil {
		if strings.TrimSpace(*signal.Quality) == "" {
			return "", errors.New("signal quality must not be empty")
		}
		result += ",quality=" + luaString(*signal.Quality)
	}
	return result + "}", nil
}

func combinatorNetworksLua(networks *CircuitNetworkSelectionInput) string {
	if networks == nil {
		return ""
	}
	red, green := true, true
	if networks.Red != nil {
		red = *networks.Red
	}
	if networks.Green != nil {
		green = *networks.Green
	}
	return fmt.Sprintf("{red=%t,green=%t}", red, green)
}

func appendCombinatorSignal(field string, signal *SignalInput, out *strings.Builder) error {
	if signal == nil {
		return nil
	}
	value, err := combinatorSignalLua(signal)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	fmt.Fprintf(out, "%s=%s,", field, value)
	return nil
}

func appendCombinatorNetworks(field string, networks *CircuitNetworkSelectionInput, out *strings.Builder) {
	if value := combinatorNetworksLua(networks); value != "" {
		fmt.Fprintf(out, "%s=%s,", field, value)
	}
}

func arithmeticCombinatorParametersLua(input *ArithmeticCombinatorParametersInput) (string, error) {
	if input == nil {
		return "", errors.New("arithmetic parameters must not be null")
	}
	validOperations := map[string]bool{"*": true, "/": true, "+": true, "-": true, "%": true, "^": true, "<<": true, ">>": true, "AND": true, "OR": true, "XOR": true}
	if !validOperations[input.Operation] {
		return "", fmt.Errorf("unsupported arithmetic operation %q", input.Operation)
	}
	if input.FirstSignal != nil && input.FirstConstant != nil {
		return "", errors.New("arithmetic firstSignal and firstConstant are mutually exclusive")
	}
	if input.SecondSignal != nil && input.SecondConstant != nil {
		return "", errors.New("arithmetic secondSignal and secondConstant are mutually exclusive")
	}
	if input.OutputSignal == nil {
		return "", errors.New("arithmetic outputSignal must not be null")
	}
	var out strings.Builder
	out.WriteByte('{')
	if err := appendCombinatorSignal("first_signal", input.FirstSignal, &out); err != nil {
		return "", err
	}
	appendCombinatorNetworks("first_signal_networks", input.FirstSignalNetworks, &out)
	if input.FirstConstant != nil {
		fmt.Fprintf(&out, "first_constant=%d,", *input.FirstConstant)
	}
	if err := appendCombinatorSignal("second_signal", input.SecondSignal, &out); err != nil {
		return "", err
	}
	appendCombinatorNetworks("second_signal_networks", input.SecondSignalNetworks, &out)
	if input.SecondConstant != nil {
		fmt.Fprintf(&out, "second_constant=%d,", *input.SecondConstant)
	}
	fmt.Fprintf(&out, "operation=%s,", luaString(input.Operation))
	if err := appendCombinatorSignal("output_signal", input.OutputSignal, &out); err != nil {
		return "", err
	}
	out.WriteByte('}')
	return out.String(), nil
}

func deciderCombinatorParametersLua(input *DeciderCombinatorParametersInput) (string, error) {
	if input == nil {
		return "", errors.New("decider parameters must not be null")
	}
	if len(input.Conditions) == 0 || len(input.Conditions) > maxControlConditions {
		return "", fmt.Errorf("decider conditions must contain between 1 and %d entries", maxControlConditions)
	}
	if len(input.Outputs) == 0 || len(input.Outputs) > maxControlConditions {
		return "", fmt.Errorf("decider outputs must contain between 1 and %d entries", maxControlConditions)
	}
	validComparators := map[string]bool{"<": true, ">": true, "=": true, "≤": true, "≥": true, "≠": true, "<=": true, ">=": true, "!=": true}
	var out strings.Builder
	out.WriteString("{conditions={")
	for i, condition := range input.Conditions {
		if condition == nil {
			return "", fmt.Errorf("decider condition %d must not be null", i+1)
		}
		if condition.SecondSignal != nil && condition.Constant != nil {
			return "", fmt.Errorf("decider condition %d secondSignal and constant are mutually exclusive", i+1)
		}
		if condition.Comparator != nil && !validComparators[*condition.Comparator] {
			return "", fmt.Errorf("decider condition %d has unsupported comparator %q", i+1, *condition.Comparator)
		}
		if condition.CompareType != nil && *condition.CompareType != "and" && *condition.CompareType != "or" {
			return "", fmt.Errorf("decider condition %d compareType must be and or or", i+1)
		}
		out.WriteByte('{')
		if err := appendCombinatorSignal("first_signal", condition.FirstSignal, &out); err != nil {
			return "", fmt.Errorf("decider condition %d: %w", i+1, err)
		}
		appendCombinatorNetworks("first_signal_networks", condition.FirstSignalNetworks, &out)
		if err := appendCombinatorSignal("second_signal", condition.SecondSignal, &out); err != nil {
			return "", fmt.Errorf("decider condition %d: %w", i+1, err)
		}
		appendCombinatorNetworks("second_signal_networks", condition.SecondSignalNetworks, &out)
		if condition.Constant != nil {
			fmt.Fprintf(&out, "constant=%d,", *condition.Constant)
		}
		if condition.Comparator != nil {
			fmt.Fprintf(&out, "comparator=%s,", luaString(*condition.Comparator))
		}
		if condition.CompareType != nil {
			fmt.Fprintf(&out, "compare_type=%s,", luaString(*condition.CompareType))
		}
		out.WriteString("},")
	}
	out.WriteString("},outputs={")
	for i, output := range input.Outputs {
		if output == nil || output.Signal == nil {
			return "", fmt.Errorf("decider output %d signal must not be null", i+1)
		}
		out.WriteByte('{')
		if err := appendCombinatorSignal("signal", output.Signal, &out); err != nil {
			return "", fmt.Errorf("decider output %d: %w", i+1, err)
		}
		copyCount := true
		if output.CopyCountFromInput != nil {
			copyCount = *output.CopyCountFromInput
		}
		fmt.Fprintf(&out, "copy_count_from_input=%t,", copyCount)
		if output.Constant != nil {
			fmt.Fprintf(&out, "constant=%d,", *output.Constant)
		}
		appendCombinatorNetworks("networks", output.Networks, &out)
		out.WriteString("},")
	}
	out.WriteString("}}")
	return out.String(), nil
}

func combinatorParametersLua(input *CombinatorParametersInput) (parameters, entityType string, err error) {
	if input == nil {
		return "", "", errors.New("parameters must not be null")
	}
	if (input.Arithmetic == nil) == (input.Decider == nil) {
		return "", "", errors.New("parameters must provide exactly one of arithmetic or decider")
	}
	if input.Arithmetic != nil {
		parameters, err = arithmeticCombinatorParametersLua(input.Arithmetic)
		return parameters, "arithmetic-combinator", err
	}
	parameters, err = deciderCombinatorParametersLua(input.Decider)
	return parameters, "decider-combinator", err
}

// executeEntityMeta runs one metadata write and returns the post-write snapshot.
func executeEntityMeta(r *Resolver, target settingsTarget, body string) (*EntityMetaResult, error) {
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	lua := luaFail + prefix + entityMetaFieldsLua + body + "\nrcon.print(helpers.table_to_json(entity_meta_fields(e)))\n"
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[EntityMetaResult](response)
}

// setEntityTags replaces an entity's tags and returns the resulting metadata.
func setEntityTags(r *Resolver, in EntityTagsInput) (*EntityMetaResult, error) {
	if err := validateTagsJSON(in.TagsJSON); err != nil {
		return nil, err
	}
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf(`local ok_tags,tags=pcall(helpers.json_to_table,%s)
if not ok_tags or type(tags)~="table" then fail("tagsJSON must be a JSON object"); return end
local ok_write,write_error=pcall(function() e.tags=tags end)
if not ok_write then fail("entity does not accept tags: "..tostring(write_error)); return end
local function tags_equal(left,right)
  if type(left)~="table" or type(right)~="table" then return false end
  for key,value in pairs(left) do
    local other=right[key]
    if type(value)=="table" then
      if not tags_equal(value,other) then return false end
    elseif other~=value then
      return false
    end
  end
  for key in pairs(right) do if left[key]==nil then return false end end
  return true
end
local ok_readback,persisted=pcall(function() return e.tags end)
if not ok_readback or not tags_equal(tags,persisted) then
  fail("entity tags are not persisted by this prototype in Factorio 2.0.77; tags are blueprint and ghost metadata - use createBlueprintFromEntities or set the tag on the ghost")
  return
end
`, luaString(in.TagsJSON))
	result, err := executeEntityMeta(r, target, body)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "set tags on "+result.Name)
	return result, nil
}

// setNameTag sets an entity's map name tag; the empty string clears it.
func setNameTag(r *Resolver, in EntityNameTagInput) (*EntityMetaResult, error) {
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf(`local ok_write,write_error=pcall(function() e.name_tag=%s end)
if not ok_write then fail("entity does not accept a name tag: "..tostring(write_error)); return end
`, luaString(in.NameTag))
	result, err := executeEntityMeta(r, target, body)
	if err != nil {
		return nil, err
	}
	summary := "set name tag on " + result.Name
	if in.NameTag == "" {
		summary = "cleared name tag on " + result.Name
	}
	announceMutation(r, in.Message, summary)
	return result, nil
}

// setCombinatorParameters replaces one arithmetic or decider behavior parameter table.
func setCombinatorParameters(r *Resolver, in SetCombinatorParametersInput) (*EntityMetaResult, error) {
	parameters, entityType, err := combinatorParametersLua(in.Parameters)
	if err != nil {
		return nil, err
	}
	target, err := settingsTargetFromRef(in.Actor, in.Target)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf(`if e.type~=%s then fail("parameter family does not match target "..e.type); return end
local ok_behavior,behavior=pcall(function() return e.get_control_behavior() end)
if not ok_behavior or not behavior then fail("entity has no control behavior"); return end
local ok_write,write_error=pcall(function() behavior.parameters=%s end)
if not ok_write then fail("entity rejected combinator parameters: "..tostring(write_error)); return end
`, luaString(entityType), parameters)
	result, err := executeEntityMeta(r, target, body)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "set combinator parameters on "+result.Name)
	return result, nil
}
