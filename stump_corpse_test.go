package main

import (
	"bytes"
	"strings"
	"testing"
)

// Factorio 2.0 leaves a tree stump corpse (type corpse, mineable=false,
// collision box 0.8x0.8) when a tree is mined. harvestArea must destroy that
// stump in the same call, because mine and remove cannot clear it afterwards
// and it keeps blocking placement.
func TestHarvestAreaLuaDestroysTreeStumps(t *testing.T) {
	lua, err := harvestAreaLua([]string{"--character=7", "--left-top-x=0", "--left-top-y=0", "--right-bottom-x=8", "--right-bottom-y=8"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`local function clear_tree_stump(surface,tree_box)`,
		`surface.find_entities_filtered{area={{tree_box.left_top.x,tree_box.left_top.y},{tree_box.right_bottom.x,tree_box.right_bottom.y}},type="corpse"}`,
		`corpse.destroy{raise_destroy=true}`,
		`local tree_box=(kind=="tree") and e.bounding_box or nil`,
		`local cleared,left=clear_tree_stump(surface,tree_box)`,
		`stumps_removed=stumps_removed+cleared`,
		`stumps_left=stumps_left+left`,
		`stumps_removed=stumps_removed,stumps_left=stumps_left`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("harvest lua missing %q:\n%s", want, lua)
		}
	}
}

// The shipped harvest Lua must mine the trees, destroy the stump each mine
// leaves, and report a stump whose destroy the engine refused instead of
// claiming a clean corridor.
func TestHarvestAreaClearsAndReportsStumps(t *testing.T) {
	r := serveLuaFixture(t, "harvest_stump_engine.lua", nil, func(command string) bool {
		return strings.Contains(command, "mine_entity")
	})
	result, err := harvestArea(r, HarvestAreaInput{
		Actor: &ActorInput{Character: new("7")},
		Area:  &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TreesMined != 2 || result.RocksMined != 0 {
		t.Fatalf("mined counts: %+v", result)
	}
	if result.StumpsRemoved != 1 || result.StumpsLeft != 1 {
		t.Fatalf("stump counts: %+v", result)
	}
}

// remove is the single-entity path that reports the unclearable stump today:
// it must destroy a corpse target instead of calling mine_entity, and it must
// clear the stump a mined tree leaves behind.
func TestRemoveLuaDestroysCorpseTargets(t *testing.T) {
	lua, err := removeLua([]string{"--character=7", "--entity=tree-08-stump", "--x=-123.5625", "--y=226.6875"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if kind=="corpse" then`,
		`removed=e.destroy{raise_destroy=true}`,
		`if kind=="tree" then clear_tree_stump(s,tree_box) end`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("remove lua missing %q:\n%s", want, lua)
		}
	}
}

// removeMany must accept a corpse target and destroy it, so a batch can clear
// the stumps an earlier harvest left behind.
func TestRemoveManyLuaDestroysCorpseTargets(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","removed":1,"failed":0,"errors":{},"results":[{"index":1,"name":"tree-08-stump","type":"corpse","position":{"x":1.5,"y":2.5},"items":{}}],"items":{}}`)
	result, err := removeMany(r, RemoveManyInput{
		Actor:   &ActorInput{Character: new("7")},
		Targets: []*EntityRefInput{{Entity: new("tree-08-stump"), Position: &PositionInput{X: 1.5, Y: 2.5}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, `if kind=="corpse" then`) || !strings.Contains(lua, `mined=e.destroy{raise_destroy=true}`) {
		t.Fatalf("removeMany lua missing corpse handling:\n%s", lua)
	}
	if result.Removed != 1 || result.Failed != 0 {
		t.Fatalf("result: %+v", result)
	}
}

// A stump corpse is a doodad that collides with tiles only, so it never blocks a
// belt or pipe: probePlacement must leave its tile placeable, exactly like place.
// A real collidable entity on another tile must still block and be named.
func TestPlacementProbeIgnoresCorpseButReportsRealOccupant(t *testing.T) {
	r := serveLuaFixture(t, "placement_probe_corpse_engine.lua", nil, func(command string) bool {
		return strings.Contains(command, "tile_occupants(surface,position,layers)")
	})
	result, err := placementProbeQuery(r, &PlacementProbeInput{
		Surface:   "nauvis",
		Force:     "player",
		Prototype: "pipe",
		Area:      &FactoryAreaInput{RightBottomX: 4, RightBottomY: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Sampled != 16 || result.Placeable != 15 {
		t.Fatalf("a corpse tile must be placeable, only the belt tile is blocked: %+v", result)
	}
	if len(result.BlockedTiles) != 1 {
		t.Fatalf("blocked tiles: %+v", result.BlockedTiles)
	}
	tile := result.BlockedTiles[0]
	if tile.Position == nil || tile.Position.X != 3.5 || tile.Position.Y != 3.5 {
		t.Fatalf("blocked tile position = %+v, want 3.5,3.5", tile.Position)
	}
	found := false
	for _, name := range tile.Blockers {
		if name == "tree-08-stump" {
			t.Fatalf("a corpse must never be a blocker: %+v", tile)
		}
		if name == "transport-belt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers %q missing the belt: %+v", "transport-belt", tile)
	}
}
