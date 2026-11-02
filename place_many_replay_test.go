package main

import (
	"strings"
	"testing"
)

func servePlaceManyReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "place_many_engine.lua", []string{"PLACE_MANY_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "create_entity")
	})
}

func TestPlaceManyReportsEngineAcceptedPlacements(t *testing.T) {
	r := servePlaceManyReplay(t, "snap")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "iron-chest",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 10, Y: 10}},
			{Position: &PositionInput{X: 12, Y: 10}},
			{Position: &PositionInput{X: 14, Y: 10}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 3 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Fatalf("engine accepted every placement but placeMany reported failures: %+v", result)
	}
	if len(result.Entities) != 3 {
		t.Fatalf("expected three placed entities, got %+v", result.Entities)
	}
	for i, entity := range result.Entities {
		want := 10.5 + float64(i)*2
		if entity.Position == nil || entity.Position.X != want || entity.Position.Y != 10.5 {
			t.Fatalf("entity %d position = %+v, want x=%v y=10.5", i, entity.Position, want)
		}
	}
}

func TestPlaceManyReportsGenuineCollision(t *testing.T) {
	r := servePlaceManyReplay(t, "blocked")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "iron-chest",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("expected one blocked placement, got %+v", result)
	}
	entry := result.Errors[0]
	if entry.Code != BatchErrorCodeCollision || entry.Index != 1 {
		t.Fatalf("expected a COLLISION error at index 1, got %+v", entry)
	}
}

// A plain placeMany over an occupied tile must report COLLISION before
// create_entity runs instead of stacking a second entity on the tile
// (factorio-ai-player-foi: burner inserter buried under a new electric one).
func TestPlaceManyRefusesOccupiedTileWithoutFastReplace(t *testing.T) {
	r := servePlaceManyReplay(t, "stack")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "inserter",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("expected one refused placement, got %+v", result)
	}
	entry := result.Errors[0]
	if entry.Code != BatchErrorCodeCollision || !strings.Contains(entry.Message, "burner-inserter") {
		t.Fatalf("expected an occupied-tile COLLISION, got %+v", entry)
	}
}

func TestPlaceManyReportsEngineRefusal(t *testing.T) {
	r := servePlaceManyReplay(t, "engine")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "iron-chest",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 10.5, Y: 10.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("expected one engine failure, got %+v", result)
	}
	if entry := result.Errors[0]; entry.Code != BatchErrorCodeEngine {
		t.Fatalf("expected ENGINE error, got %+v", entry)
	}
}

// Every placement row must carry its input index, direction and underground
// type so the caller can map results back without re-querying the surface.
func TestPlaceManyReportsIndexDirectionAndUndergroundType(t *testing.T) {
	r := servePlaceManyReplay(t, "snap")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "underground-belt",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 10, Y: 10}, Direction: 4},
			{Position: &PositionInput{X: 12, Y: 10}, Direction: 4, UndergroundType: new(UndergroundBeltTypeInput)},
			{Position: &PositionInput{X: 14, Y: 10}, Direction: 4, UndergroundType: new(UndergroundBeltTypeOutput)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) != 3 {
		t.Fatalf("expected three placed entities, got %+v", result.Entities)
	}
	for i, entity := range result.Entities {
		if entity.Index == nil || *entity.Index != i {
			t.Fatalf("entity %d index = %v, want %d", i, entity.Index, i)
		}
		if entity.Direction == nil || *entity.Direction != 4 {
			t.Fatalf("entity %d direction = %v, want 4", i, entity.Direction)
		}
		want := UndergroundBeltTypeInput
		if i == 2 {
			want = UndergroundBeltTypeOutput
		}
		if entity.UndergroundType == nil || *entity.UndergroundType != want {
			t.Fatalf("entity %d undergroundType = %v, want %v", i, entity.UndergroundType, want)
		}
	}
}

// placeMany must run the same fast-replace proof as place, so a fast replace
// cannot stack a second entity on an unreplaceable tile.
func TestPlaceManyFastReplaceRefusesUnreplaceableTile(t *testing.T) {
	r := servePlaceManyReplay(t, "unreplaceable")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "transport-belt",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}, FastReplace: new(true)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("expected one refused fast replace, got %+v", result)
	}
	if entry := result.Errors[0]; entry.Code != BatchErrorCodeCollision {
		t.Fatalf("expected COLLISION, got %+v", entry)
	}
}

// A fast replace that the engine accepts returns the replaced entities to the
// actor instead of spilling them.
func TestPlaceManyFastReplaceReturnsReplacedEntities(t *testing.T) {
	r := servePlaceManyReplay(t, "replace")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "transport-belt",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}, FastReplace: new(true)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 1 || result.Failed != 0 {
		t.Fatalf("expected a replaceable belt to be replaced, got %+v", result)
	}
}

