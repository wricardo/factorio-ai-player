package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// diagnosticsEntityLua binds e without scanning the surface or accepting a
// replacement entity at the old position after a parent snapshot goes stale.
func diagnosticsEntityLua(surface, name string, unit *string, position *Position, force string) (string, error) {
	if surface == "" || name == "" || position == nil {
		return "", errors.New("diagnostics require entity surface, name and position")
	}
	if math.IsNaN(position.X) || math.IsInf(position.X, 0) || math.IsNaN(position.Y) || math.IsInf(position.Y, 0) {
		return "", errors.New("diagnostics entity coordinates must be finite")
	}
	unitLua := "nil"
	if unit != nil {
		n, err := strconv.ParseUint(*unit, 10, 64)
		if err != nil || n == 0 {
			return "", errors.New("diagnostics entity unit number must be a positive uint64")
		}
		unitLua = luaString(strconv.FormatUint(n, 10))
	}
	return fmt.Sprintf(`
local surface = game.surfaces[%s]
if not surface then fail("surface not found"); return end
local expected_unit = %s
local e = surface.find_entity(%s, {%s,%s})
if not e or not e.valid or (expected_unit and string.format("%%.0f", e.unit_number or 0) ~= expected_unit) then
  fail("diagnostics entity no longer exists at its snapshot position"); return
end
local expected_force = %s
if expected_force ~= "" and e.force.name ~= expected_force then fail("diagnostics entity force changed"); return end
`, luaString(surface), unitLua, luaString(name), strconv.FormatFloat(position.X, 'g', -1, 64), strconv.FormatFloat(position.Y, 'g', -1, 64), luaString(force)), nil
}

type diagnosticIngredient struct {
	Name               string   `json:"name"`
	Type               string   `json:"type"`
	Amount             float64  `json:"amount"`
	Temperature        *float64 `json:"temperature"`
	MinimumTemperature *float64 `json:"minimumTemperature"`
	MaximumTemperature *float64 `json:"maximumTemperature"`
}

type diagnosticProduct struct {
	Name               string   `json:"name"`
	Type               string   `json:"type"`
	Amount             *float64 `json:"amount"`
	AmountMin          *float64 `json:"amountMin"`
	AmountMax          *float64 `json:"amountMax"`
	Probability        float64  `json:"probability"`
	ExtraCountFraction float64  `json:"extraCountFraction"`
}

type diagnosticInput struct {
	Name    string  `json:"name"`
	Quality string  `json:"quality"`
	Count   float64 `json:"count"`
}

type diagnosticFluid struct {
	Name        string  `json:"name"`
	Amount      float64 `json:"amount"`
	Temperature float64 `json:"temperature"`
}

type machineDiagnosticMining struct {
	Target                    string   `json:"target"`
	Amount                    *float64 `json:"amount"`
	TheoreticalUnitsPerSecond *float64 `json:"theoreticalUnitsPerSecond"`
	ProductivityBonus         *float64 `json:"productivityBonus"`
}

type machineDiagnosticBurner struct {
	FuelItems                  []diagnosticInput `json:"fuelItems"`
	RemainingBurningFuelJoules float64           `json:"remainingBurningFuelJoules"`
	CurrentlyBurning           *string           `json:"currentlyBurning"`
	EstimatedSecondsOfFuel     *float64          `json:"estimatedSecondsOfFuel"`
}

type machineDiagnosticSnapshot struct {
	Tick              float64                  `json:"tick"`
	Recipe            *string                  `json:"recipe"`
	RecipeQuality     *string                  `json:"recipeQuality"`
	RecipeSource      *string                  `json:"recipeSource"`
	RecipeDuration    float64                  `json:"recipeDuration"`
	CraftingSpeed     float64                  `json:"craftingSpeed"`
	ProductivityBonus *float64                 `json:"productivityBonus"`
	Ingredients       []diagnosticIngredient   `json:"ingredients"`
	Products          []diagnosticProduct      `json:"products"`
	Input             []diagnosticInput        `json:"input"`
	Fluids            []diagnosticFluid        `json:"fluids"`
	Mining            *machineDiagnosticMining `json:"mining"`
	Burner            *machineDiagnosticBurner `json:"burner"`
	RawStatusName     *string                  `json:"rawStatusName"`
	RawStatusCode     *int                     `json:"rawStatusCode"`
	Power             *MachinePowerState       `json:"power"`
}

