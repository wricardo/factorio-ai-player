package main

import (
	"strings"
	"testing"
)

func TestEntityMetaNormalizesCombinatorSignals(t *testing.T) {
	// SignalID.type is nil for item signals when reading, so the reader must
	// normalize it: otherwise a parameters read cannot round-trip into
	// setCombinatorParameters, whose signal validation rejects an empty type.
	if !strings.Contains(entityMetaFieldsLua, `type=signal_type_name(signal.type)`) {
		t.Fatal("combinator signals must default to the item type when the engine omits it")
	}
	if strings.Contains(entityMetaFieldsLua, "meta.combinator_parameters={arithmetic=parameters}") ||
		strings.Contains(entityMetaFieldsLua, "meta.combinator_parameters={decider=parameters}") {
		t.Fatal("raw engine parameters must not be forwarded unnormalized")
	}
	for _, want := range []string{
		"entity_meta_arithmetic(parameters)",
		"entity_meta_decider(parameters)",
		"firstSignal=entity_meta_signal(parameters.first_signal)",
		"firstSignalNetworks=entity_meta_networks(parameters.first_signal_networks)",
		"outputSignal=entity_meta_signal(parameters.output_signal)",
		"constant=condition.constant",
		"compareType=condition.compare_type",
		"copyCountFromInput=output.copy_count_from_input~=false",
	} {
		if !strings.Contains(entityMetaFieldsLua, want) {
			t.Fatalf("combinator parameter reader missing %q", want)
		}
	}
	// The value-free GraphQL shape must not add per-slot values.
	if strings.Contains(entityMetaFieldsLua, "value=") && !strings.Contains(entityMetaFieldsLua, "value==nil") {
		t.Fatal("combinator parameters must not emit signal values")
	}
}

func TestCombinatorSignalLuaValidatesType(t *testing.T) {
	signal, err := combinatorSignalLua(&SignalInput{Name: "iron-plate"})
	if err != nil {
		t.Fatal(err)
	}
	if signal != "{type="+luaString("item")+",name="+luaString("iron-plate")+"}" {
		t.Fatalf("default signal = %s", signal)
	}
	kind := SignalType("virtual-signal")
	if _, err := combinatorSignalLua(&SignalInput{Name: "signal-A", Type: &kind}); err == nil {
		t.Fatal("the invalid virtual-signal type must be rejected")
	}
	quality := ""
	if _, err := combinatorSignalLua(&SignalInput{Name: "signal-A", Quality: &quality}); err == nil {
		t.Fatal("an empty quality must be rejected")
	}
}
