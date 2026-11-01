package main

import (
	"fmt"
	"math"
	"os/exec"
	"strings"
	"testing"
)

func TestTopologyBeltParams(t *testing.T) {
	cases := []struct {
		name    string
		surface string
		x, y    float64
		limit   int
		valid   bool
	}{
		{"minimum", "nauvis", -1.5, 0, 1, true},
		{"maximum", "nauvis", 0, 2.5, 1000, true},
		{"missing surface", "", 0, 0, 1, false},
		{"nan x", "nauvis", math.NaN(), 0, 1, false},
		{"infinite x", "nauvis", math.Inf(1), 0, 1, false},
		{"nan y", "nauvis", 0, math.NaN(), 1, false},
		{"infinite y", "nauvis", 0, math.Inf(-1), 1, false},
		{"zero limit", "nauvis", 0, 0, 0, false},
		{"negative limit", "nauvis", 0, 0, -1, false},
		{"excess limit", "nauvis", 0, 0, 1001, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := topologyBeltParams(tc.surface, tc.x, tc.y, tc.limit)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

// Exercise the traversal itself against directed engine-connection fixtures.
// The live Factorio smoke remains responsible for validating the engine API.
func TestTopologyDirectedTraversal(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={entity_status={}}
local surface={index=1,name="nauvis"}
surface.find_entities_filtered=function() return {} end
game={tick=123,surfaces={nauvis=surface}}
local function entity(id,t,x)
  local e={valid=true,unit_number=id,type=t or "transport-belt",name=t or "transport-belt",surface=surface,position={x=x or id,y=0},belt_neighbours={inputs={},outputs={}}}
  local line={owner=e,input_lines={},output_lines={},get_contents=function() return {} end}
  e.get_transport_line=function() return line end
  e.get_max_transport_line_index=function() return 1 end
  return e,line
end
local function connect(a,b)
  a.belt_neighbours.outputs[#a.belt_neighbours.outputs+1]=b
  b.belt_neighbours.inputs[#b.belt_neighbours.inputs+1]=a
end
`
	assertions := `
local function contains(nodes,id)
  for _,n in ipairs(nodes) do if n.unitNumber==id then return true end end
  return false
end
-- Spatial proximity has no influence: only declared engine connections count.
local a=entity(1,"splitter",0)
local b=entity(2,nil,100)
local c=entity(3,nil,200)
local unrelated=entity(99,nil,0.1)
connect(a,b); connect(a,c)
local run=belt_run(b,10)
assert(#run.nodes==3 and #run.edges==2 and not run.truncated)
assert(not contains(run.nodes,99))
assert(#run.sources==1 and run.sources[1].unitNumber==1)
assert(#run.sinks==2 and contains(run.sinks,2) and contains(run.sinks,3))
-- Node bound never invents terminal nodes at an unvisited continuation.
local bounded=belt_run(a,1)
assert(#bounded.nodes==1 and bounded.truncated and #bounded.boundaryEdges==2)
assert(#bounded.sinks==0 and #bounded.sources==1)
for _,edge in ipairs(bounded.boundaryEdges) do assert(edge.boundary) end
-- Cycles terminate, preserve directed edges, and have no structural terminals.
connect(b,a); connect(c,a)
local cycle=belt_run(a,10)
assert(#cycle.nodes==3 and #cycle.edges==4 and not cycle.truncated)
assert(#cycle.sources==0 and #cycle.sinks==0)
local cut_cycle=belt_run(a,1)
assert(cut_cycle.truncated and #cut_cycle.sources==0 and #cut_cycle.sinks==0)
-- Underground and linked pairs need no adjacent map tiles or belt_neighbours.
local u=entity(10,"underground-belt",1000)
local v=entity(11,"underground-belt",1010)
u.belt_to_ground_type="input"; v.belt_to_ground_type="output"; u.neighbours=v; v.neighbours=u
local under=belt_run(v,10)
assert(#under.nodes==2 and #under.edges==1 and under.edges[1].kind=="underground")
assert(under.edges[1].from.unitNumber==10 and under.edges[1].to.unitNumber==11)
local l=entity(12,"linked-belt",2000)
local r=entity(13,"linked-belt",9000)
l.linked_belt_type="input"; r.linked_belt_type="output"; l.linked_belt_neighbour=r; r.linked_belt_neighbour=l
local linked=belt_run(r,10)
assert(#linked.nodes==2 and #linked.edges==1 and linked.edges[1].kind=="linked")
assert(linked.edges[1].from.unitNumber==12 and linked.edges[1].to.unitNumber==13)
-- Engine transport-line links also establish directed connectivity.
local t,tl=entity(20)
local z,zl=entity(21)
tl.output_lines={zl}; zl.input_lines={tl}
local transport=belt_run(z,10)
assert(#transport.nodes==2 and #transport.edges==1)
assert(transport.edges[1].from.unitNumber==20 and transport.edges[1].to.unitNumber==21)
local isolated=belt_run(unrelated,10)
assert(#isolated.nodes==1 and not isolated.truncated and #isolated.edges==0)
assert(#isolated.sources==1 and #isolated.sinks==1)
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + transportLineLua + topologyCommonLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("directed topology regression: %v\n%s", err, output)
	}
}

// Exercise the linear stretch walk against directed engine-connection fixtures:
// underground pairs collapse to one hop, splitters and merges stop the walk,
// lane names carry their side, and engine line identity deduplicates totals.
func TestTopologyBeltStretch(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={entity_status={},transport_line={left_line=1,right_line=2,left_underground_line=3,right_underground_line=4}}
local surface={index=1,name="nauvis",entities={}}
-- The fake surface answers the area queries the end classification makes.
surface.find_entities_filtered=function(spec)
  local found={}
  local a=spec.area
  for _,e in ipairs(surface.entities) do
    if e.valid and e.position.x>=a[1][1] and e.position.x<=a[2][1] and e.position.y>=a[1][2] and e.position.y<=a[2][2] then found[#found+1]=e end
  end
  return found
end
game={tick=123,surfaces={nauvis=surface}}
local id=0
local function line(entity,items,identity)
  return {owner=entity,identity=identity,get_contents=function() return items end,
    line_equals=function(other) return other.identity==identity end,
    input_lines={},output_lines={}}
end
local function entity(t,x,y)
  id=id+1
  local e={valid=true,unit_number=id,type=t,name=t,surface=surface,position={x=x,y=y},direction=4,
    belt_neighbours={inputs={},outputs={}},lines={}}
  e.get_max_transport_line_index=function() return #e.lines end
  e.get_transport_line=function(i) return e.lines[i] end
  surface.entities[#surface.entities+1]=e
  return e
end
local function lanes(e,specs)
  for i,spec in ipairs(specs) do
    e.lines[i]=line(e,spec.items or {},spec.identity or (tostring(e.unit_number)..":"..i))
  end
end
local function connect(a,b)
  a.belt_neighbours.outputs[#a.belt_neighbours.outputs+1]=b
  b.belt_neighbours.inputs[#b.belt_neighbours.inputs+1]=a
end
local a=entity("transport-belt",0,0)
lanes(a,{{items={{name="iron-plate",quality="normal",count=3}}},{items={{name="coal",quality="normal",count=1}}}})
local b=entity("transport-belt",1,0)
lanes(b,{{items={{name="iron-plate",quality="normal",count=2}}},{items={}}})
connect(a,b)
local c=entity("underground-belt",2,0)
c.belt_to_ground_type="input"
lanes(c,{{items={}},{items={}},{items={{name="iron-plate",quality="normal",count=4}}},{items={{name="coal",quality="normal",count=4}}}})
connect(b,c)
local d=entity("underground-belt",2,6)
d.belt_to_ground_type="output"
-- A tunnel exit is a separate tile, so its items are counted in addition to the
-- entrance's tunnel lines even though they belong to one internal line.
lanes(d,{{items={{name="iron-plate",quality="normal",count=4}},identity="3:3"},{items={{name="coal",quality="normal",count=4}},identity="3:4"},{items={}},{items={}}})
c.neighbours=d; d.neighbours=c
local e=entity("transport-belt",3,6)
lanes(e,{{items={{name="iron-plate",quality="normal",count=1}}},{items={}}})
connect(d,e)
local splitter=entity("splitter",4,6)
connect(e,splitter)
-- One entity exposing the same internal line twice must be counted once.
local dup=entity("transport-belt",9,9)
lanes(dup,{{items={{name="stone",quality="normal",count=2}},identity="dup"},{items={{name="stone",quality="normal",count=1}},identity="dup"}})
`
	assertions := `
local stretch=belt_stretch(a,10)
assert(not stretch.truncated)
assert(#stretch.nodes==5, "expected 5 nodes, got "..#stretch.nodes)
assert(stretch.nodes[1].position.x==0 and stretch.nodes[5].position.x==3)
assert(#stretch.ends==2)
assert(stretch.ends[1].kind=="boundary" and stretch.ends[2].kind=="split")
assert(stretch.ends[2].targets[1].type=="splitter")
assert(stretch.nodes[1].lanes[1].lineName=="left_line" and stretch.nodes[1].lanes[1].side=="left")
assert(stretch.nodes[1].lanes[2].lineName=="right_line" and stretch.nodes[1].lanes[2].side=="right")
assert(stretch.nodes[3].lanes[3].lineName=="left_underground_line")
assert(stretch.seed.direction==4, "endpoints expose the engine direction")
-- a: 3 iron left; b: 2 iron left; c tunnel: 4 iron left + 4 coal right; d exit: 4 iron left + 4 coal right; e: 1 iron left.
assert(stretch.left.itemCount==14, "left total "..tostring(stretch.left.itemCount))
assert(stretch.right.itemCount==9, "right total "..tostring(stretch.right.itemCount))
assert(stretch.left.mixed==false and stretch.right.mixed==false)
local bounded=belt_stretch(a,2)
assert(bounded.truncated and #bounded.nodes==2)
local from_exit=belt_stretch(d,10)
assert(#from_exit.nodes==5 and from_exit.nodes[1].position.x==0)
local duplicated=belt_stretch(dup,10)
assert(duplicated.left.itemCount==2 and duplicated.right.itemCount==0)

-- Forward boundary classification: the splitter end and the backward start of
-- the stretch above are not classified.
assert(stretch.ends[1].reason=="NOT_APPLICABLE" and stretch.ends[1].gap==false and stretch.ends[1].expectedTarget==nil)
assert(stretch.ends[2].reason=="NOT_APPLICABLE")
local function forward_end(seed)
  local result=belt_stretch(seed,10)
  return result.ends[#result.ends]
end
local function belt_at(x,y,dir)
  local b=entity("transport-belt",x,y)
  b.direction=dir or 4
  lanes(b,{{items={}},{items={}}})
  return b
end
-- Natural end: nothing ahead and nothing beyond.
local natural=forward_end(belt_at(0,20))
assert(natural.kind=="boundary" and natural.reason=="NATURAL_END" and natural.gap==false and natural.blocker==nil)
assert(natural.expectedTarget.x==1 and natural.expectedTarget.y==20)
-- Missing belt: empty tile, aligned belt two tiles ahead.
local missing_seed=belt_at(0,21); belt_at(2,21)
local missing=forward_end(missing_seed)
assert(missing.reason=="MISSING_BELT" and missing.gap==true and missing.blocker==nil, "missing "..tostring(missing.reason))
assert(missing.expectedTarget.x==1 and missing.expectedTarget.y==21)
-- Blocked by a chest sitting in the gap tile.
local blocked_seed=belt_at(0,22)
local chest=entity("container",1,22); chest.name="wooden-chest"
belt_at(2,22)
local blocked=forward_end(blocked_seed)
assert(blocked.reason=="BLOCKED" and blocked.gap==true and blocked.blocker=="wooden-chest", "blocked "..tostring(blocked.reason))
-- Blocked by a tree, which is not a belt either.
local tree_seed=belt_at(0,26)
local tree=entity("tree",1,26); tree.name="tree-01"
belt_at(3,26)
local tree_end=forward_end(tree_seed)
assert(tree_end.reason=="BLOCKED" and tree_end.blocker=="tree-01")
-- A belt facing the run head-on can never connect.
local head_seed=belt_at(0,23); belt_at(1,23,12)
local head_on=forward_end(head_seed)
assert(head_on.reason=="HEAD_ON" and head_on.gap==true and head_on.blocker=="transport-belt")
-- A machine in front with nothing beyond is a natural end that still names it.
local machine_seed=belt_at(0,24)
local furnace=entity("furnace",1,24); furnace.name="stone-furnace"
local machine=forward_end(machine_seed)
assert(machine.reason=="NATURAL_END" and machine.gap==false and machine.blocker=="stone-furnace")
-- Resources and items on the ground never block.
local ore_seed=belt_at(0,27)
local ore=entity("resource",1,27); ore.name="iron-ore"
assert(forward_end(ore_seed).blocker==nil)
-- An underground entrance with no exit is a gap.
local unpaired=entity("underground-belt",0,25)
unpaired.belt_to_ground_type="input"
lanes(unpaired,{{items={}},{items={}},{items={}},{items={}}})
local unpaired_end=forward_end(unpaired)
assert(unpaired_end.reason=="UNPAIRED_UNDERGROUND" and unpaired_end.gap==true)
-- A paired tunnel exit with nothing ahead is a natural end.
local entrance=entity("underground-belt",0,28); entrance.belt_to_ground_type="input"
local exit=entity("underground-belt",4,28); exit.belt_to_ground_type="output"
lanes(entrance,{{items={}},{items={}},{items={}},{items={}}})
lanes(exit,{{items={}},{items={}},{items={}},{items={}}})
entrance.neighbours=exit; exit.neighbours=entrance
local exit_end=forward_end(exit)
assert(exit_end.reason=="NATURAL_END" and exit_end.gap==false and exit_end.expectedTarget.x==5, "exit "..tostring(exit_end.reason))
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + transportLineLua + topologyCommonLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("belt stretch regression: %v\n%s", err, output)
	}
}

// A forward boundary must decode the gap classification the Lua computes from
// the last belt's direction and the tiles ahead, and the Lua must carry the
// rules that separate a natural end from a gap.
func TestBeltStretchEndClassification(t *testing.T) {
	end := func(x float64, extra string) string {
		return fmt.Sprintf(`{"position":{"x":%g,"y":0.5},"unitNumber":9,"kind":"boundary","targets":{},"gap":false,"reason":"NOT_APPLICABLE"%s}`, x, extra)
	}
	cases := []struct {
		name      string
		end       string
		reason    BeltEndReason
		gap       bool
		blocker   string
		hasTarget bool
		targetX   float64
	}{
		{"natural end", `{"position":{"x":2.5,"y":0.5},"unitNumber":9,"kind":"boundary","targets":{},"gap":false,"reason":"NATURAL_END","expectedTarget":{"x":3.5,"y":0.5}}`, BeltEndReasonNaturalEnd, false, "", true, 3.5},
		{"natural end into a machine", `{"position":{"x":2.5,"y":0.5},"unitNumber":9,"kind":"boundary","targets":{},"gap":false,"reason":"NATURAL_END","blocker":"stone-furnace","expectedTarget":{"x":3.5,"y":0.5}}`, BeltEndReasonNaturalEnd, false, "stone-furnace", true, 3.5},
		{"missing belt", `{"position":{"x":2.5,"y":0.5},"unitNumber":9,"kind":"boundary","targets":{},"gap":true,"reason":"MISSING_BELT","expectedTarget":{"x":3.5,"y":0.5}}`, BeltEndReasonMissingBelt, true, "", true, 3.5},
		{"blocked by a tree", `{"position":{"x":2.5,"y":0.5},"unitNumber":9,"kind":"boundary","targets":{},"gap":true,"reason":"BLOCKED","blocker":"tree-01","expectedTarget":{"x":3.5,"y":0.5}}`, BeltEndReasonBlocked, true, "tree-01", true, 3.5},
		{"head on", `{"position":{"x":2.5,"y":0.5},"unitNumber":9,"kind":"boundary","targets":{},"gap":true,"reason":"HEAD_ON","blocker":"transport-belt","expectedTarget":{"x":3.5,"y":0.5}}`, BeltEndReasonHeadOn, true, "transport-belt", true, 3.5},
		{"unpaired underground", `{"position":{"x":2.5,"y":0.5},"unitNumber":9,"kind":"boundary","targets":{},"gap":true,"reason":"UNPAIRED_UNDERGROUND","expectedTarget":{"x":3.5,"y":0.5}}`, BeltEndReasonUnpairedUnderground, true, "", true, 3.5},
		{"backward boundary", end(0.5, ``), BeltEndReasonNotApplicable, false, "", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"tick":1,"surface":"nauvis","seed":{"name":"transport-belt","type":"transport-belt","position":{"x":0.5,"y":0.5},"lanes":{}},"maxEntities":10,"truncated":false,"nodes":{},"ends":[` + tc.end + `],"left":{"side":"left","items":{},"itemCount":0,"mixed":false},"right":{"side":"right","items":{},"itemCount":0,"mixed":false},"unknown":{"side":"unknown","items":{},"itemCount":0,"mixed":false}}`
			r, commands := serveLuaOnce(t, response)
			x, y := 0.5, 0.5
			stretch, err := beltStretchQuery(r, "nauvis", &x, &y, nil, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(stretch.Ends) != 1 {
				t.Fatalf("ends: %+v", stretch.Ends)
			}
			got := stretch.Ends[0]
			if got.Reason != tc.reason || got.Gap != tc.gap {
				t.Fatalf("reason=%v gap=%v, want %v %v", got.Reason, got.Gap, tc.reason, tc.gap)
			}
			if (got.Blocker == nil) != (tc.blocker == "") || (got.Blocker != nil && *got.Blocker != tc.blocker) {
				t.Fatalf("blocker: %v want %q", got.Blocker, tc.blocker)
			}
			if tc.hasTarget != (got.ExpectedTarget != nil) || (tc.hasTarget && got.ExpectedTarget.X != tc.targetX) {
				t.Fatalf("expectedTarget: %+v", got.ExpectedTarget)
			}
			lua := <-commands
			for _, want := range []string{
				`local function classify_forward_end(e,entry)`,
				`local end_lookahead=8`,
				`entry.reason="HEAD_ON"`,
				`entry.reason="UNPAIRED_UNDERGROUND"`,
				`entry.reason=blocker~=nil and "BLOCKED" or "MISSING_BELT"`,
				`record_end(cursor,"boundary",succs,true)`,
				`record_end(cursor,"boundary",preds)`,
			} {
				if !strings.Contains(lua, want) {
					t.Fatalf("stretch Lua missing %q", want)
				}
			}
		})
	}
}

// Factorio leaves pickup_target/drop_target nil on inserters that have not run
// since placement or power loss. The topology helpers must fall back to the
// entity under the arm's geometric pickup/drop position without inventing
// endpoints for empty tiles or non-insertable neighbours.
func TestTopologyInserterTargetFallback(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={entity_status={}}
local world={}
local surface={index=1,name="nauvis"}
surface.find_entities_filtered=function(spec)
  local out={}
  for _,e in pairs(world) do
    local bb=e.bounding_box
    local ok=true
    if spec.position then
      ok=spec.position.x>=bb.left_top.x and spec.position.x<=bb.right_bottom.x
        and spec.position.y>=bb.left_top.y and spec.position.y<=bb.right_bottom.y
    end
    if ok and spec.name and e.name~=spec.name then ok=false end
    if ok and spec.force and e.force~=spec.force then ok=false end
    if ok then out[#out+1]=e end
  end
  return out
end
game={tick=1,surfaces={nauvis=surface}}
local force={name="player"}
local function ent(id,kind,name,cx,cy,size)
  local half=(size or 1)/2
  local e={valid=true,unit_number=id,type=kind,name=name or kind,surface=surface,force=force,
    position={x=cx,y=cy},
    bounding_box={left_top={x=cx-half,y=cy-half},right_bottom={x=cx+half,y=cy+half}}}
  world[#world+1]=e
  return e
end
-- Idle inserter between a pipe (not insertable) and a chest, no engine targets.
local pipe=ent(11,"pipe","pipe",3.5,0.5)
local chest=ent(10,"container","iron-chest",5.5,0.5)
local idle=ent(20,"inserter","inserter",4.5,0.5)
idle.pickup_position={x=3.5,y=0.5}
idle.drop_position={x=5.5,y=0.5}
-- An engine target always wins over the geometric position.
local steel=ent(12,"container","steel-chest",9.5,9.5)
local live=ent(21,"inserter","inserter",8.5,9.5)
live.pickup_position={x=7.5,y=9.5}
live.drop_position={x=0.5,y=0.5}
live.drop_target=steel
-- No positions at all: nothing resolved, nothing invented.
local bare=ent(22,"inserter","inserter",-5.5,-5.5)
-- A drill whose engine drop target is nil falls back to its drop position.
local drill=ent(30,"mining-drill","burner-mining-drill",13.5,-8.5,3)
drill.drop_position={x=15.5,y=-8.5}
local drill_chest=ent(31,"container","wooden-chest",15.5,-8.5)
`
	assertions := `
local wired=connected_inserter(idle)
assert(wired.drop and wired.drop.name=="iron-chest","geometric drop must resolve the chest")
assert(wired.pickup==nil,"a pipe is not an insertable pickup")
local from_engine=connected_inserter(live)
assert(from_engine.drop and from_engine.drop.name=="steel-chest","engine target must win")
assert(from_engine.pickup==nil,"empty pickup position must stay nil")
local empty=connected_inserter(bare)
assert(empty.pickup==nil and empty.drop==nil,"no positions must resolve nothing")
assert(machine_drop_target(drill)==drill_chest,"drill drop must resolve geometrically")
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + transportLineLua + topologyCommonLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inserter target fallback regression: %v\n%s", err, output)
	}
}
