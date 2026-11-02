package main

import (
	"strings"
	"testing"
)

// Inspecting an inserter must return that inserter's own pickup and drop, which
// the neighbour scan cannot see because it keeps arms targeting the inspected
// entity.
func TestInspectionLogisticsReportsInspectedInserter(t *testing.T) {
	response := `{"tick":5,"surface":"nauvis","inserters":[
{"inserter":{"name":"inserter","type":"inserter","surface":"nauvis","position":{"x":1.5,"y":2.5},"unitNumber":"14","status":"waiting_for_space_in_destination"},
 "pickup":{"name":"assembling-machine-1","type":"assembling-machine","surface":"nauvis","position":{"x":1.5,"y":0.5},"unitNumber":"7","lanes":{}},
 "drop":{"name":"iron-chest","type":"container","surface":"nauvis","position":{"x":1.5,"y":4.5},"unitNumber":"8","lanes":{}}}]}`
	r, commands := serveLuaOnce(t, response)
	result, err := inspectionLogistics(r, &EntityInspection{
		Surface: "nauvis", Force: "player", Name: new("inserter"), Position: &Position{X: 1.5, Y: 2.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.Inserters) != 1 {
		t.Fatalf("inserters: %+v", result)
	}
	entry := result.Inserters[0]
	if entry.Inserter == nil || entry.Inserter.UnitNumber == nil || *entry.Inserter.UnitNumber != "14" {
		t.Fatalf("the inspected inserter must be reported: %+v", entry.Inserter)
	}
	if entry.Inserter.Status == nil || *entry.Inserter.Status != "waiting_for_space_in_destination" {
		t.Fatalf("the inspected inserter must carry its status: %+v", entry.Inserter)
	}
	if entry.Pickup == nil || entry.Pickup.Name != "assembling-machine-1" || entry.Drop == nil || entry.Drop.Name != "iron-chest" {
		t.Fatalf("pickup/drop: %+v %+v", entry.Pickup, entry.Drop)
	}
	lua := <-commands
	for _, want := range []string{
		"local surface_name,force_name,entity_name=" + luaString("nauvis") + "," + luaString("player") + "," + luaString("inserter"),
		`if target.type=="inserter" then`,
		`self_entry={inserter=endpoint(target),pickup=endpoint(pickup),drop=endpoint(drop)}`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("inspection Lua missing %q\n%s", want, lua)
		}
	}
}
