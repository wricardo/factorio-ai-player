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
		{Type: SignalTypeVirtual, Name: "signal-B", Value: 1},
		{Type: SignalTypeItem, Name: "copper-plate", Value: 5},
		{Type: SignalTypeItem, Name: "iron-plate", Quality: stringPointer("normal"), Value: 3},
		{Type: SignalTypeItem, Name: "iron-plate", Value: 7},
	}
	sortCircuitSignals(signals)
	want := []string{"ITEM/copper-plate", "ITEM/iron-plate", "ITEM/iron-plate", "VIRTUAL/signal-B"}
	for i, s := range signals {
		got := string(s.Type) + "/" + s.Name
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
			in.Limit = maxPageSize + 1
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

func TestCircuitNetworkConnectorLua(t *testing.T) {
	input := CircuitNetworkInput{
		Target:    &EntityRefInput{Surface: stringPointer("nauvis"), UnitNumber: stringPointer("41")},
		Color:     WireColorRed,
		Connector: stringPointer("combinator_output_red"),
	}
	lua, err := circuitNetworkLua(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"local function signal_type_name(value)",
		`local connector_name=` + luaString("combinator_output_red"),
		`local color_name=` + luaString("red"),
		"color=" + luaString("RED"),
		"networkId=network_id",
		"networkCircuitCount=circuit_count",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("circuit network Lua missing %q:\n%s", want, lua)
		}
	}
	if strings.Contains(lua, "redConnections") || strings.Contains(lua, "greenConnections") {
		t.Fatal("the misleading connection counts must be replaced by the network circuit count")
	}

	// Without a connector the read falls back to circuit_<color>.
	fallback, err := circuitNetworkLua(CircuitNetworkInput{
		Target: &EntityRefInput{Surface: stringPointer("nauvis"), UnitNumber: stringPointer("41")},
		Color:  WireColorGreen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fallback, "local connector_name="+luaString("")) || !strings.Contains(fallback, `used_connector="circuit_"..color_name`) {
		t.Fatalf("the default connector must derive from the color:\n%s", fallback)
	}
}

func TestCircuitNetworksValidation(t *testing.T) {
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}
	valid := CircuitNetworksInput{Surface: "nauvis", Force: "player", Area: area, Limit: 500, MemberLimit: 100}
	for name, mutate := range map[string]func(*CircuitNetworksInput){
		"empty surface":  func(in *CircuitNetworksInput) { in.Surface = " " },
		"empty force":    func(in *CircuitNetworksInput) { in.Force = "" },
		"missing area":   func(in *CircuitNetworksInput) { in.Area = nil },
		"zero limit":     func(in *CircuitNetworksInput) { in.Limit = 0 },
		"excess limit":   func(in *CircuitNetworksInput) { in.Limit = maxCircuitScan + 1 },
		"zero members":   func(in *CircuitNetworksInput) { in.MemberLimit = 0 },
		"excess members": func(in *CircuitNetworksInput) { in.MemberLimit = maxCircuitMembers + 1 },
		"inverted area": func(in *CircuitNetworksInput) {
			in.Area = &FactoryAreaInput{LeftTopX: 2, RightBottomX: 1, RightBottomY: 1}
		},
	} {
		input := valid
		mutate(&input)
		if _, err := circuitNetworksQuery(nil, input); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
}

func TestCircuitNetworkPageNormalization(t *testing.T) {
	page, err := factoryDecode[CircuitNetworkPage](`{"tick":30,"surface":"nauvis","force":"player","scannedEntities":4,
"networks":[{"networkId":9,"color":"GREEN","truncated":true,"signals":[{"type":"VIRTUAL","name":"signal-B","value":1},{"type":"ITEM","name":"iron-plate","value":4}],
 "members":[{"name":"iron-chest","entityType":"container","position":{"x":1,"y":2},"connector":"circuit_green"},{"name":"inserter","entityType":"inserter","unitNumber":"7","position":{"x":3,"y":4},"connector":"circuit_green"}]},
{"networkId":2,"color":"RED"}]}`)
	if err != nil {
		t.Fatalf("factoryDecode: %v", err)
	}
	normalizeCircuitNetworkPage(page)
	if len(page.Networks) != 2 || page.Networks[0].NetworkID != 2 || page.Networks[1].NetworkID != 9 {
		t.Fatalf("networks must be sorted by id, got %+v", page.Networks)
	}
	empty := page.Networks[0]
	if empty.Signals == nil || len(empty.Signals) != 0 || empty.Members == nil || len(empty.Members) != 0 {
		t.Fatalf("empty lists must decode to empty slices, got %+v", empty)
	}
	wired := page.Networks[1]
	if wired.Color == nil || *wired.Color != WireColorGreen || !wired.Truncated {
		t.Fatalf("network = %+v", wired)
	}
	if len(wired.Signals) != 2 || wired.Signals[0].Name != "iron-plate" {
		t.Fatalf("signals must be sorted, got %+v", wired.Signals)
	}
	if len(wired.Members) != 2 || wired.Members[0].UnitNumber == nil || *wired.Members[0].UnitNumber != "7" || wired.Members[1].UnitNumber != nil {
		t.Fatalf("numbered members must sort before unnumbered ones, got %+v", wired.Members)
	}
}
