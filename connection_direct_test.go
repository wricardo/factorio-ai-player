package main

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// directPlanHarness runs the DIRECT plan Lua against a fake surface so the
// side-load, belt-feed and fluid-port rules are executed, not just grepped. The
// live integration suite stays responsible for the real engine API surface.
func directPlanHarness(t *testing.T, kind ConnectionKind, item string, from, to *PositionInput, allowBeltAdjacency bool, fixture string, assertion string) {
	t.Helper()
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	body := fixture + connectionLuaPreamble("nauvis", "player", item, from, to, kind, ConnectionBendAuto, runDirectionValue(nil), false, allowBeltAdjacency) + connectionLuaHelpers + assertion
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(body)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("direct plan harness: %v\n%s", err, output)
	}
}

// The fixture surface answers tile queries from a list of entities, exactly like
// the engine does for the DIRECT checks.
const directPlanFixture = `
local world,blocked={},{}
local function box(cx,cy,size)
  local half=(size or 1)/2
  return {left_top={x=cx-half,y=cy-half},right_bottom={x=cx+half,y=cy+half}}
end
local function belt(x,y,direction,name)
  local e={valid=true,type="transport-belt",name=name or "transport-belt",direction=direction,bounding_box=box(x,y)}
  world[#world+1]=e
  return e
end
local function fluid_port(x,y,kind,connections)
  local e={valid=true,type=kind,name=kind,direction=0,bounding_box=box(x,y,2)}
  local fluidbox={true}
  fluidbox.get_pipe_connections=function() return connections end
  e.fluidbox=fluidbox
  world[#world+1]=e
  return e
end
local surface={name="nauvis",index=1}
surface.find_entities_filtered=function(spec)
  local out={}
  local x1,y1,x2,y2
  if spec.area then
    local a=spec.area
    if a.left_top then x1,y1,x2,y2=a.left_top.x,a.left_top.y,a.right_bottom.x,a.right_bottom.y
    else x1,y1,x2,y2=a[1][1],a[1][2],a[2][1],a[2][2] end
  elseif spec.position then
    x1,y1,x2,y2=spec.position.x,spec.position.y,spec.position.x,spec.position.y
  end
  for _,e in ipairs(world) do
    local bb=e.bounding_box
    -- Strict overlap: an entity whose box only touches the query edge is not in
    -- the tile, which is how the engine answers tile-sized area queries.
    if bb.left_top.x<x2 and bb.right_bottom.x>x1 and bb.left_top.y<y2 and bb.right_bottom.y>y1 then out[#out+1]=e end
  end
  return out
end
surface.can_place_entity=function(spec)
  local key=tostring(math.floor(spec.position.x))..":"..tostring(math.floor(spec.position.y))
  return blocked[key]~=true
end
surface.get_tile=function() return {valid=true,name="grass-1",prototype={collision_mask={layers={}}}} end
defines={build_check_type={manual="manual",script="script",player="player"}}
game={tick=1,surfaces={nauvis=surface},forces={player={name="player"}}}
local function place_result(kind)
  return {place_result={type=kind,name=kind}}
end
prototypes={item={["transport-belt"]=place_result("transport-belt"),["pipe"]=place_result("pipe"),["medium-electric-pole"]=place_result("electric-pole")}}
captured=nil
helpers={table_to_json=function() return "" end}
rcon={print=function(text) captured=text end}
`

const directPlanCapture = `
rcon.print(helpers.table_to_json{possible=(#chosen.conflicts==0),needed=chosen.needed,steps=chosen.steps,conflicts=chosen.conflicts})
`

// Connecting a new run into the side of an existing belt is a normal side-load,
// so the goal tile keeps its own direction instead of reporting a mismatch.
func TestDirectPlanSideLoadsGoalBelt(t *testing.T) {
	fixture := directPlanFixture + `
belt(2.5,0.5,0,"transport-belt")
`
	assertion := directPlanCapture + `
assert(#chosen.conflicts==0,"side-loading must not conflict: "..tostring(chosen.conflicts[1] and chosen.conflicts[1].kind))
local last=chosen.steps[#chosen.steps]
assert(last.existing,"the goal belt must be reused")
assert(last.direction==0,"the goal keeps its own direction, got "..tostring(last.direction))
`
	y := 0.0
	directPlanHarness(t, ConnectionKindBelt, "transport-belt", &PositionInput{X: 0, Y: y}, &PositionInput{X: 2, Y: y}, false, fixture, assertion)
}

