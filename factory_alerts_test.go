package main

import (
	"strings"
	"testing"
)

// everyEntityStatus is the full defines.entity_status value list of Factorio
// 2.0.77. Every value must be classified: either mapped to an AttentionKind or
// explicitly acknowledged as healthy, otherwise a real fault silently vanishes
// from factoryAlerts and from the briefing attention list.
var everyEntityStatus = []string{
	"working", "normal", "ghost", "broken", "no_power", "low_power", "no_fuel", "frozen",
	"disabled_by_control_behavior", "opened_by_circuit_network", "closed_by_circuit_network",
	"disabled_by_script", "marked_for_deconstruction", "paused", "not_plugged_in_electric_network",
	"networks_connected", "networks_disconnected", "charging", "discharging", "fully_charged",
	"out_of_logistic_network", "no_recipe", "no_ingredients", "no_input_fluid", "no_research_in_progress",
	"no_minable_resources", "not_connected_to_hub_or_pad", "low_input_fluid", "fluid_ingredient_shortage",
	"full_output", "not_enough_space_in_output", "full_burnt_result_output", "item_ingredient_shortage",
	"missing_required_fluid", "missing_science_packs", "waiting_for_source_items", "waiting_for_more_items",
	"waiting_for_space_in_destination", "preparing_rocket_for_launch", "waiting_to_launch_rocket",
	"waiting_for_space_in_platform_hub", "launching_rocket", "thrust_not_required", "on_the_way",
	"waiting_in_orbit", "waiting_at_stop", "waiting_for_rockets_to_arrive", "not_enough_thrust",
	"destination_stop_full", "no_path", "no_modules_to_transmit", "recharging_after_power_outage",
	"waiting_for_target_to_be_built", "waiting_for_train", "no_ammo", "low_temperature", "disabled",
	"turned_off_during_daytime", "not_connected_to_rail", "cant_divide_segments", "no_filter",
	"no_spot_seedable_by_inputs", "waiting_for_plants_to_grow", "computing_navigation",
	"pipeline_overextended", "recipe_not_researched", "recipe_is_parameter",
}

func TestFactoryAlertStatusCoverage(t *testing.T) {
	mapped := 0
	for _, status := range everyEntityStatus {
		kind, unhealthy := factoryAlertStatusKinds[status]
		if !unhealthy {
			if !factoryAlertHealthyStatuses[status] {
				t.Errorf("status %q is neither mapped to an AttentionKind nor listed as healthy", status)
			}
			continue
		}
		mapped++
		if _, ok := briefingSeverity[kind]; !ok {
			t.Errorf("status %q maps to kind %q without a severity", status, kind)
		}
		if _, ok := briefingSuggestedAction[kind]; !ok {
			t.Errorf("status %q maps to kind %q without a suggested action", status, kind)
		}
		found := false
		for _, ordered := range briefingKindOrder {
			if ordered == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("kind %q is missing from briefingKindOrder", kind)
		}
	}
	if mapped < 40 {
		t.Fatalf("only %d statuses map to a fault kind, expected the unhealthy statuses of 2.0.77", mapped)
	}
	for status := range factoryAlertStatusKinds {
		if factoryAlertHealthyStatuses[status] {
			t.Errorf("status %q is both mapped and healthy", status)
		}
	}
}

