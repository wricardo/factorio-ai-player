package main

import (
	"math"
	"testing"
)

func TestValidateGameSpeedInput(t *testing.T) {
	paused := true
	for _, tc := range []struct {
		name  string
		input GameSpeedInput
		ok    bool
	}{
		{"default speed", GameSpeedInput{Speed: 1}, true},
		{"minimum", GameSpeedInput{Speed: 0.1}, true},
		{"maximum", GameSpeedInput{Speed: 1000, Paused: &paused}, true},
		{"zero", GameSpeedInput{Speed: 0}, false},
		{"too slow", GameSpeedInput{Speed: 0.09}, false},
		{"too fast", GameSpeedInput{Speed: 1000.5}, false},
		{"negative", GameSpeedInput{Speed: -2}, false},
		{"nan", GameSpeedInput{Speed: math.NaN()}, false},
		{"inf", GameSpeedInput{Speed: math.Inf(1)}, false},
	} {
		err := validateGameSpeedInput(tc.input)
		if tc.ok && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
}

func TestValidateTeleportInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input TeleportInput
		ok    bool
	}{
		{"player", TeleportInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Position: &PositionInput{X: 1, Y: 2}}, true},
		{"character", TeleportInput{Actor: &ActorInput{Character: stringPointer("439")}, Position: &PositionInput{X: -10.5, Y: 0}}, true},
		{"no actor", TeleportInput{Position: &PositionInput{X: 1, Y: 2}}, false},
		{"both actors", TeleportInput{Actor: &ActorInput{Player: stringPointer("a"), Character: stringPointer("439")}, Position: &PositionInput{X: 1, Y: 2}}, false},
		{"bad character", TeleportInput{Actor: &ActorInput{Character: stringPointer("abc")}, Position: &PositionInput{X: 1, Y: 2}}, false},
		{"nan x", TeleportInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Position: &PositionInput{X: math.NaN(), Y: 2}}, false},
		{"inf y", TeleportInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Position: &PositionInput{X: 1, Y: math.Inf(-1)}}, false},
		{"empty surface", TeleportInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Surface: stringPointer(""), Position: &PositionInput{X: 1, Y: 2}}, false},
	} {
		err := validateTeleportInput(tc.input)
		if tc.ok && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
}

func TestValidateCapsuleInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input CapsuleInput
		ok    bool
	}{
		{"valid", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: 1}, true},
		{"max count", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: 100}, true},
		{"no player", CapsuleInput{Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: 1}, false},
		{"blank player", CapsuleInput{Actor: &ActorInput{Player: stringPointer("  ")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: 1}, false},
		{"no item", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &PositionInput{X: 1, Y: 2}, Count: 1}, false},
		{"zero count", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}}, false},
		{"count too high", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: 101}, false},
		{"nan target", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: math.NaN(), Y: 2}, Count: 1}, false},
	} {
		err := validateCapsuleInput(tc.input)
		if tc.ok && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
}

func TestValidateRepairInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input RepairInput
		ok    bool
	}{
		{"player", RepairInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &EntityRefInput{Position: &PositionInput{X: 1, Y: 2}}, Count: 1}, true},
		{"character", RepairInput{Actor: &ActorInput{Character: stringPointer("439")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: 100}, true},
		{"no actor", RepairInput{Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: 1}, false},
		{"both actors", RepairInput{Actor: &ActorInput{Player: stringPointer("a"), Character: stringPointer("439")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: 1}, false},
		{"zero count", RepairInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}}, false},
		{"count too high", RepairInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: 101}, false},
		{"empty surface", RepairInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &EntityRefInput{Surface: stringPointer(""), UnitNumber: stringPointer("500")}, Count: 1}, false},
	} {
		err := validateRepairInput(tc.input)
		if tc.ok && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
	}
}

func TestCapsuleResultDecode(t *testing.T) {
	sample := `{"tick":100,"surface":"nauvis","item":"cliff-explosives","used":2,"position":{"x":1.5,"y":-2},"created":[{"name":"cliff-explosive","type":"explosion","position":{"x":1.5,"y":-2},"unitNumber":"900"}]}`
	result, err := factoryDecode[CapsuleResult](sample)
	if err != nil {
		t.Fatalf("decode capsule result: %v", err)
	}
	if result.Used != 2 || result.Item != "cliff-explosives" || result.Position == nil || result.Position.X != 1.5 {
		t.Fatalf("unexpected capsule result: %+v", result)
	}
	if len(result.Created) != 1 || result.Created[0].UnitNumber == nil || *result.Created[0].UnitNumber != "900" {
		t.Fatalf("unexpected created entities: %+v", result.Created)
	}
}

func TestRepairResultDecode(t *testing.T) {
	sample := `{"tick":100,"surface":"nauvis","name":"stone-furnace","unitNumber":"500","position":{"x":1,"y":2},"before":100,"after":250,"maxHealth":350,"packsUsed":1}`
	result, err := factoryDecode[RepairResult](sample)
	if err != nil {
		t.Fatalf("decode repair result: %v", err)
	}
	if result.After != 250 || result.MaxHealth != 350 || result.PacksUsed != 1 || result.UnitNumber == nil || *result.UnitNumber != "500" {
		t.Fatalf("unexpected repair result: %+v", result)
	}
}
