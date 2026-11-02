package main

import (
	"strings"
	"testing"
)

// serveResourceAddressingReplay runs the shipped mine / remove / removeMany Lua
// against a stubbed world that holds one neutral-force stone rock whose entity
// centre (-12.2, 53.3) differs from the tile the caller names (-12.5, 53.5).
func serveResourceAddressingReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "resource_addressing_engine.lua", []string{"RESOURCE_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "mine_entity")
	})
}

func rockAtNamedTile() *EntityRefInput {
	return &EntityRefInput{
		Surface:  new("nauvis"),
		Entity:   new("stone"),
		Position: &PositionInput{X: -12.5, Y: 53.5},
	}
}

func mineRockInput() MineInput {
	return MineInput{Actor: &ActorInput{Character: new("2223")}, Target: rockAtNamedTile()}
}

// A rock is addressed by the tile the caller names, not only by its exact
// centre: the engine can report a rock whose centre is off the tile grid.
func TestMineAddressesRockWhoseCentreDiffersFromNamedTile(t *testing.T) {
	result, err := command[ActionResult](serveResourceAddressingReplay(t, "ok"), "mine", mineRockInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Mined == nil || !*result.Mined {
		t.Fatalf("expected mined true, got %+v", result)
	}
	if result.Entity == nil || *result.Entity != "stone" {
		t.Fatalf("expected the stone entity in the result, got %+v", result.Entity)
	}
	if len(result.Items) != 1 || result.Items[0].Name != "stone" || result.Items[0].Count != 149 {
		t.Fatalf("expected the mined stone stack reported, got %+v", result.Items)
	}
}

// mine_entity returning true is not proof of a state change, so a mine that
// leaves the rock in place must fail instead of reporting mined: true.
func TestMineFailsWhenRockSurvivesTheMine(t *testing.T) {
	_, err := command[ActionResult](serveResourceAddressingReplay(t, "noop"), "mine", mineRockInput())
	if err == nil || !strings.Contains(err.Error(), "still present after mining") {
		t.Fatalf("expected an honest failure for a surviving rock, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "engine reported success") {
		t.Fatalf("the failure should name the false engine success, got %v", err)
	}
}

// A partial harvest still changes the world, so it is reported with the
// remaining amount instead of being treated as a failure.
func TestMineReportsRemainingAmountForPartialResourceHarvest(t *testing.T) {
	result, err := command[ActionResult](serveResourceAddressingReplay(t, "partial"), "mine", mineRockInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Remaining == nil || *result.Remaining != 144 {
		t.Fatalf("expected remaining 144, got %+v", result.Remaining)
	}
	if len(result.Items) != 1 || result.Items[0].Count != 5 {
		t.Fatalf("expected the five mined items reported, got %+v", result.Items)
	}
}

// remove must address the same neutral-force rock by position; the actor force
// filter used to hide it behind "entity not found at center on actor's force".
func TestRemoveAddressesNeutralRockByPosition(t *testing.T) {
	result, err := command[RemoveResult](serveResourceAddressingReplay(t, "ok"), "remove", RemoveInput{
		Actor:  &ActorInput{Character: new("2223")},
		Target: rockAtNamedTile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entity != "stone" {
		t.Fatalf("expected the stone entity, got %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].Name != "stone" || result.Items[0].Count != 149 {
		t.Fatalf("expected the recovered stone stack, got %+v", result.Items)
	}
}

// remove must not report success for a rock that is still standing.
func TestRemoveFailsWhenRockSurvivesTheRemoval(t *testing.T) {
	_, err := command[RemoveResult](serveResourceAddressingReplay(t, "noop"), "remove", RemoveInput{
		Actor:  &ActorInput{Character: new("2223")},
		Target: rockAtNamedTile(),
	})
	if err == nil || !strings.Contains(err.Error(), "still present after removal") {
		t.Fatalf("expected an honest failure for a surviving rock, got %v", err)
	}
}

// removeMany accepts a target addressed by name plus position, not only by unit
// number, and resolves the same neutral-force rock.
func TestRemoveManyAddressesNeutralRockByPosition(t *testing.T) {
	result, err := removeMany(serveResourceAddressingReplay(t, "ok"), RemoveManyInput{
		Actor:   &ActorInput{Character: new("2223")},
		Targets: []*EntityRefInput{rockAtNamedTile()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || result.Failed != 0 {
		t.Fatalf("expected one removal, got %+v", result)
	}
	if len(result.Results) != 1 || result.Results[0].Name != "stone" || result.Results[0].Position == nil {
		t.Fatalf("expected a stone result row with its position, got %+v", result.Results)
	}
	if len(result.Items) != 1 || result.Items[0].Count != 149 {
		t.Fatalf("expected the recovered stone stack, got %+v", result.Items)
	}
}

// removeMany reports a per-target ENGINE error when the engine claims a mine
// succeeded but the entity is still present.
func TestRemoveManyReportsEngineRefusalForSurvivingRock(t *testing.T) {
	result, err := removeMany(serveResourceAddressingReplay(t, "noop"), RemoveManyInput{
		Actor:   &ActorInput{Character: new("2223")},
		Targets: []*EntityRefInput{rockAtNamedTile()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 || result.Failed != 1 {
		t.Fatalf("expected the removal to fail, got %+v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].Code != BatchErrorCodeEngine {
		t.Fatalf("expected one ENGINE error, got %+v", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Message, "still present") {
		t.Fatalf("expected the surviving entity named, got %+v", result.Errors[0])
	}
}
