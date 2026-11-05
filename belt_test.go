package main

import (
	"io"
	"strings"
	"testing"
)

// serveBeltReplay runs the shipped belt Lua against the stubbed engine in
// testdata/belt_engine.lua. The fixture asserts which transport line changed,
// so a replay proves the lane selection, not just the counters the caller asked
// for.
func serveBeltReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "belt_engine.lua", []string{"BELT_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "rcon.print")
	})
}

func beltTakeInput(unit string, item string, count float64, lanes ...BeltLane) BeltTakeInput {
	return BeltTakeInput{
		Actor:  &ActorInput{Character: new("4")},
		Target: &EntityRefInput{UnitNumber: &unit},
		Item:   item,
		Count:  &count,
		Lanes:  lanes,
	}
}

// A requested lane must be the only line emptied; the sibling lane keeps its
// contents even when it carries a different item.
func TestBeltTakeLeftLaneOnly(t *testing.T) {
	result, err := beltTakeMutation(serveBeltReplay(t, "take-left"), beltTakeInput("700", "iron-plate", 10, BeltLaneLeft))
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved == nil || *result.Moved != 4 {
		t.Fatalf("moved = %v, want 4", numberValue(result.Moved))
	}
	if result.Requested == nil || *result.Requested != 10 {
		t.Fatalf("requested = %v, want 10", numberValue(result.Requested))
	}
}

// Omitting lanes must keep the historical both-lane behaviour, so an item on the
// right line is still taken without a lane list.
func TestBeltTakeDefaultsToBothLanes(t *testing.T) {
	result, err := beltTakeMutation(serveBeltReplay(t, "take-both"), beltTakeInput("700", "copper-plate", 10))
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved == nil || *result.Moved != 3 {
		t.Fatalf("moved = %v, want 3", numberValue(result.Moved))
	}
}

// lanes are walked left before right regardless of the order the caller lists
// them, so a count the left lane satisfies never drains the right lane.
func TestBeltTakeWalksLeftLaneFirst(t *testing.T) {
	result, err := beltTakeMutation(serveBeltReplay(t, "take-order"), beltTakeInput("700", "iron-plate", 4, BeltLaneRight, BeltLaneLeft))
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved == nil || *result.Moved != 4 {
		t.Fatalf("moved = %v, want 4", numberValue(result.Moved))
	}
}

// A lane the belt does not expose is a caller mistake: the call fails and the
// existing lane is left untouched.
func TestBeltTakeRejectsAbsentLane(t *testing.T) {
	_, err := beltTakeMutation(serveBeltReplay(t, "take-invalid-lane"), beltTakeInput("701", "iron-plate", 1, BeltLaneRight))
	if err == nil || !strings.Contains(err.Error(), "belt has no right lane") {
		t.Fatalf("expected a missing-lane refusal, got %v", err)
	}
}

// The CLI flag accepts the same lane names and rejects anything else before a
// game round trip.
func TestBeltTakeLaneFlagValidation(t *testing.T) {
	if _, err := beltTakeLua([]string{"--character=4", "--unitNumber=1", "--item=iron-plate", "--lanes=sideways"}, io.Discard); err == nil || !strings.Contains(err.Error(), "lanes must be a comma-separated list of left and right") {
		t.Fatalf("expected a lane-name refusal, got %v", err)
	}
	lanes, err := parseBeltLanes(" LEFT , right,left ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lanes, ",") != "left,right" {
		t.Fatalf("lanes = %v, want left,right with duplicates dropped", lanes)
	}
	if lanes, err := parseBeltLanes("  "); err != nil || lanes != nil {
		t.Fatalf("an empty lane list must keep both lanes, got %v %v", lanes, err)
	}
}

func beltPurgeArea() *FactoryAreaInput {
	return &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 3, RightBottomY: 3}
}

