package main

import (
	"strings"
	"testing"
)

func serveSetDirectionReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "set_direction_engine.lua", []string{"SET_DIRECTION_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "e.direction=requested")
	})
}

func setInserterDirection(t *testing.T, scenario string, direction int) (*ActionResult, error) {
	t.Helper()
	player := "wricardo"
	unit := "77"
	return setDirection(serveSetDirectionReplay(t, scenario), SetDirectionInput{
		Actor:     &ActorInput{Player: &player},
		Target:    &EntityRefInput{UnitNumber: &unit},
		Direction: direction,
	})
}

// The field report for factorio-ai-player-tfi saw setDirection report success
// with tick 0, which reads as an ack that never ran. A real write must report
// the tick it happened in and the direction the engine stored.
func TestSetDirectionAppliesTheWriteAndReportsTheTick(t *testing.T) {
	result, err := setInserterDirection(t, "writable", 0)
	if err != nil {
		t.Fatalf("setDirection failed on a writable entity: %v", err)
	}
	if result.Tick == 0 {
		t.Fatalf("tick = 0, want the tick the write happened in: %+v", result)
	}
	if result.Before == nil || *result.Before != 8 {
		t.Fatalf("before = %v, want the engine direction 8 the inserter was placed with", intValue(result.Before))
	}
	if result.Direction == nil || *result.Direction != 0 {
		t.Fatalf("direction = %v, want the requested 0", intValue(result.Direction))
	}
	if result.Turns == nil || *result.Turns != 0 {
		t.Fatalf("turns = %v, want 0: a writable prototype does not need a rotation", intValue(result.Turns))
	}
}

// A 180-degree change on a prototype that ignores the direct write needs two
// quarter turns; the single-rotation fallback used to give up and fail here.
func TestSetDirectionRotatesATurnByTurnPrototypeTwice(t *testing.T) {
	result, err := setInserterDirection(t, "belted", 0)
	if err != nil {
		t.Fatalf("setDirection failed on a quarter-turn prototype: %v", err)
	}
	if result.Direction == nil || *result.Direction != 0 {
		t.Fatalf("direction = %v, want 0", intValue(result.Direction))
	}
	if result.Turns == nil || *result.Turns != 2 {
		t.Fatalf("turns = %v, want 2 rotations from 8 to 0", intValue(result.Turns))
	}
	if result.Tick == 0 {
		t.Fatalf("tick = 0, want the tick the rotation happened in")
	}
}

// A prototype the engine will not turn must fail with an actionable error
// instead of reporting the direction it never reached.
func TestSetDirectionFailsWhenTheEngineRefusesToTurn(t *testing.T) {
	_, err := setInserterDirection(t, "fixed", 0)
	if err == nil {
		t.Fatal("expected a refusal for an entity the engine will not turn, got success")
	}
	for _, want := range []string{"engine refused the direction change", "remove it and re-place it"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

// The rotation fallback can only reach quarter turns, so a finer request on a
// prototype that ignores the direct write must say so instead of silently
// landing somewhere else.
func TestSetDirectionRejectsFinerDirectionsOnTheRotationPath(t *testing.T) {
	_, err := setInserterDirection(t, "belted", 2)
	if err == nil {
		t.Fatal("expected a refusal for direction 2 on a quarter-turn prototype, got success")
	}
	if !strings.Contains(err.Error(), "only 0, 4, 8 and 12 are reachable") {
		t.Fatalf("error %q does not name the reachable directions", err)
	}
}

// The generated chunk is what the backend runs, so prove it carries the tick and
// the readback, not only the write.
func TestSetDirectionLuaVerifiesReadbackAndReportsTick(t *testing.T) {
	player := "wricardo"
	unit := "77"
	lua, err := setDirectionLua(SetDirectionInput{
		Actor:     &ActorInput{Player: &player},
		Target:    &EntityRefInput{UnitNumber: &unit},
		Direction: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"local requested=12",
		"e.direction=requested",
		"local applied=(e.direction==requested)",
		"e.rotate{reverse=reverse}",
		"tick=game.tick",
		"turns=turns",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("generated Lua is missing %q:\n%s", want, lua)
		}
	}
}
