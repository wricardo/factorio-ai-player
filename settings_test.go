package main

import (
	"math"
	"strings"
	"testing"
)

func TestSettingsActor(t *testing.T) {
	if unit, player, err := settingsActor("439", ""); err != nil || unit != 439 || player != "" {
		t.Fatalf("character actor: unit=%d player=%q err=%v", unit, player, err)
	}
	if unit, player, err := settingsActor("", "wricardo"); err != nil || unit != 0 || player != "wricardo" {
		t.Fatalf("player actor: unit=%d player=%q err=%v", unit, player, err)
	}
	for _, tc := range []struct{ character, player string }{
		{"", ""},
		{"439", "wricardo"},
		{"abc", ""},
		{"0", ""},
	} {
		if _, _, err := settingsActor(tc.character, tc.player); err == nil {
			t.Fatalf("expected an error for character=%q player=%q", tc.character, tc.player)
		}
	}
}

func TestCircuitApplyLua(t *testing.T) {
	line := circuitApplyLua("readContents", []string{"circuit_read_contents", "read_contents"}, "true")
	want := "if not apply(" + luaString("readContents") + "," + luaKeys([]string{"circuit_read_contents", "read_contents"}) + ",true) then return end\n"
	if line != want {
		t.Fatalf("apply line = %q, want %q", line, want)
	}
	if !strings.Contains(line, luaString("read_contents")) {
		t.Fatal("engine key candidates must be tried in order")
	}
}

func TestCircuitFieldKeys(t *testing.T) {
	if keys := circuitFieldKeys["enabled"]; len(keys) != 1 || keys[0] != "disabled" {
		t.Fatalf("enabled must invert the engine disabled flag, got %v", keys)
	}
	seen := map[string]bool{}
	for _, field := range append(append([]circuitField{}, circuitBooleanFields...), circuitConditionFields...) {
		if seen[field.name] {
			t.Fatalf("duplicate circuit field %q", field.name)
		}
		seen[field.name] = true
		if len(field.keys) == 0 {
			t.Fatalf("field %q has no engine keys", field.name)
		}
	}
}

func TestValidateCircuitInts(t *testing.T) {
	mode := 3
	if err := validateCircuitInts(map[string]*int{"colorMode": &mode}); err == nil {
		t.Fatal("colorMode outside 0..2 must fail")
	}
	mode = 2
	if err := validateCircuitInts(map[string]*int{"colorMode": &mode}); err != nil {
		t.Fatalf("colorMode 2 rejected: %v", err)
	}
	negative := -1
	if err := validateCircuitInts(map[string]*int{"siloReadMode": &negative}); err == nil {
		t.Fatal("a negative read mode must fail")
	}
}

func TestConditionLuaSecondSignalAndQuality(t *testing.T) {
	lua, err := conditionLua(SignalConditionInput{
		Comparator:   ">=",
		FirstSignal:  &SignalInput{Type: stringPointer("fluid"), Name: "steam", Quality: stringPointer("uncommon")},
		SecondSignal: &SignalInput{Name: "iron-plate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "{comparator=" + luaString(">=") +
		",first_signal={type=" + luaString("fluid") + ",name=" + luaString("steam") + ",quality=" + luaString("uncommon") + "}" +
		",second_signal={type=" + luaString("item") + ",name=" + luaString("iron-plate") + "}}"
	if lua != want {
		t.Fatalf("got %q, want %q", lua, want)
	}
}

func TestSignalLuaRejectsEmptyName(t *testing.T) {
	if _, err := signalLua(SignalInput{}); err == nil {
		t.Fatal("an empty signal name must fail")
	}
}

func TestValidateColor(t *testing.T) {
	if err := validateColor(ColorInput{R: 0, G: 0.5, B: 1}); err != nil {
		t.Fatalf("valid color rejected: %v", err)
	}
	for _, in := range []ColorInput{
		{R: -0.1},
		{G: 1.1},
		{B: math.NaN()},
		{R: 0, A: floatPointer(2)},
	} {
		if err := validateColor(in); err == nil {
			t.Fatalf("expected an error for %+v", in)
		}
	}
}

func TestSettingsTargetFromDefaults(t *testing.T) {
	target, err := settingsTargetFrom(nil, stringPointer("439"), nil, stringPointer("splitter"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if target.Surface != "nauvis" || target.Character != "439" || target.Entity != "splitter" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if !math.IsNaN(target.X) || !math.IsNaN(target.Y) {
		t.Fatalf("missing coordinates must stay NaN, got %v/%v", target.X, target.Y)
	}
}

func TestSettingsLuaDefinesSharedReaders(t *testing.T) {
	for _, needle := range []string{
		"local function settings_sections(e,inventory_name)",
		"local function settings_print(e,inventory_name)",
		"local function inserter_settings(e)",
		"local function settings_circuit(e)",
	} {
		if !strings.Contains(settingsLua, needle) {
			t.Fatalf("settings Lua is missing %q", needle)
		}
	}
	mutation := settingsMutationLua("", "chest")
	if !strings.HasPrefix(mutation, luaFail) || !strings.Contains(mutation, settingsLua) || !strings.HasSuffix(mutation, "settings_print(e,"+luaString("chest")+")\n") {
		t.Fatal("settingsMutationLua must define fail, the shared readers and the print tail")
	}
}

func TestSettingsReadLuaRejectsUnknownInventory(t *testing.T) {
	if _, err := settingsReadLua("", "cargo"); err == nil {
		t.Fatal("an unknown inventory must fail")
	}
	if _, err := settingsReadLua("", ""); err != nil {
		t.Fatalf("an empty inventory must default to chest: %v", err)
	}
}

func floatPointer(value float64) *float64 { return &value }

func stringPointer(value string) *string { return &value }