// machineDiagnosticsSnapshotLua defines machine_diagnostics_snapshot(e), which
// reads one entity into a raw snapshot table. It never calls fail(), so batch
// callers can compose it; the supported-type guard lives in the body below.
const machineDiagnosticsSnapshotLua = `
local function machine_diagnostics_quality_name(quality)
  if quality==nil then return "normal" end
  local ok,name=pcall(function() return quality.name end)
  if ok and name then return name end
  if type(quality)=="string" then return quality end
  return "normal"
end
local function machine_diagnostics_contents(inv)
  if not inv then return {} end
  local result={}
  for _,stack in pairs(inv.get_contents()) do
    result[#result+1]={name=stack.name,quality=machine_diagnostics_quality_name(stack.quality),count=stack.count}
  end
  table.sort(result,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
  return result
end
local function machine_diagnostics_snapshot(e)
  local status_names={}; for name,code in pairs(defines.entity_status) do status_names[code]=name end
  local snapshot={tick=game.tick,ingredients={},products={},input={},fluids={},
    raw_status_name=status_names[e.status] or "unknown",raw_status_code=e.status}
  local recipe,recipe_quality
  if e.type=="furnace" or e.type=="assembling-machine" or e.type=="rocket-silo" then
    recipe,recipe_quality=e.get_recipe()
    if recipe then
      snapshot.recipe_source="CURRENT"
    elseif e.type=="furnace" then
      local previous=e.previous_recipe
      if previous then
        recipe,recipe_quality=previous.name,previous.quality
        snapshot.recipe_source="PREVIOUS"
      end
    end
  end
  if recipe then
    snapshot.recipe=recipe.name
    snapshot.recipe_quality=machine_diagnostics_quality_name(recipe_quality)
    snapshot.recipe_duration=recipe.energy
    snapshot.ingredients=recipe.ingredients or {}
    snapshot.products=recipe.products or {}
  end
  local ok_speed,speed=pcall(function() return e.crafting_speed end)
  snapshot.crafting_speed=ok_speed and speed or 0
  local ok_productivity,productivity=pcall(function() return e.productivity_bonus end)
  if ok_productivity then snapshot.productivity_bonus=productivity end
  if e.type=="furnace" then
    snapshot.input=machine_diagnostics_contents(e.get_inventory(defines.inventory.furnace_source))
  elseif e.type=="assembling-machine" or e.type=="rocket-silo" then
    snapshot.input=machine_diagnostics_contents(e.get_inventory(defines.inventory.crafter_input))
  elseif e.type=="lab" then
    snapshot.input=machine_diagnostics_contents(e.get_inventory(defines.inventory.lab_input))
  end
  for index=1,#e.fluidbox do
    local fluid=e.fluidbox[index]
    local include=true
    local ok_proto,box=pcall(function() return e.fluidbox.get_prototype(index) end)
    if ok_proto and box and box.production_type then include=box.production_type~="output" end
    if include and fluid then
      snapshot.fluids[#snapshot.fluids+1]={name=fluid.name,amount=fluid.amount,temperature=fluid.temperature}
    end
  end
  if e.type=="mining-drill" then
    local target=e.mining_target
    if target and target.valid then
      local properties=target.prototype.mineable_properties
      local mining={target=target.name}
      if not target.prototype.infinite_resource then mining.amount=target.amount end
      local yield=0
      for _,product in pairs(properties.products or {}) do
        if product.type=="item" then
          yield=yield+(product.amount or ((product.amount_min or 0)+(product.amount_max or 0))/2)*(product.probability or 1)
        end
      end
      if properties.mining_time and properties.mining_time>0 then
        mining.theoretical_units_per_second=e.prototype.mining_speed*(1+e.speed_bonus)/properties.mining_time*yield
      end
      if ok_productivity then mining.productivity_bonus=productivity end
      snapshot.mining=mining
    end
  end
  local ok_burner,burner=pcall(function() return e.burner end)
  if ok_burner and burner then
    local fuel=machine_diagnostics_contents(e.get_fuel_inventory())
    local burner_state={fuel_items=fuel,remaining_burning_fuel_joules=burner.remaining_burning_fuel}
    local burning=burner.currently_burning
    if burning then burner_state.currently_burning=burning.name end
    local ok_usage,usage=pcall(function() return e.prototype.get_max_energy_usage(e.quality) end)
    if ok_usage and usage and usage>0 then
      local joules=burner.remaining_burning_fuel or 0
      for _,stack in pairs(fuel) do
        local prototype=prototypes.item[stack.name]
        if prototype and prototype.fuel_value then joules=joules+prototype.fuel_value*stack.count end
      end
      burner_state.estimated_seconds_of_fuel=joules/(usage*60)
    end
    snapshot.burner=burner_state
  end
  local buffer=(e.electric_buffer_size or 0)
  local drain=(e.electric_drain or 0)
  local electric=buffer>0 or drain>0
  local connected,network_id,producing=nil,nil,nil
  if electric then
    local ok_id,id=pcall(function() return e.electric_network_id end)
    if ok_id and id then network_id=tostring(id); connected=true else connected=false end
    local ok_connected,value=pcall(function() return e.is_connected_to_electric_network() end)
    if ok_connected then producing=value end
  end
  snapshot.power={electric=electric,connected=connected,network_id=network_id,
    connected_to_producing_network=producing,satisfaction_unavailable_reason="Factorio exposes no instantaneous per-machine power satisfaction ratio"}
  return snapshot
end
`

