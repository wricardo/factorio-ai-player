package main

import (
	"strings"
	"testing"
)

// The safety scan must let the engine do the force filtering and rank threats by
// distance to the base, so distant nests never outrank a nearby biter and no
// tree or resource tile is visited.
func TestSafetyForceFilteredAndDistanceRanked(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":55,"surface":"nauvis","force":"player","basePosition":{"x":0,"y":0},"enemies":{"pageInfo":{"total":2,"limit":10,"offset":0,"hasMore":false},"entities":[{"name":"small-biter","type":"unit","force":"enemy","position":{"x":-40.5,"y":3.5},"unitNumber":7,"health":15,"maxHealth":15,"status":"working","statusCode":1,"pollution":{"position":{"x":-40.5,"y":3.5},"amount":0},"distance":40.65},{"name":"small-worm-turret","type":"turret","force":"enemy","position":{"x":120.5,"y":10.5},"distance":120.9}]},"damaged":{"pageInfo":{"total":1,"limit":10,"offset":0,"hasMore":false},"entities":[{"name":"stone-wall","type":"wall","force":"player","position":{"x":-4.5,"y":0.5},"health":50,"maxHealth":350,"distance":4.53}]},"playerAlerts":{"available":false,"pageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"alerts":{}},"evolutionFactor":0,"evolutionByTime":0,"evolutionByPollution":0,"evolutionByKills":0,"totalPollution":0,"killCounts":{}}`)
	area := &FactoryAreaInput{LeftTopX: -8, LeftTopY: -8, RightBottomX: 8, RightBottomY: 8}
	result, err := safetyQuery(r, &SafetyInput{
		Surface: "nauvis", Force: "player", Area: area,
		EnemyLimit: 10, DamagedLimit: 10, AlertLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		`local enemy_forces,friend_forces={},{}`,
		`if force.is_enemy(candidate) then enemy_forces[#enemy_forces+1]=candidate.name end`,
		`if candidate==force or force.is_friend(candidate) then friend_forces[#friend_forces+1]=candidate.name end`,
		`surface.find_entities_filtered{area=area,force=enemy_forces}`,
		`surface.find_entities_filtered{area=area,force=friend_forces}`,
		`base_position={x=(area[1][1]+area[2][1])/2,y=(area[1][2]+area[2][2])/2}`,
		`force.get_spawn_position(surface)`,
		`distance=distance_to_base(e.position)`,
		`local function enemy_less(a,b)`,
		`entity_page(enemies,enemy_limit,enemy_offset,enemy_less)`,
		`basePosition=base_position`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("safety Lua missing %q\n%s", want, lua)
		}
	}
	if result.BasePosition == nil || result.BasePosition.X != 0 || result.BasePosition.Y != 0 {
		t.Fatalf("base position: %+v", result.BasePosition)
	}
	if len(result.Enemies.Entities) != 2 {
		t.Fatalf("enemies: %+v", result.Enemies.Entities)
	}
	first := result.Enemies.Entities[0]
	if first.Name != "small-biter" || first.Distance != 40.65 {
		t.Fatalf("nearest enemy first: %+v", first)
	}
	if second := result.Enemies.Entities[1]; second.Distance != 120.9 {
		t.Fatalf("second enemy distance: %+v", second)
	}
	if len(result.Damaged.Entities) != 1 || result.Damaged.Entities[0].Distance != 4.53 {
		t.Fatalf("damaged entities carry distance too: %+v", result.Damaged.Entities)
	}
}

// Without an area the base reference must be the force spawn, not a hard-coded
// origin, and the surfaces must still be scanned with a force filter.
func TestSafetyWithoutAreaUsesSpawnBase(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","basePosition":{"x":10,"y":-4},"enemies":{"pageInfo":{"total":0,"limit":5,"offset":0,"hasMore":false},"entities":{}},"damaged":{"pageInfo":{"total":0,"limit":5,"offset":0,"hasMore":false},"entities":{}},"playerAlerts":{"available":false,"pageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"alerts":{}},"evolutionFactor":0,"evolutionByTime":0,"evolutionByPollution":0,"evolutionByKills":0,"totalPollution":0,"killCounts":{}}`)
	result, err := safetyQuery(r, &SafetyInput{Surface: "nauvis", Force: "player", EnemyLimit: 5, DamagedLimit: 5, AlertLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, "local area, pollution_position, player_name=nil,nil,nil") {
		t.Fatalf("an omitted area must stay nil:\n%s", lua)
	}
	if !strings.Contains(lua, `local ok,spawn=pcall(function() return force.get_spawn_position(surface) end)`) {
		t.Fatalf("spawn fallback missing:\n%s", lua)
	}
	if result.BasePosition == nil || result.BasePosition.X != 10 || result.BasePosition.Y != -4 {
		t.Fatalf("base position: %+v", result.BasePosition)
	}
	if result.Enemies.Entities == nil || len(result.Enemies.Entities) != 0 {
		t.Fatalf("empty enemies must decode as an empty list: %#v", result.Enemies.Entities)
	}
}
