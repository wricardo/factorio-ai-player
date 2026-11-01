package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Ground drops must never be offered to belts and must fail when the engine
// creates no ground item: that combination previously reported success while
// destroying the stack.
func TestDropGroundSpill(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
local calls={}
local surface={}
local actor={force="player"}
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
assert(spill_on_ground(surface,{x=37,y=97},stack(1),actor)==false, "empty spill result must fail")
assert(#calls==1 and calls[1].allow_belts==false, "ground drops must pass allow_belts=false")
assert(calls[1].enable_looted==true and calls[1].force=="player")
-- A successful spill returns the created item entity.
surface.spill_item_stack=function(params) return {{valid=true}} end
assert(spill_on_ground(surface,{x=50.5,y=165.5},stack(1),actor)==true, "created ground item must succeed")
-- A throwing engine call is a failure, not a silent success.
surface.spill_item_stack=function() error("boom") end
assert(spill_on_ground(surface,{x=0,y=0},stack(1),actor)==false, "engine error must fail")
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + dropGroundLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ground spill regression: %v\n%s", err, output)
	}
}