// machineDiagnosticsBody keeps the single-entity body. The final print and the
// snapshot local are part of the contract with batch callers that rewrite it.
const machineDiagnosticsBody = machineDiagnosticsSnapshotLua + `
local supported={["assembling-machine"]=true,furnace=true,["mining-drill"]=true,lab=true,["rocket-silo"]=true}
if not supported[e.type] then fail("endpoint is no longer a supported machine"); return end
local snapshot = machine_diagnostics_snapshot(e)
rcon.print(helpers.table_to_json(snapshot))`

// machineDiagnosticsQuery reads one entity into the modelled diagnostics shape.
// Machine pages already compute diagnostics inside their own snapshot, so this
// single-entity path serves callers that only hold an entity reference.
func machineDiagnosticsQuery(r *Resolver, obj *FactoryMachine) (*MachineDiagnostics, error) {
	if obj == nil || obj.Force == "" {
		return nil, errors.New("diagnostics require a machine and force")
	}
	entityLua, err := diagnosticsEntityLua(obj.Surface, obj.Name, obj.UnitNumber, obj.Position, obj.Force)
	if err != nil {
		return nil, err
	}
	lua := luaFail + entityLua + machineDiagnosticsBody
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	snapshot, err := factoryDecode[machineDiagnosticSnapshot](response)
	if err != nil {
		return nil, err
	}
	return calculateMachineDiagnostics(snapshot), nil
}

func calculateMachineDiagnostics(s *machineDiagnosticSnapshot) *MachineDiagnostics {
	result := &MachineDiagnostics{Tick: s.Tick, Recipe: s.Recipe, RecipeQuality: s.RecipeQuality,
		Blocked: diagnosticBlocked(s.RawStatusName, s.RawStatusCode), Power: s.Power}
	if s.RecipeSource != nil {
		source := RecipeSource(*s.RecipeSource)
		result.RecipeSource = &source
	}
	if s.Mining != nil {
		result.Mining = &MachineMiningRate{Target: s.Mining.Target, Amount: s.Mining.Amount,
			TheoreticalUnitsPerSecond: s.Mining.TheoreticalUnitsPerSecond, ProductivityBonus: s.Mining.ProductivityBonus}
	}
	if s.Burner != nil {
		fuel := make([]*InventoryStack, 0, len(s.Burner.FuelItems))
		for _, stack := range s.Burner.FuelItems {
			fuel = append(fuel, &InventoryStack{Name: stack.Name, Quality: stack.Quality, Count: stack.Count})
		}
		result.Burner = &MachineBurnerState{FuelItems: fuel, RemainingBurningFuelJoules: s.Burner.RemainingBurningFuelJoules,
			CurrentlyBurning: s.Burner.CurrentlyBurning, EstimatedSecondsOfFuel: s.Burner.EstimatedSecondsOfFuel}
	}
	if s.Recipe == nil {
		reason := "No crafting recipe selected and no rate applies to this machine type; labs research and machines without a recipe do not craft"
		switch {
		case s.Mining != nil:
			reason = "Mining rate is reported in mining; there is no crafting recipe to derive a crafting throughput"
		case s.Burner != nil && s.Power == nil:
			reason = "Machine has no crafting recipe; only its burner fuel state is known"
		}
		result.ThroughputUnavailableReason = &reason
		return result
	}
	quality := "normal"
	if s.RecipeQuality != nil {
		quality = *s.RecipeQuality
	}
	result.Shortfall = make([]*MachineIngredientDemand, 0, len(s.Ingredients))
	for _, ingredient := range s.Ingredients {
		demand := &MachineIngredientDemand{Name: ingredient.Name, Type: ingredient.Type, RequiredPerCraft: ingredient.Amount}
		if ingredient.Type == "item" {
			demand.Quality = new(quality)
			for _, stack := range s.Input {
				if stack.Name == ingredient.Name && stack.Quality == quality {
					demand.Available += stack.Count
				}
			}
		} else if ingredient.Type == "fluid" {
			demand.MinimumTemperatureCelsius = ingredient.MinimumTemperature
			demand.MaximumTemperatureCelsius = ingredient.MaximumTemperature
			if ingredient.Temperature != nil {
				demand.MinimumTemperatureCelsius = ingredient.Temperature
				demand.MaximumTemperatureCelsius = ingredient.Temperature
			}
			for _, fluid := range s.Fluids {
				if fluid.Name != ingredient.Name ||
					(demand.MinimumTemperatureCelsius != nil && fluid.Temperature < *demand.MinimumTemperatureCelsius) ||
					(demand.MaximumTemperatureCelsius != nil && fluid.Temperature > *demand.MaximumTemperatureCelsius) {
					continue
				}
				demand.Available += fluid.Amount
			}
		}
		demand.Missing = math.Max(0, demand.RequiredPerCraft-demand.Available)
		result.Shortfall = append(result.Shortfall, demand)
	}
	if s.RecipeDuration <= 0 || !diagnosticFinite(s.RecipeDuration) || s.CraftingSpeed < 0 || !diagnosticFinite(s.CraftingSpeed) || !diagnosticFinite(s.CraftingSpeed/s.RecipeDuration) {
		reason := "Recipe duration and crafting speed do not define a finite theoretical rate"
		result.ThroughputUnavailableReason = &reason
		return result
	}
	rate := s.CraftingSpeed / s.RecipeDuration
	throughput := &MachineTheoreticalThroughput{TheoreticalCraftsPerSecond: rate, RecipeDurationSeconds: s.RecipeDuration,
		CraftingSpeed: s.CraftingSpeed, ProductivityBonus: s.ProductivityBonus,
		TheoreticalBaseProducts: make([]*MachineTheoreticalProductRate, 0, len(s.Products))}
	for _, product := range s.Products {
		amount := 0.0
		if product.Amount != nil {
			amount = *product.Amount
		} else if product.AmountMin != nil && product.AmountMax != nil {
			amount = (*product.AmountMin + *product.AmountMax) / 2
		}
		expected := amount*product.Probability + product.ExtraCountFraction
		throughput.TheoreticalBaseProducts = append(throughput.TheoreticalBaseProducts, &MachineTheoreticalProductRate{
			Name: product.Name, Type: product.Type, ExpectedBaseUnitsPerCraft: expected, TheoreticalBaseUnitsPerSecond: expected * rate})
	}
	result.Throughput = throughput
	return result
}

func diagnosticFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func diagnosticBlocked(status *string, code *int) *MachineBlockedReason {
	result := &MachineBlockedReason{Category: MachineBlockCategoryOther, RawStatusName: status, RawStatusCode: code}
	if status == nil {
		return result
	}
	var action string
	switch *status {
	case "working", "normal":
		result.Category = MachineBlockCategoryNone
	case "no_recipe":
		result.Category, action = MachineBlockCategoryMissingRecipe, "Select a compatible recipe"
	case "no_ingredients", "item_ingredient_shortage", "fluid_ingredient_shortage", "missing_science_packs", "missing_required_fluid", "no_input_fluid", "low_input_fluid":
		result.Category, action = MachineBlockCategoryMissingIngredients, "Supply the missing compatible ingredients; inspect one-craft shortfall where a recipe is selected"
	case "no_research_in_progress", "recipe_not_researched":
		result.Category, action = MachineBlockCategoryMissingResearch, "Start an available research technology"
	case "no_fuel":
		result.Category, action = MachineBlockCategoryMissingFuel, "Supply compatible fuel"
	case "no_power", "low_power", "not_plugged_in_electric_network", "recharging_after_power_outage":
		result.Category, action = MachineBlockCategoryMissingPower, "Check electric network connection, generation and demand"
	case "full_output", "full_burnt_result_output", "waiting_for_space_in_destination", "not_enough_space_in_output":
		result.Category, action = MachineBlockCategoryOutputBlocked, "Clear or transport the blocked output"
	case "disabled_by_control_behavior", "disabled_by_script", "disabled", "frozen", "paused", "marked_for_deconstruction":
		result.Category, action = MachineBlockCategoryDisabled, "Inspect the disabling control behavior, script, freeze, pause or deconstruction order"
	case "no_minable_resources":
		result.Category, action = MachineBlockCategoryResourceDepleted, "Relocate the machine to remaining resources or remove it"
	}
	if action != "" {
		result.SuggestedAction = &action
	}
	return result
}

