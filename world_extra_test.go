package main

import (
	"strings"
	"testing"
)

func TestSurfacesForceValidation(t *testing.T) {
	if _, err := surfaceQueryLua(""); err == nil {
		t.Fatal("empty force must be rejected")
	}
}

func TestCircuitNetworkValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input CircuitNetworkInput
		want  string
	}{
		{"unknown color", CircuitNetworkInput{Target: &EntityRefInput{Entity: stringPointer("iron-chest"), Position: &PositionInput{X: 1, Y: 2}}, Color: WireColor("BLUE")}, "color"},
		{"empty color", CircuitNetworkInput{Target: &EntityRefInput{Entity: stringPointer("iron-chest"), Position: &PositionInput{X: 1, Y: 2}}, Color: WireColor("")}, "color"},
		{"no address", CircuitNetworkInput{Target: &EntityRefInput{Surface: stringPointer("nauvis")}, Color: WireColorRed}, "entity name or unitNumber"},
		{"unit and coordinates", CircuitNetworkInput{Target: &EntityRefInput{UnitNumber: stringPointer("7"), Position: &PositionInput{X: 1, Y: 2}}, Color: WireColorRed}, "not both"},
		{"name without coordinates", CircuitNetworkInput{Target: &EntityRefInput{Entity: stringPointer("iron-chest")}, Color: WireColorRed}, "finite"},
		{"bad unit number", CircuitNetworkInput{Target: &EntityRefInput{UnitNumber: stringPointer("0")}, Color: WireColorRed}, "unit number"},
	} {
		if _, err := circuitNetworkLua(tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want mention of %q", tc.name, err, tc.want)
		}
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
