package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// FactoryArea bounds the surface area included in a factory snapshot.
type FactoryArea struct {
	LeftTopX, LeftTopY, RightBottomX, RightBottomY float64
}

// machineBucketLua classifies every 2.0.77 machine status into one summary
// bucket. Statuses that describe an operator or circuit state stay in other
// rather than being silently counted as working.
const machineBucketLua = `
local function machine_bucket(name)
  if name=="working" then return "working" end
  if name=="normal" or name=="no_recipe" or name=="no_research_in_progress" or name=="recipe_not_researched" then return "idle" end
  if name=="no_fuel" then return "noFuel" end
  if name=="no_power" or name=="low_power" or name=="not_plugged_in_electric_network" or name=="recharging_after_power_outage" then return "noPower" end
  if name=="no_ingredients" or name=="item_ingredient_shortage" or name=="fluid_ingredient_shortage" or name=="missing_science_packs" or name=="missing_required_fluid" or name=="no_input_fluid" or name=="low_input_fluid" then return "noIngredients" end
  if name=="no_minable_resources" then return "resourceDepleted" end
  if name=="full_output" or name=="full_burnt_result_output" or name=="waiting_for_space_in_destination" or name=="not_enough_space_in_output" then return "outputFull" end
  return "other"
end
`

// machinePageSnapshot decodes the page Lua before the modelled shape is built:
// each row carries its diagnostics as a raw entity snapshot so Go can derive
// shortfall, throughput, mining and burner state once per row.
type machinePageSnapshot struct {
	Tick      float64               `json:"tick"`
	Surface   string                `json:"surface"`
	Force     string                `json:"force"`
	PageInfo  *PageInfo             `json:"pageInfo"`
	Machines  []*machineRowSnapshot `json:"machines"`
	Summaries []*MachineSummary     `json:"summaries"`
}

type machineRowSnapshot struct {
	Name               string                      `json:"name"`
	Type               string                      `json:"type"`
	Position           *Position                   `json:"position"`
	Status             string                      `json:"status"`
	Fuel               []*InventoryStack           `json:"fuel"`
	Surface            string                      `json:"surface"`
	Force              string                      `json:"force"`
	UnitNumber         *string                     `json:"unitNumber"`
	Energy             *float64                    `json:"energy"`
	ElectricBufferSize *float64                    `json:"electricBufferSize"`
	ElectricDrain      *float64                    `json:"electricDrain"`
	RecipeName         *string                     `json:"recipeName"`
	RecipeQuality      *string                     `json:"recipeQuality"`
	RecipeIngredients  []*RecipeIngredient         `json:"recipeIngredients"`
	CraftingProgress   *float64                    `json:"craftingProgress"`
	CraftingSpeed      *float64                    `json:"craftingSpeed"`
	Input              []*InventoryStack           `json:"input"`
	Output             []*InventoryStack           `json:"output"`
	MachineFluids      []*MachineInsightFluid      `json:"machineFluids"`
	MiningProgress     *float64                    `json:"miningProgress"`
	MiningTarget       *MachineInsightMiningTarget `json:"miningTarget"`
	Diagnostics        *machineDiagnosticSnapshot  `json:"diagnostics"`
}