// luaInserterSettingsBody defines inserter_settings(e) for both the inserter
// settings read path and the entity settings API. It only reads an existing
// control behavior; reading never creates one.
const luaInserterSettingsBody = `
local function inserter_quality_name(q)
  if type(q) == "string" then return q end
  return q and q.name
end
local function inserter_signal(s)
  if not s then return nil end
  return {name=s.name, type=s.type or "item", quality=inserter_quality_name(s.quality)}
end
local function inserter_condition(c)
  if not c then return nil end
  return {comparator=c.comparator, constant=c.constant, fulfilled=c.fulfilled,
    firstSignal=inserter_signal(c.first_signal), secondSignal=inserter_signal(c.second_signal)}
end
local function inserter_settings(e)
  local result = {tick=game.tick, filterSlotCount=e.filter_slot_count, filterSlots={},
    useFilters=e.use_filters, filterMode=e.inserter_filter_mode,
    stackSizeOverride=e.inserter_stack_size_override, active=e.active,
    pickupFromLeftLane=e.pickup_from_left_lane, pickupFromRightLane=e.pickup_from_right_lane}
  for index=1,e.filter_slot_count do
    local filter = e.get_filter(index)
    local slot = {index=index}
    if type(filter) == "string" then
      slot.name=filter
    elseif filter then
      slot.name=type(filter.name) == "string" and filter.name or (filter.name and filter.name.name)
      slot.quality=inserter_quality_name(filter.quality)
      slot.comparator=filter.comparator
    end
    result.filterSlots[#result.filterSlots+1]=slot
  end
  local control = e.get_control_behavior()
  if control then
    result.circuit = {enabled=not control.disabled, circuitEnableDisable=control.circuit_enable_disable, disabledByControlBehavior=control.disabled,
      setFilters=control.circuit_set_filters, readHandContents=control.circuit_read_hand_contents,
      handReadMode=control.circuit_hand_read_mode, setStackSize=control.circuit_set_stack_size,
      stackControlSignal=inserter_signal(control.circuit_stack_control_signal),
      connectToLogisticNetwork=control.connect_to_logistic_network,
      circuitCondition=inserter_condition(control.circuit_condition), logisticCondition=inserter_condition(control.logistic_condition)}
  end
  return result
end
`

func inserterSettingsQuery(r *Resolver, obj *LogisticsEndpoint) (*InserterSettings, error) {
	if obj == nil {
		return nil, errors.New("inserter settings require an endpoint")
	}
	if obj.Type != "inserter" {
		return nil, nil
	}
	entityLua, err := diagnosticsEntityLua(obj.Surface, obj.Name, obj.UnitNumber, obj.Position, "")
	if err != nil {
		return nil, err
	}
	lua := luaFail + entityLua + `
if e.type ~= "inserter" then fail("endpoint is no longer an inserter"); return end
local function quality_name(q)
  if type(q) == "string" then return q end
  return q and q.name
end
local function signal(s)
  if not s then return nil end
  return {name=s.name, type=s.type or "item", quality=quality_name(s.quality)}
end
local function condition(c)
  if not c then return nil end
  return {comparator=c.comparator, constant=c.constant, fulfilled=c.fulfilled,
    firstSignal=signal(c.first_signal), secondSignal=signal(c.second_signal)}
end
local result = {tick=game.tick, filterSlotCount=e.filter_slot_count, filterSlots={},
  useFilters=e.use_filters, filterMode=e.inserter_filter_mode,
  stackSizeOverride=e.inserter_stack_size_override, active=e.active,
  pickupFromLeftLane=e.pickup_from_left_lane, pickupFromRightLane=e.pickup_from_right_lane}
for index=1,e.filter_slot_count do
  local filter = e.get_filter(index)
  local slot = {index=index}
  if type(filter) == "string" then
    slot.name=filter
  elseif filter then
    slot.name=type(filter.name) == "string" and filter.name or (filter.name and filter.name.name)
    slot.quality=quality_name(filter.quality)
    slot.comparator=filter.comparator
  end
  result.filterSlots[#result.filterSlots+1]=slot
end
local control = e.get_control_behavior()
if control then
  result.circuit = {enabled=not control.disabled, circuitEnableDisable=control.circuit_enable_disable, disabledByControlBehavior=control.disabled,
    setFilters=control.circuit_set_filters, readHandContents=control.circuit_read_hand_contents,
    handReadMode=control.circuit_hand_read_mode, setStackSize=control.circuit_set_stack_size,
    stackControlSignal=signal(control.circuit_stack_control_signal),
    connectToLogisticNetwork=control.connect_to_logistic_network,
    circuitCondition=condition(control.circuit_condition), logisticCondition=condition(control.logistic_condition)}
end
rcon.print(helpers.table_to_json(result))`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[InserterSettings](response)
}