func TestFactoryAlertsLuaAndDecode(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":77,"surface":"nauvis","force":"player","area":{"leftTopX":-8,"leftTopY":-8,"rightBottomX":8,"rightBottomY":8},"scanned":40,"total":3,"alerts":[
		{"kind":"NO_POWER","severity":"CRITICAL","status":"no_power","name":"inserter","type":"inserter","position":{"x":47.5,"y":-48.5},"unitNumber":901,"direction":0,"suggestedAction":"connect the entity to a powered electric network"},
		{"kind":"NO_FUEL","severity":"CRITICAL","status":"no_fuel","name":"boiler","type":"boiler","position":{"x":10.5,"y":4.5},"unitNumber":902,"suggestedAction":"feed fuel to the entity"},
		{"kind":"NO_RESOURCES","severity":"WARNING","status":"no_minable_resources","name":"burner-mining-drill","type":"mining-drill","position":{"x":-48,"y":-30},"unitNumber":198,"direction":4,"suggestedAction":"move the drill onto resources or remove it"}],
		"attentionCounts":[{"kind":"NO_POWER","count":1},{"kind":"NO_FUEL","count":1},{"kind":"NO_RESOURCES","count":1}],
		"machines":[{"type":"mining-drill","total":2,"working":1,"idle":0,"noFuel":0,"noPower":0,"noIngredients":0,"outputFull":0,"other":1}]}`)
	area := &FactoryAreaInput{LeftTopX: -8, LeftTopY: -8, RightBottomX: 8, RightBottomY: 8}
	result, err := factoryAlertsQuery(r, &FactoryAlertsInput{
		Surface: "nauvis", Force: "player", Area: area, Limit: 100,
		Kinds: []AttentionKind{AttentionKindNoPower, AttentionKindNoFuel, AttentionKindNoResources},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		`local surface_name,force_name=` + luaString("nauvis") + `,` + luaString("player"),
		`local area,area_result={{-8,-8},{8,8}}`,
		`local limit=100`,
		"local kind_list={" + luaString("NO_POWER") + "," + luaString("NO_FUEL") + "," + luaString("NO_RESOURCES") + "}",
		"[" + luaString("no_power") + "]=" + luaString("NO_POWER") + ",",
		"[" + luaString("no_minable_resources") + "]=" + luaString("NO_RESOURCES") + ",",
		"[" + luaString("no_ammo") + "]=" + luaString("NO_AMMO") + ",",
		"local kind_info={[" + luaString("ENEMY") + "]={severity=" + luaString("CRITICAL") + ",rank=0,",
		"local kind_order={" + luaString("ENEMY") + "," + luaString("NO_POWER") + ",",
		`surface.find_entities_filtered{area=area,force=force}`,
		`pcall(function() return entity.status end)`,
		`local machine_types={["assembling-machine"]=true,furnace=true,["mining-drill"]=true,lab=true,["rocket-silo"]=true}`,
		`force.get_spawn_position(surface)`,
		`attentionCounts=count_list,machines=summaries`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("factoryAlerts Lua missing %q\n%s", want, lua)
		}
	}
	if result.Tick != 77 || result.Scanned != 40 || result.Total != 3 {
		t.Fatalf("header: %+v", result)
	}
	if len(result.Alerts) != 3 {
		t.Fatalf("alerts: %+v", result.Alerts)
	}
	first := result.Alerts[0]
	if first.Kind != AttentionKindNoPower || first.Severity != AttentionSeverityCritical || first.Status != "no_power" ||
		first.Name != "inserter" || first.Type != "inserter" || first.UnitNumber == nil || *first.UnitNumber != "901" ||
		first.Position == nil || first.Position.X != 47.5 || !strings.Contains(first.SuggestedAction, "powered electric network") {
		t.Fatalf("inserter alert: %+v", first)
	}
	drill := result.Alerts[2]
	if drill.Kind != AttentionKindNoResources || drill.Direction == nil || *drill.Direction != 4 {
		t.Fatalf("depleted drill alert: %+v", drill)
	}
	if len(result.AttentionCounts) != 3 || result.AttentionCounts[2].Kind != AttentionKindNoResources || result.AttentionCounts[2].Count != 1 {
		t.Fatalf("counts: %+v", result.AttentionCounts)
	}
	if len(result.Machines) != 1 || result.Machines[0].Type != "mining-drill" || result.Machines[0].Other != 1 {
		t.Fatalf("machine summaries: %+v", result.Machines)
	}
}

func TestFactoryAlertsWithoutAreaUsesDefaults(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","scanned":0,"total":0,"alerts":{},"attentionCounts":{},"machines":{}}`)
	result, err := factoryAlertsQuery(r, &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, "local area,area_result=nil,nil") {
		t.Fatalf("an omitted area must scan the surface:\n%s", lua)
	}
	if !strings.Contains(lua, "local kind_list={}") {
		t.Fatalf("no kinds filter must keep every kind:\n%s", lua)
	}
	if result.Alerts == nil || len(result.Alerts) != 0 || result.Machines == nil || len(result.Machines) != 0 ||
		result.AttentionCounts == nil || len(result.AttentionCounts) != 0 {
		t.Fatalf("empty tables must decode as empty lists: %+v", result)
	}
}

// The kinds filter must reject kinds this force-scoped status scan can never
// produce. ENEMY lives on enemy forces, so accepting it produced a silent
// no-op: total 0 with no error while the surface held hostiles.
func TestFactoryAlertsRejectsUnscannableKinds(t *testing.T) {
	scanKinds := factoryAlertScanKinds()
	reachable := map[AttentionKind]bool{}
	for _, kind := range scanKinds {
		reachable[kind] = true
	}
	for status, kind := range factoryAlertStatusKinds {
		if !reachable[kind] {
			t.Fatalf("status %q maps to %s, which the filter would reject", status, kind)
		}
	}
	unscannable := []AttentionKind{AttentionKindEnemy, AttentionKindGhostBacklog, AttentionKindLowStock}
	for _, kind := range unscannable {
		if reachable[kind] {
			t.Fatalf("%s must not be scannable", kind)
		}
	}

	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	for _, kind := range unscannable {
		_, err := factoryAlertsQuery(r, &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: 10, Kinds: []AttentionKind{kind}})
		if err == nil || !strings.Contains(err.Error(), string(kind)) || !strings.Contains(err.Error(), "only produces") || !strings.Contains(err.Error(), string(AttentionKindNoPower)) {
			t.Fatalf("kind %s: want a rejection naming it and the supported kinds, got %v", kind, err)
		}
	}
	// Every reachable kind passes validation and only then reaches the dial (a
	// dead address here), so the rejection above can only be about the filter.
	for _, kind := range scanKinds {
		_, err := factoryAlertsQuery(r, &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: 10, Kinds: []AttentionKind{kind}})
		if err != nil && strings.Contains(err.Error(), "unsupported alert kind") {
			t.Fatalf("kind %s must be accepted: %v", kind, err)
		}
	}
}

func TestFactoryAlertsValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	area := &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8}
	cases := []struct {
		name  string
		input *FactoryAlertsInput
		want  string
	}{
		{"nil", nil, "surface and force are required"},
		{"empty surface", &FactoryAlertsInput{Force: "player", Limit: 10}, "surface and force are required"},
		{"empty force", &FactoryAlertsInput{Surface: "nauvis", Limit: 10}, "surface and force are required"},
		{"zero limit", &FactoryAlertsInput{Surface: "nauvis", Force: "player"}, "limit must be between 1 and 10000"},
		{"huge limit", &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: maxPageSize + 1}, "limit must be between 1 and 10000"},
		{"huge area", &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: 10, Area: &FactoryAreaInput{RightBottomX: maxAreaTiles + 1, RightBottomY: 8}}, "no greater than 1024"},
		{"unknown kind", &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: 10, Area: area, Kinds: []AttentionKind{"MELTDOWN"}}, "unsupported alert kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := factoryAlertsQuery(r, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// The status scan must run against an engine stub: every status-bearing type is
// visited in one pass, non-crafting faults (inserter power, depleted drills,
// boilers) become rows, and crafting-machine summaries stay on the shared shape.
func TestFactoryAlertsLuaRuns(t *testing.T) {
	r := serveLuaFixture(t, "world_scan_engine.lua", nil, func(command string) bool {
		return strings.Contains(command, "local status_kind=")
	})
	result, err := factoryAlertsQuery(r, &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 6 || result.Total != 4 {
		t.Fatalf("scan counts: scanned=%d total=%d", result.Scanned, result.Total)
	}
	kinds := map[AttentionKind]int{}
	for _, count := range result.AttentionCounts {
		kinds[count.Kind] = count.Count
	}
	if kinds[AttentionKindNoPower] != 1 || kinds[AttentionKindNoFuel] != 2 || kinds[AttentionKindNoResources] != 1 {
		t.Fatalf("attention counts: %+v", result.AttentionCounts)
	}
	found := map[string]*FactoryAlert{}
	for _, alert := range result.Alerts {
		found[alert.Name] = alert
	}
	inserter, ok := found["inserter"]
	if !ok || inserter.Kind != AttentionKindNoPower || inserter.Position == nil || inserter.Position.X != 47.5 || inserter.Position.Y != -48.5 {
		t.Fatalf("unpowered inserter row: %+v", found)
	}
	drill, ok := found["electric-mining-drill"]
	if !ok || drill.Kind != AttentionKindNoResources || drill.Status != "no_minable_resources" || drill.UnitNumber == nil || *drill.UnitNumber != "198" {
		t.Fatalf("depleted drill row: %+v", found)
	}
	if boiler, ok := found["boiler"]; !ok || boiler.Kind != AttentionKindNoFuel || boiler.Direction == nil {
		t.Fatalf("unfuelled boiler row: %+v", found)
	}
	if len(result.Machines) != 3 {
		t.Fatalf("machine summaries: %+v", result.Machines)
	}
	for _, summary := range result.Machines {
		switch summary.Type {
		case "assembling-machine":
			if summary.Total != 1 || summary.Working != 1 {
				t.Fatalf("assembling summary: %+v", summary)
			}
		case "furnace":
			if summary.Total != 1 || summary.NoFuel != 1 {
				t.Fatalf("furnace summary: %+v", summary)
			}
		case "mining-drill":
			if summary.Total != 2 || summary.Working != 1 || summary.Other != 1 {
				t.Fatalf("drill summary: %+v", summary)
			}
		}
	}
}

// A kinds filter must keep the scan complete but only materialize matching rows.
func TestFactoryAlertsKindFilterRuns(t *testing.T) {
	r := serveLuaFixture(t, "world_scan_engine.lua", nil, func(command string) bool {
		return strings.Contains(command, "local status_kind=")
	})
	result, err := factoryAlertsQuery(r, &FactoryAlertsInput{
		Surface: "nauvis", Force: "player", Limit: 100,
		Kinds: []AttentionKind{AttentionKindNoResources},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Alerts) != 1 || result.Alerts[0].Kind != AttentionKindNoResources {
		t.Fatalf("kind-filtered rows: total=%d alerts=%+v", result.Total, result.Alerts)
	}
	if result.Scanned != 6 {
		t.Fatalf("kind filter must not narrow the scan: scanned=%d", result.Scanned)
	}
}
