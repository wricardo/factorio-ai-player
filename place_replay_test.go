package main

import (
	"strings"
	"testing"
)

func servePlaceReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "place_engine.lua", []string{"PLACE_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "create_entity")
	})
}

func placeIronChest(t *testing.T, scenario string, x, y float64) (*ActionResult, error) {
	t.Helper()
	return command[ActionResult](servePlaceReplay(t, scenario), "place", PlaceInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "iron-chest",
		Position: &PositionInput{X: x, Y: y},
	})
}

func TestPlaceReportsEngineAcceptedPlacement(t *testing.T) {
	result, err := placeIronChest(t, "snap", 10, 10)
	if err != nil {
		t.Fatalf("engine accepted the placement but place failed: %v", err)
	}
	if result.Name == nil || *result.Name != "iron-chest" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Position == nil || result.Position.X != 10.5 || result.Position.Y != 10.5 {
		t.Fatalf("position = %+v, want the engine-snapped 10.5,10.5", result.Position)
	}
}

func TestPlaceReportsGenuineCollision(t *testing.T) {
	_, err := placeIronChest(t, "blocked", 9.5, 9.5)
	if err == nil || !strings.Contains(err.Error(), "cannot place entity at position") {
		t.Fatalf("expected a collision refusal, got %v", err)
	}
}

func TestPlaceReportsEngineRefusal(t *testing.T) {
	_, err := placeIronChest(t, "engine", 10.5, 10.5)
	if err == nil || !strings.Contains(err.Error(), "entity creation failed") {
		t.Fatalf("expected an engine failure, got %v", err)
	}
}

// A plain place over an occupied tile must refuse before create_entity runs.
// The live engine ignores collision boxes and used to stack an electric
// inserter on top of a burner inserter, leaving the burner buried and stuck at
// no_fuel forever (factorio-ai-player-foi).
func TestPlaceRefusesOccupiedTileWithoutFastReplace(t *testing.T) {
	_, err := command[ActionResult](servePlaceReplay(t, "stack"), "place", PlaceInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "inserter",
		Position: &PositionInput{X: 9.5, Y: 9.5},
	})
	if err == nil || !strings.Contains(err.Error(), "tile is occupied by burner-inserter") {
		t.Fatalf("expected the occupied tile to be refused, got %v", err)
	}
}

func placeFastReplaceChest(t *testing.T, scenario string, x, y float64) (*ActionResult, error) {
	t.Helper()
	return command[ActionResult](servePlaceReplay(t, scenario), "place", PlaceInput{
		Actor:       &ActorInput{Character: new("4")},
		Item:        "iron-chest",
		Position:    &PositionInput{X: x, Y: y},
		FastReplace: new(true),
	})
}

