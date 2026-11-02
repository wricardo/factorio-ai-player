package main

import (
	"io"
	"strings"
	"testing"
)

// serveDropReplay runs the shipped dropItem Lua against testdata/drop_engine.lua.
func serveDropReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "drop_engine.lua", []string{"DROP_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "get_item_insert_specification")
	})
}

func dropFive(t *testing.T, scenario string, target DropTarget) (*DropItemResult, error) {
	t.Helper()
	return dropItem(serveDropReplay(t, scenario), DropItemInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "iron-plate",
		Count:    5,
		Position: &PositionInput{X: 1.5, Y: 2.5},
		Target:   target,
	})
}

// A belt line holds one belt stack per insert_at call, so a drop of five items
// must report the one item the line accepted and keep the other four in the
// inventory instead of claiming five moved.
func TestDropItemBeltReportsAcceptedCount(t *testing.T) {
	result, err := dropFive(t, "belt", DropTargetBelt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 1 || result.Remaining != 4 || result.Requested != 5 {
		t.Fatalf("belt drop must report one accepted item and four remaining, got %+v", result)
	}
	if len(result.GroundPositions) != 0 {
		t.Fatalf("a belt drop creates no ground items, got %+v", result.GroundPositions)
	}
	if result.Line != 1 {
		t.Fatalf("line = %d, want 1", result.Line)
	}
}

// A belt that rejects every insert must fail without touching the inventory.
func TestDropItemBeltRefusalIsHardFailure(t *testing.T) {
	_, err := dropFive(t, "belt_full", DropTargetBelt)
	if err == nil || !strings.Contains(err.Error(), "belt cannot accept items here") {
		t.Fatalf("expected a belt refusal, got %v", err)
	}
}

// A ground drop spills the whole stack as a single item-on-ground entity
// without a force (which would mark it for deconstruction) and reports where
// the created stack is.
func TestDropItemGroundSpillsWholeStack(t *testing.T) {
	result, err := dropFive(t, "ground", DropTargetGround)
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 5 || result.Remaining != 0 {
		t.Fatalf("a ground drop must move the whole stack, got %+v", result)
	}
	if len(result.GroundPositions) != 1 || result.GroundPositions[0].X != 1.5 || result.GroundPositions[0].Y != 2.5 {
		t.Fatalf("expected one created ground stack at the drop position, got %+v", result.GroundPositions)
	}
}

// Every transport belt tier has the same entity type, so beltTake must resolve
// the prototype the caller named instead of hard-coding transport-belt.
func TestBeltTakeResolvesEveryBeltTier(t *testing.T) {
	for _, name := range []string{"transport-belt", "fast-transport-belt", "express-transport-belt", "turbo-transport-belt"} {
		lua, err := beltTakeLua([]string{"--character=4", "--entity=" + name, "--x=1", "--y=2", "--item=iron-plate"}, io.Discard)
		if err != nil {
			t.Fatalf("%s must be accepted: %v", name, err)
		}
		if !strings.Contains(lua, "local target_name="+luaString(name)) {
			t.Fatalf("%s must be resolved by its own prototype name", name)
		}
		if !strings.Contains(lua, `if e.type~="transport-belt"`) {
			t.Fatalf("%s must still pass the transport belt type check", name)
		}
	}
	// A unit-number-only target has no prototype name to resolve with.
	lua, err := beltTakeLua([]string{"--character=4", "--unitNumber=77", "--item=iron-plate"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lua, `local target_name=""`) || !strings.Contains(lua, "local target_unit=77") {
		t.Fatalf("a unit-number belt target must resolve through the unit-number index:\n%s", lua)
	}
}

// Resources, trees and rocks carry no unit number, so mine rejects one in Go
// instead of scanning the whole surface for a match that can never exist.
func TestMineRejectsUnitNumber(t *testing.T) {
	_, err := resourceLua("mine", []string{"--character=4", "--unitNumber=9"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no unit numbers") {
		t.Fatalf("expected a unit-number rejection, got %v", err)
	}
	if _, err := resourceLua("mine", []string{"--character=4"}, io.Discard); err == nil {
		t.Fatal("mine without a target must fail")
	}
	lua, err := resourceLua("mine", []string{"--character=4", "--entity=iron-ore", "--x=1", "--y=2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// The shared resolver is included for coordinate lookups, but mine must
	// never address by unit number: the target unit stays the zero placeholder
	// that selects the coordinate branch.
	if !strings.Contains(lua, "local target_unit=0") || strings.Contains(lua, "local target_unit=9") {
		t.Fatalf("mine must address its target only by name plus position:\n%s", lua)
	}
	// An omitted surface falls back to the actor's surface, as before the shared
	// resolver replaced mine's own lookup.
	if !strings.Contains(lua, `if surface_name=="" then surface_name=actor.surface.name end`) {
		t.Fatalf("mine must default an empty surface to the actor's surface:\n%s", lua)
	}
}
