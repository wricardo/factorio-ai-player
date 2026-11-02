package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// luaTestEntityStub returns Lua that installs a minimal Factorio runtime: a
// surface, game.tick, helpers.table_to_json, rcon.print, and entity tables whose
// unset members raise inside pcall exactly like the engine's absent fields.
// Stubs are intentionally narrow: an unset field errors, so a builder that
// accesses something it should guard shows up as a test failure.
const luaTestEntityStub = `local tick=1000
local function stub_force(name)
  return {name=name, get_item_production_statistics=function() return nil end}
end
local unit_scans=0
local function stub_surface()
  local s={name="nauvis",entities={}}
  s.find_entities_filtered=function(filter)
    if filter.invert==true then
      unit_scans=unit_scans+1
      assert(type(filter.type)=="table", "unit-number scan must filter by excluded type list")
    end
    local out={}
    for _,e in ipairs(s.entities) do
      local match=true
      if filter.invert==true then
        for _,kind in ipairs(filter.type) do if e.type==kind then match=false end end
      else
        if filter.name~=nil and e.name~=filter.name then match=false end
        if filter.force~=nil and e.force.name~=filter.force then match=false end
        if filter.type~=nil and e.type~=filter.type then match=false end
      end
      if match then out[#out+1]=e end
    end
    return out
  end
  return s
end
local surface=stub_surface()
local function stub_entity(name,kind,unit,pos,force_name,extra)
  local e={name=name,type=kind,unit_number=unit,position={x=pos[1],y=pos[2]},surface=surface,force=stub_force(force_name)}
  e.get_fluid_contents=function() return {} end
  e.get_fuel_inventory=function() return nil end
  e.get_inventory=function() return {get_contents=function() return {} end} end
  if extra then for k,v in pairs(extra) do e[k]=v end end
  surface.entities[#surface.entities+1]=e
  return e
end
game={tick=tick,surfaces={nauvis=surface},players={},connected_players={},get_entity_by_unit_number=function(unit)
  for _,e in ipairs(surface.entities) do if e.unit_number==unit and e.get_by_unit_number then return e end end
  return nil
end}
game.get_player=function(name) return game.players[name] end
defines={entity_status={},inventory={},controllers={}}
prototypes={entity={}}
helpers={table_to_json=function(value) return value end}
local printed
rcon={print=function(value) printed=value end}
`

