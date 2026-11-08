package main

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// runLuaFixture executes repo Lua against stub engine globals with the standalone
// interpreter. rcon.print captures the table instead of serializing it, so
// assertions can inspect the real payload the Lua would send.
func runLuaFixture(t *testing.T, source, stubs, assertions string) {
	t.Helper()
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	prelude := `
local captured
rcon = {print=function(value) captured = value end}
helpers = {table_to_json=function(value) return value end}
function fail(message) captured = {error=message} end
defines = {entity_status={working=1,normal=2,no_recipe=19,no_fuel=53,no_ingredients=18,
  low_power=14,not_plugged_in_electric_network=5,recharging_after_power_outage=48,no_minable_resources=21},
  inventory={crafter_input=1,crafter_output=2,furnace_source=3,furnace_result=4,lab_input=5},
  flow_precision_index={one_minute=0,ten_minutes=1,one_hour=2}}
game = {tick=500, forces={player={name="player"}}, surfaces={nauvis={name="nauvis"}}}
local function inventory(contents)
  return {get_contents=function() return contents end}
end
`
	// executeLua prefixes the console command; the fixture runs plain Lua.
	source = strings.TrimPrefix(source, "/silent-command ")
	script := prelude + stubs + source + assertions
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(script)
	if output, err := cmd.CombinedOutput(); err != nil {
		var numbered strings.Builder
		for index, line := range strings.Split(script, "\n") {
			numbered.WriteString(strconv.Itoa(index+1) + ": " + line + "\n")
		}
		t.Fatalf("lua fixture failed: %v\n%s\n%s", err, output, numbered.String())
	}
}

// The itemFlow Lua must classify fuel burners as FUEL consumers with an upper
// bound rate, and must fall back to a furnace's previous recipe.
func TestItemFlowLuaReportsFuelConsumersAndPreviousRecipe(t *testing.T) {
	r, commands := serveLuaOnce(t, `{}`)
	if _, err := itemFlowQuery(r, ItemFlowInput{Force: "player", Surface: "nauvis", Item: "coal", Limit: 100}); err != nil {
		t.Fatal(err)
	}
	source := <-commands
	stubs := `
prototypes = {item={coal={name="coal",fuel_value=4000000,fuel_category="chemical"}},fluid={}}
local mineable = {mining_time=1, products={{type="item",name="coal",amount=1,probability=1}}}
local drill_target = {name="coal",amount=9000,valid=true,prototype={mineable_properties=mineable,infinite_resource=false}}
-- previous_recipe.name is a recipe prototype in the engine, so the stub models
-- that shape instead of a bare name string.
local iron_plate = {name="iron-plate",energy=3.2,products={{type="item",name="iron-plate",amount=1}},
  ingredients={{type="item",name="coal",amount=1}}}
local function furnace()
  return {name="stone-furnace",type="furnace",unit_number=11,position={x=1,y=1},status=18,
    energy=0,electric_buffer_size=nil,electric_drain=nil,fluidbox={},
    crafting_speed=1, productivity_bonus=0, speed_bonus=0,
    get_recipe=function() return nil,nil end,
    previous_recipe={name=iron_plate,quality={name="normal"}},
    burner={remaining_burning_fuel=1000,fuel_categories={chemical=true}},consumption_bonus=0,
    prototype={burner_prototype={effectivity=1,fuel_categories={chemical=true}},
      get_max_energy_usage=function() return 30000 end},
    get_inventory=function() return inventory({{name="coal",quality="normal",count=4}}) end,
    get_output_inventory=function() return inventory({}) end,
    get_fuel_inventory=function() return inventory({{name="coal",quality="normal",count=4}}) end}
end
local drill = {name="burner-mining-drill",type="mining-drill",unit_number=7,position={x=2,y=2},status=1,
  energy=0,fluidbox={},mining_target=drill_target,mining_speed=0.25,speed_bonus=0,productivity_bonus=0,
  consumption_bonus=0,burner={fuel_categories={chemical=true}},
  prototype={mining_speed=0.25,burner_prototype={effectivity=1,fuel_categories={chemical=true}},
    get_max_energy_usage=function() return 2500 end},
  get_output_inventory=function() return inventory({}) end,
  get_fuel_inventory=function() return inventory({{name="coal",quality="normal",count=9}}) end}
local machines = {drill, furnace()}
local everything = {drill, furnace()}
game.surfaces["nauvis"].find_entities_filtered = function(filter)
  if filter.type then return machines end
  return everything
end
`
	assertions := `
assert(captured, "no payload captured")
assert(#captured.producers==1, "producers "..#captured.producers)
local produced = captured.producers[1]
assert(produced.role=="RECIPE", "producer role "..tostring(produced.role))
assert(produced.theoreticalRatePerSecond==0.25, "drill rate "..tostring(produced.theoreticalRatePerSecond))
assert(produced.miningTarget=="coal", "mining target")
local fuel_rows, previous_rows = 0, 0
for _, row in pairs(captured.consumers) do
  if row.role=="FUEL" then
    fuel_rows = fuel_rows + 1
    assert(row.rateBasis=="FUEL_UPPER_BOUND", "fuel basis "..tostring(row.rateBasis))
    local expected = row.name=="burner-mining-drill" and 0.0375 or 0.45
    assert(math.abs(row.theoreticalRatePerSecond-expected)<1e-9, "fuel rate "..tostring(row.theoreticalRatePerSecond))
    local buffered = row.name=="burner-mining-drill" and 9 or 4
    assert(#row.bufferedItems==1 and row.bufferedItems[1].count==buffered, "fuel inventory")
  elseif row.recipeSource=="PREVIOUS" then
    previous_rows = previous_rows + 1
    assert(row.recipe=="iron-plate", "previous recipe "..tostring(row.recipe))
    assert(row.recipeQuality=="normal", "previous recipe quality")
    assert(row.rateBasis=="IDLE_PREVIOUS_RECIPE_POTENTIAL", "previous basis "..tostring(row.rateBasis))
  end
end
assert(fuel_rows>=2, "fuel consumers "..fuel_rows)
assert(previous_rows==1, "previous recipe rows "..previous_rows)
`
	runLuaFixture(t, source, stubs, assertions)
}

