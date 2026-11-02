package main

import (
	"strings"
	"testing"
)

func serveTransferReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "transfer_many_engine.lua", []string{"TRANSFER_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "local target_name=")
	})
}

// A single transfer that moves nothing must name the cause exactly like
// transferMany does, so a limited bar is not reported as a plain moved: 0.
func TestTransferReportsBlockReason(t *testing.T) {
	for scenario, want := range map[string]string{
		"bar":      "BAR_LIMITED",
		"filtered": "FILTERED",
		"decon":    "MARKED_FOR_DECONSTRUCTION",
		"source":   "NO_SOURCE",
	} {
		result, err := command[ActionResult](serveTransferReplay(t, scenario), "transfer", TransferInput{
			Actor:     &ActorInput{Character: new("4")},
			Target:    &EntityRefInput{Surface: new("nauvis"), Entity: new("wooden-chest"), UnitNumber: new("71")},
			Inventory: InventoryKindChest,
			Direction: TransferDirectionToEntity,
			Item:      "iron-plate",
			Count:     5,
		})
		if err != nil {
			t.Fatalf("%s: %v", scenario, err)
		}
		if result.Moved != nil && *result.Moved != 0 {
			t.Fatalf("%s: expected nothing moved, got %+v", scenario, result)
		}
		if result.Reason == nil || *result.Reason != want {
			t.Fatalf("%s: reason = %v, want %s", scenario, result.Reason, want)
		}
		if want == "BAR_LIMITED" && (result.Bar == nil || *result.Bar != 1) {
			t.Fatalf("a bar-limited transfer must report the bar, got %+v", result)
		}
		if want == "MARKED_FOR_DECONSTRUCTION" && (result.ToBeDeconstructed == nil || !*result.ToBeDeconstructed) {
			t.Fatalf("a deconstruction-marked transfer must report it, got %+v", result)
		}
	}
}
