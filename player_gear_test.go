package main

import (
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
		return []*LogisticSectionInput{{Slots: []*LogisticRequestInput{{Index: 1, Name: "iron-plate", Min: &min, Max: 10}}}}
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
		"max below min":     func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Max = 0 },
		"negative slot max": func(in *ActorLogisticRequestsInput) { in.Sections[0].Slots[0].Max = -1 },
	} {
		in := base()
		mutate(&in)
		if _, err := actorLogisticRequestsLua(in); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
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
