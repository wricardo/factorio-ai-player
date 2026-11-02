package main

import (
	"os/exec"
	"strings"
	"testing"
)

func servePlaceTileReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "place_tile_engine.lua", []string{"TILE_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "set_tiles")
	})
}

// Re-placing the tile that is already there must not spend an item or report
// success: set_tiles would silently consume it.
func TestPlaceTileRefusesMatchingTile(t *testing.T) {
	_, err := placeTile(servePlaceTileReplay(t, "same"), PlaceTileInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "concrete",
		Position: &PositionInput{X: 5, Y: 5},
	})
	if err == nil || !strings.Contains(err.Error(), "tile already matches") {
		t.Fatalf("expected a matching-tile refusal, got %v", err)
	}
}

// Replacing a minable player tile must return that tile's mined product, which
// set_tiles destroys.
func TestPlaceTileReturnsReplacedTileProduct(t *testing.T) {
	result, err := placeTile(servePlaceTileReplay(t, "replace"), PlaceTileInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "concrete",
		Position: &PositionInput{X: 5, Y: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tile != "concrete" {
		t.Fatalf("tile = %q, want concrete", result.Tile)
	}
}

// A tile blocked by an entity must still be refused.
func TestPlaceTileRefusesBlockedTile(t *testing.T) {
	_, err := placeTile(servePlaceTileReplay(t, "blocked"), PlaceTileInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "concrete",
		Position: &PositionInput{X: 5, Y: 5},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked by wooden-chest") {
		t.Fatalf("expected a blocked tile refusal, got %v", err)
	}
}

func serveBlueprintReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "blueprint_engine.lua", []string{"BP_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "build_blueprint")
	})
}

