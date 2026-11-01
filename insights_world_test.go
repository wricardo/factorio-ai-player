package main

import (
	"strings"
	"testing"
)

// Kinds must narrow the entry list and total without hiding the unfiltered
// partition counts, and the Lua must carry the filter as a set.
func TestWorldResourcesKindsFilter(t *testing.T) {
	r, _ := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":32,"rightBottomY":32},"drillRadius":10,"pageInfo":{"total":1,"limit":10,"offset":0,"hasMore":false},"resourceTotal":4,"obstacleTotal":900,"waterTotal":120,"worldEntries":[{"kind":"water","name":"water","position":{"x":1.5,"y":2.5},"nearbyDrills":{}}],"patches":{}}`)
	result, err := worldResourcesQuery(r, &WorldResourcesInput{
		Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: 32, RightBottomY: 32},
		Kinds: []string{"water"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PageInfo == nil || result.PageInfo.Total != 1 || result.ResourceTotal != 4 || result.ObstacleTotal != 900 || result.WaterTotal != 120 {
		t.Fatalf("totals: %+v", result)
	}
	if len(result.WorldEntries) != 1 || result.WorldEntries[0].Kind != "water" {
		t.Fatalf("entries: %+v", result.WorldEntries)
	}
	if result.Patches == nil || len(result.Patches) != 0 {
		t.Fatalf("patches must be an empty list: %#v", result.Patches)
	}
}

// Patch grouping must decode bounds, centroid, total amount and per-patch drill
// linkage, so one call answers which ore patch is worth building around.
func TestWorldResourcesPatches(t *testing.T) {
	r, _ := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":64,"rightBottomY":64},"drillRadius":10,"pageInfo":{"total":2,"limit":10,"offset":0,"hasMore":false},"resourceTotal":2,"obstacleTotal":0,"waterTotal":0,"worldEntries":{},"patches":[{"name":"iron-ore","tiles":2,"totalAmount":24100,"bounds":{"leftTopX":10.5,"leftTopY":20.5,"rightBottomX":11.5,"rightBottomY":20.5},"centroid":{"x":11,"y":20.5},"nearbyDrills":[{"kind":"drill","name":"electric-mining-drill","position":{"x":10.5,"y":20.5},"unitNumber":42}]},{"name":"copper-ore","tiles":1,"totalAmount":9000,"bounds":{"leftTopX":30.5,"leftTopY":40.5,"rightBottomX":30.5,"rightBottomY":40.5},"centroid":{"x":30.5,"y":40.5},"nearbyDrills":{}}]}`)
	result, err := worldResourcesQuery(r, &WorldResourcesInput{
		Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: 64, RightBottomY: 64},
		ResourceName: new("iron-ore"), GroupPatches: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 2 {
		t.Fatalf("patches: %+v", result.Patches)
	}
	patch := result.Patches[0]
	if patch.Name != "iron-ore" || patch.Tiles != 2 || patch.TotalAmount != 24100 {
		t.Fatalf("patch facts: %+v", patch)
	}
	if patch.Bounds == nil || patch.Bounds.LeftTopX != 10.5 || patch.Bounds.RightBottomY != 20.5 {
		t.Fatalf("patch bounds: %+v", patch.Bounds)
	}
	if patch.Centroid == nil || patch.Centroid.X != 11 || patch.Centroid.Y != 20.5 {
		t.Fatalf("patch centroid: %+v", patch.Centroid)
	}
	if len(patch.NearbyDrills) != 1 || patch.NearbyDrills[0].UnitNumber == nil || *patch.NearbyDrills[0].UnitNumber != "42" {
		t.Fatalf("patch drills: %+v", patch.NearbyDrills)
	}
	if result.Patches[1].NearbyDrills == nil || len(result.Patches[1].NearbyDrills) != 0 {
		t.Fatalf("empty drills must decode as an empty list: %#v", result.Patches[1].NearbyDrills)
	}
}

// An unsupported kind must fail before any RCON dial.
func TestWorldResourcesKindValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	_, err := worldResourcesQuery(r, &WorldResourcesInput{
		Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8},
		Kinds: []string{"boulder"}, Limit: 10,
	})
	if err == nil || !strings.Contains(err.Error(), `unsupported resource kind "boulder"`) {
		t.Fatalf("expected unsupported kind error, got %v", err)
	}
}

// A refused single-position check must explain itself: blockers from the
// prototype collision mask, the ground tile, and the terrain flag decode
// without a follow-up probe.
func TestWorldPlacementBlockers(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"small-lamp","position":{"x":4.5,"y":5.5},"direction":0,"canPlace":false,"blockers":["small-lamp"],"terrain":false,"tile":"grass-1"}`)
	result, err := worldPlacementQuery(r, &WorldPlacementInput{
		Surface: "nauvis", Force: "player", Prototype: "small-lamp",
		Position: &PositionInput{X: 4.5, Y: 5.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CanPlace || result.Tile != "grass-1" || result.Terrain {
		t.Fatalf("placement result: %+v", result)
	}
	if len(result.Blockers) != 1 || result.Blockers[0] != "small-lamp" {
		t.Fatalf("blockers: %v", result.Blockers)
	}
	lua := <-commands
	for _, want := range []string{
		"collision_mask=proto.collision_mask.layers",
		"table.sort(blockers)",
		"tile.prototype.collision_mask",
		"canPlace=can_place",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("worldPlacement Lua missing %q\n%s", want, lua)
		}
	}
}

// An allowed position must decode empty blocker and terrain fields.
func TestWorldPlacementClear(t *testing.T) {
	r, _ := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"small-lamp","position":{"x":0.5,"y":0.5},"direction":0,"canPlace":true,"blockers":{},"terrain":false,"tile":"grass-1"}`)
	result, err := worldPlacementQuery(r, &WorldPlacementInput{
		Surface: "nauvis", Force: "player", Prototype: "small-lamp",
		Position: &PositionInput{X: 0.5, Y: 0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.CanPlace || result.Blockers == nil || len(result.Blockers) != 0 {
		t.Fatalf("clear placement: %+v", result)
	}
}
