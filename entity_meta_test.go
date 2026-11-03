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

// An entity-ghost's own prototype is always "entity-ghost", so the ghost-only
// fields are the only way an inspection can say what the ghost would build.
// Every read stays guarded: ghost_prototype and get_recipe exist only on
// ghosts, and a ghost can be invalidated between the lookup and the read.
func TestEntityMetaGhostFieldsLua(t *testing.T) {
	for _, want := range []string{
		`if entity.type=="entity-ghost" then`,
		`pcall(function() return entity.ghost_prototype end)`,
		`pcall(function() return entity.ghost_name end)`,
		`meta.ghost_name=ghost_name`,
		`pcall(function() return entity.quality end)`,
		`meta.ghost_quality=quality_name`,
		`pcall(function() return entity.get_recipe() end)`,
		`meta.ghost_recipe=recipe_name`,
	} {
		if !strings.Contains(entityMetaFieldsLua, want) {
			t.Fatalf("ghost prototype reader missing %q", want)
		}
	}
}

// A ghost plan is unverifiable without the target prototype, so an inspection
// of an entity-ghost must report ghostName, ghostQuality and ghostRecipe next
// to the direction it already carries.
func TestInspectReportsGhostPrototype(t *testing.T) {
	r, recorder := serveScriptedRCON(t, []scriptRule{{
		marker: "inspect_snapshot(e,s)",
		response: `{"tick":2648603,"name":"entity-ghost","type":"entity-ghost",` +
			`"position":{"x":100.5,"y":-20.5},"surface":"nauvis","force":"player","unit_number":6867,` +
			`"direction":4,"ghost_name":"assembling-machine-1","ghost_quality":"uncommon",` +
			`"ghost_recipe":"iron-gear-wheel","inventories":{},"fluids":[],"to_be_deconstructed":false}`,
	}})
	character, unit, entity := "10815", "6867", "entity-ghost"
	result, err := inspect(r, InspectInput{
		Actor:  &ActorInput{Character: &character},
		Target: &EntityRefInput{Entity: &entity, UnitNumber: &unit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.GhostName == nil || *result.GhostName != "assembling-machine-1" {
		t.Fatalf("ghostName = %v, want the ghost's target prototype", result.GhostName)
	}
	if result.GhostQuality == nil || *result.GhostQuality != "uncommon" {
		t.Fatalf("ghostQuality = %v, want the ghost's target quality", result.GhostQuality)
	}
	if result.GhostRecipe == nil || *result.GhostRecipe != "iron-gear-wheel" {
		t.Fatalf("ghostRecipe = %v, want the recipe set on the ghost", result.GhostRecipe)
	}
	if result.Direction == nil || *result.Direction != 4 {
		t.Fatalf("direction = %v, want the ghost's engine direction", result.Direction)
	}
	if recorder.seen("entity.ghost_prototype") != 1 {
		t.Fatal("the inspect chunk must read entity.ghost_prototype")
	}
}

// Null on a real entity keeps a mixed ghost/entity list readable: a client
// detects a ghost by ghostName instead of comparing the entity-ghost name.
func TestInspectRealEntityReportsNoGhostFields(t *testing.T) {
	r, _ := serveScriptedRCON(t, []scriptRule{{
		marker: "inspect_snapshot(e,s)",
		response: `{"tick":2648610,"name":"assembling-machine-1","type":"assembling-machine",` +
			`"position":{"x":100.5,"y":-20.5},"surface":"nauvis","force":"player","unit_number":6868,` +
			`"direction":0,"recipe":"iron-gear-wheel","crafting_progress":0.25,` +
			`"inventories":{},"fluids":[],"to_be_deconstructed":false}`,
	}})
	character, unit, entity := "10815", "6868", "assembling-machine-1"
	result, err := inspect(r, InspectInput{
		Actor:  &ActorInput{Character: &character},
		Target: &EntityRefInput{Entity: &entity, UnitNumber: &unit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.GhostName != nil || result.GhostQuality != nil || result.GhostRecipe != nil {
		t.Fatalf("a real entity must not report ghost fields: %v %v %v", result.GhostName, result.GhostQuality, result.GhostRecipe)
	}
	if result.Recipe == nil || *result.Recipe != "iron-gear-wheel" {
		t.Fatalf("recipe = %v, want the machine recipe", result.Recipe)
	}
}

// A ghost removed in the same tick as the address must fail the whole
// inspection instead of returning a snapshot with empty ghost fields, so an
// audit cannot mistake a missing ghost for a real entity.
func TestInspectRemovedGhostReportsEngineError(t *testing.T) {
	r, _ := serveScriptedRCON(t, []scriptRule{{
		marker:   "inspect_snapshot(e,s)",
		response: `{"error":"entity not found"}`,
	}})
	character, unit, entity := "10815", "6867", "entity-ghost"
	_, err := inspect(r, InspectInput{
		Actor:  &ActorInput{Character: &character},
		Target: &EntityRefInput{Entity: &entity, UnitNumber: &unit},
	})
	if err == nil {
		t.Fatal("a removed ghost must fail the inspection")
	}
	if !strings.Contains(err.Error(), "entity not found") {
		t.Fatalf("error = %v, want the engine message", err)
	}
}