// TestInspectManyLuaResolvesAndReportsPerTargetRows runs the shipped batch chunk
// and proves one Lua call returns a row per target: unit-number-only targets
// resolve through the bounded scan, neutral and enemy entities resolve without
// the actor force, and an unresolvable target fails only its own row.
func TestInspectManyLuaResolvesAndReportsPerTargetRows(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	player := "wricardo"
	unitBelt, inserterName, furnaceName, resourceName, missingUnit, badTarget := "803", "inserter", "stone-furnace", "iron-ore", "4242", (*EntityRefInput)(nil)
	extraBeltA, extraBeltB := "804", "805"
	beltSurface := "nauvis"
	targets := []*EntityRefInput{
		{UnitNumber: &unitBelt, Surface: &beltSurface},
		{Entity: &inserterName, Position: &PositionInput{X: 1.5, Y: 2.5}},
		{Entity: &furnaceName, Position: &PositionInput{X: 4.5, Y: 2.5}},
		{Entity: &resourceName, Position: &PositionInput{X: 9.5, Y: 9.5}},
		{UnitNumber: &extraBeltA},
		{UnitNumber: &extraBeltB},
		{UnitNumber: &missingUnit},
		badTarget,
	}
	page := "nauvis"
	input := InspectManyInput{Actor: &ActorInput{Player: &player}, Surface: &page, Targets: targets}
	parsedTargets, err := inspectManyTargets(input)
	if err != nil {
		t.Fatal(err)
	}
	if parsedTargets[7].Error == "" {
		t.Fatal("a null target must be rejected for its own row")
	}
	if parsedTargets[5].Error != "" {
		t.Fatalf("unit-number-only target must reach the batch, got %q", parsedTargets[5].Error)
	}
	actorLua, err := inspectManyActorLua(input.Actor, "nauvis")
	if err != nil {
		t.Fatal(err)
	}
	body := inspectManyLua(parsedTargets, false, false)
	setup := luaTestEntityStub + `
game.players.wricardo={name="wricardo",force=stub_force("player")}
local belt=stub_entity("transport-belt","transport-belt",803,{0.5,-20.5},"player")
belt.get_transport_line=function() return {get_contents=function() return {} end} end
stub_entity("inserter","inserter",1334,{1.5,2.5},"player",{
  pickup_position={x=1.5,y=1.5},drop_position={x=1.5,y=3.5},
  held_stack={valid_for_read=true,name="coal",count=1,quality={name="uncommon"}}})
stub_entity("stone-furnace","furnace",900,{4.5,2.5},"enemy",{
  get_recipe=function() return nil end, crafting_progress=0.25})
stub_entity("iron-ore","resource",nil,{9.5,9.5},"neutral",{amount=1500})
stub_entity("fast-transport-belt","transport-belt",804,{8.5,-20.5},"player",{get_transport_line=function() return {get_contents=function() return {} end} end})
stub_entity("express-transport-belt","transport-belt",805,{9.5,-20.5},"player",{get_transport_line=function() return {get_contents=function() return {} end} end})
assert(belt.unit_number==803)
`
	assertions := `
local page=printed
assert(page.force=="player", "page force")
assert(page.surface=="nauvis", "page surface")
assert(#page.results==8, "one row per target, got "..#page.results)
local belt_row=page.results[1]
assert(belt_row.index==0, "index 0")
assert(belt_row.name=="transport-belt", "unit-number-only target resolved: "..tostring(belt_row.name))
assert(belt_row.unit_number==803, "unit number reported")
local inserter_row=page.results[2]
assert(inserter_row.inspection~=nil, "inserter inspection: "..tostring(inserter_row.error))
assert(inserter_row.inspection.held_stack~=nil, "held stack")
assert(inserter_row.inspection.held_stack.quality=="uncommon", "held stack quality: "..tostring(inserter_row.inspection.held_stack.quality))
assert(inserter_row.inspection.pickup_position.x==1.5, "pickup position")
local furnace_row=page.results[3]
assert(furnace_row.inspection~=nil, "enemy entity must inspect without the actor force: "..tostring(furnace_row.error))
assert(furnace_row.inspection.force=="enemy", "enemy force reported")
assert(furnace_row.inspection.crafting_progress==0.25, "crafting progress")
local resource_row=page.results[4]
assert(resource_row.inspection~=nil, "neutral resource must inspect: "..tostring(resource_row.error))
assert(resource_row.inspection.force=="neutral", "neutral force reported")
assert(page.results[5].name=="fast-transport-belt", "unit-number-only target 804: "..tostring(page.results[5].error))
assert(page.results[6].name=="express-transport-belt", "unit-number-only target 805: "..tostring(page.results[6].error))
local missing_row=page.results[7]
assert(missing_row.error~=nil, "unresolvable unit number must fail its own row")
assert(missing_row.inspection==nil, "failed row carries no inspection")
local bad_row=page.results[8]
assert(bad_row.error~=nil, "invalid target must fail its own row")
assert(bad_row.index==7, "invalid row keeps its index")
assert(unit_scans==1, "one unit-number scan per surface, got "..unit_scans)
printed="ok"
`
	harness := setup + actorLua + body + assertions + `assert(printed=="ok")`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestInspectLuaResolvesNeutralEntities proves the single inspect path resolves
// entities the actor's force does not own, which the previous force-filtered
// lookup rejected.
func TestInspectLuaResolvesNeutralEntities(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	lua, err := productionLua("inspect", []string{"--player=wricardo", "--entity=iron-ore", "--x=9.5", "--y=9.5"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	harness := luaTestEntityStub + `
game.players.wricardo={name="wricardo",force=stub_force("player")}
local ore=stub_entity("iron-ore","resource",nil,{9.5,9.5},"neutral",{amount=1500})
` + lua + `
assert(printed and printed.name=="iron-ore", "inspect must resolve the neutral resource: "..tostring(printed and printed.error))
assert(printed.force=="neutral", "neutral force reported")
assert(printed.tick==1000, "snapshot tick")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestInspectLuaReportsHeldStackQuality keeps the copy-paste contract: a held
// stack must carry the quality prototype name because InventoryStack.quality is
// non-null and transfer rejects an empty quality.
func TestInspectLuaReportsHeldStackQuality(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	lua, err := productionLua("inspect", []string{"--player=wricardo", "--entity=inserter", "--x=1.5", "--y=2.5"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	harness := luaTestEntityStub + `
game.players.wricardo={name="wricardo",force=stub_force("player")}
stub_entity("inserter","inserter",1334,{1.5,2.5},"player",{
  pickup_position={x=1.5,y=1.5},drop_position={x=1.5,y=3.5},
  held_stack={valid_for_read=true,name="coal",count=1,quality={name="rare"}}})
` + lua + `
assert(printed.held_stack, "held stack present")
assert(printed.held_stack.quality=="rare", "held stack quality: "..tostring(printed.held_stack.quality))
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestNearbyLuaRejectsPrototypeNameAsType covers the filter mistake the engine
// silently ignores: a prototype name in the type filter must be rejected with a
// message that names the fix instead of returning zero rows.
func TestNearbyLuaRejectsPrototypeNameAsType(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	cases := []struct {
		kind string
		want string
	}{
		{"stone-furnace", "pass it as name"},
		{"bogus-type", "unknown entity type: bogus-type"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
game={tick=1,surfaces={nauvis={name="nauvis",find_entities_filtered=function() return {} end}}}
prototypes={entity={["stone-furnace"]={type="furnace"},["transport-belt"]={type="transport-belt"}}}
` + nearbyLua("nauvis", 0, 0, 4, "", tc.kind, "", "distance", 0, 10) + `
assert(printed and printed.error, "expected a rejected type filter")
assert(string.find(printed.error, ` + luaString(tc.want) + `, 1, true), "unexpected message: "..tostring(printed.error))
`
			out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// TestNearbyLuaAcceptsKnownEngineType keeps the validation from rejecting a real
// engine type, and proves the filter reaches the engine together with the force.
func TestNearbyLuaAcceptsKnownEngineType(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
game={tick=1,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  assert(filter.type=="furnace", "engine type filter")
  assert(filter.force=="enemy", "force filter")
  return {{name="stone-furnace",type="furnace",position={x=1,y=0},force={name="enemy"},unit_number=7}}
end}}}
prototypes={entity={["stone-furnace"]={type="furnace"}}}
` + nearbyLua("nauvis", 0, 0, 4, "", "furnace", "enemy", "distance", 0, 10) + `
assert(#printed.entities==1, "one row")
assert(printed.entities[1].force=="enemy", "row force")
assert(printed.entities[1].tick==1, "row tick")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestEntityQueryScansUnitNumbersWithoutWholeSurfaceWalk proves the entity query
// uses the excluded-type scan instead of an unfiltered table of every entity.
func TestEntityQueryScansUnitNumbersWithoutWholeSurfaceWalk(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	lua, err := entityLua([]string{"--unitNumber=803"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
local calls=0
game={tick=5,get_entity_by_unit_number=function() return nil end,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  calls=calls+1
  assert(filter.invert==true, "unit-number lookup must invert the neutral-type filter")
  local excluded={}
  for _,kind in ipairs(filter.type) do excluded[kind]=true end
  for _,kind in ipairs({"tree","simple-entity","resource","item-entity","fish","cliff"}) do
    assert(excluded[kind], "unit-number scan must exclude "..kind)
  end
  return {{name="transport-belt",type="transport-belt",unit_number=803,position={x=0.5,y=1.5},surface={name="nauvis"},force={name="player"}}}
end}}}
` + lua + `
assert(printed and printed.unit_number==803, "unit number resolved")
assert(calls==1, "one filtered scan")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestStatusLuaReportsCharacterSpeedAndPause proves the status snapshot carries
// the pause state, speed and each connected player's character unit number.
func TestStatusLuaReportsCharacterSpeedAndPause(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
game={tick=500,speed=2.5,tick_paused=true,connected_players={
  {name="wricardo",surface={name="nauvis"},position={x=1,y=2},character={unit_number=7}},
  {name="god",surface={name="nauvis"},position={x=3,y=4}},
}}
` + luaStatus + `
assert(printed.tick==500, "tick")
assert(printed.speed==2.5, "speed")
assert(printed.paused==true, "paused")
assert(printed.players[1].character==7, "character unit number")
assert(printed.players[2].character==nil, "god controller has no character")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestPlayerInspectCharacterReportsController proves a character-addressed
// player() reports the controlling player and its connection state, so a client
// can tell a detached worker from a human's body.
func TestPlayerInspectCharacterReportsController(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	lua, err := playerInspectLua("player", []string{"--character=7"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		player  string
		subject string
	}{
		{"controlled", `{name="wricardo",connected=true,controller_type=1}`, `assert(printed.player=="wricardo")
assert(printed.connected==true)
assert(printed.controller_type==1)`},
		{"detached", `nil`, `assert(printed.player==nil)
assert(printed.connected==nil)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
local character={name="character",type="character",unit_number=7,valid=true,health=100,
  position={x=1,y=2},surface={name="nauvis"},force={name="player"},player=` + tc.player + `}
game={tick=9,get_entity_by_unit_number=function(unit) if unit==7 then return character end return nil end}
` + lua + `
assert(printed.character==7, "character unit number")
` + tc.subject + `
`
			out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// TestCharacterMissingIsTerminalFailure proves a job that ended because its
// character vanished reports a terminal FAILED instead of an unknown OTHER.
func TestCharacterMissingIsTerminalFailure(t *testing.T) {
	status := engineStatus("CHARACTER_MISSING")
	if status.Category != StatusCategoryFailed {
		t.Fatalf("category = %v, want FAILED", status.Category)
	}
	if !status.Terminal {
		t.Fatal("CHARACTER_MISSING must be terminal so pollers stop waiting")
	}
}

// TestGraphQLErrorSummaryIncludesPaths proves a partial failure reports which
// field or list row failed instead of an unattributed message.
func TestGraphQLErrorSummaryIncludesPaths(t *testing.T) {
	errs := []interface{}{
		map[string]interface{}{
			"message": `server: "entity not found at center"`,
			"path":    []interface{}{"nearby", "entities", float64(2), "inspection"},
		},
		map[string]interface{}{
			"message": "boom",
		},
	}
	got := graphqlErrorSummary(errs)
	want := `nearby.entities.2.inspection: server: "entity not found at center"; boom`
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

// TestMachineDiagnosticsSnapshotLuaContract guards the shared builder contract
// inspectMany depends on: the batch embeds machine_diagnostics_snapshot(e) as a
// function, so it must return its snapshot table and never print or fail (a
// stray print would break the single-JSON-response contract of a batch call).
func TestMachineDiagnosticsSnapshotLuaContract(t *testing.T) {
	if !strings.Contains(machineDiagnosticsSnapshotLua, "local function machine_diagnostics_snapshot(e)") {
		t.Fatal("diagnostics snapshot builder must define machine_diagnostics_snapshot(e)")
	}
	if !strings.Contains(machineDiagnosticsSnapshotLua, "return snapshot") {
		t.Fatal("diagnostics snapshot builder must return its snapshot")
	}
	if strings.Contains(machineDiagnosticsSnapshotLua, "rcon.print") {
		t.Fatal("the batch-embedded diagnostics builder must not print")
	}
	if strings.Contains(machineDiagnosticsSnapshotLua, "fail(") {
		t.Fatal("the batch-embedded diagnostics builder must not call fail")
	}
}

// TestInspectManyTargetsValidatesEachRow covers the Go-side validation that runs
// before any game call: limits, per-target addressing, and per-surface checks.
func TestInspectManyTargetsValidatesEachRow(t *testing.T) {
	player := "wricardo"
	page := "nauvis"
	name := "transport-belt"
	unit := "803"
	blank := ""
	both := "804"
	empty := []*EntityRefInput{}
	if _, err := inspectManyTargets(InspectManyInput{Actor: &ActorInput{Player: &player}, Targets: empty}); err == nil {
		t.Fatal("empty targets must be rejected")
	}
	tooMany := make([]*EntityRefInput, inspectManyLimit+1)
	for i := range tooMany {
		tooMany[i] = &EntityRefInput{UnitNumber: &unit}
	}
	if _, err := inspectManyTargets(InspectManyInput{Actor: &ActorInput{Player: &player}, Surface: &page, Targets: tooMany}); err == nil {
		t.Fatal("more targets than the limit must be rejected")
	}
	targets, err := inspectManyTargets(InspectManyInput{Actor: &ActorInput{Player: &player}, Surface: &page, Targets: []*EntityRefInput{
		{UnitNumber: &unit},
		{Entity: &name, Position: &PositionInput{X: 1.5, Y: 2.5}},
		{UnitNumber: &both, Position: &PositionInput{X: 1.5, Y: 2.5}},
		{Surface: &blank, UnitNumber: &unit},
		nil,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if targets[0].Unit != 803 || targets[0].Surface != "nauvis" {
		t.Fatalf("unit-number target = %+v", targets[0])
	}
	if targets[1].Error != "" || targets[1].Name != name {
		t.Fatalf("name target = %+v", targets[1])
	}
	if targets[2].Error != "" || targets[2].Unit != 804 {
		t.Fatalf("coordinates alongside a unitNumber are a hint, got %+v", targets[2])
	}
	if targets[3].Error == "" {
		t.Fatal("an empty target surface must fail its own row")
	}
	if targets[4].Error == "" {
		t.Fatal("a null target must fail its own row")
	}
	if _, err := inspectManyActorLua(&ActorInput{}, "nauvis"); err == nil {
		t.Fatal("an actor without player or character must be rejected")
	}
	if _, err := inspectManyActorLua(&ActorInput{Player: &player, Character: &unit}, "nauvis"); err == nil {
		t.Fatal("both player and character must be rejected")
	}
}

// TestInspectManyActorLuaBindsSurface proves the page surface reaches the chunk
// instead of being assumed.
func TestInspectManyActorLuaBindsSurface(t *testing.T) {
	character := "7"
	lua, err := inspectManyActorLua(&ActorInput{Character: &character}, "vulcanus")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lua, "game.get_entity_by_unit_number(7)") {
		t.Fatalf("character binding missing:\n%s", lua)
	}
	if !strings.Contains(lua, "local result_surface="+luaString("vulcanus")) {
		t.Fatalf("surface binding missing:\n%s", lua)
	}
}

// TestInspectManyDecodesRows drives the full decode path from a canned backend
// response, so schema normalization, list handling and the diagnostics
// conversion are exercised without a live server.
func TestInspectManyDecodesRows(t *testing.T) {
	response := `{"tick":1000,"surface":"nauvis","force":"player","results":[
  {"index":0,"tick":1000,"name":"transport-belt","type":"transport-belt","position":{"x":0.5,"y":-20.5},"unit_number":803,
   "inspection":{"tick":1000,"name":"transport-belt","type":"transport-belt","surface":"nauvis","force":"player","inventories":{},"fluids":[],"toBeDeconstructed":false}},
  {"index":1,"error":"entity not found by unit number on that surface"},
  {"index":2,"tick":1000,"name":"assembling-machine-1","type":"assembling-machine","position":{"x":4.5,"y":2.5},"unit_number":900,
   "inspection":{"tick":1000,"name":"assembling-machine-1","type":"assembling-machine","surface":"nauvis","force":"player","inventories":{},"fluids":[],"toBeDeconstructed":false},
   "diagnostics":{"tick":1000,"recipe":"iron-gear-wheel","recipeDuration":0.5,"craftingSpeed":0.5,"ingredients":[],"products":[],"input":[],"fluids":[],"raw_status_name":"working","raw_status_code":4}}
]}`
	decoded, err := factoryDecode[batchInspectionResponse](response)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Results) != 3 {
		t.Fatalf("rows = %d, want 3", len(decoded.Results))
	}
	if decoded.Results[0].Inspection == nil || decoded.Results[0].UnitNumber == nil || *decoded.Results[0].UnitNumber != "803" {
		t.Fatalf("row 0 = %+v", decoded.Results[0])
	}
	if decoded.Results[1].Error == nil || decoded.Results[1].Inspection != nil {
		t.Fatalf("row 1 = %+v", decoded.Results[1])
	}
	if decoded.Results[2].Diagnostics == nil {
		t.Fatal("row 2 diagnostics missing")
	}
	diagnostics := calculateMachineDiagnostics(decoded.Results[2].Diagnostics)
	if diagnostics.Recipe == nil || *diagnostics.Recipe != "iron-gear-wheel" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(decoded.Results[1].Error); err != nil {
		t.Fatal(err)
	}
}

// cannedRCON serves one scripted response per received game command and records
// what was asked, so query builders can be driven end to end without a live
// server.
type cannedRCON struct {
	resolver *Resolver
	mu       sync.Mutex
	commands []string
}

func newCannedRCON(t *testing.T, responses ...string) *cannedRCON {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	canned := &cannedRCON{resolver: &Resolver{addr: listener.Addr().String(), password: "password"}}
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func(conn net.Conn) {
				defer workers.Done()
				defer conn.Close()
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				if err := writePacket(conn, 1, 2, ""); err != nil {
					return
				}
				_, _, command, err := readPacket(conn)
				if err != nil {
					return
				}
				canned.mu.Lock()
				canned.commands = append(canned.commands, command)
				index := len(canned.commands) - 1
				canned.mu.Unlock()
				response := ""
				if index < len(responses) {
					response = responses[index]
				}
				if err := writePacket(conn, 2, 0, response+"\n"); err != nil {
					return
				}
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				if err := writePacket(conn, 3, 0, "factorio-ai-player-response-end\n"); err != nil {
					return
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done; workers.Wait() })
	return canned
}

func (c *cannedRCON) received() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.commands...)
}

// TestBeltsQueryEmitsPrototypeName keeps the belt tier addressable: BeltInfo.name
// must come back so a client can address the belt again by name and position.
func TestBeltsQueryEmitsPrototypeName(t *testing.T) {
	canned := newCannedRCON(t, `{"tick":7,"surface":"nauvis","page_info":{"total":1,"limit":2,"offset":0,"has_more":false},"belts":[{"name":"express-transport-belt","position":{"x":1.5,"y":2.5},"direction":4,"unit_number":42,"line1":{"items":[]},"line2":{"items":[]}}]}`)
	page, err := beltsQuery(canned.resolver, BeltsInput{Surface: "nauvis", Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 4, RightBottomY: 4}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Belts) != 1 || page.Belts[0].Name != "express-transport-belt" {
		t.Fatalf("belts = %+v", page.Belts)
	}
	commands := canned.received()
	if len(commands) != 1 || !strings.Contains(commands[0], "name=e.name") {
		t.Fatalf("belts query must select the prototype name: %v", commands)
	}
}

// TestEntitiesQueryReportsForceAndTick proves an area scan can tell own, neutral
// and enemy entities apart without a second lookup per row.
func TestEntitiesQueryReportsForceAndTick(t *testing.T) {
	canned := newCannedRCON(t, `{"tick":7,"surface":"nauvis","page_info":{"total":2,"limit":5,"offset":0,"has_more":false},"entities":[{"name":"iron-ore","type":"resource","position":{"x":1,"y":2},"force":"neutral","tick":7,"amount":100},{"name":"stone-wall","type":"wall","position":{"x":3,"y":4},"force":"enemy","tick":7}]}`)
	page, err := entitiesQuery(canned.resolver, EntitiesInput{Surface: "nauvis", Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 8, RightBottomY: 8}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entities) != 2 {
		t.Fatalf("entities = %+v", page.Entities)
	}
	if page.Entities[0].Force == nil || *page.Entities[0].Force != "neutral" {
		t.Fatalf("neutral row force = %v", page.Entities[0].Force)
	}
	if page.Entities[1].Force == nil || *page.Entities[1].Force != "enemy" {
		t.Fatalf("enemy row force = %v", page.Entities[1].Force)
	}
	for _, entity := range page.Entities {
		if entity.Tick == nil || *entity.Tick != 7 {
			t.Fatalf("row tick = %v", entity.Tick)
		}
	}
	commands := canned.received()
	if len(commands) != 1 || !strings.Contains(commands[0], "force=e.force and e.force.name") {
		t.Fatalf("entities query must select the row force: %v", commands)
	}
}

// TestInspectManyQueryIsOneRoundTrip proves the batch resolver opens a single
// backend connection for the whole target list, which is the point of the API.
func TestInspectManyQueryIsOneRoundTrip(t *testing.T) {
	canned := newCannedRCON(t, `{"tick":9,"surface":"nauvis","force":"player","results":[
      {"index":0,"tick":9,"name":"transport-belt","type":"transport-belt","position":{"x":1.5,"y":2.5},"unit_number":803,"inspection":{"tick":9,"name":"transport-belt","type":"transport-belt","surface":"nauvis","force":"player","inventories":{},"fluids":[],"toBeDeconstructed":false}},
      {"index":1,"error":"entity not found by unit number on that surface"}]}`)
	player := "wricardo"
	surface := "nauvis"
	unitA, unitB := "803", "4242"
	page, err := inspectManyQuery(canned.resolver, InspectManyInput{
		Actor:   &ActorInput{Player: &player},
		Surface: &surface,
		Targets: []*EntityRefInput{{UnitNumber: &unitA}, {UnitNumber: &unitB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(canned.received()); got != 1 {
		t.Fatalf("connections = %d, want 1 for the whole batch", got)
	}
	if len(page.Results) != 2 {
		t.Fatalf("results = %+v", page.Results)
	}
	if page.Results[0].Inspection == nil || page.Results[0].UnitNumber == nil || *page.Results[0].UnitNumber != "803" {
		t.Fatalf("row 0 = %+v", page.Results[0])
	}
	if page.Results[1].Error == nil {
		t.Fatalf("row 1 = %+v", page.Results[1])
	}
	if page.Surface != "nauvis" || page.Force != "player" || page.Tick != 9 {
		t.Fatalf("page = %+v", page)
	}
}

// TestConcurrentResolversRespectRCONConnectionCap proves the process-wide RCON
// semaphore: a fan-out of forced resolvers queues instead of opening one socket
// per row, which is what made Factorio reset connections on a 300-row query.
func TestConcurrentResolversRespectRCONConnectionCap(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	active, peak := 0, 0
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func(conn net.Conn) {
				defer workers.Done()
				defer conn.Close()
				mu.Lock()
				active++
				if active > peak {
					peak = active
				}
				mu.Unlock()
				defer func() {
					mu.Lock()
					active--
					mu.Unlock()
				}()
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				if err := writePacket(conn, 1, 2, ""); err != nil {
					return
				}
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
				if err := writePacket(conn, 2, 0, `{"tick":1}`+"\n"); err != nil {
					return
				}
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				if err := writePacket(conn, 3, 0, "factorio-ai-player-response-end\n"); err != nil {
					return
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done; workers.Wait() })

	addr := listener.Addr().String()
	const calls = 18
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := executeLua(addr, "password", `rcon.print("{}")`); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent call failed: %v", err)
	}
	mu.Lock()
	observedPeak := peak
	mu.Unlock()
	if observedPeak > rconConcurrency {
		t.Fatalf("peak concurrent RCON connections = %d, want at most %d", observedPeak, rconConcurrency)
	}
	if observedPeak < 2 {
		t.Fatalf("peak concurrent RCON connections = %d, the fan-out should still run in parallel", observedPeak)
	}
}

// TestGraphQLPartialDataIsPrintedWhenErrorsExist covers the CLI decision: a
// response that carries both data and errors keeps the resolved fields instead
// of throwing the page away for one failed row.
func TestGraphQLPartialDataIsPrintedWhenErrorsExist(t *testing.T) {
	partial, ok := graphqlPartialData(map[string]interface{}{"data": map[string]interface{}{"status": map[string]interface{}{"tick": 5.0}}})
	if !ok {
		t.Fatal("data with errors must be printed")
	}
	if _, ok := partial["data"]; !ok {
		t.Fatalf("partial document = %v", partial)
	}
	if _, ok := partial["errors"]; ok {
		t.Fatalf("the printed document must not repeat the errors: %v", partial)
	}
	if _, ok := graphqlPartialData(map[string]interface{}{"data": nil}); ok {
		t.Fatal("null data must not be printed")
	}
	if _, ok := graphqlPartialData(map[string]interface{}{"errors": []interface{}{}}); ok {
		t.Fatal("a response without data must not be printed")
	}
}

// TestInspectManyFullChunkCompiles proves the batch chunk parses with every
// optional section enabled, so a settings or diagnostics shape error cannot slip
// past the unit tests that only inspect the emitted text.
func TestInspectManyFullChunkCompiles(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	player, name, unit := "wricardo", "assembling-machine-1", "1234"
	targets, err := inspectManyTargets(InspectManyInput{
		Actor:   &ActorInput{Player: &player},
		Targets: []*EntityRefInput{{UnitNumber: &unit}, {Entity: &name, Position: &PositionInput{X: 1.5, Y: 2.5}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	actorLua, err := inspectManyActorLua(&ActorInput{Player: &player}, "nauvis")
	if err != nil {
		t.Fatal(err)
	}
	chunk := actorLua + inspectManyLua(targets, true, true)
	cmd := exec.Command(luaBin, "-e", `local chunk=assert(load(io.read("a"))); assert(chunk)`)
	cmd.Stdin = strings.NewReader(chunk)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestEntitiesLuaAcceptsTypeThatIsAlsoAPrototypeName covers the overlap that
// makes type validation subtle: transport-belt, inserter, container and furnace
// are engine types AND prototype names, so the engine-type set must win. Only a
// prototype name that is not an engine type gets the name hint.
func TestEntitiesLuaAcceptsTypeThatIsAlsoAPrototypeName(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	for _, tc := range []struct {
		kind         string
		want         string
		wantRejected bool
	}{
		{kind: "transport-belt", want: "transport-belt"},
		{kind: "inserter", want: "inserter"},
		{kind: "container", want: "steel-chest"},
		{kind: "stone-furnace", want: "pass it as name", wantRejected: true},
		{kind: "assembling-machine-3", want: "pass it as name", wantRejected: true},
		{kind: "bogus-type", want: "unknown entity type: bogus-type", wantRejected: true},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			kind := tc.kind
			lua := entitiesLua(EntitiesInput{
				Surface: "nauvis",
				Area:    &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 8, RightBottomY: 8},
				Type:    &kind,
				Limit:   10,
			}, &areaBounds{LeftTopX: 0, LeftTopY: 0, RightBottomX: 8, RightBottomY: 8})
			harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
prototypes={entity={
  ["transport-belt"]={type="transport-belt"},
  ["inserter"]={type="inserter"},
  ["steel-chest"]={type="container"},
  ["stone-furnace"]={type="furnace"},
  ["assembling-machine-3"]={type="assembling-machine"},
}}
local catalog={
  {name="transport-belt",type="transport-belt",unit_number=1,position={x=1.5,y=1.5},force={name="player"}},
  {name="inserter",type="inserter",unit_number=2,position={x=2.5,y=1.5},force={name="player"}},
  {name="steel-chest",type="container",unit_number=3,position={x=3.5,y=1.5},force={name="player"}},
  {name="stone-furnace",type="furnace",unit_number=4,position={x=4.5,y=1.5},force={name="player"}},
  {name="assembling-machine-3",type="assembling-machine",unit_number=5,position={x=5.5,y=1.5},force={name="player"}},
}
defines={entity_status={working=4,no_fuel=53}}
game={tick=11,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  local out={}
  for _,e in ipairs(catalog) do
    if filter.type==nil or e.type==filter.type then out[#out+1]=e end
  end
  return out
end}}}
` + lua
			if tc.wantRejected {
				harness += `
assert(printed and printed.error, "expected a rejected type filter")
assert(string.find(printed.error, ` + luaString(tc.want) + `, 1, true), "unexpected message: "..tostring(printed.error))
`
			} else {
				harness += `
assert(printed and not printed.error, "engine type rejected: "..tostring(printed and printed.error))
assert(#printed.entities==1, "rows = "..#printed.entities)
assert(printed.entities[1].name==` + luaString(tc.want) + `, "row name "..tostring(printed.entities[1].name))
assert(printed.entities[1].force=="player", "row force")
assert(printed.entities[1].tick==11, "row tick")
`
			}
			out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// TestNearbyLuaAcceptsTypeThatIsAlsoAPrototypeName is the same overlap check for
// the nearby filter, which uses the shared validator.
func TestNearbyLuaAcceptsTypeThatIsAlsoAPrototypeName(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
game={tick=3,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  assert(filter.type=="transport-belt", "engine type filter")
  return {{name="transport-belt",type="transport-belt",unit_number=9,position={x=1.5,y=0},force={name="player"}}}
end}}}
prototypes={entity={["transport-belt"]={type="transport-belt"},["stone-furnace"]={type="furnace"}}}
` + nearbyLua("nauvis", 0, 0, 4, "", "transport-belt", "", "distance", 0, 10) + `
assert(printed and not printed.error, "engine type rejected: "..tostring(printed and printed.error))
assert(#printed.entities==1, "rows = "..#printed.entities)
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
