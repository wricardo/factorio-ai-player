package main

import (
	"strings"
	"testing"
)

func batchRefs(units ...string) []*EntityRefInput {
	refs := make([]*EntityRefInput, len(units))
	for i, unit := range units {
		refs[i] = &EntityRefInput{Surface: new("nauvis"), Entity: new("stone-furnace"), UnitNumber: new(unit)}
	}
	return refs
}

// fuelMany must run every target in one RCON call, report typed per-target
// failures beside successes, and decode a numeric unit number as a string.
func TestFuelManyPartialSuccess(t *testing.T) {
	r, commands := serveLuaTwoCommands(t, `{"tick":90,"item":"coal","quality":"normal","fueled":1,"failed":2,"moved":5,"results":[{"index":1,"entity":"stone-furnace","unitNumber":41,"position":{"x":1.5,"y":2.5},"requested":5,"moved":5}],"errors":[{"index":2,"code":"INVENTORY","message":"fuel inventory cannot accept requested items","target":{"surface":"nauvis","entity":"stone-furnace","unitNumber":42,"position":{"x":3.5,"y":2.5}}},{"index":3,"code":"LOOKUP","message":"entity not found by unit number on that surface","target":{"surface":"nauvis","entity":"stone-furnace","unitNumber":43}}]}`)
	input := FuelManyInput{Actor: &ActorInput{Character: new("7")}, Targets: batchRefs("41", "42", "43"), Item: "coal", CountPerTarget: 5}
	result, err := fuelMany(r, input)
	result, err = setActorResult(result, input.Actor, err)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fueled != 1 || result.Failed != 2 || result.Moved != 5 || len(result.Results) != 1 || result.Results[0].UnitNumber == nil || *result.Results[0].UnitNumber != "41" {
		t.Fatalf("result: %+v", result)
	}
	if len(result.Errors) != 2 || result.Errors[0].Code != BatchErrorCodeInventory || result.Errors[0].Index != 2 || result.Errors[1].Code != BatchErrorCodeLookup {
		t.Fatalf("errors: %+v", result.Errors)
	}
	if result.Actor == nil || result.Actor.Character == nil || *result.Actor.Character != "7" {
		t.Fatalf("actor identity: %+v", result.Actor)
	}
	lua := <-commands
	for _, want := range []string{
		`local unit=7`,
		`local item=` + luaString("coal") + `; local quality=` + luaString("normal"),
		`{surface=` + luaString("nauvis") + `,unit=41,name=` + luaString("stone-furnace"),
		`local count=5`,
		// countPerTarget mode leaves topUpTo disabled and asks for the count.
		`local top_up=-1`,
		`local requested=count`,
		// topUpTo mode asks for the shortfall against the target's own fuel.
		`requested=top_up-fuel.get_item_count(stack)`,
		`skipped=skipped+1`,
		`elseif inv.get_item_count(stack)<requested then fail_at(i,"INVENTORY","actor lacks requested fuel"`,
		`elseif fuel.get_insertable_count(stack)<requested then fail_at(i,"INVENTORY","fuel inventory cannot accept requested items"`,
		`if inserted<removed then inv.insert{`,
		`if false then fail_at(i,"REACH"`,
		// one unit->entity index per surface and prototype name, not per target
		`local function find_batch_entity(ref)`,
		`local key=ref.surface..string.char(31)..ref.name`,
		`local e,err=find_batch_entity(ref)`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("fuelMany Lua missing %q\n%s", want, lua)
		}
	}
	if strings.Count(lua, "find_batch_entity(") < 2 {
		t.Fatalf("every target must resolve through the cached batch lookup:\n%s", lua)
	}
}

// transferMany must map the typed inventory and direction to the transfer
// semantics and keep a short move out of the error list.
func TestTransferManyDecodeAndLua(t *testing.T) {
	r, commands := serveLuaTwoCommands(t, `{"tick":91,"item":"iron-plate","quality":"normal","moved":12,"transferredStacks":2,"failed":1,"results":[{"index":1,"entity":"wooden-chest","unitNumber":50,"position":{"x":1.5,"y":1.5},"requested":8,"available":20,"capacity":16,"moved":8},{"index":2,"entity":"wooden-chest","unitNumber":51,"position":{"x":2.5,"y":1.5},"requested":8,"available":12,"capacity":4,"moved":4}],"errors":[{"index":3,"code":"VALIDATION","message":"entity does not have requested inventory","target":{"surface":"nauvis","entity":"stone-furnace","unitNumber":52,"position":{"x":5.5,"y":5.5}}}]}`)
	input := TransferManyInput{
		Actor: &ActorInput{Player: new("wricardo")}, Targets: batchRefs("50", "51", "52"),
		Inventory: InventoryKindChest, Direction: TransferDirectionToEntity, Item: "iron-plate", CountPerTarget: 8,
	}
	result, err := transferMany(r, input)
	result, err = setActorResult(result, input.Actor, err)
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 12 || result.TransferredStacks != 2 || result.Failed != 1 || len(result.Results) != 2 {
		t.Fatalf("result: %+v", result)
	}
	if result.Results[1].Moved != 4 || result.Results[1].Capacity != 4 {
		t.Fatalf("a short move must be reported without an error: %+v", result.Results[1])
	}
	if result.Inventory != InventoryKindChest || result.Direction != TransferDirectionToEntity {
		t.Fatalf("typed echo: %v %v", result.Inventory, result.Direction)
	}
	if len(result.Errors) != 1 || result.Errors[0].Code != BatchErrorCodeValidation {
		t.Fatalf("errors: %+v", result.Errors)
	}
	lua := <-commands
	for _, want := range []string{
		`game.get_player(` + luaString("wricardo") + `)`,
		`local inventory_key=` + luaString("chest") + `; local direction=` + luaString("to-entity") + `; local requested=8`,
		`local function inventories(entity)`,
		`destination.supports_bar()`,
		`transfer_stack`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("transferMany Lua missing %q\n%s", want, lua)
		}
	}

	toActor := input
	toActor.Direction = TransferDirectionToActor
	toActor.Inventory = InventoryKindFuel
	r2, commands2 := serveLuaTwoCommands(t, `{"tick":92,"item":"iron-plate","quality":"normal","moved":0,"transferredStacks":0,"failed":0,"results":{},"errors":{}}`)
	if _, err := transferMany(r2, toActor); err != nil {
		t.Fatal(err)
	}
	if lua := <-commands2; !strings.Contains(lua, `local inventory_key=`+luaString("fuel")+`; local direction=`+luaString("to-player")) {
		t.Fatalf("direction/inventory mapping:\n%s", lua)
	}
}