// build_blueprint silently drops colliding entities, so the result must report
// the blueprint's entity count and the number of skipped ghosts.
func TestPlaceBlueprintReportsEntityCountAndSkipped(t *testing.T) {
	result, err := placeBlueprint(serveBlueprintReplay(t, "full"), PlaceBlueprintInput{
		Blueprint: "0eNqtest",
		Position:  &PositionInput{X: 0, Y: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ghosts != 7 || result.EntityCount != 10 || result.Skipped != 3 {
		t.Fatalf("expected 7 of 10 ghosts with 3 skipped, got %+v", result)
	}
}

// A blueprint book string makes is_blueprint_setup raise, which used to leak
// the temporary script inventory.
func TestPlaceBlueprintRejectsBlueprintBook(t *testing.T) {
	_, err := placeBlueprint(serveBlueprintReplay(t, "book"), PlaceBlueprintInput{
		Blueprint: "0eNqbook",
		Position:  &PositionInput{X: 0, Y: 0},
	})
	if err == nil || !strings.Contains(err.Error(), "not a single blueprint") {
		t.Fatalf("expected a blueprint-book rejection, got %v", err)
	}
}

// Blueprints rotate only in 90 degree steps, so a non-cardinal direction must be
// rejected in Go instead of being rounded by the engine.
func TestPlaceBlueprintRejectsNonCardinalDirection(t *testing.T) {
	for _, direction := range []int{1, 2, 3, 5, 15} {
		_, err := placeBlueprint(&Resolver{}, PlaceBlueprintInput{
			Blueprint: "0eNqtest",
			Position:  &PositionInput{X: 0, Y: 0},
			Direction: direction,
		})
		if err == nil || !strings.Contains(err.Error(), "90 degree steps") {
			t.Fatalf("direction %d must be rejected, got %v", direction, err)
		}
	}
	for _, direction := range []int{0, 4, 8, 12} {
		if _, err := placeBlueprint(&Resolver{}, PlaceBlueprintInput{
			Blueprint: "0eNqtest",
			Position:  &PositionInput{X: 0, Y: 0},
			Direction: direction,
		}); err == nil || strings.Contains(err.Error(), "90 degree steps") {
			t.Fatalf("direction %d must be accepted by validation, got %v", direction, err)
		}
	}
}

func serveResearchReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "research_engine.lua", []string{"RESEARCH_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "add_research")
	})
}

// A trigger technology cannot be researched, so start-research must explain the
// trigger instead of reporting that research could not start.
func TestStartResearchExplainsTriggerTechnology(t *testing.T) {
	_, err := command[ResearchState](serveResearchReplay(t, "trigger"), "start-research", StartResearchInput{
		Technology: "automation-science-pack",
	})
	if err == nil || !strings.Contains(err.Error(), "trigger technology") {
		t.Fatalf("expected a trigger explanation, got %v", err)
	}
	if !strings.Contains(err.Error(), "craft-item") {
		t.Fatalf("the trigger description must be included, got %v", err)
	}
}

// A batch removal must report the identity of every removed entity and the items
// it returned, plus the aggregate gain, so the caller does not have to re-query
// the area and the inventory.
func TestRemoveManyReportsPerTargetResultsAndItems(t *testing.T) {
	r := serveLuaFixture(t, "remove_many_engine.lua", nil, func(command string) bool {
		return strings.Contains(command, "mine_entity")
	})
	result, err := removeMany(r, RemoveManyInput{
		Actor: &ActorInput{Character: new("4")},
		Targets: []*EntityRefInput{
			{Surface: new("nauvis"), Entity: new("wooden-chest"), UnitNumber: new("51")},
			{Surface: new("nauvis"), Entity: new("wooden-chest"), UnitNumber: new("52")},
			{Surface: new("nauvis"), Entity: new("wooden-chest"), UnitNumber: new("53")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 2 || result.Failed != 1 {
		t.Fatalf("expected two removals and one failure, got %+v", result)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected two result rows, got %+v", result.Results)
	}
	first := result.Results[0]
	if first.Index != 1 || first.Name != "wooden-chest" || first.UnitNumber == nil || *first.UnitNumber != "51" {
		t.Fatalf("unexpected first result row: %+v", first)
	}
	if first.Position == nil || first.Position.X != 1.5 || first.Position.Y != 1.5 {
		t.Fatalf("unexpected first result position: %+v", first.Position)
	}
	if len(first.Items) != 2 {
		t.Fatalf("expected the first target's two stacks, got %+v", first.Items)
	}
	if len(result.Items) != 3 {
		t.Fatalf("expected three recovered item rows in the aggregate, got %+v", result.Items)
	}
}

func serveFuelManyReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "fuel_many_engine.lua", []string{"FUEL_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "get_fuel_inventory")
	})
}

func topUpFuel(t *testing.T, scenario string, topUpTo int) *FuelManyResult {
	t.Helper()
	result, err := fuelMany(serveFuelManyReplay(t, scenario), FuelManyInput{
		Actor:   &ActorInput{Character: new("4")},
		Targets: batchRefs("61", "62"),
		Item:    "coal",
		TopUpTo: new(topUpTo),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// topUpTo fills each target up to the requested amount and is idempotent: a
// target that already holds that many items is skipped and adds nothing, so the
// second identical call also adds nothing.
func TestFuelManyTopUpToIsIdempotent(t *testing.T) {
	for attempt := 1; attempt <= 2; attempt++ {
		result := topUpFuel(t, "full", 10)
		if result.Moved != 0 || result.Fueled != 0 || result.Failed != 0 || result.Skipped != 2 {
			t.Fatalf("attempt %d must add nothing to full targets, got %+v", attempt, result)
		}
		if len(result.Results) != 2 {
			t.Fatalf("every skipped target must still be reported, got %+v", result.Results)
		}
		for _, row := range result.Results {
			if !row.Skipped || row.Requested != 0 || row.Moved != 0 {
				t.Fatalf("attempt %d skipped row: %+v", attempt, row)
			}
		}
	}
}

// On an empty target topUpTo reports the shortfall as requested and the inserted
// amount as moved.
func TestFuelManyTopUpToFillsShortfall(t *testing.T) {
	result := topUpFuel(t, "empty", 10)
	if result.Moved != 20 || result.Fueled != 2 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("expected both targets filled to 10, got %+v", result)
	}
	for _, row := range result.Results {
		if row.Requested != 10 || row.Moved != 10 || row.Skipped {
			t.Fatalf("unexpected shortfall row: %+v", row)
		}
	}
}

// chestRefs addresses wooden-chest unit numbers, matching the transfer fixture.
func chestRefs(units ...string) []*EntityRefInput {
	refs := make([]*EntityRefInput, len(units))
	for i, unit := range units {
		refs[i] = &EntityRefInput{Surface: new("nauvis"), Entity: new("wooden-chest"), UnitNumber: new(unit)}
	}
	return refs
}

func serveTransferManyReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "transfer_many_engine.lua", []string{"TRANSFER_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "transfer_stack")
	})
}

// A destination that moves nothing must say why: limited bar, deconstruction
// mark, slot filter or no source items.
func TestTransferManyReportsBlockReason(t *testing.T) {
	for scenario, want := range map[string]string{
		"bar":      "BAR_LIMITED",
		"filtered": "FILTERED",
		"decon":    "MARKED_FOR_DECONSTRUCTION",
		"source":   "NO_SOURCE",
	} {
		result, err := transferMany(serveTransferManyReplay(t, scenario), TransferManyInput{
			Actor:          &ActorInput{Character: new("4")},
			Targets:        chestRefs("71"),
			Inventory:      InventoryKindChest,
			Direction:      TransferDirectionToEntity,
			Item:           "iron-plate",
			CountPerTarget: 5,
		})
		if err != nil {
			t.Fatalf("%s: %v", scenario, err)
		}
		if result.Moved != 0 || len(result.Results) != 1 {
			t.Fatalf("%s: expected one unmoved target, got %+v", scenario, result)
		}
		row := result.Results[0]
		if row.Reason == nil || *row.Reason != want {
			t.Fatalf("%s: reason = %v, want %s", scenario, row.Reason, want)
		}
		if want == "BAR_LIMITED" && (row.Bar == nil || *row.Bar != 1) {
			t.Fatalf("a bar-limited row must report the bar, got %+v", row.Bar)
		}
		if want == "MARKED_FOR_DECONSTRUCTION" && !row.ToBeDeconstructed {
			t.Fatalf("a deconstruction-marked row must report it, got %+v", row)
		}
	}
}

// underground_info must survive every shape 2.0.77 can hand back for a half's
// neighbours (plain dictionary, array, or a userdata handle where pairs raises)
// and must never touch those fields on anything that is not an underground belt.
func TestUndergroundInfoLua(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	harness := `
local function half(neighbours, ground)
  return {type = "underground-belt", belt_to_ground_type = ground, neighbours = neighbours}
end
local kind, paired = underground_info(half({[4] = {valid = true, unit_number = 99}}, "output"))
assert(kind == "OUTPUT", "dictionary neighbours: ground type " .. tostring(kind))
assert(paired == 99, "dictionary neighbours must yield the paired unit number")
kind, paired = underground_info(half({{valid = true, unit_number = 77}}, "input"))
assert(kind == "INPUT" and paired == 77, "array neighbours must yield the paired unit number")
kind, paired = underground_info(half(io.stdout, "input"))
assert(kind == "INPUT", "a userdata neighbour handle must still report the ground type")
assert(paired == nil, "a userdata neighbour handle yields no unit number, not an error")
kind, paired = underground_info({type = "container"})
assert(kind == nil and paired == nil, "a non-underground entity has no underground info")
kind, paired = underground_info({type = "underground-belt"})
assert(kind == nil and paired == nil, "a half without neighbours is fine")
`
	cmd := exec.Command(luaBin, "-")
	cmd.Stdin = strings.NewReader(luaUndergroundInfoLua + harness)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("underground_info regression: %v\n%s", err, output)
	}
}
