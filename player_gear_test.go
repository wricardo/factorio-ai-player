package main

import (
	"io"
	"strings"
	"testing"
)

func TestPlayerSnapshotGearLua(t *testing.T) {
	for _, want := range []string{
		"c.grid", "grid.get_contents()", "c.get_requester_point()",
		"subject.equipment_grid", "subject.logistic_requests",
		"get_section(", "get_slot(", "min=", "max=",
	} {
		if !strings.Contains(luaActorGear, want) {
			t.Fatalf("luaActorGear must contain %q", want)
		}
	}
	if strings.Contains(luaActorGear, "settings_logistic(") {
		t.Fatal("the player snapshot must not pull in the whole settings reader")
	}
}

func TestPlayerSnapshotIncludesGear(t *testing.T) {
	for _, args := range [][]string{
		{"--player", "wricardo"},
		{"--character", "4"},
	} {
		lua, err := playerInspectLua("player", args, io.Discard)
		if err != nil {
			t.Fatalf("playerInspectLua(%v): %v", args, err)
		}
		for _, want := range []string{"grid.get_contents()", "get_requester_point()", "rcon.print(helpers.table_to_json(subject))"} {
			if !strings.Contains(lua, want) {
				t.Fatalf("player snapshot for %v must contain %q", args, want)
			}
		}
	}
}

func TestEquipGridValidation(t *testing.T) {
	valid := func() EquipGridInput {
		return EquipGridInput{Player: stringPointer("wricardo"), Item: "solar-panel-equipment", Count: 5, Direction: EquipmentDirectionToGrid}
	}
	for name, mutate := range map[string]func(*EquipGridInput){
		"no actor":          func(in *EquipGridInput) { in.Player = nil },
		"both actors":       func(in *EquipGridInput) { in.Character = stringPointer("4") },
		"zero character":    func(in *EquipGridInput) { in.Player = nil; in.Character = stringPointer("0") },
		"empty item":        func(in *EquipGridInput) { in.Item = "  " },
		"zero count":        func(in *EquipGridInput) { in.Count = 0 },
		"huge count":        func(in *EquipGridInput) { in.Count = 4294967296 },
		"unknown direction": func(in *EquipGridInput) { in.Direction = EquipmentDirection("SIDEWAYS") },
	} {
		in := valid()
		mutate(&in)
		if _, err := equipGridLua(in); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
}

func TestEquipGridLua(t *testing.T) {
	// 2.0.77: LuaEquipmentGrid.put/take act on exactly one equipment, so the
	// built Lua must insert and remove equipment one at a time.

	in := EquipGridInput{Character: stringPointer("4"), Item: "battery-equipment", Count: 2, Direction: EquipmentDirectionToGrid}
	lua, err := equipGridLua(in)
	if err != nil {
		t.Fatalf("equipGridLua: %v", err)
	}
	for _, want := range []string{
		"c.grid", "grid.put{name=gear_item,quality=gear_quality}",
		"main.remove{name=gear_item,quality=gear_quality,count=gear_requested}",
		"grid.count{name=gear_item,quality=gear_quality}", "grid.get_contents()", "gear_out.character=4",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("TO_GRID Lua must contain %q", want)
		}
	}

	in.Direction = EquipmentDirectionToMain
	lua, err = equipGridLua(in)
	if err != nil {
		t.Fatalf("equipGridLua(TO_MAIN): %v", err)
	}
	for _, want := range []string{
		"grid.find{name=gear_item,quality=gear_quality}",
		"grid.take{equipment=gear_equipment}",
		"main.insert{name=gear_item,quality=gear_quality,count=1}",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("TO_MAIN Lua must contain %q", want)
		}
	}
}

func TestSetActorLogisticRequestsValidation(t *testing.T) {
	section := func() []*LogisticSectionInput {
		min := 1
		return []*LogisticSectionInput{{Slots: []*LogisticRequestInput{{Index: 1, Name: "iron-plate", Min: &min, Max: 10}}}}
	}
	base := func() ActorLogisticRequestsInput {
		return ActorLogisticRequestsInput{Player: stringPointer("wricardo"), Sections: section()}
	}
	for name, mutate := range map[string]func(*ActorLogisticRequestsInput){
		"no actor":          func(in *ActorLogisticRequestsInput) { in.Player = nil },
		"both actors":       func(in *ActorLogisticRequestsInput) { in.Character = stringPointer("4") },
		"neither":           func(in *ActorLogisticRequestsInput) { in.Sections = nil },
		"both":              func(in *ActorLogisticRequestsInput) { in.Clear = true },
		"zero slot index":   func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Index = 0 },
		"empty slot name":   func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Name = "" },
		"max below min":     func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Max = 0 },
		"negative slot max": func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Max = -1 },
	} {
		in := base()
		mutate(&in)
		if _, err := actorLogisticRequestsLua(in); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
	if _, err := actorLogisticRequestsLua(ActorLogisticRequestsInput{Character: stringPointer("4"), Clear: true}); err != nil {
		t.Fatalf("clear only must be valid: %v", err)
	}
}