// Invalid batches must fail before any RCON dial, including duplicates that
// would apply an action twice to one entity.
func TestBatchActionValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	many := make([]*EntityRefInput, maxBatchTargets+1)
	for i := range many {
		many[i] = &EntityRefInput{UnitNumber: new("5")}
	}
	byPosition := func(x float64) *EntityRefInput {
		return &EntityRefInput{Surface: new("nauvis"), Entity: new("wooden-chest"), Position: &PositionInput{X: x, Y: 1}}
	}
	actor := &ActorInput{Character: new("1")}
	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"no targets", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: actor, Item: "coal", CountPerTarget: 1})
			return err
		}, "targets must contain between 1 and 1000"},
		{"too many targets", func() error {
			_, err := transferMany(r, TransferManyInput{Actor: actor, Targets: many, Inventory: InventoryKindChest, Direction: TransferDirectionToEntity, Item: "iron-plate", CountPerTarget: 1})
			return err
		}, "targets must contain between 1 and 1000"},
		{"duplicate unit", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: actor, Targets: batchRefs("5", "6", "5"), Item: "coal", CountPerTarget: 1})
			return err
		}, "target 3 duplicates target 1"},
		{"duplicate position", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: actor, Targets: []*EntityRefInput{byPosition(2), byPosition(2)}, Item: "coal", CountPerTarget: 1})
			return err
		}, "target 2 duplicates target 1"},
		{"unaddressed target", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: actor, Targets: []*EntityRefInput{{}}, Item: "coal", CountPerTarget: 1})
			return err
		}, "target 1: entity name or unitNumber is required"},
		{"nil target", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: actor, Targets: []*EntityRefInput{nil}, Item: "coal", CountPerTarget: 1})
			return err
		}, "target 1 is missing"},
		{"fuel empty item", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: actor, Targets: batchRefs("5"), CountPerTarget: 1})
			return err
		}, "item must not be empty"},
		{"fuel zero count", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: actor, Targets: batchRefs("5"), Item: "coal"})
			return err
		}, "countPerTarget must be at least 1"},
		{"fuel bad actor", func() error {
			_, err := fuelMany(r, FuelManyInput{Actor: &ActorInput{}, Targets: batchRefs("5"), Item: "coal", CountPerTarget: 1})
			return err
		}, "exactly one of player or character"},
		{"transfer fraction", func() error {
			_, err := transferMany(r, TransferManyInput{Actor: actor, Targets: batchRefs("5"), Inventory: InventoryKindChest, Direction: TransferDirectionToEntity, Item: "iron-plate", CountPerTarget: 1.5})
			return err
		}, "whole number"},
		{"transfer zero", func() error {
			_, err := transferMany(r, TransferManyInput{Actor: actor, Targets: batchRefs("5"), Inventory: InventoryKindChest, Direction: TransferDirectionToEntity, Item: "iron-plate"})
			return err
		}, "whole number"},
		{"transfer bad inventory", func() error {
			_, err := transferMany(r, TransferManyInput{Actor: actor, Targets: batchRefs("5"), Inventory: InventoryKind("TRUNK"), Direction: TransferDirectionToEntity, Item: "iron-plate", CountPerTarget: 1})
			return err
		}, "inventoryKind must be one of"},
		{"transfer bad direction", func() error {
			_, err := transferMany(r, TransferManyInput{Actor: actor, Targets: batchRefs("5"), Inventory: InventoryKindChest, Direction: TransferDirection("SIDEWAYS"), Item: "iron-plate", CountPerTarget: 1})
			return err
		}, "unsupported transfer direction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// Reach enforcement must reach every target of a batch.
func TestBatchActionReachPolicy(t *testing.T) {
	t.Setenv("FACTORIO_ENFORCE_REACH", "1")
	r, commands := serveLuaTwoCommands(t, `{"tick":1,"item":"coal","quality":"normal","fueled":0,"failed":0,"moved":0,"results":{},"errors":{}}`)
	if _, err := fuelMany(r, FuelManyInput{Actor: &ActorInput{Character: new("2")}, Targets: batchRefs("5"), Item: "coal", CountPerTarget: 1}); err != nil {
		t.Fatal(err)
	}
	if lua := <-commands; !strings.Contains(lua, "if not c.can_reach_entity(e) then fail_at(i,\"REACH\"") {
		t.Fatalf("reach check missing:\n%s", lua)
	}
}
