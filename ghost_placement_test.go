package main

import (
	"strings"
	"testing"
)

// countInCommands counts marker occurrences across every RCON command the
// scripted server received, unlike scriptedRCON.seen which counts commands.
func countInCommands(recorder *scriptedRCON, marker string) int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	occurrences := 0
	for _, command := range recorder.commands {
		occurrences += strings.Count(command, marker)
	}
	return occurrences
}

func ghostEntity(name string, x, y float64) *GhostEntityInput {
	return &GhostEntityInput{Name: name, Position: &PositionInput{X: x, Y: y}}
}

// PlaceGhosts validates the whole request before touching the game, so a bad
// entry cannot leave a half-planned build behind.
func TestPlaceGhostsValidation(t *testing.T) {
	tooManyEntities := make([]*GhostEntityInput, ghostMaxEntities+1)
	for i := range tooManyEntities {
		tooManyEntities[i] = ghostEntity("transport-belt", float64(i), 0)
	}
	tooManyTiles := make([]*GhostTileInput, ghostMaxTiles+1)
	for i := range tooManyTiles {
		tooManyTiles[i] = &GhostTileInput{Name: "stone-path", X: i, Y: 0}
	}
	recipeQuality := ghostEntity("assembling-machine-1", 0, 0)
	recipeQuality.RecipeQuality = new("uncommon")
	tests := []struct {
		name  string
		input PlaceGhostsInput
		want  string
	}{
		{"empty", PlaceGhostsInput{}, "at least one entity or tile is required"},
		{"entities_over_limit", PlaceGhostsInput{Entities: tooManyEntities}, "at most 10000 entities are allowed"},
		{"tiles_over_limit", PlaceGhostsInput{Tiles: tooManyTiles}, "at most 20000 tiles are allowed"},
		{"nil_entity", PlaceGhostsInput{Entities: []*GhostEntityInput{nil}}, "entity 1 is missing"},
		{"empty_name", PlaceGhostsInput{Entities: []*GhostEntityInput{{Position: &PositionInput{}}}}, "entity 1 name must not be empty"},
		{"no_position", PlaceGhostsInput{Entities: []*GhostEntityInput{{Name: "transport-belt"}}}, "entity 1 position is required"},
		{"bad_direction", PlaceGhostsInput{Entities: []*GhostEntityInput{{Name: "inserter", Position: &PositionInput{}, Direction: 16}}}, "entity 1 direction must be between 0 and 15"},
		{"negative_direction", PlaceGhostsInput{Entities: []*GhostEntityInput{{Name: "inserter", Position: &PositionInput{}, Direction: -1}}}, "entity 1 direction must be between 0 and 15"},
		{"recipe_quality_without_recipe", PlaceGhostsInput{Entities: []*GhostEntityInput{recipeQuality}}, "entity 1 recipeQuality requires recipe"},
		{"nil_tile", PlaceGhostsInput{Tiles: []*GhostTileInput{nil}}, "tile 1 is missing"},
		{"empty_tile_name", PlaceGhostsInput{Tiles: []*GhostTileInput{{X: 1, Y: 2}}}, "tile 1 name must not be empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := placeGhosts(&Resolver{}, test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

// The plan must reach the engine intact: names, positions, direction, quality
// and recipe travel in the Lua payload, and the engine's answer is decoded.
func TestPlaceGhostsSendsPlanAndDecodesResult(t *testing.T) {
	r, recorder := serveScriptedRCON(t, []scriptRule{{
		marker: "name=\"entity-ghost\"",
		response: `{"tick":42,"surface":"nauvis","force":"player","entities_created":1,"tiles_created":1,"failed":1,` +
			`"errors":[{"index":3,"code":"LOOKUP","message":"tile prototype not found","target":{"surface":"nauvis","entity":"nope","position":{"x":9,"y":9}}}],` +
			`"ghosts":[{"name":"entity-ghost","type":"entity-ghost","inner_name":"assembling-machine-1","quality":"uncommon","position":{"x":100.5,"y":-20.5},"unit_number":"17"},` +
			`{"name":"tile-ghost","type":"tile-ghost","inner_name":"stone-path","position":{"x":12,"y":13}}]}`,
	}})
	direction := 4
	result, err := placeGhosts(r, PlaceGhostsInput{
		Entities: []*GhostEntityInput{{
			Name: "assembling-machine-1", Position: &PositionInput{X: 100.5, Y: -20.5}, Direction: direction,
			Quality: new("uncommon"), Recipe: new("iron-gear-wheel"),
		}},
		Tiles: []*GhostTileInput{{Name: "stone-path", X: 12, Y: 13}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EntitiesCreated != 1 || result.TilesCreated != 1 || result.Failed != 1 {
		t.Fatalf("counts = %+v", result)
	}
	if len(result.Ghosts) != 2 || len(result.Errors) != 1 {
		t.Fatalf("ghosts=%v errors=%v", result.Ghosts, result.Errors)
	}
	ghost := result.Ghosts[0]
	if ghost.InnerName != "assembling-machine-1" || ghost.Quality == nil || *ghost.Quality != "uncommon" {
		t.Fatalf("ghost = %+v", ghost)
	}
	if ghost.UnitNumber == nil || *ghost.UnitNumber != "17" {
		t.Fatalf("unit number = %v", ghost.UnitNumber)
	}
	if result.Ghosts[1].InnerName != "stone-path" || result.Ghosts[1].UnitNumber != nil {
		t.Fatalf("tile ghost = %+v", result.Ghosts[1])
	}
	if result.Errors[0].Code != BatchErrorCodeLookup || result.Errors[0].Index != 3 {
		t.Fatalf("error = %+v", result.Errors[0])
	}
	for _, want := range []struct {
		label string
		count int
	}{
		{"direction=4", 1},
		{"recipe=", 1},
		{luaString("assembling-machine-1"), 1},
		{luaString("stone-path"), 1},
		{luaString("uncommon"), 1},
		{luaString("iron-gear-wheel"), 1},
	} {
		if got := countInCommands(recorder, want.label); got != want.count {
			t.Fatalf("engine request contains %q %d times, want %d", want.label, got, want.count)
		}
	}
}

func TestPlaceGhostsEngineErrorPropagates(t *testing.T) {
	r, _ := serveScriptedRCON(t, []scriptRule{{
		marker:   "name=\"entity-ghost\"",
		response: `{"error":"surface not found"}`,
	}})
	_, err := placeGhosts(r, PlaceGhostsInput{Entities: []*GhostEntityInput{ghostEntity("transport-belt", 1.5, 2.5)}})
	if err == nil || !strings.Contains(err.Error(), "surface not found") {
		t.Fatalf("error = %v, want the engine message", err)
	}
}
