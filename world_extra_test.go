package main

import (
	"math"
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

func TestChartTagInputValidation(t *testing.T) {
	validList := ChartTagsInput{Surface: "nauvis", Force: "player", Limit: 100}
	for name, mutate := range map[string]func(*ChartTagsInput){
		"empty surface": func(in *ChartTagsInput) { in.Surface = "" },
		"empty force":   func(in *ChartTagsInput) { in.Force = "" },
		"negative offset": func(in *ChartTagsInput) {
			in.Offset = -1
		},
		"zero limit": func(in *ChartTagsInput) { in.Limit = 0 },
		"excess limit": func(in *ChartTagsInput) {
			in.Limit = 1001
		},
		"inverted area": func(in *ChartTagsInput) {
			in.Area = &FactoryAreaInput{LeftTopX: 1, LeftTopY: 0, RightBottomX: 0, RightBottomY: 1}
		},
		"nonfinite area": func(in *ChartTagsInput) {
			in.Area = &FactoryAreaInput{LeftTopX: math.NaN(), LeftTopY: 0, RightBottomX: 1, RightBottomY: 1}
		},
	} {
		t.Run("list "+name, func(t *testing.T) {
			input := validList
			mutate(&input)
			if _, err := chartTagsQuery(nil, input); err == nil {
				t.Fatal("invalid chart tag list input accepted")
			}
		})
	}

	validAdd := AddChartTagInput{
		Surface: "nauvis", Force: "player",
		Position: &PositionInput{X: 1, Y: 2}, Text: stringPointer("marker"),
	}
	for name, mutate := range map[string]func(*AddChartTagInput){
		"missing position": func(in *AddChartTagInput) { in.Position = nil },
		"nonfinite position": func(in *AddChartTagInput) {
			in.Position.X = math.Inf(1)
		},
		"no text or icon": func(in *AddChartTagInput) {
			in.Text = nil
		},
		"blank text": func(in *AddChartTagInput) {
			in.Text = stringPointer(" \n ")
		},
		"empty icon": func(in *AddChartTagInput) {
			in.Text = nil
			in.Icon = &SignalInput{}
		},
	} {
		t.Run("add "+name, func(t *testing.T) {
			input := validAdd
			position := *validAdd.Position
			input.Position = &position
			mutate(&input)
			if _, err := addChartTag(nil, input); err == nil {
				t.Fatal("invalid chart tag add input accepted")
			}
		})
	}

	for _, tagNumber := range []string{"", "-1", "abc", "4294967296"} {
		t.Run("remove "+tagNumber, func(t *testing.T) {
			input := RemoveChartTagInput{Surface: "nauvis", Force: "player", TagNumber: tagNumber}
			if _, err := removeChartTag(nil, input); err == nil {
				t.Fatal("invalid chart tag number accepted")
			}
		})
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