// A fast replace must be proven replaceable before create_entity runs, and it
// must hand the replaced entity and its contents back to the actor instead of
// spilling them on the ground.
func TestPlaceFastReplaceReturnsReplacedEntity(t *testing.T) {
	result, err := placeFastReplaceChest(t, "replace", 9.5, 9.5)
	if err != nil {
		t.Fatalf("fast replace on a replaceable chest failed: %v", err)
	}
	if result.Name == nil || *result.Name != "iron-chest" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// create_entity ignores collision boxes, so a fast replace on a tile that holds
// something unreplaceable must be refused instead of stacking a second entity.
func TestPlaceFastReplaceRefusesUnreplaceableTile(t *testing.T) {
	_, err := placeFastReplaceChest(t, "unreplaceable", 9.5, 9.5)
	if err == nil || !strings.Contains(err.Error(), "cannot fast-replace at position") {
		t.Fatalf("expected a fast-replace refusal, got %v", err)
	}
}

// A belt is replaceable even though can_place_entity rejects the tile, so the
// check must accept either answer.
func TestPlaceFastReplaceAcceptsReplaceableBelt(t *testing.T) {
	if _, err := placeFastReplaceChest(t, "replace_belt", 9.5, 9.5); err != nil {
		t.Fatalf("a replaceable belt must be fast replaceable: %v", err)
	}
}

// A fast replace by a player must also pass the player, which is what makes the
// engine return the replaced contents to that player.
func TestPlaceFastReplacePassesPlayer(t *testing.T) {
	result, err := command[ActionResult](servePlaceReplay(t, "replace_belt"), "place", PlaceInput{
		Actor:       &ActorInput{Player: new("wricardo")},
		Item:        "iron-chest",
		Position:    &PositionInput{X: 9.5, Y: 9.5},
		FastReplace: new(true),
	})
	if err != nil {
		t.Fatalf("player fast replace failed: %v", err)
	}
	// The fixture asserts inside create_entity that the player was passed; here
	// the engine-visible placement result is what matters.
	if result.Name == nil || *result.Name != "iron-chest" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Position == nil || result.Position.X != 9.5 || result.Position.Y != 9.5 {
		t.Fatalf("position = %+v, want 9.5,9.5", result.Position)
	}
}

// create_entity can commit the entity and still raise. Reporting failure there
// refunded the item and duplicated it, so the mutation must detect the entity
// that appeared on the tile and report the placement instead (foi).
func TestPlaceReportsCommittedCreationAfterEngineError(t *testing.T) {
	result, err := placeIronChest(t, "commit_then_raise", 10.5, 10.5)
	if err != nil {
		t.Fatalf("the engine committed the entity but place reported failure: %v", err)
	}
	if result.Name == nil || *result.Name != "iron-chest" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.UnitNumber == nil || *result.UnitNumber != "10010" {
		t.Fatalf("unit number = %v, want the committed 10010", result.UnitNumber)
	}
}

// A resource tile is not an occupied tile: belts build over ore, and
// probePlacement already reports those tiles placeable. Counting the resource
// as an occupant made the whole ore patch unbuildable (factorio-ai-player-y18).
func TestPlaceAcceptsResourceTile(t *testing.T) {
	result, err := command[ActionResult](servePlaceReplay(t, "resource"), "place", PlaceInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "transport-belt",
		Position: &PositionInput{X: 9.5, Y: 9.5},
	})
	if err != nil {
		t.Fatalf("a belt over an ore patch must be placeable: %v", err)
	}
	if result.Name == nil || *result.Name != "transport-belt" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// A tree stump is a corpse that collides with tiles only, so the engine builds
// over it. place and placeMany share the same occupancy rule and both must
// accept the tile (factorio-ai-player-sth).
func TestPlaceAcceptsCorpseTile(t *testing.T) {
	result, err := command[ActionResult](servePlaceReplay(t, "corpse"), "place", PlaceInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "transport-belt",
		Position: &PositionInput{X: 9.5, Y: 9.5},
	})
	if err != nil {
		t.Fatalf("a belt over a tree stump corpse must be placeable: %v", err)
	}
	if result.Name == nil || *result.Name != "transport-belt" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

// An inserter's direction is the side it faces, which on the live engine is its
// pickup side: direction 0 reports pickup one tile north and drop one tile south,
// direction 8 reports the reverse (engine-verified on 2.0.77 for units 11740 and
// 10489). A machine north of the inserter and a chest south of it therefore
// drains plant-to-chest with direction 0 and chest-to-plant with direction 8, so
// place must hand the requested code to create_entity unchanged and report the
// engine's stored direction plus the tick that proves the write happened
// (factorio-ai-player-tfi).
func TestPlacePassesInserterDirectionThrough(t *testing.T) {
	result, err := command[ActionResult](servePlaceReplay(t, "direction"), "place", PlaceInput{
		Actor:     &ActorInput{Character: new("4")},
		Item:      "inserter",
		Position:  &PositionInput{X: 10.5, Y: 10.5},
		Direction: 8,
	})
	if err != nil {
		t.Fatalf("placing an inserter with direction 8 failed: %v", err)
	}
	if result.Direction == nil || *result.Direction != 8 {
		t.Fatalf("direction = %v, want the engine direction 8 that create_entity received", intValue(result.Direction))
	}
	if result.Tick == 0 {
		t.Fatalf("tick = 0, want the tick the entity was created in: %+v", result)
	}
}

// A 3x3 prototype whose centre tile is free can still cover a neighbour's tile.
// create_entity ignores collision boxes and the pre-check only tests the centre
// tile, so the post-create overlap check must refuse the placement and leave no
// buried entity (factorio-ai-player-2im).
func TestPlaceRejectsFootprintOverlap(t *testing.T) {
	_, err := command[ActionResult](servePlaceReplay(t, "footprint"), "place", PlaceInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "assembling-machine-1",
		Position: &PositionInput{X: 10.5, Y: 10.5},
	})
	if err == nil || !strings.Contains(err.Error(), "footprint overlaps inserter") {
		t.Fatalf("expected a footprint overlap refusal, got %v", err)
	}
}
