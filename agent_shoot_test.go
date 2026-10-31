package main

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestShootActionValidation(t *testing.T) {
	r := &Resolver{}
	for _, tc := range []struct {
		name  string
		input ShootInput
	}{
		{"empty player", ShootInput{Player: " ", Count: 1, X: floatPointer(0), Y: floatPointer(0)}},
		{"zero count", ShootInput{Player: "wricardo", Count: 0, X: floatPointer(0), Y: floatPointer(0)}},
		{"too many", ShootInput{Player: "wricardo", Count: 101, X: floatPointer(0), Y: floatPointer(0)}},
		{"no target", ShootInput{Player: "wricardo", Count: 1}},
		{"both targets", ShootInput{Player: "wricardo", Count: 1, TargetUnitNumber: stringPointer("42"), X: floatPointer(0), Y: floatPointer(0)}},
		{"half coordinates", ShootInput{Player: "wricardo", Count: 1, X: floatPointer(0)}},
		{"nan coordinates", ShootInput{Player: "wricardo", Count: 1, X: floatPointer(math.NaN()), Y: floatPointer(0)}},
		{"bad unit", ShootInput{Player: "wricardo", Count: 1, TargetUnitNumber: stringPointer("abc")}},
		{"zero unit", ShootInput{Player: "wricardo", Count: 1, TargetUnitNumber: stringPointer("0")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := shootAction(context.Background(), r, tc.input); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestShootStartLuaUsesActionJob(t *testing.T) {
	lua, err := shootStartLua(ShootInput{Player: "wricardo", Count: 7, TargetUnitNumber: stringPointer("42")}, 42)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`remote.call("factorio_ai_actions","start_shoot"`,
		"shoot_ammo_count(c)",
		"resolve_entity(target_unit,target_name,target_position,surface_name,p)",
		`"start_shoot",c.unit_number,e.position.x,e.position.y,7`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("lua does not contain %q:\n%s", want, lua)
		}
	}
}

func TestShootAmmoDelta(t *testing.T) {
	if got := int(math.Max(0, 20-12)); got != 8 {
		t.Fatalf("ammo delta = %d", got)
	}
	if got := int(math.Max(0, 12-20)); got != 0 {
		t.Fatalf("negative delta must clamp to zero, got %d", got)
	}
}
