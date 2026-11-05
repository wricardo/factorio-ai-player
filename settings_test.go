package main

import (
	"math"
	"os/exec"
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
	if _, ok := circuitFieldKeys["enabled"]; ok {
		t.Fatal("enabled maps to the read-only disabled flag and must not be writable")
	}
	seen := map[string]bool{}
	for _, fields := range [][]circuitField{circuitBooleanFields, circuitIntFields, circuitSignalFields, circuitConditionFields} {
		for _, field := range fields {
			if seen[field.name] {
				t.Fatalf("duplicate circuit field %q", field.name)
			}
			seen[field.name] = true
			if len(field.keys) == 0 {
				t.Fatalf("field %q has no engine keys", field.name)
			}
		}
	}
	// The 2.0 fields the API review found missing must be writable.
	for _, name := range []string{
		"circuitConditionEnabled", "exclusiveModeOfOperation", "handReadMode", "stackControlSignal",
		"contentsReadMode", "readRecipeFinished", "recipeFinishedSignal", "workingSignal",
		"includeInCrafting", "includeFuel", "setTrainsLimit", "readTrainsCount", "setPriority",
		"redSignal", "greenSignal", "blueSignal", "rgbSignal",
	} {
		if !seen[name] {
			t.Fatalf("field %q must be writable through setCircuitBehavior", name)
		}
	}
	if got := circuitFieldKeys["colorMode"]; len(got) != 1 || got[0] != "color_mode" {
		t.Fatalf("colorMode keys = %v", got)
	}
}

func TestValidateCircuitInts(t *testing.T) {
	negative := -1
	if err := validateCircuitInts(map[string]*int{"handReadMode": &negative}); err == nil {
		t.Fatal("a negative read mode must fail")
	}
	mode := 0
	if err := validateCircuitInts(map[string]*int{"exclusiveModeOfOperation": &mode}); err != nil {
		t.Fatalf("zero is a valid exclusive mode: %v", err)
	}
}

