package main

import (
	"math"
	"strings"
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

func TestSetGameSpeedLua(t *testing.T) {
	lua := setGameSpeedLua(GameSpeedInput{Speed: 2.5})
	for _, want := range []string{"game.speed=", "tick", "speed=game.speed", "paused=game.tick_paused"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("speed lua missing %q: %s", want, lua)
		}
	}
	if strings.Contains(lua, "game.tick_paused=") {
		t.Fatalf("a nil paused must leave the pause state untouched: %s", lua)
	}
	paused := false
	lua = setGameSpeedLua(GameSpeedInput{Speed: 3, Paused: &paused})
	if !strings.Contains(lua, "game.tick_paused=false") {
		t.Fatalf("paused lua missing the explicit pause write: %s", lua)
	}
}

func TestValidateTeleportInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input TeleportInput
		ok    bool
	}{
		{"player", TeleportInput{Player: stringPointer("wricardo"), X: 1, Y: 2}, true},
		{"character", TeleportInput{Character: stringPointer("439"), X: -10.5, Y: 0}, true},
		{"no actor", TeleportInput{X: 1, Y: 2}, false},
		{"both actors", TeleportInput{Player: stringPointer("a"), Character: stringPointer("439"), X: 1, Y: 2}, false},
		{"bad character", TeleportInput{Character: stringPointer("abc"), X: 1, Y: 2}, false},
		{"nan x", TeleportInput{Player: stringPointer("wricardo"), X: math.NaN(), Y: 2}, false},
		{"inf y", TeleportInput{Player: stringPointer("wricardo"), X: 1, Y: math.Inf(-1)}, false},
		{"empty surface", TeleportInput{Player: stringPointer("wricardo"), Surface: stringPointer(""), X: 1, Y: 2}, false},
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

func TestTeleportLua(t *testing.T) {
	lua := teleportLua(439, "", "nauvis", 10, 20, true)
	for _, want := range []string{"c.teleport(", "defines.build_check_type.script_ghost", "game.surfaces[", "surface not found"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("teleport lua missing %q: %s", want, lua)
		}
	}
	if !strings.Contains(lua, "false, true, defines.build_check_type.script_ghost") {
		t.Fatalf("snapToGrid must be emitted explicitly: %s", lua)
	}
	if strings.Contains(lua, "game.players") {
		t.Fatalf("a character actor must not be resolved through game.players: %s", lua)
	}
	playerLua := teleportLua(0, "wricardo", "nauvis", 1, 2, false)
	if !strings.Contains(playerLua, "game.players[") {
		t.Fatalf("player teleport must resolve the player: %s", playerLua)
	}
	if !strings.Contains(playerLua, "false, false, defines.build_check_type.script_ghost") {
		t.Fatalf("snapToGrid must be emitted explicitly: %s", playerLua)
	}
}

func TestValidateCapsuleInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input CapsuleInput
		ok    bool
	}{
		{"valid", CapsuleInput{Player: "wricardo", Item: "cliff-explosives", X: 1, Y: 2, Count: 1}, true},
		{"max count", CapsuleInput{Player: "wricardo", Item: "cliff-explosives", X: 1, Y: 2, Count: 100}, true},
		{"no player", CapsuleInput{Item: "cliff-explosives", X: 1, Y: 2, Count: 1}, false},
		{"blank player", CapsuleInput{Player: "  ", Item: "cliff-explosives", X: 1, Y: 2, Count: 1}, false},
		{"no item", CapsuleInput{Player: "wricardo", X: 1, Y: 2, Count: 1}, false},
		{"zero count", CapsuleInput{Player: "wricardo", Item: "cliff-explosives", X: 1, Y: 2}, false},
		{"count too high", CapsuleInput{Player: "wricardo", Item: "cliff-explosives", X: 1, Y: 2, Count: 101}, false},
		{"nan target", CapsuleInput{Player: "wricardo", Item: "cliff-explosives", X: math.NaN(), Y: 2, Count: 1}, false},
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

func TestUseCapsuleLua(t *testing.T) {
	lua := useCapsuleLua(CapsuleInput{Player: "wricardo", Item: "cliff-explosives", X: 4, Y: -2, Count: 3})
	for _, want := range []string{"stack.use_capsule(c,", "prototypes.item[item]", "find_item_stack(item)", "reach_distance", "p.connected", "requested"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("capsule lua missing %q: %s", want, lua)
		}
	}
	if strings.Contains(lua, "game.players[") && !strings.Contains(lua, "p.character") {
		t.Fatalf("capsule use must require a player character: %s", lua)
	}
}

func TestValidateRepairInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input RepairInput
		ok    bool
	}{
		{"player", RepairInput{Player: stringPointer("wricardo"), X: floatPointer(1), Y: floatPointer(2), Count: 1}, true},
		{"character", RepairInput{Character: stringPointer("439"), UnitNumber: stringPointer("500"), Count: 100}, true},
		{"no actor", RepairInput{UnitNumber: stringPointer("500"), Count: 1}, false},
		{"both actors", RepairInput{Player: stringPointer("a"), Character: stringPointer("439"), UnitNumber: stringPointer("500"), Count: 1}, false},
		{"zero count", RepairInput{Player: stringPointer("wricardo"), UnitNumber: stringPointer("500")}, false},
		{"count too high", RepairInput{Player: stringPointer("wricardo"), UnitNumber: stringPointer("500"), Count: 101}, false},
		{"empty surface", RepairInput{Player: stringPointer("wricardo"), Surface: stringPointer(""), UnitNumber: stringPointer("500"), Count: 1}, false},
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

func TestRepairLua(t *testing.T) {
	target, err := settingsTargetFrom(stringPointer("wricardo"), nil, nil, stringPointer("stone-furnace"), stringPointer("500"), nil, nil)
	if err != nil {
		t.Fatalf("settingsTargetFrom: %v", err)
	}
	lua, err := repairLua(target, 4)
	if err != nil {
		t.Fatalf("repairLua: %v", err)
	}
	for _, want := range []string{"repair-pack", "type(health)~=\"number\"", "max_health", "math.ceil(damage/durability)", "packs_used", "e.health=after", "can_reach_entity",
		"local durability=300", `pcall(function() return prototypes.item["repair-pack"].durability end)`, "if durability_ok and type(durability_value)==\"number\" and durability_value>0 then durability=durability_value end",
		"local max_ok,max_health=pcall(function() return e.max_health end)", "local set_ok,set_error=pcall(function() e.health=after end)"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("repair lua missing %q: %s", want, lua)
		}
	}
	if !strings.Contains(lua, "math.min(needed, 4)") {
		t.Fatalf("repair lua must bound the packs by count: %s", lua)
	}
}

func TestRepairLuaRejectsBadActor(t *testing.T) {
	target, _ := settingsTargetFrom(nil, stringPointer("abc"), nil, stringPointer("stone-furnace"), stringPointer("500"), nil, nil)
	if _, err := repairLua(target, 1); err == nil {
		t.Fatal("expected an error for an invalid character unit number")
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
