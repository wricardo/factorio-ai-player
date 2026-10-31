package main

import (
	"strings"
	"testing"
)

func TestSurfacesForceValidation(t *testing.T) {
	if _, err := surfaceQueryLua(""); err == nil {
		t.Fatal("empty force must be rejected")
	}
	lua, err := surfaceQueryLua("player")
	if err != nil {
		t.Fatalf("player force: %v", err)
	}
	for _, want := range []string{"game.forces", "game.surfaces", "get_total_pollution", "evolution_factor(", "evolution_for(force,surface)", "surface.index"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("surfaces lua missing %q:\n%s", want, lua)
		}
	}
	if !strings.Contains(lua, luaString("player")) {
		t.Fatal("surfaces lua must interpolate the force name safely")
	}
}

func TestCircuitNetworkValidation(t *testing.T) {
	valid := CircuitNetworkInput{
		Surface:    stringPointer("nauvis"),
		Entity:     stringPointer("constant-combinator"),
		UnitNumber: stringPointer("42"),
		Color:      stringPointer("green"),
	}
	for _, tc := range []struct {
		name  string
		input CircuitNetworkInput
		want  string
	}{
		{"unknown color", CircuitNetworkInput{Entity: stringPointer("iron-chest"), X: floatPointer(1), Y: floatPointer(2), Color: stringPointer("blue")}, "color"},
		{"empty color", CircuitNetworkInput{Entity: stringPointer("iron-chest"), X: floatPointer(1), Y: floatPointer(2), Color: stringPointer("")}, "color"},
		{"no address", CircuitNetworkInput{Surface: stringPointer("nauvis")}, "entity name or unitNumber"},
		{"unit and coordinates", CircuitNetworkInput{UnitNumber: stringPointer("7"), X: floatPointer(1), Y: floatPointer(2)}, "not both"},
		{"name without coordinates", CircuitNetworkInput{Entity: stringPointer("iron-chest")}, "finite"},
		{"bad unit number", CircuitNetworkInput{UnitNumber: stringPointer("0")}, "unit number"},
	} {
		if _, err := circuitNetworkLua(tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want mention of %q", tc.name, err, tc.want)
		}
	}
	lua, err := circuitNetworkLua(valid)
	if err != nil {
		t.Fatalf("valid input: %v", err)
	}
	for _, want := range []string{"get_circuit_network", "defines.wire_type.red", "defines.wire_type.green", "network.signals", "connected_circuit_count", "find_entities_filtered"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("circuit lua missing %q:\n%s", want, lua)
		}
	}
	if !strings.Contains(lua, "local color_name="+luaString("green")) {
		t.Fatal("circuit lua must emit the selected color")
	}
	if strings.Contains(lua, "can_reach_entity") {
		t.Fatal("circuit queries must not enforce build reach")
	}
}

func TestCircuitSignalSorting(t *testing.T) {
	signals := []*CircuitSignal{
		{Type: "virtual", Name: "signal-B", Value: 1},
		{Type: "item", Name: "copper-plate", Value: 5},
		{Type: "item", Name: "iron-plate", Quality: stringPointer("normal"), Value: 3},
		{Type: "item", Name: "iron-plate", Value: 7},
	}
	sortCircuitSignals(signals)
	want := []string{"item/copper-plate", "item/iron-plate", "item/iron-plate", "virtual/signal-B"}
	for i, s := range signals {
		got := s.Type + "/" + s.Name
		if got != want[i] {
			t.Fatalf("position %d = %s, want %s", i, got, want[i])
		}
	}
	if signals[1].Value != 7 || signals[2].Quality == nil {
		t.Fatalf("ties must keep the unqualified signal first: %+v", signals[1:3])
	}
}

func TestSafetySnapshotLuaEvolutionAndKills(t *testing.T) {
	input := &SafetyInput{Surface: "nauvis", Force: "player", EnemyLimit: 10, DamagedLimit: 10, AlertLimit: 10}
	lua, err := safetySnapshotLua(input)
	if err != nil {
		t.Fatalf("safety lua: %v", err)
	}
	for _, want := range []string{
		"evolution_factor(surface)",
		"get_evolution_factor_by_time(surface)",
		"get_evolution_factor_by_pollution(surface)",
		"get_evolution_factor_by_killing_spawners(surface)",
		"get_total_pollution",
		"get_kill_count_statistics(surface)",
		"evolutionFactor=",
		"evolutionByTime=",
		"evolutionByPollution=",
		"evolutionByKills=",
		"totalPollution=",
		"killCounts=",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("safety lua missing %q", want)
		}
	}
	// Existing response keys must stay untouched.
	for _, want := range []string{"enemies=entity_page", "damaged=entity_page", "playerAlerts=", "pollution=pollution_position"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("safety lua lost existing field %q", want)
		}
	}
}

func TestSafetySnapshotLuaValidation(t *testing.T) {
	if _, err := safetySnapshotLua(&SafetyInput{Surface: "", Force: "player", EnemyLimit: 1, DamagedLimit: 1, AlertLimit: 1}); err == nil {
		t.Fatal("empty surface must fail")
	}
	if _, err := safetySnapshotLua(&SafetyInput{Surface: "nauvis", Force: "", EnemyLimit: 1, DamagedLimit: 1, AlertLimit: 1}); err == nil {
		t.Fatal("empty force must fail")
	}
	if _, err := safetySnapshotLua(&SafetyInput{Surface: "nauvis", Force: "player", EnemyLimit: 0, DamagedLimit: 1, AlertLimit: 1}); err == nil {
		t.Fatal("out of range enemy limit must fail")
	}
}