// The machines page must bucket every status, attach diagnostics from the same
// snapshot and reject an unknown status filter.
func TestFactoryMachinesLuaBucketsAndAttachesDiagnostics(t *testing.T) {
	r, commands := serveLuaOnce(t, `{}`)
	if _, err := factoryMachines(r, "nauvis", "player", "", "", nil, 100, 0); err != nil {
		t.Fatal(err)
	}
	source := <-commands
	stubs := `
defines.entity_status = {working=1,normal=2,no_recipe=19,no_fuel=53,no_ingredients=18,
  low_power=14,not_plugged_in_electric_network=5,recharging_after_power_outage=48,no_minable_resources=21}
prototypes = {item={coal={name="coal",fuel_value=4000000,fuel_category="chemical"}},fluid={}}
local function drill(status)
  return {name="burner-mining-drill",type="mining-drill",unit_number=21,position={x=3,y=3},status=status,
    energy=0,fluidbox={},mining_progress=0.4,mining_target=nil,productivity_bonus=0,crafting_speed=1,
    burner={remaining_burning_fuel=1000},consumption_bonus=0,
    prototype={burner_prototype={effectivity=1,fuel_categories={chemical=true}},
      get_max_energy_usage=function() return 2500 end},
    get_output_inventory=function() return inventory({}) end,
    get_fuel_inventory=function() return inventory({}) end}
end
local machine = {name="stone-furnace",type="furnace",unit_number=31,position={x=1,y=1},status=53,
  energy=0,fluidbox={},crafting_progress=0.1,crafting_speed=1,productivity_bonus=0,
  get_recipe=function() return nil,nil end,
  previous_recipe={name={name="iron-plate",energy=3.2,
    products={{type="item",name="iron-plate",amount=1}},ingredients={{type="item",name="iron-ore",amount=1}}},quality={name="normal"}},
  get_inventory=function() return inventory({{name="iron-ore",quality="normal",count=2}}) end,
  get_output_inventory=function() return inventory({}) end,
  get_fuel_inventory=function() return inventory({}) end}
game.surfaces["nauvis"].find_entities_filtered = function() return {machine, drill(21)} end
`
	assertions := `
assert(captured, "no payload captured")
assert(captured.pageInfo.total==2, "total "..tostring(captured.pageInfo.total))
assert(#captured.machines==2, "machines")
local furnace = captured.machines[1]
assert(furnace.recipeName=="iron-plate", "previous recipe surfaced "..tostring(furnace.recipeName))
assert(furnace.diagnostics, "diagnostics attached")
assert(furnace.diagnostics.recipe_source=="PREVIOUS", "diagnostics source "..tostring(furnace.diagnostics.recipe_source))
assert(furnace.diagnostics.burner==nil, "electric-free furnace has no burner")
local depleted, no_fuel = 0, 0
for _, summary in pairs(captured.summaries) do
  if summary.type=="mining-drill" then depleted = summary.resourceDepleted end
  if summary.type=="furnace" then no_fuel = summary.noFuel end
end
assert(depleted==1, "resourceDepleted bucket "..tostring(depleted))
assert(no_fuel==1, "noFuel bucket "..tostring(no_fuel))
`
	runLuaFixture(t, source, stubs, assertions)

	r2, commands2 := serveLuaOnce(t, `{}`)
	if _, err := factoryMachines(r2, "nauvis", "player", "no_such_status", "", nil, 10, 0); err != nil {
		t.Fatal(err)
	}
	source2 := <-commands2
	badStubs := `
game.surfaces["nauvis"].find_entities_filtered = function() return {} end
`
	badAssertions := `
assert(captured and captured.error, "unknown status must fail")
assert(string.find(captured.error, "unknown entity status", 1, true), "error "..tostring(captured.error))
`
	runLuaFixture(t, source2, badStubs, badAssertions)
}