func TestConditionLuaSecondSignalAndQuality(t *testing.T) {
	fluid := SignalTypeFluid
	lua, err := conditionLua(SignalConditionInput{
		Comparator:   ">=",
		FirstSignal:  &SignalInput{Type: &fluid, Name: "steam", Quality: stringPointer("uncommon")},
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

func TestSetSplitterFilterRejectsInertPriority(t *testing.T) {
	r := &Resolver{}
	name := "splitter"
	none := "none"
	_, err := setSplitterSettings(r, SplitterSettingsInput{
		Actor:          &ActorInput{Character: stringPointer("439")},
		Target:         &EntityRefInput{Entity: &name, Position: &PositionInput{X: 0, Y: 0}},
		Filter:         &ItemFilterInput{Name: "copper-ore"},
		OutputPriority: &none,
	})
	if err == nil || !strings.Contains(err.Error(), "inert") {
		t.Fatalf("filter with outputPriority none must fail, got %v", err)
	}
}

func TestSetInserterSettingsPickupLanes(t *testing.T) {
	r := &Resolver{}
	actor := ActorInput{Player: strPtr("wricardo")}
	target := EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")}
	no := false
	clear := true

	if _, err := setInserterSettings(r, InserterSettingsInput{Actor: &actor, Target: &target, Clear: &clear, PickupFromLeftLane: &no}); err == nil {
		t.Fatal("clear must not combine with pickup lane flags")
	}
	if _, err := setInserterSettings(r, InserterSettingsInput{Actor: &actor, Target: &target}); err == nil {
		t.Fatal("an empty settings request must be rejected")
	}
	// A lane-only request passes validation and reaches RCON; the fake address
	// fails before any game state is touched.
	if _, err := setInserterSettings(r, InserterSettingsInput{Actor: &actor, Target: &target, PickupFromLeftLane: &no}); err == nil {
		t.Fatal("expected a transport error after validation")
	}
}

func TestSetCircuitBehaviorRejectsReadOnlyEnabled(t *testing.T) {
	yes := true
	_, err := setCircuitBehavior(&Resolver{}, CircuitBehaviorInput{
		Actor:   &ActorInput{Player: strPtr("wricardo")},
		Target:  &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		Enabled: &yes,
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("enabled must be rejected as read-only, got %v", err)
	}
}

func TestLogisticSectionStatementsLimitAndOrder(t *testing.T) {
	slot := []*LogisticSectionInput{{Slots: []*LogisticRequestInput{{Index: 1, Name: "iron-plate", Max: intPointer(5)}}}}
	var sections []*LogisticSectionInput
	for range maxLogisticSections {
		sections = append(sections, slot[0])
	}
	check, writes, err := logisticSectionStatements(sections, "p", "s")
	if err != nil {
		t.Fatalf("%d sections must be accepted: %v", maxLogisticSections, err)
	}
	if strings.Count(writes, "do local s=") != maxLogisticSections || strings.Count(check, "prototypes.item[") != maxLogisticSections {
		t.Fatal("each section needs its own do-block and prototype check")
	}
	if _, _, err := logisticSectionStatements(append(sections, slot[0]), "p", "s"); err == nil {
		t.Fatal("more than the supported section count must be rejected")
	}
}

func strPtr(value string) *string { return &value }

func intPointer(value int) *int { return &value }

func TestCircuitApplyLuaProbesKeyExistence(t *testing.T) {
	// The apply prelude must probe each candidate key and surface the engine
	// message, so a rejected value is not reported as a missing feature.
	if !strings.Contains(circuitControlApplyLua, "local function field_exists(key)") {
		t.Fatal("apply must probe key existence before assigning")
	}
	if !strings.Contains(circuitControlApplyLua, `fail(name..": "..tostring(err))`) {
		t.Fatal("apply must fail with the engine message once a key exists")
	}
	if strings.Count(circuitControlApplyLua, "is not supported by this control behavior") != 1 {
		t.Fatal("the unsupported message must be reserved for the no-candidate-key case")
	}
	probe := strings.Index(circuitControlApplyLua, "field_exists(key)")
	assign := strings.Index(circuitControlApplyLua, "control[key]=value")
	if probe < 0 || assign < 0 || probe > assign {
		t.Fatalf("apply must probe before assigning:\n%s", circuitControlApplyLua)
	}
}

func TestSignalTypeName(t *testing.T) {
	for kind, name := range map[SignalType]string{
		SignalTypeItem:          "item",
		SignalTypeFluid:         "fluid",
		SignalTypeVirtual:       "virtual",
		SignalTypeEntity:        "entity",
		SignalTypeRecipe:        "recipe",
		SignalTypeSpaceLocation: "space-location",
		SignalTypeAsteroidChunk: "asteroid-chunk",
		SignalTypeQuality:       "quality",
	} {
		value := kind
		got, err := signalTypeName(&value)
		if err != nil || got != name {
			t.Fatalf("%s: got %q err %v, want %q", kind, got, err, name)
		}
	}
	if got, err := signalTypeName(nil); err != nil || got != "item" {
		t.Fatalf("an omitted type must default to item, got %q err %v", got, err)
	}
	invalid := SignalType("virtual-signal")
	if _, err := signalTypeName(&invalid); err == nil || !strings.Contains(err.Error(), "virtual-signal") {
		t.Fatalf("an unknown signal type must be rejected with its value, got %v", err)
	}
	if _, err := signalLua(SignalInput{Name: "signal-A", Type: &invalid}); err == nil {
		t.Fatal("signalLua must reject an unknown signal type")
	}
}

func TestLampColorModeName(t *testing.T) {
	for mode, name := range map[LampColorMode]string{
		LampColorModeColorMapping: "color_mapping",
		LampColorModeComponents:   "components",
		LampColorModePackedRgb:    "packed_rgb",
	} {
		value := mode
		got, err := lampColorModeName(&value)
		if err != nil || got != name {
			t.Fatalf("%s: got %q err %v, want %q", mode, got, err, name)
		}
	}
	if got, err := lampColorModeName(nil); err != nil || got != "" {
		t.Fatalf("an omitted mode must stay unset, got %q err %v", got, err)
	}
	invalid := LampColorMode("2")
	if _, err := lampColorModeName(&invalid); err == nil {
		t.Fatal("a numeric color mode must be rejected")
	}
}

func TestSetLampSettingsWritesDefinesColorMode(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":9,"surface":"nauvis","force":"player","name":"lamp","position":{"x":1.5,"y":2.5},"lamp":{"color":{"r":0,"g":0,"b":0,"a":1},"alwaysOn":false,"useColors":true,"colorMode":"COMPONENTS"}}`)
	mode := LampColorModeComponents
	result, err := setLampSettings(fake.resolver, LampSettingsInput{
		Actor:     &ActorInput{Player: strPtr("wricardo")},
		Target:    &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		ColorMode: &mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := fake.commands()[0]
	if !strings.Contains(lua, "control.color_mode=defines.control_behavior.lamp.color_mode["+luaString("components")+"]") {
		t.Fatalf("colorMode must be written through the engine defines:\n%s", lua)
	}
	if result.Lamp == nil || result.Lamp.ColorMode != LampColorModeComponents {
		t.Fatalf("lamp section = %+v", result.Lamp)
	}
}

func TestSetContainerBarAllowedSlots(t *testing.T) {
	response := `{"tick":9,"surface":"nauvis","force":"player","name":"wooden-chest","position":{"x":1.5,"y":2.5},"container":{"inventory":"chest","bar":4,"allowedSlots":3,"capacity":16}}`
	fake := newFakeRCON(t, response)
	allowed := 3
	result, err := setContainerBar(fake.resolver, ContainerBarInput{
		Actor:        &ActorInput{Player: strPtr("wricardo")},
		Target:       &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("202")},
		AllowedSlots: &allowed,
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := fake.commands()[0]
	if !strings.Contains(lua, "local allowed_slots=3") || !strings.Contains(lua, "e.set_inventory_bar(index,(3+1))") {
		t.Fatalf("allowedSlots must be written as the engine bar index:\n%s", lua)
	}
	if result.Container == nil || result.Container.Bar != 4 || result.Container.AllowedSlots == nil || *result.Container.AllowedSlots != 3 {
		t.Fatalf("container section = %+v", result.Container)
	}

	unlimited := newFakeRCON(t, `{"tick":9,"surface":"nauvis","force":"player","name":"wooden-chest","position":{"x":1.5,"y":2.5},"container":{"inventory":"chest","bar":0,"capacity":16}}`)
	page, err := setContainerBar(unlimited.resolver, ContainerBarInput{
		Actor:  &ActorInput{Player: strPtr("wricardo")},
		Target: &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("202")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lua := unlimited.commands()[0]; !strings.Contains(lua, "e.set_inventory_bar(index,nil)") {
		t.Fatalf("a null allowedSlots must clear the engine bar:\n%s", lua)
	}
	if page.Container == nil || page.Container.AllowedSlots != nil {
		t.Fatalf("an unlimited container must report a null allowedSlots, got %+v", page.Container)
	}

	negative := -1
	if _, err := setContainerBar(&Resolver{}, ContainerBarInput{
		Actor:        &ActorInput{Player: strPtr("wricardo")},
		Target:       &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		AllowedSlots: &negative,
	}); err == nil {
		t.Fatal("a negative allowedSlots must fail")
	}
	tooLarge := 65537
	if _, err := setContainerBar(&Resolver{}, ContainerBarInput{
		Actor:        &ActorInput{Player: strPtr("wricardo")},
		Target:       &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		AllowedSlots: &tooLarge,
	}); err == nil {
		t.Fatal("an oversized allowedSlots must fail")
	}
}

func TestConfigureInfinityContainerWritesFilters(t *testing.T) {
	response := `{"tick":9,"surface":"nauvis","force":"player","name":"infinity-chest","position":{"x":1.5,"y":2.5},"infinity":{"filters":[{"index":1,"name":"electric-mining-drill","quality":"normal","count":1000,"mode":"exactly"}],"removeUnfilteredItems":true}}`
	fake := newFakeRCON(t, response)
	remove := true
	result, err := configureInfinityContainer(fake.resolver, ConfigureInfinityContainerInput{
		Actor:  &ActorInput{Player: strPtr("wricardo")},
		Target: &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("77")},
		Filters: []*InfinityContainerFilterInput{{
			Item:  "electric-mining-drill",
			Count: 1000,
			Mode:  InfinityFilterModeExactly,
		}},
		RemoveUnfilteredItems: &remove,
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := fake.commands()[0]
	for _, want := range []string{
		`if e.type=="infinity-pipe" then`,
		`elseif e.type=="infinity-container" then`,
		"if not prototypes.item[",
		"if not prototypes.quality[",
		"filters[#filters+1]={index=1,name=",
		"count=1000,mode=",
		`filter.mode~="at-most"`,
		"e.infinity_container_filters=filters",
		"e.remove_unfiltered_items=true",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("Lua must contain %q:\n%s", want, lua)
		}
	}
	if result.Infinity == nil || len(result.Infinity.Filters) != 1 {
		t.Fatalf("infinity section = %+v", result.Infinity)
	}
	filter := result.Infinity.Filters[0]
	if filter.Name != "electric-mining-drill" || filter.Count != 1000 || filter.Mode != "exactly" {
		t.Fatalf("filter = %+v", filter)
	}

	cleared := newFakeRCON(t, `{"tick":9,"surface":"nauvis","force":"player","name":"infinity-chest","position":{"x":1.5,"y":2.5},"infinity":{"filters":[],"removeUnfilteredItems":false}}`)
	if _, err := configureInfinityContainer(cleared.resolver, ConfigureInfinityContainerInput{
		Actor:  &ActorInput{Player: strPtr("wricardo")},
		Target: &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("77")},
	}); err != nil {
		t.Fatal(err)
	}
	if lua := cleared.commands()[0]; strings.Contains(lua, "filters[#filters+1]") || !strings.Contains(lua, "e.infinity_container_filters=filters") {
		t.Fatalf("an empty filter list must assign an empty array once:\n%s", lua)
	}
}

func TestConfigureInfinityContainerValidation(t *testing.T) {
	base := ConfigureInfinityContainerInput{
		Actor:  &ActorInput{Player: strPtr("wricardo")},
		Target: &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("77")},
	}
	cases := []struct {
		name   string
		mutate func(*ConfigureInfinityContainerInput)
	}{
		{"blank item", func(in *ConfigureInfinityContainerInput) {
			in.Filters = []*InfinityContainerFilterInput{{Item: " ", Count: 1, Mode: InfinityFilterModeAtLeast}}
		}},
		{"fractional count", func(in *ConfigureInfinityContainerInput) {
			in.Filters = []*InfinityContainerFilterInput{{Item: "iron-plate", Count: 1.5, Mode: InfinityFilterModeAtLeast}}
		}},
		{"negative count", func(in *ConfigureInfinityContainerInput) {
			in.Filters = []*InfinityContainerFilterInput{{Item: "iron-plate", Count: -1, Mode: InfinityFilterModeAtLeast}}
		}},
	}
	for _, tc := range cases {
		in := base
		tc.mutate(&in)
		if _, err := configureInfinityContainer(&Resolver{}, in); err == nil {
			t.Fatalf("%s must fail before RCON", tc.name)
		}
	}
	if _, err := infinityFilterModeName(InfinityFilterMode("sometimes")); err == nil {
		t.Fatal("an unknown filter mode must be rejected")
	}
}

func TestInfinityFilterModeName(t *testing.T) {
	for mode, want := range map[InfinityFilterMode]string{
		InfinityFilterModeAtLeast: "at-least",
		InfinityFilterModeAtMost:  "at-most",
		InfinityFilterModeExactly: "exactly",
	} {
		if got, err := infinityFilterModeName(mode); err != nil || got != want {
			t.Fatalf("%s: got %q err %v, want %q", mode, got, err, want)
		}
	}
}

func TestLogisticSectionStatementsInfiniteMax(t *testing.T) {
	check, writes, err := logisticSectionStatements([]*LogisticSectionInput{{Slots: []*LogisticRequestInput{
		{Index: 1, Name: "iron-plate", Min: intPointer(200)},
	}}}, "p", "s")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(writes, ",max=") {
		t.Fatalf("an infinite request must omit the engine max:\n%s", writes)
	}
	if !strings.Contains(writes, "min=200") || !strings.Contains(writes, "slot 1: ") {
		t.Fatalf("the slot write must keep min and surface the engine message:\n%s", writes)
	}
	if !strings.Contains(check, "prototypes.item[") {
		t.Fatalf("the prototype check must still run before any write:\n%s", check)
	}
	if _, _, err := logisticSectionStatements([]*LogisticSectionInput{{Slots: []*LogisticRequestInput{
		{Index: 1, Name: "iron-plate", Min: intPointer(5), Max: intPointer(4)},
	}}}, "p", "s"); err == nil {
		t.Fatal("max below min must fail")
	}
	if _, _, err := logisticSectionStatements([]*LogisticSectionInput{{Slots: []*LogisticRequestInput{
		{Index: 1, Name: "iron-plate", Min: intPointer(-1)},
	}}}, "p", "s"); err == nil {
		t.Fatal("a negative min must fail")
	}
}

func TestSetTrainStopClearLimit(t *testing.T) {
	yes := true
	limit := 3
	if _, err := setTrainStop(&Resolver{}, TrainStopInput{
		Actor:            &ActorInput{Player: strPtr("wricardo")},
		Target:           &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		TrainsLimit:      &limit,
		ClearTrainsLimit: &yes,
	}); err == nil {
		t.Fatal("clearTrainsLimit must not combine with trainsLimit")
	}
	fake := newFakeRCON(t, `{"tick":9,"surface":"nauvis","force":"player","name":"train-stop","position":{"x":1.5,"y":2.5},"trainStop":{"backerName":"iron-drop","priority":0}}`)
	result, err := setTrainStop(fake.resolver, TrainStopInput{
		Actor:            &ActorInput{Player: strPtr("wricardo")},
		Target:           &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		ClearTrainsLimit: &yes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lua := fake.commands()[0]; !strings.Contains(lua, "e.trains_limit=nil") {
		t.Fatalf("clearing the limit must write nil:\n%s", lua)
	}
	if result.TrainStop == nil || result.TrainStop.TrainsLimit != nil {
		t.Fatalf("an unlimited stop must decode to a null trainsLimit, got %+v", result.TrainStop)
	}
	if summary := trainStopSettingsSummary(result); !strings.Contains(summary, "unlimited") {
		t.Fatalf("summary = %q, want an unlimited note", summary)
	}
}

func TestSetInserterSettingsFilterUseFlag(t *testing.T) {
	response := `{"tick":9,"surface":"nauvis","force":"player","name":"inserter","position":{"x":1.5,"y":2.5},"inserter":{"tick":9,"filterSlotCount":5,"filterSlots":[],"useFilters":false,"stackSizeOverride":0,"active":true,"pickupFromLeftLane":true,"pickupFromRightLane":true}}`
	empty := newFakeRCON(t, response)
	if _, err := setInserterSettings(empty.resolver, InserterSettingsInput{
		Actor:   &ActorInput{Player: strPtr("wricardo")},
		Target:  &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		Filters: []*InserterFilterInput{},
	}); err != nil {
		t.Fatal(err)
	}
	if lua := empty.commands()[0]; !strings.Contains(lua, "e.use_filters=false") {
		t.Fatalf("an empty filter list must switch filter use off:\n%s", lua)
	}

	one := newFakeRCON(t, response)
	if _, err := setInserterSettings(one.resolver, InserterSettingsInput{
		Actor:  &ActorInput{Player: strPtr("wricardo")},
		Target: &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("1")},
		Filters: []*InserterFilterInput{
			{Index: 1, Name: "iron-plate"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if lua := one.commands()[0]; !strings.Contains(lua, "e.use_filters=true") {
		t.Fatalf("a listed filter must switch filter use on:\n%s", lua)
	}
}

func TestSetConstantCombinatorLua(t *testing.T) {
	check, writes, err := constantCombinatorSectionStatements([]*ConstantCombinatorSectionInput{{
		Active:     boolPointer(true),
		Multiplier: floatPointer(2),
		Group:      strPtr("smelting"),
		Slots: []*ConstantCombinatorSlotInput{
			{Index: 1, Signal: &SignalInput{Name: "signal-A", Type: signalTypePointer(SignalTypeVirtual)}, Count: 5},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"behavior.add_section()", "section.active=true", "section.multiplier=2", "section.group=" + luaString("smelting"),
		"{value={type=" + luaString("virtual") + ",name=" + luaString("signal-A") + ",quality=" + luaString("normal") + "},min=5}",
	} {
		if !strings.Contains(writes, want) {
			t.Fatalf("constant combinator write missing %q:\n%s", want, writes)
		}
	}
	if !strings.Contains(check, "prototypes.quality["+luaString("normal")+"]") {
		t.Fatalf("quality prototypes must be checked first:\n%s", check)
	}

	for name, sections := range map[string][]*ConstantCombinatorSectionInput{
		"zero slot index": {{Slots: []*ConstantCombinatorSlotInput{{Index: 0, Signal: &SignalInput{Name: "signal-A"}, Count: 1}}}},
		"missing signal":  {{Slots: []*ConstantCombinatorSlotInput{{Index: 1, Count: 1}}}},
		"blank name":      {{Slots: []*ConstantCombinatorSlotInput{{Index: 1, Signal: &SignalInput{Name: "  "}, Count: 1}}}},
		"negative count":  {{Slots: []*ConstantCombinatorSlotInput{{Index: 1, Signal: &SignalInput{Name: "signal-A"}, Count: -1}}}},
	} {
		if _, _, err := constantCombinatorSectionStatements(sections); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}

	fake := newFakeRCON(t, `{"tick":9,"surface":"nauvis","force":"player","name":"constant-combinator","position":{"x":1.5,"y":2.5},"constantCombinator":{"enabled":true,"sections":[{"index":1,"active":true,"multiplier":1,"group":"","slots":[{"index":1,"signal":{"type":"VIRTUAL","name":"signal-A"},"count":5}]}]}}`)
	enabled := true
	result, err := setConstantCombinator(fake.resolver, SetConstantCombinatorInput{
		Actor:   &ActorInput{Player: strPtr("wricardo")},
		Target:  &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("5")},
		Enabled: &enabled,
		Sections: []*ConstantCombinatorSectionInput{{
			Slots: []*ConstantCombinatorSlotInput{{Index: 1, Signal: &SignalInput{Name: "signal-A", Type: signalTypePointer(SignalTypeVirtual)}, Count: 5}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := fake.commands()[0]
	if !strings.Contains(lua, "behavior.enabled=true") || !strings.Contains(lua, "for index=behavior.sections_count,1,-1 do behavior.remove_section(index) end") {
		t.Fatalf("replacing sections must enable and clear first:\n%s", lua)
	}
	if result.ConstantCombinator == nil || len(result.ConstantCombinator.Sections) != 1 || len(result.ConstantCombinator.Sections[0].Slots) != 1 {
		t.Fatalf("constantCombinator = %+v", result.ConstantCombinator)
	}
	if _, err := setConstantCombinator(&Resolver{}, SetConstantCombinatorInput{
		Actor:  &ActorInput{Player: strPtr("wricardo")},
		Target: &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("5")},
	}); err == nil {
		t.Fatal("an empty request must be rejected")
	}
	clear := true
	if _, err := setConstantCombinator(&Resolver{}, SetConstantCombinatorInput{
		Actor:         &ActorInput{Player: strPtr("wricardo")},
		Target:        &EntityRefInput{Surface: strPtr("nauvis"), UnitNumber: strPtr("5")},
		ClearSections: &clear,
		Sections:      []*ConstantCombinatorSectionInput{{}},
	}); err == nil {
		t.Fatal("clearSections must not combine with sections")
	}
}

func TestEntitySettingsQueryValidation(t *testing.T) {
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}
	valid := EntitySettingsInput{Surface: "nauvis", Force: "player", Area: area, Limit: 50}
	for name, mutate := range map[string]func(*EntitySettingsInput){
		"empty surface": func(in *EntitySettingsInput) { in.Surface = " " },
		"empty force":   func(in *EntitySettingsInput) { in.Force = "" },
		"missing area":  func(in *EntitySettingsInput) { in.Area = nil },
		"inverted area": func(in *EntitySettingsInput) {
			in.Area = &FactoryAreaInput{LeftTopX: 1, RightBottomX: 0, RightBottomY: 1}
		},
		"zero limit":        func(in *EntitySettingsInput) { in.Limit = 0 },
		"excess limit":      func(in *EntitySettingsInput) { in.Limit = maxPageSize + 1 },
		"unknown inventory": func(in *EntitySettingsInput) { in.Inventory = strPtr("cargo") },
		"blank name":        func(in *EntitySettingsInput) { in.Names = []string{"iron-chest", " "} },
		"blank type":        func(in *EntitySettingsInput) { in.Types = []string{""} },
	} {
		input := valid
		mutate(&input)
		if _, err := entitySettingsQuery(nil, input); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
	if got := luaStringList(nil); got != "nil" {
		t.Fatalf("an empty filter must render as nil, got %s", got)
	}
	want := "{" + luaString("iron-chest") + "," + luaString("steel-chest") + "}"
	if got := luaStringList([]string{"iron-chest", "steel-chest"}); got != want {
		t.Fatalf("filter list = %s, want %s", got, want)
	}
}

func TestFactoryDecodeSettingsReviewFields(t *testing.T) {
	result, err := factoryDecode[SettingsResult](`{"tick":12,"surface":"nauvis","force":"player","name":"wooden-chest","position":{"x":1.5,"y":2.5},
"container":{"inventory":"chest","bar":4,"allowedSlots":3,"capacity":16},
"logistic":{"sections":[{"index":1,"active":true,"multiplier":1,"group":"","slots":[{"index":1,"name":"iron-plate","min":200},{"index":3,"name":"copper-plate","quality":"normal","min":1,"max":9}]}]},
"trainStop":{"backerName":"iron-drop","priority":4},
"circuit":{"behaviorType":1,"circuitCondition":{"comparator":">","constant":5,"firstSignal":{"type":"ITEM","name":"iron-plate"}},
  "handReadMode":1,"stackControlSignal":{"type":"VIRTUAL","name":"signal-A"},"contentsReadMode":2,
  "readRecipeFinished":true,"workingSignal":{"type":"VIRTUAL","name":"signal-B"},"includeFuel":true,
  "setTrainsLimit":true,"trainsCountSignal":{"type":"VIRTUAL","name":"signal-C"},"setPriority":false,
  "redSignal":{"type":"VIRTUAL","name":"signal-D"},"colorMode":"COMPONENTS",
  "circuitConditionEnabled":true,"exclusiveModeOfOperation":2},
"constantCombinator":{"enabled":true,"sections":[{"index":1,"active":true,"multiplier":1,"group":"","slots":[{"index":1,"signal":{"type":"VIRTUAL","name":"signal-A"},"count":5}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Container == nil || result.Container.AllowedSlots == nil || *result.Container.AllowedSlots != 3 {
		t.Fatalf("container = %+v", result.Container)
	}
	if result.TrainStop == nil || result.TrainStop.TrainsLimit != nil {
		t.Fatalf("an absent trainsLimit must decode to null, got %+v", result.TrainStop)
	}
	slots := result.Logistic.Sections[0].Slots
	if len(slots) != 2 || slots[0].Max != nil || slots[0].Min != 200 {
		t.Fatalf("an infinite slot must keep a null max, got %+v", slots)
	}
	if slots[1].Index != 3 || slots[1].Max == nil || *slots[1].Max != 9 {
		t.Fatalf("slot gaps must be preserved and finite maxima decoded, got %+v", slots[1])
	}
	circuit := result.Circuit
	if circuit == nil || circuit.HandReadMode == nil || *circuit.HandReadMode != 1 || circuit.ContentsReadMode == nil || *circuit.ContentsReadMode != 2 {
		t.Fatalf("circuit modes = %+v", circuit)
	}
	if circuit.CircuitCondition == nil || circuit.CircuitCondition.Constant == nil || *circuit.CircuitCondition.Constant != 5 {
		t.Fatalf("circuitCondition = %+v", circuit.CircuitCondition)
	}
	if circuit.StackControlSignal == nil || circuit.StackControlSignal.Name != "signal-A" || circuit.StackControlSignal.Type != SignalTypeVirtual {
		t.Fatalf("stackControlSignal = %+v", circuit.StackControlSignal)
	}
	if circuit.ColorMode == nil || *circuit.ColorMode != LampColorModeComponents || circuit.ExclusiveModeOfOperation == nil || *circuit.ExclusiveModeOfOperation != 2 {
		t.Fatalf("lamp/container circuit fields = %+v", circuit)
	}
	if result.ConstantCombinator == nil || !result.ConstantCombinator.Enabled {
		t.Fatalf("constantCombinator = %+v", result.ConstantCombinator)
	}
	if result.ConstantCombinator.Sections[0].Slots[0].Count != 5 {
		t.Fatalf("constant combinator slot = %+v", result.ConstantCombinator.Sections[0].Slots[0])
	}
}

func boolPointer(value bool) *bool { return &value }

func signalTypePointer(value SignalType) *SignalType { return &value }

// Signal types from the engine are lowercase ("fluid", "space-location") while
// the GraphQL SignalType enum is uppercase with underscores. Every serialized
// signal must be normalized, or a fluid circuit condition cannot be decoded at
// all: an inserter condition with sulfuric-acid failed with "fluid is not a
// valid SignalType" before this was covered.
func TestSignalTypeNormalizationInLuaReaders(t *testing.T) {
	interpreter, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	fixture := `
local function slot(value,min,max)
  return {value=value,min=min,max=max}
end
local function section(slots)
  return {filters_count=#slots,active=true,multiplier=1,group="",
    get_slot=function(i) local s=slots[i]; if not s then return nil end; return s end}
end
local sections={
  section({slot({name="sulfuric-acid",type="fluid",quality="normal"},0,100)}),
  section({slot({name="iron-plate",quality="normal"},5,10)}),
  section({slot({name="calcite",type="space-location",quality="normal"},1,1)}),
}
local logistic={sections_count=#sections,get_section=function(i) return sections[i] end}
`
	checks := `
local serialized=settings_logistic_sections(logistic)
assert(serialized[1].slots[1].type=="FLUID", "a fluid signal must normalize to FLUID, got "..tostring(serialized[1].slots[1].type))
assert(serialized[2].slots[1].type=="ITEM", "a signal without a type must default to ITEM, got "..tostring(serialized[2].slots[1].type))
assert(serialized[3].slots[1].type=="SPACE_LOCATION", "a hyphenated engine type must become SPACE_LOCATION, got "..tostring(serialized[3].slots[1].type))

local fluid=inserter_signal({name="sulfuric-acid",type="fluid"})
assert(fluid.type=="FLUID", "inserter_signal must normalize the engine type, got "..tostring(fluid.type))
assert(fluid.quality==nil, "inserter_signal must not invent a quality")
local item=inserter_signal({name="iron-plate"})
assert(item.type=="ITEM", "inserter_signal must default a missing type to ITEM, got "..tostring(item.type))
assert(inserter_signal(nil)==nil, "an absent signal must stay nil")
`
	cmd := exec.Command(interpreter, "-")
	cmd.Stdin = strings.NewReader(fixture + luaSignalTypeName + luaInserterSettingsBody + settingsLogisticSectionsLua + checks)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("signal type normalization: %v\n%s", err, out)
	}
}

// The composed read/write chunk must define signal_type_name before the bodies
// that call it, because Lua locals are lexically scoped: a body spliced first
// would resolve a nil global and fail at read time.
func TestSettingsLuaDefinesSignalTypeNameFirst(t *testing.T) {
	helper := strings.Index(settingsLua, "local function signal_type_name(value)")
	body := strings.Index(settingsLua, "local function inserter_signal(s)")
	sections := strings.Index(settingsLua, "local function settings_logistic_sections(logistic)")
	if helper < 0 || body < 0 || sections < 0 {
		t.Fatalf("settingsLua must contain the signal helper and both readers")
	}
	if helper > body || helper > sections {
		t.Fatalf("signal_type_name must be defined before its callers (helper=%d body=%d sections=%d)", helper, body, sections)
	}
}
