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
		`local require_resource,min_resource_tiles=nil,0`,
		`local build_check=defines.build_check_type.manual`,
		`if direction==4 or direction==12 then w,h=h,w end`,
		`table.sort(candidates`,
		`surface.can_place_entity{name=prototype,position=c,direction=direction,force=force,build_check_type=build_check}`,
		`supply_scan_margin=math.max(supply_scan_margin,supply_area_of(pole_proto))`,
		`local function supply_distance(pole)`,
		`return supply_area_of(pole.prototype)`,
		`area=box(c,supply_scan_margin)`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("nearestPlacement Lua missing %q\n%s", want, lua)
		}
	}
}

// A drill search that requires ore must count the resource inside the drill's
// mining area and report the covered tiles and amount on the winner, so siting a
// drill never needs a separate worldResources plus probe round trip.
func TestNearestPlacementRequiresResource(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"electric-mining-drill","direction":0,"found":true,"position":{"x":19.5,"y":39.5},"distance":0.707,"blockers":{},"terrain":false,"tile":"grass-1","expanded":1,"resourceTiles":7,"resourceAmount":140000}`)
	minTiles := 4
	result, err := nearestPlacementQuery(r, &NearestPlacementInput{
		Surface: "nauvis", Force: "player", Prototype: "electric-mining-drill",
		Origin: &PositionInput{X: 20, Y: 40}, MaxRadius: 8,
		RequireResource: new("iron-ore"), MinResourceTiles: &minTiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.ResourceTiles != 7 || result.ResourceAmount != 140000 {
		t.Fatalf("resource coverage: %+v", result)
	}
	lua := <-commands
	for _, want := range []string{
		`local require_resource,min_resource_tiles=` + luaString("iron-ore") + `,4`,
		`local mining_radius=nil`,
		`local ok_read,value=pcall(function()`,
		`local radius=proto.mining_drill_radius`,
		`return proto.get_mining_drill_radius("normal")`,
		`surface.find_entities_filtered{area=mining_box(c),type="resource",name=require_resource}`,
		`if covered<min_resource_tiles then return false end`,
		`resourceTiles=found_tiles,resourceAmount=found_amount`,
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
	negative, huge := -1.0, nearestPlacementMaxGap+1
	positive, negativeTiles, hugeTiles := 5, -1, nearestPlacementMaxResourceTiles+1
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
		{"zero radius", func(in *NearestPlacementInput) { in.MaxRadius = 0 }, "maxRadius must be greater than 0 and at most 512"},
		{"huge radius", func(in *NearestPlacementInput) { in.MaxRadius = nearestPlacementMaxRadius + 1 }, "maxRadius must be greater than 0 and at most 512"},
		{"negative gap", func(in *NearestPlacementInput) { in.MinDistanceFromEntities = &negative }, "minDistanceFromEntities must be between 0 and 64"},
		{"huge gap", func(in *NearestPlacementInput) { in.MinDistanceFromEntities = &huge }, "minDistanceFromEntities must be between 0 and 64"},
		{"min tiles without resource", func(in *NearestPlacementInput) { in.MinResourceTiles = &positive }, "minResourceTiles requires requireResource"},
		{"negative min tiles", func(in *NearestPlacementInput) {
			in.RequireResource = new("iron-ore")
			in.MinResourceTiles = &negativeTiles
		}, "minResourceTiles must be between 1 and 10000"},
		{"huge min tiles", func(in *NearestPlacementInput) {
			in.RequireResource = new("iron-ore")
			in.MinResourceTiles = &hugeTiles
		}, "minResourceTiles must be between 1 and 10000"},
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
