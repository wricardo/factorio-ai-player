package main

import (
	"os/exec"
	"strings"
	"testing"
)

// machinePageSnapshot decodes a page; the fixture only captures the Lua, so the
// backend answer just has to be decodable.
const machinePageStubResponse = `{"tick":674522,"surface":"nauvis","force":"player",
  "pageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
  "machines":[{"name":"burner-mining-drill","type":"mining-drill","position":{"x":-96,"y":-4},
    "status":"working","fuel":[{"name":"coal","count":5}],"surface":"nauvis","force":"player",
    "unitNumber":"11","output":[],"machineFluids":[]}],
  "summaries":[{"type":"mining-drill","total":1,"working":1,"idle":0,"noFuel":0,"noPower":0,
    "noIngredients":0,"resourceDepleted":0,"outputFull":0,"other":0}]}`

// A burner mining drill keeps no output buffer, so Factorio returns its fuel
// inventory from get_output_inventory(). machines() must not report that coal
// fuel as an output stack; inspect() already reports it under fuel.
func TestMachinesBurnerDrillFuelIsNotOutput(t *testing.T) {
	r, commands := serveLuaOnce(t, machinePageStubResponse)
	if _, err := factoryMachines(r, "nauvis", "player", "", "mining-drill", nil, 100, 0); err != nil {
		t.Fatal(err)
	}
	source := <-commands
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	stubs := `
local captured
rcon = {print=function(value) captured = value end}
helpers = {table_to_json=function(value) return value end}
prototypes = {item={coal={name="coal",fuel_value=4000000}}}
defines = {entity_status={working=1,normal=2,no_fuel=53},inventory={}}
local function inv(index, contents)
  return {index=index, get_contents=function() return contents end}
end
-- A burner drill exposes the same inventory from both accessors.
local fuel = inv(2, {{name="coal",quality="normal",count=5}})
local drill = {name="burner-mining-drill", type="mining-drill", unit_number=11, status=1,
  position={x=-96,y=-4}, fluidbox={},
  get_fuel_inventory=function() return fuel end,
  get_output_inventory=function() return fuel end}
local surface = {name="nauvis", find_entities_filtered=function(filter) return {drill} end}
game = {tick=674522, players={}, forces={player={name="player"}}, surfaces={nauvis=surface}}
`
	assertions := `
assert(captured, "machines Lua printed nothing")
assert(not captured.error, "machines Lua failed: "..tostring(captured.error))
local machine = captured.machines[1]
assert(machine, "expected one machine row")
assert(#machine.fuel==1 and machine.fuel[1].name=="coal" and machine.fuel[1].count==5,
  "drill fuel must stay fuel, got "..#machine.fuel.." stacks")
assert(#machine.output==0, "drill fuel must not be reported as output, got "..#machine.output.." stacks")
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(stubs + strings.TrimPrefix(source, "/silent-command ") + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("machines burner drill fixture: %v\n%s\n%s", err, output, source)
	}
}
