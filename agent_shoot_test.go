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
		{"empty player", ShootInput{Actor: &ActorInput{Player: stringPointer(" ")}, Count: 1, Target: &EntityRefInput{Position: &PositionInput{X: 0, Y: 0}}}},
		{"zero count", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: 0, Target: &EntityRefInput{Position: &PositionInput{X: 0, Y: 0}}}},
		{"too many", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: maxBatchTargets + 1, Target: &EntityRefInput{Position: &PositionInput{X: 0, Y: 0}}}},
		{"no target", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: 1}},
		{"both targets", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: 1, Target: &EntityRefInput{UnitNumber: stringPointer("42"), Position: &PositionInput{X: 0, Y: 0}}}},
		{"nan coordinates", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: 1, Target: &EntityRefInput{Position: &PositionInput{X: math.NaN(), Y: 0}}}},
		{"bad unit", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: 1, Target: &EntityRefInput{UnitNumber: stringPointer("abc")}}},
		{"zero unit", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: 1, Target: &EntityRefInput{UnitNumber: stringPointer("0")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := shootAction(context.Background(), r, tc.input); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

// Firing spends rounds inside the current magazine, so the counter must report
// rounds rather than whole magazines. This runs the real Lua helper.
func TestShootAmmoCountsRounds(t *testing.T) {
	script := shootAmmoLua + `
defines = {inventory = {character_ammo = 5}}
local slots = {
  {name = "firearm-magazine", count = 3, ammo = 4, is_ammo = true, valid_for_read = true},
  {name = "piercing-rounds-magazine", count = 2, ammo = 10, is_ammo = true, valid_for_read = true},
  {name = "repair-pack", count = 5, is_ammo = false, valid_for_read = true},
  {valid_for_read = false},
}
prototypes = {item = {
  ["firearm-magazine"] = {magazine_size = 10},
  ["piercing-rounds-magazine"] = {magazine_size = 20},
  ["repair-pack"] = {},
}}
local loaded = {get_inventory = function() return slots end}
local empty = {get_inventory = function() return nil end}
print(shoot_ammo_count(loaded))
print(shoot_ammo_count(empty))
`
	// (3-1)*10+4 + (2-1)*20+10 + 5 plain rounds, then 0 for a character without ammo.
	lines := strings.Fields(runLuaScript(t, script))
	if len(lines) != 2 || lines[0] != "59" || lines[1] != "0" {
		t.Fatalf("ammo rounds = %v, want [59 0]", lines)
	}
}