// A lane-selected area purge removes only the named item from the named lane:
// other items on that lane and the whole sibling lane stay intact.
func TestBeltPurgeLeftLaneAcrossArea(t *testing.T) {
	result, err := beltPurge(serveBeltReplay(t, "purge-left"), BeltPurgeInput{
		Actor: &ActorInput{Character: new("4")},
		Area:  beltPurgeArea(),
		Item:  "iron-plate",
		Lanes: []BeltLane{BeltLaneLeft},
		Limit: new(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 8 || result.TilesChanged != 2 || result.TilesVisited != 2 {
		t.Fatalf("purge result = %+v, want 8 removed on 2 of 2 tiles", result)
	}
	if result.Tick != 777 || result.Surface != "nauvis" {
		t.Fatalf("purge snapshot = %+v", result)
	}
}

// limit counts belt tiles, so one call never changes more belts than asked and
// the sweep stops after the first changed belt.
func TestBeltPurgeStopsAtLimit(t *testing.T) {
	result, err := beltPurge(serveBeltReplay(t, "purge-limit"), BeltPurgeInput{
		Actor: &ActorInput{Character: new("4")},
		Area:  beltPurgeArea(),
		Item:  "iron-plate",
		Lanes: []BeltLane{BeltLaneLeft},
		Limit: new(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TilesChanged != 1 || result.TilesVisited != 1 || result.Removed != 5 {
		t.Fatalf("purge result = %+v, want the sweep to stop after the first belt", result)
	}
}

// A target addresses one belt instead of an area, and leaves its neighbours in
// the same area alone.
func TestBeltPurgeSingleTarget(t *testing.T) {
	result, err := beltPurge(serveBeltReplay(t, "purge-target"), BeltPurgeInput{
		Actor:  &ActorInput{Character: new("4")},
		Target: &EntityRefInput{UnitNumber: new("711")},
		Item:   "iron-plate",
		Lanes:  []BeltLane{BeltLaneLeft},
		Limit:  new(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 3 || result.TilesVisited != 1 || result.TilesChanged != 1 {
		t.Fatalf("purge result = %+v, want one targeted belt", result)
	}
}

// A belt that does not expose the requested lane is visited but unchanged; a
// sweep must not abort because one belt lacks a side.
func TestBeltPurgeSkipsBeltsWithoutLane(t *testing.T) {
	result, err := beltPurge(serveBeltReplay(t, "purge-missing-lane"), BeltPurgeInput{
		Actor: &ActorInput{Character: new("4")},
		Area:  beltPurgeArea(),
		Item:  "iron-plate",
		Lanes: []BeltLane{BeltLaneRight},
		Limit: new(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TilesVisited != 3 || result.TilesChanged != 1 || result.Removed != 2 {
		t.Fatalf("purge result = %+v, want the lane-less belt skipped", result)
	}
}

// Addressing and limit problems are rejected before any RCON dial.
func TestBeltPurgeValidatesInput(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	actor := &ActorInput{Character: new("4")}
	cases := []struct {
		name  string
		input BeltPurgeInput
		want  string
	}{
		{"missing item", BeltPurgeInput{Actor: actor, Area: beltPurgeArea()}, "item must not be empty"},
		{"missing address", BeltPurgeInput{Actor: actor, Item: "iron-plate"}, "exactly one of area or target is required"},
		{"both addresses", BeltPurgeInput{Actor: actor, Area: beltPurgeArea(), Target: &EntityRefInput{UnitNumber: new("710")}, Item: "iron-plate"}, "exactly one of area or target is required"},
		{"zero limit", BeltPurgeInput{Actor: actor, Area: beltPurgeArea(), Item: "iron-plate", Limit: new(0)}, "limit must be between 1 and 1000"},
		{"excess limit", BeltPurgeInput{Actor: actor, Area: beltPurgeArea(), Item: "iron-plate", Limit: new(1001)}, "limit must be between 1 and 1000"},
		{"empty quality", BeltPurgeInput{Actor: actor, Area: beltPurgeArea(), Item: "iron-plate", Quality: new("")}, "quality must not be empty"},
		{"empty surface", BeltPurgeInput{Actor: actor, Area: beltPurgeArea(), Item: "iron-plate", Surface: new("")}, "surface must not be empty"},
		{"inverted area", BeltPurgeInput{Actor: actor, Area: &FactoryAreaInput{LeftTopX: 5, RightBottomX: 2, RightBottomY: 2}, Item: "iron-plate"}, "no greater than 1024 tiles"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := beltPurge(r, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