func TestSetActorLogisticRequestsLua(t *testing.T) {
	min := 1
	in := ActorLogisticRequestsInput{Character: stringPointer("4"), Sections: []*LogisticSectionInput{{
		Slots: []*LogisticRequestInput{{Index: 2, Name: "copper-plate", Min: &min, Max: 7}},
	}}}
	lua, err := actorLogisticRequestsLua(in)
	if err != nil {
		t.Fatalf("actorLogisticRequestsLua: %v", err)
	}
	for _, want := range []string{
		"c.get_requester_point()", "gear_point.remove_section(", "gear_point.add_section()",
		"gear_section.set_slot(2,{value={name=" + luaString("copper-plate") + ",quality=" + luaString("normal") + "},min=1,max=7})",
		"settings_logistic_point(gear_point)", "gear_out.character=4",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("mutation Lua must contain %q", want)
		}
	}
}

func TestSettingsLogisticMemberIndex(t *testing.T) {
	for _, want := range []string{
		"defines.logistic_member_index.rocket_silo_requester",
		"defines.logistic_member_index.logistic_container",
		"local function settings_logistic_member(e)",
	} {
		if !strings.Contains(settingsLua, want) {
			t.Fatalf("settingsLua must contain %q", want)
		}
	}
	if strings.Contains(settingsLua, "get_logistic_point(defines.logistic_member_index.logistic_container)") {
		t.Fatal("the logistic point index must be selected per entity type")
	}
	if !strings.Contains(settingsLua, `if e.type=="logistic-container" or e.type=="infinity-container" or e.type=="rocket-silo" then out.logistic=settings_logistic(e) end`) {
		t.Fatal("settings_sections must emit logistic sections for rocket silos too")
	}
	in := LogisticRequestsInput{
		Player:   stringPointer("wricardo"),
		Entity:   stringPointer("rocket-silo"),
		X:        floatPointer(10),
		Y:        floatPointer(10),
		Sections: []*LogisticSectionInput{{}},
	}
	target, err := settingsTargetFrom(in.Player, in.Character, in.Surface, in.Entity, in.UnitNumber, in.X, in.Y)
	if err != nil {
		t.Fatalf("settingsTargetFrom: %v", err)
	}
	prefix, err := target.lua()
	if err != nil {
		t.Fatalf("target lua: %v", err)
	}
	body := prefix + `local member_index=settings_logistic_member(e)`
	if !strings.Contains(body, "settings_logistic_member(e)") {
		t.Fatal("the write path must select the logistic member index by entity type")
	}
}

func TestActorSnapshotGearDecode(t *testing.T) {
	actor, err := factoryDecode[Actor](`{"tick":7,"character":4,"equipment_grid":[{"name":"solar-panel-equipment","count":2,"quality":"normal"}],"logistic_requests":{"sections":[{"index":1,"slots":[{"index":1,"name":"iron-plate","quality":"normal","min":1,"max":10}]}]}}`)
	if err != nil {
		t.Fatalf("factoryDecode: %v", err)
	}
	if len(actor.EquipmentGrid) != 1 || actor.EquipmentGrid[0].Name == nil || *actor.EquipmentGrid[0].Name != "solar-panel-equipment" {
		t.Fatalf("equipmentGrid = %+v", actor.EquipmentGrid)
	}
	if actor.LogisticRequests == nil || len(actor.LogisticRequests.Sections) != 1 {
		t.Fatalf("logisticRequests = %+v", actor.LogisticRequests)
	}
	slot := actor.LogisticRequests.Sections[0].Slots[0]
	if slot.Name == nil || *slot.Name != "iron-plate" || slot.Min != 1 || slot.Max != 10 {
		t.Fatalf("slot = %+v", slot)
	}

	empty, err := factoryDecode[Actor](`{"tick":7,"character":4,"equipment_grid":{},"logistic_requests":{"sections":{}}}`)
	if err != nil {
		t.Fatalf("factoryDecode empty: %v", err)
	}
	if empty.EquipmentGrid == nil || len(empty.EquipmentGrid) != 0 {
		t.Fatalf("an empty grid must decode to an empty list, got %+v", empty.EquipmentGrid)
	}
	if empty.LogisticRequests == nil || len(empty.LogisticRequests.Sections) != 0 {
		t.Fatalf("empty sections must decode to an empty list, got %+v", empty.LogisticRequests)
	}
}