// The engine hands back underground neighbours as a plain table, an array-like
// LuaCustomTable or a userdata handle depending on the half; none of those may
// crash the placement.
func TestPlaceManyUndergroundNeighbourShapes(t *testing.T) {
	for _, scenario := range []string{"underground_array", "underground_userdata"} {
		t.Run(scenario, func(t *testing.T) {
			r := servePlaceManyReplay(t, scenario)
			result, err := placeMany(r, PlaceManyInput{
				Actor: &ActorInput{Character: new("4")},
				Item:  "underground-belt",
				Placements: []*PlacementInput{
					{Position: &PositionInput{X: 10, Y: 10}, Direction: 4, UndergroundType: new(UndergroundBeltTypeInput)},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Placed != 1 || result.Failed != 0 || len(result.Entities) != 1 {
				t.Fatalf("expected one placed half, got %+v", result)
			}
			entity := result.Entities[0]
			if entity.UndergroundType == nil || *entity.UndergroundType != UndergroundBeltTypeInput {
				t.Fatalf("undergroundType = %v, want INPUT", entity.UndergroundType)
			}
		})
	}
}

// One committed-then-raising position must count as placed, not as an ENGINE
// failure with the item refunded (foi).
func TestPlaceManyReportsCommittedCreationAfterEngineError(t *testing.T) {
	r := servePlaceManyReplay(t, "commit_then_raise")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "iron-chest",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 10.5, Y: 10.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 1 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Fatalf("expected the committed placement to count, got %+v", result)
	}
	if len(result.Entities) != 1 || result.Entities[0].UnitNumber == nil || *result.Entities[0].UnitNumber != "10010" {
		t.Fatalf("expected the committed entity row, got %+v", result.Entities)
	}
}

// A resource tile is not an occupied tile: placeMany must build belts over ore
// and agree with probePlacement (factorio-ai-player-y18).
func TestPlaceManyAcceptsResourceTile(t *testing.T) {
	r := servePlaceManyReplay(t, "resource")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "transport-belt",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 1 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Fatalf("a belt over an ore patch must be placed, got %+v", result)
	}
}

// A tile that holds only our own planned entity-ghost plus a tree corpse must be
// placed: ghosts are not obstacles and corpses only collide with tiles, so the
// engine builds over both. The old occupancy rule counted the corpse as an
// occupant and refused the tile, naming a decorative entity the caller could not
// find again (factorio-ai-player-sth).
func TestPlaceManyPlacesOverGhostTileWithCorpse(t *testing.T) {
	r := servePlaceManyReplay(t, "ghost_corpse")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "pipe",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 1 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Fatalf("a ghost plus a corpse must not block a placement, got %+v", result)
	}
}

// An entity returned by a search that ignores the requested position must never
// become the reported occupant: the message names only entities whose collision
// box overlaps the tile being placed on (factorio-ai-player-sth).
func TestPlaceManyIgnoresOccupantFromAnotherTile(t *testing.T) {
	r := servePlaceManyReplay(t, "stray_corpse")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "pipe",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 1 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Fatalf("an entity from another tile must not be an occupant, got %+v", result)
	}
}

// A tile genuinely occupied by a belt is a COLLISION, and the message names that
// belt with the requested position, not something else on the surface.
func TestPlaceManyCollisionNamesTileOccupant(t *testing.T) {
	r := servePlaceManyReplay(t, "belt_occupied")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "pipe",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("expected one blocked placement, got %+v", result)
	}
	entry := result.Errors[0]
	if entry.Code != BatchErrorCodeCollision || !strings.Contains(entry.Message, "transport-belt") {
		t.Fatalf("expected a COLLISION naming the belt, got %+v", entry)
	}
	if strings.Contains(entry.Message, "tree-09-stump") {
		t.Fatalf("collision named an entity that is not on the tile: %q", entry.Message)
	}
	if entry.Target == nil || entry.Target.Position == nil || entry.Target.Position.X != 9.5 || entry.Target.Position.Y != 9.5 {
		t.Fatalf("collision reported the wrong target position: %+v", entry.Target)
	}
}

// Occupancy is decided per tile, not per requested point: a machine in the next
// tile whose collision box clips this tile blocks the build even though the
// tile centre is outside its box.
func TestPlaceManyRefusesTileClippedByNeighbourFootprint(t *testing.T) {
	r := servePlaceManyReplay(t, "neighbour")
	result, err := placeMany(r, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "pipe",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 9.5, Y: 9.5}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("expected the clipped tile to be refused, got %+v", result)
	}
	entry := result.Errors[0]
	if entry.Code != BatchErrorCodeCollision || !strings.Contains(entry.Message, "assembling-machine-2") {
		t.Fatalf("expected a COLLISION naming the clipping machine, got %+v", entry)
	}
}
