package main

import (
	"math"
	"os/exec"
	"strings"
	"testing"
)

func TestAdjacencyParams(t *testing.T) {
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}
	cases := []struct {
		name      string
		area      *FactoryAreaInput
		force     string
		surface   string
		tolerance float64
		limit     int
		ok        bool
	}{
		{"valid", area, "player", "nauvis", 0.5, 1000, true},
		{"zero tolerance", area, "player", "nauvis", 0, 1, true},
		{"max tolerance", area, "player", "nauvis", 4, 2000, true},
		{"missing surface", area, "player", "", 0.5, 1000, false},
		{"missing force", area, "", "nauvis", 0.5, 1000, false},
		{"negative tolerance", area, "player", "nauvis", -0.1, 1000, false},
		{"excess tolerance", area, "player", "nauvis", 4.1, 1000, false},
		{"nan tolerance", area, "player", "nauvis", math.NaN(), 1000, false},
		{"infinite tolerance", area, "player", "nauvis", math.Inf(1), 1000, false},
		{"zero limit", area, "player", "nauvis", 0.5, 0, false},
		{"excess limit", area, "player", "nauvis", 0.5, 2001, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := adjacencyParams(tc.area, tc.force, tc.surface, tc.tolerance, tc.limit)
			if tc.ok && err != nil {
				t.Fatalf("expected valid params, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAdjacencyAreaBound(t *testing.T) {
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 65, RightBottomY: 10}
	if err := adjacencyParams(area, "player", "nauvis", 0.5, 1000); err != nil {
		t.Fatalf("params should pass before the area bound check: %v", err)
	}
	if _, _, err := worldAreaBoundsLua(area, 64); err == nil {
		t.Fatal("expected the 64 tile area bound to reject a 65 tile dimension")
	}
}

// Pair detection is geometric, so the fixture exercises bounding-box distances
// and engine verification together. The live Factorio smoke validates the
// engine API surface, which unit stubs cannot.
func TestAdjacencyPairsLua(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={entity_status={}}
local surface={index=1,name="nauvis"}
local world={}
surface.find_entities_filtered=function(filter)
  local out={}
  local a=filter.area
  local x1,y1,x2,y2
  if a.left_top then x1,y1,x2,y2=a.left_top.x,a.left_top.y,a.right_bottom.x,a.right_bottom.y
  else x1,y1,x2,y2=a[1][1],a[1][2],a[2][1],a[2][2] end
  for _,e in ipairs(world) do
    local bb=e.bounding_box
    if bb.left_top.x<=x2 and bb.right_bottom.x>=x1 and bb.left_top.y<=y2 and bb.right_bottom.y>=y1 then out[#out+1]=e end
  end
  return out
end
game={tick=7,surfaces={nauvis=surface}}
force={name="player"}
area={{-100,-100},{100,100}}
area_result={leftTopX=-100,leftTopY=-100,rightBottomX=100,rightBottomY=100}
tolerance=1
max_nodes=100
max_pairs=100
captured=nil
helpers={table_to_json=function(t) captured=t return "" end}
rcon={print=function() end}
local function box(x1,y1,x2,y2) return {left_top={x=x1,y=y1},right_bottom={x=x2,y=y2}} end
local function ent(id,t,x1,y1,x2,y2)
  local e={valid=true,unit_number=id,type=t,name=t,surface=surface,position={x=(x1+x2)/2,y=(y1+y2)/2},bounding_box=box(x1,y1,x2,y2)}
  if t=="transport-belt" then
    e.belt_neighbours={inputs={},outputs={}}
    local line={owner=e,input_lines={},output_lines={},get_contents=function() return {} end}
    e.get_transport_line=function() return line end
    e.get_max_transport_line_index=function() return 1 end
  end
  if t=="pipe" or t=="boiler" or t=="offshore-pump" then
    e.fluidbox={}
    if t=="boiler" then e.fluidbox[1]=true end
  end
  return e
end
-- Touching belts with a real engine link.
local belt_a=ent(1,"transport-belt",0,0,1,1)
local belt_b=ent(2,"transport-belt",1,0,2,1)
belt_a.belt_neighbours.outputs[1]=belt_b
belt_b.belt_neighbours.inputs[1]=belt_a
world[#world+1]=belt_a; world[#world+1]=belt_b
-- Touching assemblers without any engine link.
world[#world+1]=ent(3,"assembling-machine",10,0,13,3)
world[#world+1]=ent(4,"assembling-machine",13,0,16,3)
-- One tile gap: inside tolerance 1, excluded by a smaller tolerance.
world[#world+1]=ent(20,"pipe",20,0,21,1)
world[#world+1]=ent(21,"pipe",22,0,23,1)
-- Touching pipe and boiler with a real fluid connection.
local pipe=ent(30,"pipe",30,0,31,1)
local boiler=ent(31,"boiler",31,0,34,2)
pipe.fluidbox.get_pipe_connections=function(index) return {{target={valid=true,owner=boiler}}} end
boiler.fluidbox.get_pipe_connections=function(index) return {{target={valid=true,owner=pipe},flow_direction="output"}} end
world[#world+1]=pipe; world[#world+1]=boiler
-- Touching inserter and chest where the inserter actually targets the chest.
local chest=ent(40,"container",40,0,41,1)
local inserter=ent(41,"inserter",39,0,40,1)
inserter.pickup_target=nil; inserter.drop_target=chest
world[#world+1]=chest; world[#world+1]=inserter
-- Distant entity: never a pair.
world[#world+1]=ent(50,"transport-belt",90,90,91,91)
`
	assertions := `
local by_key={}
local seen={}
for _,p in ipairs(captured.pairs) do
  local k=p.a.unitNumber.."-"..p.b.unitNumber
  assert(not seen[k],"duplicate pair "..k)
  seen[k]=true
  by_key[k]=p
end
assert(#captured.nodes==11,"expected every non-skipped entity as a node, got "..#captured.nodes)
assert(not captured.truncated)
local belt=by_key["1-2"]
assert(belt and belt.connection=="belt","touching connected belts must report belt, got "..tostring(belt and belt.connection))
assert(belt.gap==0,"touching belts must have zero gap")
local plain=by_key["3-4"]
assert(plain and plain.connection=="","touching machines without an engine link must leave connection empty")
assert(by_key["20-21"] and by_key["20-21"].gap==1,"one tile gap must be reported with gap 1")
assert(not by_key["20-50"] and not by_key["1-50"],"distant entities must not pair")
local fluid=by_key["30-31"]
assert(fluid and fluid.connection=="fluid","touching pipe and boiler must report fluid")
local drop=by_key["40-41"]
assert(drop and drop.connection=="drop","chest below an inserter drop target must report drop")
`
	body := strings.NewReader(fixture + transportLineLua + topologyCommonLua + adjacencyLua + assertions)
	cmd := exec.Command(lua, "-")
	cmd.Stdin = body
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("adjacency regression: %v\n%s", err, output)
	}
}

func TestAdjacencyToleranceExcludesGapLua(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={entity_status={}}
local surface={index=1,name="nauvis"}
local world={}
surface.find_entities_filtered=function(filter)
  local out={}
  local a=filter.area
  local x1,y1,x2,y2
  if a.left_top then x1,y1,x2,y2=a.left_top.x,a.left_top.y,a.right_bottom.x,a.right_bottom.y
  else x1,y1,x2,y2=a[1][1],a[1][2],a[2][1],a[2][2] end
  for _,e in ipairs(world) do
    local bb=e.bounding_box
    if bb.left_top.x<=x2 and bb.right_bottom.x>=x1 and bb.left_top.y<=y2 and bb.right_bottom.y>=y1 then out[#out+1]=e end
  end
  return out
end
game={tick=1,surfaces={nauvis=surface}}
force={name="player"}
area={{-10,-10},{10,10}}
area_result={leftTopX=-10,leftTopY=-10,rightBottomX=10,rightBottomY=10}
tolerance=0.5
max_nodes=100
max_pairs=100
captured=nil
helpers={table_to_json=function(t) captured=t return "" end}
rcon={print=function() end}
local function ent(id,t,x1,y1,x2,y2)
  return {valid=true,unit_number=id,type=t,name=t,surface=surface,position={x=(x1+x2)/2,y=(y1+y2)/2},
    bounding_box={left_top={x=x1,y=y1},right_bottom={x=x2,y=y2}}}
end
world[#world+1]=ent(1,"pipe",0,0,1,1)
world[#world+1]=ent(2,"pipe",2,0,3,1)
`
	assertions := `
assert(#captured.nodes==2)
assert(#captured.pairs==0,"a one tile gap must not pair at tolerance 0.5, got "..#captured.pairs)
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + transportLineLua + topologyCommonLua + adjacencyLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("adjacency tolerance regression: %v\n%s", err, output)
	}
}

func TestAdjacencyTruncationLua(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={entity_status={}}
local surface={index=1,name="nauvis"}
local world={}
surface.find_entities_filtered=function(filter)
  local out={}
  local a=filter.area
  local x1,y1,x2,y2
  if a.left_top then x1,y1,x2,y2=a.left_top.x,a.left_top.y,a.right_bottom.x,a.right_bottom.y
  else x1,y1,x2,y2=a[1][1],a[1][2],a[2][1],a[2][2] end
  for _,e in ipairs(world) do
    local bb=e.bounding_box
    if bb.left_top.x<=x2 and bb.right_bottom.x>=x1 and bb.left_top.y<=y2 and bb.right_bottom.y>=y1 then out[#out+1]=e end
  end
  return out
end
game={tick=1,surfaces={nauvis=surface}}
force={name="player"}
area={{-10,-10},{10,10}}
area_result={leftTopX=-10,leftTopY=-10,rightBottomX=10,rightBottomY=10}
tolerance=0.5
max_nodes=100
max_pairs=1
captured=nil
helpers={table_to_json=function(t) captured=t return "" end}
rcon={print=function() end}
local function ent(id,x1,y1,x2,y2)
  return {valid=true,unit_number=id,type="pipe",name="pipe",surface=surface,position={x=(x1+x2)/2,y=(y1+y2)/2},
    bounding_box={left_top={x=x1,y=y1},right_bottom={x=x2,y=y2}}}
end
world[#world+1]=ent(1,0,0,1,1)
world[#world+1]=ent(2,1,0,2,1)
world[#world+1]=ent(3,2,0,3,1)
`
	assertions := `
assert(#captured.pairs==1,"pairs must stop at the limit, got "..#captured.pairs)
assert(captured.truncated,"more pairs than the limit must set truncated")
assert(#captured.nodes==3,"nodes are not pair-limited")
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + transportLineLua + topologyCommonLua + adjacencyLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("adjacency truncation regression: %v\n%s", err, output)
	}
}
