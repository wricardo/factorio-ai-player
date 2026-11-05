package main

import (
	"strings"
	"testing"
)

// transferBetween must resolve both entities in one Lua chunk, address the two
// typed inventories separately, and report the engine's own move figures.
func TestTransferBetweenDecodeAndLua(t *testing.T) {
	r, commands := serveLuaTwoCommands(t, `{"tick":93,"item":"iron-plate","quality":"normal","requested":50,"available":80,"capacity":200,"moved":50,"transferredStacks":1,"fromUnitNumber":"50","toUnitNumber":"51","fromName":"iron-chest","toName":"wooden-chest"}`)
	input := TransferBetweenInput{
		From:  &EntityRefInput{Surface: new("nauvis"), Entity: new("iron-chest"), UnitNumber: new("50")},
		To:    &EntityRefInput{Surface: new("nauvis"), Entity: new("wooden-chest"), UnitNumber: new("51")},
		Item:  "iron-plate",
		Count: 50,
	}
	result, err := transferBetween(r, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 50 || result.Requested != 50 || result.Available != 80 || result.Capacity != 200 {
		t.Fatalf("result: %+v", result)
	}
	if result.TransferredStacks != 1 || result.FromName != "iron-chest" || result.ToName != "wooden-chest" {
		t.Fatalf("result: %+v", result)
	}
	if result.FromUnitNumber == nil || *result.FromUnitNumber != "50" || result.ToUnitNumber == nil || *result.ToUnitNumber != "51" {
		t.Fatalf("unit numbers: %+v", result)
	}
	// Both inventories default to CHEST and are echoed as enum values, not keys.
	if result.FromInventory != InventoryKindChest || result.ToInventory != InventoryKindChest {
		t.Fatalf("inventory echo: %v %v", result.FromInventory, result.ToInventory)
	}
	lua := <-commands
	for _, want := range []string{
		`local from_surface_name=` + luaString("nauvis"),
		`local to_surface_name=` + luaString("nauvis"),
		`local from_unit=50`,
		`local to_unit=51`,
		`local source=inventories(from)[` + luaString("chest") + `]`,
		`local destination=inventories(to)[` + luaString("chest") + `]`,
		`local requested=50`,
		`destination.supports_bar()`,
		`transfer_stack`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("transferBetween Lua missing %q\n%s", want, lua)
		}
	}
}

// A transfer that moves nothing must come back with the reason instead of an
// error, and explicit inventory selectors must reach the Lua unchanged.
func TestTransferBetweenReasonAndInventorySelectors(t *testing.T) {
	r, commands := serveLuaTwoCommands(t, `{"tick":94,"item":"coal","quality":"normal","requested":10,"available":0,"capacity":50,"moved":0,"transferredStacks":0,"fromUnitNumber":"50","toUnitNumber":"51","fromName":"stone-furnace","toName":"iron-chest","reason":"NO_SOURCE"}`)
	from := InventoryKindOutput
	to := InventoryKindChest
	input := TransferBetweenInput{
		From:          &EntityRefInput{UnitNumber: new("50"), Entity: new("stone-furnace")},
		To:            &EntityRefInput{UnitNumber: new("51"), Entity: new("iron-chest")},
		Item:          "coal",
		Count:         10,
		FromInventory: &from,
		ToInventory:   &to,
	}
	result, err := transferBetween(r, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 0 || result.Reason == nil || *result.Reason != "NO_SOURCE" {
		t.Fatalf("a source that holds nothing must report its reason, not fail: %+v", result)
	}
	if result.FromInventory != InventoryKindOutput || result.ToInventory != InventoryKindChest {
		t.Fatalf("inventory echo: %v %v", result.FromInventory, result.ToInventory)
	}
	lua := <-commands
	if !strings.Contains(lua, `local source=inventories(from)[`+luaString("output")+`]`) ||
		!strings.Contains(lua, `local destination=inventories(to)[`+luaString("chest")+`]`) {
		t.Fatalf("inventory selectors must map to engine keys:\n%s", lua)
	}
}

// Invalid input must fail before any RCON dial.
func TestTransferBetweenValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	from := &EntityRefInput{UnitNumber: new("50"), Entity: new("iron-chest")}
	to := &EntityRefInput{UnitNumber: new("51"), Entity: new("wooden-chest")}
	badInventory := InventoryKind("BOGUS")
	cases := []struct {
		name  string
		input TransferBetweenInput
		want  string
	}{
		{"missing from", TransferBetweenInput{To: to, Item: "coal", Count: 1}, "from:"},
		{"missing to", TransferBetweenInput{From: from, Item: "coal", Count: 1}, "to:"},
		{"empty item", TransferBetweenInput{From: from, To: to, Count: 1}, "item must not be empty"},
		{"zero count", TransferBetweenInput{From: from, To: to, Item: "coal"}, "count must be a whole number"},
		{"fractional count", TransferBetweenInput{From: from, To: to, Item: "coal", Count: 1.5}, "count must be a whole number"},
		{"unknown fromInventory", TransferBetweenInput{From: from, To: to, Item: "coal", Count: 1, FromInventory: &badInventory}, "fromInventory:"},
	}
	for _, tc := range cases {
		input := tc.input
		if _, err := transferBetween(r, input); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want error containing %q, got %v", tc.name, tc.want, err)
		}
	}
}
