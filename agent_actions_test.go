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
		{"max count", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: maxBatchTargets}, true},
		{"no player", CapsuleInput{Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: 1}, false},
		{"blank player", CapsuleInput{Actor: &ActorInput{Player: stringPointer("  ")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: 1}, false},
		{"no item", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &PositionInput{X: 1, Y: 2}, Count: 1}, false},
		{"zero count", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}}, false},
		{"count too high", CapsuleInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "cliff-explosives", Target: &PositionInput{X: 1, Y: 2}, Count: maxBatchTargets + 1}, false},
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
		{"character", RepairInput{Actor: &ActorInput{Character: stringPointer("439")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: maxBatchTargets}, true},
		{"no actor", RepairInput{Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: 1}, false},
		{"both actors", RepairInput{Actor: &ActorInput{Player: stringPointer("a"), Character: stringPointer("439")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: 1}, false},
		{"zero count", RepairInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}}, false},
		{"count too high", RepairInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Target: &EntityRefInput{UnitNumber: stringPointer("500")}, Count: maxBatchTargets + 1}, false},
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

// A normal repair pack restores get_durability("normal") * RepairToolPrototype
// speed health, and count caps the packs rather than the health.
func TestRepairPackHealthPerPack(t *testing.T) {
	script := `
prototypes = {item = {["repair-pack"] = {get_durability = function() return 300 end, speed = 2}}}
health, max_health = 100, 1300
` + repairPacksLua(2) + `
print(per_pack, needed, packs, after)
`
	lines := strings.Fields(runLuaScript(t, script))
	// per_pack=600, needed=ceil(1200/600)=2, count allows 2, after=100+2*600.
	if len(lines) != 4 || lines[0] != "600" || lines[1] != "2" || lines[2] != "2" || lines[3] != "1300" {
		t.Fatalf("repair arithmetic = %v, want [600 2 2 1300]", lines)
	}

	capped := `
prototypes = {item = {["repair-pack"] = {get_durability = function() return 300 end, speed = 2}}}
health, max_health = 100, 1300
` + repairPacksLua(1) + `
print(per_pack, packs, after)
`
	lines = strings.Fields(runLuaScript(t, capped))
	if len(lines) != 3 || lines[0] != "600" || lines[1] != "1" || lines[2] != "700" {
		t.Fatalf("repair cap = %v, want [600 1 700]", lines)
	}

	// A prototype that publishes no usability data falls back to 300 per pack.
	fallback := `
prototypes = {item = {["repair-pack"] = {get_durability = function() error("no durability") end}}}
health, max_health = 0, 900
` + repairPacksLua(1) + `
print(per_pack, packs, after)
`
	lines = strings.Fields(runLuaScript(t, fallback))
	if len(lines) != 3 || lines[0] != "300" || lines[1] != "1" || lines[2] != "300" {
		t.Fatalf("repair fallback = %v, want [300 1 300]", lines)
	}
}

// Teleporting without a surface stays on the actor's surface; a cross-surface
// move goes through LuaPlayer, and a detached character cannot cross at all.
func TestTeleportLuaSurfaceSelection(t *testing.T) {
	const stubs = `
defines = {build_check_type = {script_ghost = 17}}
fail = function(message) error(message, 0) end
helpers = {table_to_json = function(t) return "surface=" .. t.surface .. " x=" .. tostring(t.position.x) .. " y=" .. tostring(t.position.y) end}
rcon = {print = function(text) io.write(text) end}
local nauvis = {name = "nauvis", index = 1}
local vulcanus = {name = "vulcanus", index = 2}
local function make_character(with_player)
  local character = {type = "character", position = {x = 0, y = 0}, surface = nauvis, force = {name = "player"}, name = "character"}
  character.teleport = function(position, surface) character.position = position; character.surface = surface; return true end
  if with_player then
    local player = {name = "wricardo"}
    player.teleport = function(position, surface) character.position = position; character.surface = surface; return true end
    character.player = player
  end
  return character
end
current = make_character(true)
game = {surfaces = {nauvis = nauvis, vulcanus = vulcanus}, get_entity_by_unit_number = function() return current end}
`
	t.Run("current surface by default", func(t *testing.T) {
		out := runLuaScript(t, stubs+teleportLua(1, "", "", 3, 4, false))
		if out != "surface=nauvis x=3 y=4" {
			t.Fatalf("teleport output = %q", out)
		}
	})
	t.Run("cross-surface player", func(t *testing.T) {
		out := runLuaScript(t, stubs+teleportLua(1, "", "vulcanus", 3, 4, false))
		if out != "surface=vulcanus x=3 y=4" {
			t.Fatalf("teleport output = %q", out)
		}
	})
	t.Run("cross-surface detached", func(t *testing.T) {
		detached := strings.Replace(stubs, "current = make_character(true)", "current = make_character(false)", 1)
		out, err := runLuaScriptError(t, detached+teleportLua(1, "", "vulcanus", 3, 4, false))
		if err == nil || !strings.Contains(out, "cross-surface teleport requires a player-controlled character") {
			t.Fatalf("expected a cross-surface rejection, got output %q error %v", out, err)
		}
	})
}
