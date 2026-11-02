package main

import (
	"strings"
	"testing"
)

func servePlaceNextToReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "place_next_to_engine.lua", []string{"PLACE_NEXT_TO_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "create_entity")
	})
}

func placeTurretNextTo(t *testing.T, scenario string) (*PlaceNextToResult, error) {
	t.Helper()
	return placeNextTo(servePlaceNextToReplay(t, scenario), PlaceNextToInput{
		Actor:   &ActorInput{Character: new("4")},
		Surface: "nauvis",
		Item:    "gun-turret",
		RelativeTo: &EntityRefInput{
			Entity:     new("stone-furnace"),
			UnitNumber: new("42"),
		},
		Side: RelativeSideEast,
		Gap:  1,
	})
}

func TestPlaceNextToReportsEngineAcceptedPlacement(t *testing.T) {
	result, err := placeTurretNextTo(t, "snap")
	if err != nil {
		t.Fatalf("engine accepted the placement but placeNextTo failed: %v", err)
	}
	if result.Name != "gun-turret" || *result.UnitNumber != "700" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Position == nil || result.Position.X != 3.5 || result.Position.Y != 0.5 {
		t.Fatalf("position = %+v, want the engine-snapped 3.5,0.5", result.Position)
	}
}

func TestPlaceNextToReportsEngineRefusal(t *testing.T) {
	_, err := placeTurretNextTo(t, "blocked")
	if err == nil || !strings.Contains(err.Error(), "entity creation failed") {
		t.Fatalf("expected an engine refusal, got %v", err)
	}
}

func TestPlaceNextToReportsEngineError(t *testing.T) {
	_, err := placeTurretNextTo(t, "engine")
	if err == nil || !strings.Contains(err.Error(), "entity creation failed") || !strings.Contains(err.Error(), "scripted engine failure") {
		t.Fatalf("expected the scripted engine error, got %v", err)
	}
}

// placeNextTo must skip a candidate tile that already holds an entity: the live
// engine ignores collision boxes and would stack the new entity on top (foi).
func TestPlaceNextToSkipsOccupiedTile(t *testing.T) {
	result, err := placeTurretNextTo(t, "occupied")
	if err != nil {
		t.Fatalf("an occupied candidate must be skipped, not fail the placement: %v", err)
	}
	// The fixture raises if create_entity is called on the occupied tile, so a
	// successful placement proves the candidate was skipped. The fallback offset
	// snaps back to the same centre, so position is not the evidence here.
	if result.Name != "gun-turret" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// The engine can commit the entity and still raise; placeNextTo must report the
// placement instead of refunding the item.
func TestPlaceNextToReportsCommittedCreationAfterEngineError(t *testing.T) {
	result, err := placeTurretNextTo(t, "commit_then_raise")
	if err != nil {
		t.Fatalf("the engine committed the entity but placeNextTo reported failure: %v", err)
	}
	if result.Name != "gun-turret" || result.UnitNumber == nil || *result.UnitNumber != "700" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
