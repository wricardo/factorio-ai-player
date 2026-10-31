package main

import (
	"context"
	"math"
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
		{"too many", ShootInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Count: 101, Target: &EntityRefInput{Position: &PositionInput{X: 0, Y: 0}}}},
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