func factoryMachines(r *Resolver, surface, force, status, entityType string, area *FactoryArea, limit, offset int) (string, error) {
	if surface == "" || force == "" {
		return "", errors.New("surface and force are required")
	}
	if limit < 1 || limit > maxPageSize {
		return "", pageLimitError("limit")
	}
	if offset < 0 {
		return "", errors.New("offset must be non-negative")
	}
	areaLua := "nil"
	if area != nil {
		coords := []float64{area.LeftTopX, area.LeftTopY, area.RightBottomX, area.RightBottomY}
		for _, value := range coords {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return "", errors.New("area coordinates must be finite")
			}
		}
		if area.LeftTopX > area.RightBottomX || area.LeftTopY > area.RightBottomY {
			return "", errors.New("area left top must not exceed right bottom")
		}
		areaLua = fmt.Sprintf("{{%s,%s},{%s,%s}}", strconv.FormatFloat(coords[0], 'g', -1, 64), strconv.FormatFloat(coords[1], 'g', -1, 64), strconv.FormatFloat(coords[2], 'g', -1, 64), strconv.FormatFloat(coords[3], 'g', -1, 64))
	}

	lua := luaFail + luaOutputInventoryHelper + machineDiagnosticsSnapshotLua + machineBucketLua + fmt.Sprintf(`
local surface_name, force_name = %s, %s
local requested_status, requested_type = %s, %s
local area, limit, offset = %s, %d, %d
local surface = game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force = game.forces[force_name]
if not force then fail("force not found"); return end
local types = {"assembling-machine", "furnace", "mining-drill", "lab", "rocket-silo"}
local filter = {type=types, force=force, area=area}
local status_names, status_known = {}, {}
for name, value in pairs(defines.entity_status) do
  status_names[value] = name
  status_known[name] = true
end
if requested_status ~= "" and not status_known[requested_status] then
  fail("unknown entity status: "..requested_status); return
end
local grouped, matches = {}, {}
for _, entity in pairs(surface.find_entities_filtered(filter)) do
  if requested_type == "" or entity.type == requested_type then
    local name = status_names[entity.status] or "unknown"
    local summary = grouped[entity.type]
    if not summary then
      summary = {type=entity.type, working=0, idle=0, noFuel=0, noPower=0, noIngredients=0, resourceDepleted=0, outputFull=0, other=0, total=0}
      grouped[entity.type] = summary
    end
    summary.total = summary.total + 1
    local bucket = machine_bucket(name)
    summary[bucket] = summary[bucket] + 1
    if requested_status == "" or name == requested_status then
      matches[#matches+1] = {entity=entity, status=name}
    end
  end
end
table.sort(matches, function(a,b)
  local x, y = a.entity, b.entity
  if x.type ~= y.type then return x.type < y.type end
  if x.name ~= y.name then return x.name < y.name end
  if x.position.x ~= y.position.x then return x.position.x < y.position.x end
  if x.position.y ~= y.position.y then return x.position.y < y.position.y end
  return (x.unit_number or 0) < (y.unit_number or 0)
end)
local function contents(inv)
  if not inv then return {} end
  local items=inv.get_contents()
  table.sort(items, function(a,b)
    if a.name ~= b.name then return a.name < b.name end
    return a.quality < b.quality
  end)
  return items
end
local machines = {}
for i=offset+1, math.min(#matches, offset+limit) do
  local match = matches[i]
  local entity = match.entity
  local machine = {name=entity.name, type=entity.type, position=entity.position, status=match.status, fuel=contents(entity.get_fuel_inventory()),
    surface=surface.name,force=force.name,unitNumber=entity.unit_number,
    energy=entity.energy, electricBufferSize=entity.electric_buffer_size, electricDrain=entity.electric_drain}
  local t = entity.type
  if t=="furnace" then
    machine.input=contents(entity.get_inventory(defines.inventory.furnace_source))
    machine.output=contents(entity.get_inventory(defines.inventory.furnace_result))
  elseif t=="assembling-machine" or t=="rocket-silo" then
    machine.input=contents(entity.get_inventory(defines.inventory.crafter_input))
    machine.output=contents(entity.get_inventory(defines.inventory.crafter_output))
  elseif t=="lab" then
    machine.input=contents(entity.get_inventory(defines.inventory.lab_input))
  elseif t=="mining-drill" then
    machine.output=contents(output_inventory(entity))
    machine.miningProgress=entity.mining_progress
    local target=entity.mining_target
    if target and target.valid then
      machine.miningTarget={name=target.name, position=target.position, amount=target.amount}
    end
  end
  if t=="furnace" or t=="assembling-machine" or t=="rocket-silo" then
    local recipe, quality=entity.get_recipe()
    if not recipe and t=="furnace" then
      local previous=entity.previous_recipe
      if previous then recipe, quality=previous.name, previous.quality end
    end
    machine.craftingProgress=entity.crafting_progress
    machine.craftingSpeed=entity.crafting_speed
    if recipe then
      machine.recipeName=recipe.name
      if quality then machine.recipeQuality=quality.name end
      machine.recipeIngredients=recipe.ingredients
    end
  end
  local fluids={}
  for index=1,#entity.fluidbox do
    local fluid=entity.fluidbox[index]
    if fluid then fluids[#fluids+1]={index=index, name=fluid.name, amount=fluid.amount, temperature=fluid.temperature} end
  end
  machine.machineFluids=fluids
  machine.diagnostics=machine_diagnostics_snapshot(entity)
  machines[#machines+1] = machine
end
local summaries = {}
for _, summary in pairs(grouped) do summaries[#summaries+1] = summary end
table.sort(summaries, function(a,b) return a.type < b.type end)
rcon.print(helpers.table_to_json({tick=game.tick, surface=surface.name, force=force.name, pageInfo={total=#matches,limit=limit,offset=offset,hasMore=#matches>offset+limit}, machines=machines, summaries=summaries}))`, luaString(surface), luaString(force), luaString(status), luaString(entityType), areaLua, limit, offset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return "", err
	}
	return machinePageJSON(response)
}

// machinePageJSON turns the raw page into the modelled response. Diagnostics are
// derived here from the snapshot each row already carries, which is why a page
// selection costs one game call instead of one per row.
func machinePageJSON(response string) (string, error) {
	snapshot, err := factoryDecode[machinePageSnapshot](response)
	if err != nil {
		return "", err
	}
	page := &MachinePage{Tick: snapshot.Tick, Surface: snapshot.Surface, Force: snapshot.Force,
		PageInfo: snapshot.PageInfo, Machines: make([]*FactoryMachine, 0, len(snapshot.Machines)),
		Summaries: make([]*MachineSummary, 0, len(snapshot.Summaries))}
	for _, row := range snapshot.Machines {
		if row == nil {
			continue
		}
		machine := &FactoryMachine{Name: row.Name, Type: row.Type, Position: row.Position, Status: row.Status,
			Fuel: row.Fuel, Surface: row.Surface, Force: row.Force, UnitNumber: row.UnitNumber,
			Energy: row.Energy, ElectricBufferSize: row.ElectricBufferSize, ElectricDrain: row.ElectricDrain,
			RecipeName: row.RecipeName, RecipeQuality: row.RecipeQuality, RecipeIngredients: row.RecipeIngredients,
			CraftingProgress: row.CraftingProgress, CraftingSpeed: row.CraftingSpeed,
			Input: row.Input, Output: row.Output, MachineFluids: row.MachineFluids,
			MiningProgress: row.MiningProgress, MiningTarget: row.MiningTarget}
		if row.Diagnostics != nil {
			machine.Diagnostics = calculateMachineDiagnostics(row.Diagnostics)
		}
		page.Machines = append(page.Machines, machine)
	}
	for _, summary := range snapshot.Summaries {
		if summary != nil {
			page.Summaries = append(page.Summaries, summary)
		}
	}
	data, err := json.Marshal(page)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