// A goal belt facing the arrival head-on can never connect, so it stays a typed
// mismatch instead of being silently reused.
func TestDirectPlanRejectsHeadOnGoalBelt(t *testing.T) {
	fixture := directPlanFixture + `
belt(2.5,0.5,12,"transport-belt")
`
	assertion := directPlanCapture + `
assert(#chosen.conflicts==1 and chosen.conflicts[1].kind=="DIRECTION_MISMATCH",tostring(chosen.conflicts[1] and chosen.conflicts[1].kind))
`
	y := 0.0
	directPlanHarness(t, ConnectionKindBelt, "transport-belt", &PositionInput{X: 0, Y: y}, &PositionInput{X: 2, Y: y}, false, fixture, assertion)
}

// A foreign belt pointing into a new route tile side-loads its own items, so the
// plan refuses that tile unless the caller accepts the merge.
func TestDirectPlanRejectsForeignBeltFeed(t *testing.T) {
	fixture := directPlanFixture + `
-- A belt above the middle tile, facing south into it.
belt(1.5,-0.5,8,"transport-belt")
`
	y := 0.0
	assertion := directPlanCapture + `
local found=false
for _,conflict in ipairs(chosen.conflicts) do
  if conflict.kind=="BELT_ADJACENCY" then found=true end
end
assert(found,"a foreign belt feeding the route must be reported")
assert(chosen.needed>0)
`
	directPlanHarness(t, ConnectionKindBelt, "transport-belt", &PositionInput{X: 0, Y: y}, &PositionInput{X: 2, Y: y}, false, fixture, assertion)

	allowed := `
local found=false
for _,conflict in ipairs(chosen.conflicts) do
  if conflict.kind=="BELT_ADJACENCY" then found=true end
end
assert(not found,"allowBeltAdjacency must accept the merge")
`
	directPlanHarness(t, ConnectionKindBelt, "transport-belt", &PositionInput{X: 0, Y: y}, &PositionInput{X: 2, Y: y}, true, fixture, allowed+directPlanCapture)
}

// A machine with a fluid port next to a pipe route joins that network, so the
// plan reports FLUID_ADJACENCY even though the entity type is not pipe-like.
func TestDirectPlanDetectsFluidPortMachines(t *testing.T) {
	fixture := directPlanFixture + `
-- Not a pipe-like type: only its fluidbox says it joins a fluid network.
fluid_port(1.5,-0.5,"custom-port-machine",{})
`
	assertion := directPlanCapture + `
local found=false
for _,conflict in ipairs(chosen.conflicts) do
  if conflict.kind=="FLUID_ADJACENCY" then found=true end
end
assert(found,"a fluid-port machine must count as pipe-like")
`
	y := 0.0
	directPlanHarness(t, ConnectionKindPipe, "pipe", &PositionInput{X: 0, Y: y}, &PositionInput{X: 2, Y: y}, false, fixture, assertion)
}

// The fluid-port predicate follows the engine's connection targets: a port that
// reports connections to other tiles is not pipe-like on this one.
func TestDirectPlanFluidPortPrecision(t *testing.T) {
	fixture := directPlanFixture + `
fluid_port(1.5,-0.5,"custom-port-machine",{{target_position={x=1.5,y=-1.5}}})
`
	assertion := directPlanCapture + `
local found=false
for _,conflict in ipairs(chosen.conflicts) do
  if conflict.kind=="FLUID_ADJACENCY" then found=true end
end
assert(not found,"a port connected elsewhere must not conflict")
`
	y := 0.0
	directPlanHarness(t, ConnectionKindPipe, "pipe", &PositionInput{X: 0, Y: y}, &PositionInput{X: 2, Y: y}, false, fixture, assertion)
}

