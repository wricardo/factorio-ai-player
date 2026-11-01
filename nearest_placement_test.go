package main

import (
	"strings"
	"testing"
)

// The query must send every constraint to one Lua call and decode a found
// candidate with its distance, so one round trip replaces per-tile probes.
func TestNearestPlacementFound(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"stone-furnace","direction":0,"found":true,"position":{"x":4,"y":-2},"distance":2.5,"blockers":{},"terrain":false,"tile":"grass-1","expanded":7}`)
	gap := 2.0
	result, err := nearestPlacementQuery(r, &NearestPlacementInput{
		Surface: "nauvis", Force: "player", Prototype: "stone-furnace", Direction: 4,
		Origin: &PositionInput{X: 3.25, Y: -4.5}, MaxRadius: 12,
		AvoidResources: true, AvoidWater: true, MinDistanceFromEntities: &gap, RequirePowerCoverage: true, RequireWaterAdjacency: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Position == nil || result.Position.X != 4 || result.Position.Y != -2 || result.Distance == nil || *result.Distance != 2.5 || result.Expanded != 7 {
		t.Fatalf("candidate: %+v", result)
	}
	if result.Blockers == nil || len(result.Blockers) != 0 {
		t.Fatalf("an accepted origin must decode empty blockers: %#v", result.Blockers)
	}
	lua := <-commands
	for _, want := range []string{
		`local prototype=` + luaString("stone-furnace") + `; local direction=4`,
		`local ox,oy,radius=3.25,-4.5,12`,
		`local avoid_resources,avoid_water,min_gap,need_power,need_water_adjacent=true,true,2,true,true`,
		`if direction==4 or direction==12 then w,h=h,w end`,
		`table.sort(candidates`,
		`surface.can_place_entity{name=prototype,position=c,direction=direction,force=force}`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("nearestPlacement Lua missing %q\n%s", want, lua)
		}
	}
}

// A search with no match must report null position and distance, plus why the
// snapped origin was refused.
func TestNearestPlacementNotFound(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"stone-furnace","direction":0,"found":false,"blockers":["wooden-chest"],"terrain":false,"tile":"grass-1","expanded":41}`)
	result, err := nearestPlacementQuery(r, &NearestPlacementInput{
		Surface: "nauvis", Force: "player", Prototype: "stone-furnace", Origin: &PositionInput{X: 0, Y: 0}, MaxRadius: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Found || result.Position != nil || result.Distance != nil || result.Expanded != 41 {
		t.Fatalf("not found: %+v", result)
	}
	if len(result.Blockers) != 1 || result.Blockers[0] != "wooden-chest" || result.Tile != "grass-1" {
		t.Fatalf("origin diagnosis: %+v", result)
	}
	lua := <-commands
	if !strings.Contains(lua, `min_gap,need_power,need_water_adjacent=false,false,-1,false,false`) {
		t.Fatalf("default constraints missing:\n%s", lua)
	}
}

// Invalid searches must fail before any RCON dial, so a bad radius cannot make
// the server enumerate an unbounded candidate set.
func TestNearestPlacementValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	valid := func() *NearestPlacementInput {
		return &NearestPlacementInput{Surface: "nauvis", Force: "player", Prototype: "stone-furnace", Origin: &PositionInput{X: 0, Y: 0}, MaxRadius: 8}
	}
	negative, huge := -1.0, 17.0
	cases := []struct {
		name   string
		mutate func(*NearestPlacementInput)
		want   string
	}{
		{"nil", nil, "surface, force and prototype are required"},
		{"empty surface", func(in *NearestPlacementInput) { in.Surface = "" }, "surface, force and prototype are required"},
		{"empty prototype", func(in *NearestPlacementInput) { in.Prototype = "" }, "surface, force and prototype are required"},
		{"missing origin", func(in *NearestPlacementInput) { in.Origin = nil }, "origin is required"},
		{"direction", func(in *NearestPlacementInput) { in.Direction = 16 }, "direction must be between 0 and 15"},
		{"zero radius", func(in *NearestPlacementInput) { in.MaxRadius = 0 }, "maxRadius must be greater than 0 and at most 64"},
		{"huge radius", func(in *NearestPlacementInput) { in.MaxRadius = 65 }, "maxRadius must be greater than 0 and at most 64"},
		{"negative gap", func(in *NearestPlacementInput) { in.MinDistanceFromEntities = &negative }, "minDistanceFromEntities must be between 0 and 16"},
		{"huge gap", func(in *NearestPlacementInput) { in.MinDistanceFromEntities = &huge }, "minDistanceFromEntities must be between 0 and 16"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var input *NearestPlacementInput
			if tc.mutate != nil {
				input = valid()
				tc.mutate(input)
			}
			_, err := nearestPlacementQuery(r, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