// Electric networks must sum generator capacity in watts, keep accumulator state
// and derive the load ratio instead of reporting a null loadRatio.
func TestElectricNetworksLuaReportsCapacityAndLoadRatio(t *testing.T) {
	r, commands := serveLuaOnce(t, `{}`)
	if _, err := electricNetworksQuery(r, &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	source := <-commands
	stubs := `
prototypes = {item={},fluid={}}
local network_pole = {name="steel-power-pole",type="electric-pole",electric_network_id=4,position={x=0,y=0},
  electric_network_statistics={input_counts={energy=true},output_counts={energy=true},
    get_flow_count=function(args)
      if args.category=="input" then return 100 end
      return 100
    end}}
local generator = {name="steam-engine",type="generator",electric_network_id=4,position={x=1,y=1},status=1,energy=0,
  prototype={get_max_energy_production=function() return 15000 end}}
local accumulator = {name="accumulator",type="accumulator",electric_network_id=4,position={x=2,y=2},status=1,energy=2500000,
  prototype={electric_energy_source_prototype={buffer_capacity=5000000}}}
local low = {name="assembling-machine-1",type="assembling-machine",electric_network_id=4,position={x=3,y=3},
  status=14,energy=0,prototype={}}
local everything = {network_pole, generator, accumulator, low}
game.surfaces["nauvis"].find_entities_filtered = function(filter)
  if filter.type=="electric-pole" then return {network_pole} end
  return everything
end
`
	assertions := `
assert(captured, "no payload captured")
local network = captured.networks[1]
assert(network.polesInArea==1, "poles "..tostring(network.polesInArea))
assert(network.maxProductionWatts==900000, "capacity "..tostring(network.maxProductionWatts))
assert(network.accumulatorChargeJoules==2500000, "charge")
assert(network.accumulatorCapacityJoules==5000000, "buffer")
assert(network.lowPowerConsumers==1, "low power consumers "..tostring(network.lowPowerConsumers))
assert(math.abs(network.loadRatio-6000/900000)<1e-9, "loadRatio "..tostring(network.loadRatio))
`
	runLuaFixture(t, source, stubs, assertions)
}
