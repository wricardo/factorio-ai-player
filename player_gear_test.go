package main

import (
	"strings"
	"testing"
)

func TestEquipGridValidation(t *testing.T) {
	valid := func() EquipGridInput {
		return EquipGridInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Item: "solar-panel-equipment", Count: 5, Direction: EquipmentDirectionToGrid}
	}
	for name, mutate := range map[string]func(*EquipGridInput){
		"no actor":          func(in *EquipGridInput) { in.Actor = nil },
		"both actors":       func(in *EquipGridInput) { in.Actor.Character = stringPointer("4") },
		"zero character":    func(in *EquipGridInput) { in.Actor = &ActorInput{Character: stringPointer("0")} },
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

func TestSetActorLogisticRequestsValidation(t *testing.T) {
	section := func() []*LogisticSectionInput {
		min := 1
		return []*LogisticSectionInput{{Slots: []*LogisticRequestInput{{Index: 1, Name: "iron-plate", Min: &min, Max: intPointer(10)}}}}
	}
	base := func() ActorLogisticRequestsInput {
		return ActorLogisticRequestsInput{Actor: &ActorInput{Player: stringPointer("wricardo")}, Sections: section()}
	}
	for name, mutate := range map[string]func(*ActorLogisticRequestsInput){
		"no actor":          func(in *ActorLogisticRequestsInput) { in.Actor = nil },
		"both actors":       func(in *ActorLogisticRequestsInput) { in.Actor.Character = stringPointer("4") },
		"neither":           func(in *ActorLogisticRequestsInput) { in.Sections = nil },
		"both":              func(in *ActorLogisticRequestsInput) { in.Clear = true },
		"zero slot index":   func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Index = 0 },
		"empty slot name":   func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Name = "" },
		"max below min":     func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Max = intPointer(0) },
		"negative slot max": func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Max = intPointer(-1) },
	} {
		in := base()
		mutate(&in)
		if _, err := actorLogisticRequestsLua(in); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
	// An omitted max is the natural way to express an infinite request and must
	// stay valid, writing no engine max at all.
	infinite := ActorLogisticRequestsInput{Actor: &ActorInput{Player: stringPointer("wricardo")},
		Sections: []*LogisticSectionInput{{Slots: []*LogisticRequestInput{{Index: 1, Name: "iron-plate"}}}}}
	lua, err := actorLogisticRequestsLua(infinite)
	if err != nil {
		t.Fatalf("an infinite request must be accepted: %v", err)
	}
	if !strings.Contains(lua, "min=0}") || strings.Contains(lua, "min=0,max=") {
		t.Fatalf("an infinite request must omit the engine max:\n%s", lua)
	}
	if _, err := actorLogisticRequestsLua(ActorLogisticRequestsInput{Actor: &ActorInput{Character: stringPointer("4")}, Clear: true}); err != nil {
		t.Fatalf("clear only must be valid: %v", err)
	}
}

func TestActorSnapshotGearDecode(t *testing.T) {
	actor, err := factoryDecode[Actor](`{"tick":7,"character":4,"equipment_grid":[{"name":"solar-panel-equipment","count":2,"quality":"normal"}],"logistic_requests":{"sections":[{"index":1,"slots":[{"index":1,"name":"iron-plate","quality":"normal","min":1,"max":10}]}]}}`)
	if err != nil {
		t.Fatalf("factoryDecode: %v", err)
	}
	if len(actor.EquipmentGrid) != 1 || actor.EquipmentGrid[0].Name != "solar-panel-equipment" {
		t.Fatalf("equipmentGrid = %+v", actor.EquipmentGrid)
	}
	if actor.LogisticRequests == nil || len(actor.LogisticRequests.Sections) != 1 {
		t.Fatalf("logisticRequests = %+v", actor.LogisticRequests)
	}
	slot := actor.LogisticRequests.Sections[0].Slots[0]
	if slot.Name == nil || *slot.Name != "iron-plate" || slot.Min != 1 || slot.Max == nil || *slot.Max != 10 {
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

	// Personal requests default to an infinite max and may have gaps: both must
	// survive decoding instead of ending the section or reading as max 0.
	gaps, err := factoryDecode[Actor](`{"tick":7,"character":4,"logistic_requests":{"sections":[{"index":1,"active":true,"multiplier":1,"group":"","slots":[{"index":1,"name":"iron-plate","quality":"normal","min":200},{"index":3,"name":"copper-plate","min":1,"max":9}]}]}}`)
	if err != nil {
		t.Fatalf("factoryDecode gaps: %v", err)
	}
	slots := gaps.LogisticRequests.Sections[0].Slots
	if len(slots) != 2 {
		t.Fatalf("a request after an empty slot must be kept, got %+v", slots)
	}
	if slots[0].Max != nil {
		t.Fatalf("an infinite request must decode to a null max, got %+v", slots[0])
	}
	if slots[1].Index != 3 || slots[1].Max == nil || *slots[1].Max != 9 {
		t.Fatalf("slot after a gap = %+v", slots[1])
	}
}

func TestActorGearReusesLogisticReader(t *testing.T) {
	for _, want := range []string{"local function settings_logistic_sections(logistic)", "section.filters_count", "max=filter.max"} {
		if !strings.Contains(luaActorGear, want) {
			t.Fatalf("the actor snapshot must reuse the shared logistic reader, missing %q", want)
		}
	}
	if strings.Contains(luaActorGear, "if gear_name==nil then break end") {
		t.Fatal("the actor snapshot must not stop at the first empty slot")
	}
}
