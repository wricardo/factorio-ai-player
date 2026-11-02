package main

import (
	"bytes"
	"strings"
	"testing"
)

// serveRemoveSingleReplay runs the shipped single-target remove Lua against a
// stubbed Factorio runtime whose surface raises on unknown keys, exactly like
// the real LuaSurface. A chunk that glues `s.name` into the following statement
// (`s.namelocal`) therefore fails here instead of silently reading nil.
func serveRemoveSingleReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "remove_single_engine.lua", []string{"REMOVE_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "mine_entity")
	})
}

func removeBeltInput() RemoveInput {
	return RemoveInput{
		Actor:  &ActorInput{Character: new("10599")},
		Target: &EntityRefInput{Entity: new("transport-belt"), UnitNumber: new("10610")},
	}
}

// remove addressed by unit number must mine the entity, report its surface and
// return the recovered items; the fixture asserts the belt left the world.
func TestRemoveSingleRemovesEntityByUnitNumber(t *testing.T) {
	result, err := command[RemoveResult](serveRemoveSingleReplay(t, "ok"), "remove", removeBeltInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Entity != "transport-belt" {
		t.Fatalf("expected the belt entity, got %+v", result.Entity)
	}
	if result.Surface != "nauvis" {
		t.Fatalf("expected the resolved surface name, got %q", result.Surface)
	}
	if result.Position == nil || result.Position.X != -31.5 || result.Position.Y != 67.5 {
		t.Fatalf("expected the belt position, got %+v", result.Position)
	}
	counts := map[string]float64{}
	for _, item := range result.Items {
		counts[item.Name] = item.Count
	}
	if counts["transport-belt"] != 1 || counts["iron-plate"] != 2 {
		t.Fatalf("expected the belt and its contents returned, got %+v", result.Items)
	}
}

// A mine_entity that reports success without changing the world must still fail
// the removal, exactly like removeMany does per target.
func TestRemoveSingleFailsWhenEntitySurvives(t *testing.T) {
	_, err := command[RemoveResult](serveRemoveSingleReplay(t, "noop"), "remove", removeBeltInput())
	if err == nil || !strings.Contains(err.Error(), "still present after removal") {
		t.Fatalf("expected an honest failure for a surviving entity, got %v", err)
	}
}

// The generated chunk must not glue the surface name into the next statement:
// `local surface=s.name` immediately followed by `local function ...` used to
// emit `s.namelocal`, which the real engine rejects with
// "LuaSurface doesn't contain key namelocal".
func TestRemoveLuaSeparatesSurfaceNameFromNextStatement(t *testing.T) {
	lua, err := removeLua([]string{"--character=10599", "--entity=transport-belt", "--unitNumber=10610"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lua, "namelocal") {
		t.Fatalf("remove lua glues the surface name into the next token:\n%s", lua)
	}
	if !strings.Contains(lua, "local surface=s.name\n") {
		t.Fatalf("remove lua must terminate the surface local with a newline:\n%s", lua)
	}
}