// A single-tile run has no heading of its own; before this test it silently
// became a north-facing belt. The route builder must use the explicit direction
// for a one-tile run and keep deriving every direction from the route otherwise.
func TestDirectRouteUsesExplicitDirectionForSingleTile(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	from := &PositionInput{X: 10.5, Y: 10.5}
	to := &PositionInput{X: 10.5, Y: 10.5}
	run := func(direction int, assertion string) {
		t.Helper()
		body := directPlanFixture + connectionLuaPreamble("nauvis", "player", "transport-belt", from, to, ConnectionKindBelt, ConnectionBendAuto, direction, false, false) + connectionLuaHelpers + assertion
		cmd := exec.Command(lua, "-")
		cmd.Stdin = strings.NewReader(body)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("single-tile harness: %v\n%s", err, output)
		}
	}
	run(8, `
local steps=build("AUTO")
assert(#steps==1, "want one step, got "..#steps)
assert(steps[1].direction==8, "want south (8), got "..tostring(steps[1].direction))
assert(steps[1].x==10.5 and steps[1].y==10.5, "want tile centre 10.5,10.5 got "..tostring(steps[1].x)..","..tostring(steps[1].y))
`)
	// A two-tile run still derives both directions from the route.
	from = &PositionInput{X: 10.5, Y: 10.5}
	to = &PositionInput{X: 13.5, Y: 10.5}
	run(-1, `
local steps=build("AUTO")
assert(#steps==4, "want four steps, got "..#steps)
for i=1,3 do
  assert(steps[i].direction==4, "step "..i.." want east (4), got "..tostring(steps[i].direction))
end
assert(steps[4].direction==4, "last step want east (4), got "..tostring(steps[4].direction))
`)
}

// anchorHarness runs the entity-endpoint resolver against a fake surface that
// holds one chemical plant (unit 812) with a north and a south fluid port. The
// fake get_entity_by_unit_number answers nil, like the engine does for every
// prototype that is not flagged get-by-unit-number.
func anchorHarness(t *testing.T, toRef string, otherX, otherY float64, assertion string) {
	t.Helper()
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	body := luaFail + directPlanFixture + `
local plant={valid=true,type="chemical-plant",name="chemical-plant",direction=0,unit_number=812,bounding_box=box(23.5,37.5,3)}
local plant_box={true}
plant_box.get_pipe_connections=function()
  return {{target_position={x=22.5,y=35.5},connection_type="normal"},{target_position={x=22.5,y=39.5},connection_type="normal"},{target_position={x=60.5,y=60.5},connection_type="underground"}}
end
plant.fluidbox=plant_box
world[#world+1]=plant
local base_find=surface.find_entities_filtered
surface.find_entities_filtered=function(spec)
  if spec.type then
    local out={}
    for _,e in ipairs(world) do if e.unit_number then out[#out+1]=e end end
    return out
  end
  return base_find(spec)
end
game.get_entity_by_unit_number=function() return nil end
helpers.table_to_json=function(value) return value end
local surface_name,force_name,item_name="nauvis","player","pipe"
local from_ref,to_ref=nil,` + toRef + `
local from_position,to_position={x=` + strconv.FormatFloat(otherX, 'g', -1, 64) + `,y=` + strconv.FormatFloat(otherY, 'g', -1, 64) + `},nil
` + connectionAnchorLua + assertion
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(body)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("anchor harness: %v\n%s", err, output)
	}
}

// A machine resolves by unit number even though the engine's direct lookup does
// not know it, and a pipe run ends on the fluid port facing the other endpoint
// instead of the tile beside the machine centre (factorio-ai-player-0zl).
func TestAnchorResolvesMachinePortByUnitNumber(t *testing.T) {
	anchorHarness(t, `{unitNumber="812"}`, 23.5, 60.5, `
assert(captured.to,"entity endpoint must resolve")
assert(captured.to.x==22.5 and captured.to.y==39.5 and captured.to.side=="south",
  "expected the south port tile, got "..tostring(captured.to.x)..","..tostring(captured.to.y))
`)
	anchorHarness(t, `{unitNumber="812",side="north"}`, 23.5, 60.5, `
assert(captured.to.x==22.5 and captured.to.y==35.5 and captured.to.side=="north",
  "side hint must pick the north port, got "..tostring(captured.to.x)..","..tostring(captured.to.y))
`)
}
