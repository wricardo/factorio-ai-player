package main

import (
	"net"
	"strings"
	"testing"
)

// serveLuaTwoCommands answers the mutation exchange and the follow-up
// notification exchange, so a mutation test does not wait on a dead listener.
func serveLuaTwoCommands(t *testing.T, response string) (*Resolver, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	commands := make(chan string, 1)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if _, _, _, err = readPacket(conn); err != nil {
				conn.Close()
				continue
			}
			_ = writePacket(conn, 1, 2, "")
			if _, _, body, err := readPacket(conn); err == nil {
				select {
				case commands <- body:
				default:
				}
			}
			_ = writePacket(conn, 2, 0, response+"\n")
			_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
			conn.Close()
		}
	}()
	return &Resolver{addr: listener.Addr().String(), password: "password"}, commands
}

// The prototype query must read item and entity data in one RCON call and
// normalize engine snake_case keys into the schema shape.
func TestPrototypeQueryDecodeAndLua(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"name":"stone-furnace","item":{"name":"stone-furnace","stackSize":50,"stackable":true,"placeResult":"stone-furnace"},"entity":{"name":"stone-furnace","type":"furnace","tileWidth":2,"tileHeight":2,"collision_box":{"left_top":{"x":-1,"y":-1},"right_bottom":{"x":1,"y":1}},"maxHealth":200,"crafting_categories":{},"fluidBoxCount":0,"mineable":true}}`)
	result, err := prototypeQuery(r, "stone-furnace")
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "stone-furnace" || result.Item == nil || result.Entity == nil {
		t.Fatalf("prototype: %+v", result)
	}
	if result.Item.StackSize != 50 || !result.Item.Stackable || result.Item.PlaceResult == nil || *result.Item.PlaceResult != "stone-furnace" {
		t.Fatalf("item prototype: %+v", result.Item)
	}
	entity := result.Entity
	if entity.TileWidth != 2 || entity.TileHeight != 2 || !entity.Mineable || entity.Type != "furnace" {
		t.Fatalf("entity prototype: %+v", entity)
	}
	if entity.CollisionBox == nil || entity.CollisionBox.LeftTop == nil || entity.CollisionBox.LeftTop.X != -1 || entity.CollisionBox.RightBottom.Y != 1 {
		t.Fatalf("collision box: %+v", entity.CollisionBox)
	}
	if entity.MaxHealth == nil || *entity.MaxHealth != 200 {
		t.Fatalf("max health: %+v", entity.MaxHealth)
	}
	if entity.CraftingCategories == nil || len(entity.CraftingCategories) != 0 {
		t.Fatalf("crafting categories must decode as an empty list: %#v", entity.CraftingCategories)
	}
	lua := <-commands
	for _, want := range []string{
		`local name=` + luaString("stone-furnace"),
		`prototypes.item[name]`,
		`prototypes.entity[name]`,
		`get(item,"place_result")`,
		`get(entity,"crafting_categories")`,
		`get(entity,"fluidbox_prototypes")`,
		`call(entity,"get_max_wire_distance")`,
		`call(entity,"get_supply_area_distance")`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("prototype Lua missing %q\n%s", want, lua)
		}
	}
}

// An empty prototype name must fail before any RCON dial.
func TestPrototypeQueryValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	if _, err := prototypeQuery(r, ""); err == nil || !strings.Contains(err.Error(), "name must not be empty") {
		t.Fatalf("expected empty name error, got %v", err)
	}
}

// placeNextTo must resolve both bounding boxes in Lua, keep only candidates
// that preserve the gap, consume one item, and refund it when creation fails.
func TestPlaceNextToDecodeAndLua(t *testing.T) {
	r, commands := serveLuaTwoCommands(t, `{"tick":20,"surface":"nauvis","force":"player","item":"wooden-chest","name":"wooden-chest","unitNumber":77,"position":{"x":2.5,"y":1},"relativeTo":42,"side":"EAST","gap":1}`)
	input := PlaceNextToInput{
		Actor:   &ActorInput{Character: new("55")},
		Surface: "nauvis",
		Item:    "wooden-chest",
		RelativeTo: &EntityRefInput{
			Surface: new("nauvis"), Entity: new("stone-furnace"), UnitNumber: new("42"),
		},
		Side: RelativeSideEast, Gap: 1,
	}
	result, err := placeNextTo(r, input)
	result, err = setActorResult(result, input.Actor, err)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "wooden-chest" || result.Position == nil || result.Position.X != 2.5 || result.Position.Y != 1 {
		t.Fatalf("placement: %+v", result)
	}
	if result.UnitNumber == nil || *result.UnitNumber != "77" {
		t.Fatalf("unit number: %+v", result.UnitNumber)
	}
	if result.RelativeTo == nil || *result.RelativeTo != "42" {
		t.Fatalf("relativeTo must decode a numeric unit number as a string: %+v", result.RelativeTo)
	}
	if result.Actor == nil || result.Actor.Character == nil || *result.Actor.Character != "55" {
		t.Fatalf("actor identity: %+v", result.Actor)
	}
	if result.Side != RelativeSideEast || result.Gap != 1 {
		t.Fatalf("side/gap: %+v", result)
	}
	lua := <-commands
	for _, want := range []string{
		`local unit=55`,
		`local side=` + luaString("EAST"),
		`local gap=1`,
		`local direction=0`,
		`local quality=` + luaString("normal"),
		`local box=target.bounding_box`,
		`new_proto.tile_width`,
		`if direction==4 or direction==12 then w,h=h,w end`,
		`keeps_gap`,
		`surface.can_place_entity{name=new_proto.name,position=position,direction=direction,force=c.force}`,
		`inv.remove{name=item,quality=quality,count=1}`,
		`inv.insert{name=item,quality=quality,count=1}`,
		`if false then fail("out of build reach")`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("placeNextTo Lua missing %q\n%s", want, lua)
		}
	}
}

// Reach enforcement must inject the distance check.
func TestPlaceNextToReachPolicy(t *testing.T) {
	t.Setenv("FACTORIO_ENFORCE_REACH", "1")
	r, commands := serveLuaTwoCommands(t, `{"tick":1,"surface":"nauvis","force":"player","item":"wooden-chest","name":"wooden-chest","position":{"x":1.5,"y":0.5},"side":"EAST","gap":0}`)
	_, err := placeNextTo(r, PlaceNextToInput{
		Actor:   &ActorInput{Character: new("1")},
		Surface: "nauvis", Item: "wooden-chest",
		RelativeTo: &EntityRefInput{Entity: new("stone-furnace"), UnitNumber: new("9")},
		Side:       RelativeSideEast,
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, "dx*dx+dy*dy>(c.build_distance)*(c.build_distance)") {
		t.Fatalf("reach check missing:\n%s", lua)
	}
}

// Invalid geometry, addresses and item selectors must fail before any RCON
// dial.
func TestPlaceNextToValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	actor := &ActorInput{Character: new("1")}
	ref := &EntityRefInput{Surface: new("nauvis"), Entity: new("stone-furnace"), UnitNumber: new("2")}
	valid := func() PlaceNextToInput {
		return PlaceNextToInput{Actor: actor, Surface: "nauvis", Item: "wooden-chest", RelativeTo: ref, Side: RelativeSideEast}
	}
	cases := []struct {
		name   string
		mutate func(*PlaceNextToInput)
		want   string
	}{
		{"missing actor", func(in *PlaceNextToInput) { in.Actor = nil }, "exactly one of player or character"},
		{"empty surface", func(in *PlaceNextToInput) { in.Surface = "" }, "surface must not be empty"},
		{"empty item", func(in *PlaceNextToInput) { in.Item = "" }, "item must not be empty"},
		{"diagonal direction", func(in *PlaceNextToInput) { in.Direction = 2 }, "cardinal engine direction"},
		{"negative gap", func(in *PlaceNextToInput) { in.Gap = -1 }, "gap must be between"},
		{"large gap", func(in *PlaceNextToInput) { in.Gap = 17 }, "gap must be between"},
		{"missing target", func(in *PlaceNextToInput) { in.RelativeTo = nil }, "relativeTo is required"},
		{"unaddressed target", func(in *PlaceNextToInput) { in.RelativeTo = &EntityRefInput{} }, "entity name or unitNumber is required"},
		{"foreign surface", func(in *PlaceNextToInput) {
			in.RelativeTo = &EntityRefInput{Surface: new("vulcanus"), UnitNumber: new("2")}
		}, "must match surface"},
		{"bad quality", func(in *PlaceNextToInput) { empty := ""; in.Quality = &empty }, "quality must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid()
			tc.mutate(&input)
			_, err := placeNextTo(r, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
