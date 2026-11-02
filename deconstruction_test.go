package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeconstructionAreaRejectsInvalidBoundsBeforeRCON(t *testing.T) {
	tests := []string{
		`mutation { deconstructArea(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:1025,rightBottomY:1}}) { orders } }`,
		`mutation { cancelDeconstructArea(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:0,rightBottomY:1}}) { orders } }`,
	}
	for _, mutation := range tests {
		var out bytes.Buffer
		err := runWithCredentials([]string{"mutation", mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), "area") {
			t.Errorf("expected invalid-area error for %s, got %v", mutation, err)
		}
	}
}

func TestDeconstructionEntitiesValidateTargetsBeforeRCON(t *testing.T) {
	tests := []struct {
		mutation string
		want     string
	}{
		{`mutation { deconstructEntities(input:{targets:[]}) { orders } }`, "targets"},
		{`mutation { deconstructEntities(input:{targets:[{}]}) { orders } }`, "entity name or unitNumber"},
		{`mutation { deconstructEntities(input:{targets:[{entity:"transport-belt",position:{x:1,y:2},unitNumber:"3"}]}) { orders } }`, "not both"},
		{`mutation { cancelDeconstructEntities(input:{targets:[{entity:"transport-belt",position:{x:1}}]}) { orders } }`, "y"},
		{`mutation { deconstructEntities(input:{force:"",targets:[{unitNumber:"3"}]}) { orders } }`, "force"},
	}
	for _, test := range tests {
		var out bytes.Buffer
		err := runWithCredentials([]string{"mutation", test.mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("expected %q error for %s, got %v", test.want, test.mutation, err)
		}
	}
}

// TestDeconstructionAreaScansNeutralAndSkipsCharacters proves an area request
// selects neutral trees (which the player-force-only filter missed) and never
// turns the actor's character into a per-target engine error.
func TestDeconstructionAreaScansNeutralAndSkipsCharacters(t *testing.T) {
	stubs := `
local surface={name="nauvis",index=1}
local force={name="player"}
local neutral={name="neutral"}
game={tick=10,surfaces={nauvis=surface},forces={player=force,neutral=neutral}}
local tree_ordered=false
local tree={valid=true,type="tree",name="tree-01",position={x=1.5,y=1.5},
  to_be_deconstructed=function() return tree_ordered end,
  order_deconstruction=function(order_force) tree_ordered=true; return true end,
  cancel_deconstruction=function() end}
local character={valid=true,type="character",name="character",position={x=2.5,y=2.5},
  to_be_deconstructed=function() error("a character must never be queried") end}
local filter_seen
surface.find_entities_filtered=function(filter) filter_seen=filter; return {tree,character} end
`
	assertions := `
assert(filter_seen~=nil,"the area scan must run")
assert(#filter_seen.force==2,"the scan must cover the requested force and neutral, got "..#filter_seen.force)
assert(filter_seen.force[1]==game.forces.player and filter_seen.force[2]==game.forces.neutral)
assert(captured.orders==1,"only the tree is a target")
assert(captured.skipped==0,"a skipped character must not count against the limit")
assert(#captured.errors==0,"a character must not become an ENGINE error")
`
	options := deconstructionAreaOptions{
		Surface: "nauvis", Force: "player", Bounds: areaBounds{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10},
		Limit: 10,
	}
	runLuaFixture(t, deconstructionAreaLua(options), stubs, assertions)
}

// TestDeconstructionInstantSpillsLanesAndHands proves the destroy path empties
// belt lanes, an inserter's hand and inventories before destroying, and does not
// mark spilled items for deconstruction without logistic coverage.
func TestDeconstructionInstantSpillsLanesAndHands(t *testing.T) {
	stubs := `
defines={inventory={chest=1,fuel=2}}
local surface={name="nauvis",index=1}
local force={name="player"}
local neutral={name="neutral"}
game={tick=20,surfaces={nauvis=surface},forces={player=force,neutral=neutral}}
local recorded={spill={}}
local chest_inventory={is_empty=function() return false end,
  get_contents=function() return {{name="coal",count=4,quality="normal"}} end,
  clear=function() recorded.inventory_cleared=true end}
local line={get_contents=function() return {{name="iron-ore",count=2,quality="normal"}} end}
local held={valid_for_read=true,name="copper-plate",count=1,quality={name="normal"},clear=function() recorded.held_cleared=true end}
local inserter={valid=true,type="inserter",name="inserter",position={x=1.5,y=1.5},unit_number=5,
  get_inventory=function(kind) if kind==defines.inventory.chest then return chest_inventory end return nil end,
  get_max_transport_line_index=function() return 2 end,
  get_transport_line=function(index) return line end,
  held_stack=held,
  prototype={items_to_place_this={{name="inserter",count=1}}},
  destroy=function(params) recorded.destroyed=true end}
surface.find_entities_filtered=function(filter) return {inserter} end
surface.spill_item_stack=function(params) recorded.spill[#recorded.spill+1]=params end
surface.find_logistic_network_by_position=function(position,force) return nil end
`
	assertions := `
local spilled={}
for _,params in ipairs(recorded.spill) do
  assert(params.force==nil,"uncovered positions must not mark items for deconstruction")
  local key=params.stack.name
  spilled[key]=(spilled[key] or 0)+params.stack.count
end
assert(spilled["coal"]==4,"container contents must be spilled")
assert(spilled["iron-ore"]==4,"both belt lanes must be spilled")
assert(spilled["copper-plate"]==1,"the inserter hand must be spilled")
assert(spilled["inserter"]==1,"the placement item must be spilled")
assert(recorded.inventory_cleared and recorded.held_cleared,"spilled sources must be emptied")
assert(recorded.destroyed,"the entity must still be destroyed")
assert(captured.removed==1 and captured.skipped==0)
`
	options := deconstructionAreaOptions{
		Surface: "nauvis", Force: "player", Bounds: areaBounds{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10},
		Limit: 10, Instant: true,
	}
	runLuaFixture(t, deconstructionAreaLua(options), stubs, assertions)
}
