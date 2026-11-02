package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Ground drops must never be offered to belts, must not pass force (which would
// mark every created item for deconstruction), must keep the whole stack in one
// item-on-ground entity, and must fail when the engine creates no ground item:
// that combination previously reported success while destroying the stack.
func TestDropGroundSpill(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
local calls={}
local surface={}
local function stack(count) return {name="iron-plate",quality="normal",count=count} end
`
	assertions := `
-- A belt tile must be refused before any item moves.
surface.find_entities_filtered=function(filter)
  assert(filter.position.x==37 and filter.position.y==97)
  return {{type="transport-belt"}}
end
assert(ground_drop_blocked(surface,{x=37,y=97})==true, "belt tile must block a ground drop")
surface.find_entities_filtered=function() return {} end
assert(ground_drop_blocked(surface,{x=50.5,y=165.5})==false, "clear tile must allow a ground drop")
-- A belt tile that discards the items: the engine returns no item entity.
surface.spill_item_stack=function(params)
  calls[#calls+1]=params
  return {}
end
assert(spill_on_ground(surface,{x=37,y=97},stack(1))==nil, "empty spill result must fail")
assert(#calls==1 and calls[1].allow_belts==false, "ground drops must pass allow_belts=false")
assert(calls[1].enable_looted==true)
assert(calls[1].force==nil, "force marks spilled items for deconstruction and must not be passed")
assert(calls[1].drop_full_stack==true, "the whole stack must become one item-on-ground entity")
-- A successful spill reports the created item positions.
surface.spill_item_stack=function(params) return {{valid=true,position={x=3.5,y=4.5}}} end
local positions=spill_on_ground(surface,{x=3.5,y=4.5},stack(4))
assert(positions and #positions==1, "created ground items must be reported")
assert(positions[1].x==3.5 and positions[1].y==4.5, "reported position must be the created item")
-- A throwing engine call is a failure, not a silent success.
surface.spill_item_stack=function() error("boom") end
assert(spill_on_ground(surface,{x=0,y=0},stack(1))==nil, "engine error must fail")
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + dropGroundLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ground spill regression: %v\n%s", err, output)
	}
}
