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
	r, _ := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":64,"rightBottomY":64},"drillRadius":10,"pageInfo":{"total":2,"limit":10,"offset":0,"hasMore":false},"resourceTotal":2,"obstacleTotal":0,"waterTotal":0,"worldEntries":{},"patches":[{"name":"iron-ore","tiles":2,"totalAmount":24100,"infinite":false,"yieldPercent":0,"bounds":{"leftTopX":10.5,"leftTopY":20.5,"rightBottomX":11.5,"rightBottomY":20.5},"centroid":{"x":11,"y":20.5},"nearbyDrills":[{"kind":"drill","name":"electric-mining-drill","position":{"x":10.5,"y":20.5},"unitNumber":42,"status":"working","coveredTiles":2,"coveredAmount":24100}],"coveredTiles":2,"coveredAmount":24100,"clippedByArea":false},{"name":"crude-oil","tiles":1,"totalAmount":0,"infinite":true,"yieldPercent":250,"bounds":{"leftTopX":30.5,"leftTopY":40.5,"rightBottomX":30.5,"rightBottomY":40.5},"centroid":{"x":30.5,"y":40.5},"nearbyDrills":{},"coveredTiles":0,"coveredAmount":0,"clippedByArea":true}]}`)
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
	if patch.Infinite || patch.ClippedByArea || patch.CoveredTiles != 2 || patch.CoveredAmount != 24100 {
		t.Fatalf("finite coverage: %+v", patch)
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
	drill := patch.NearbyDrills[0]
	if drill.Status == nil || *drill.Status != "working" || drill.CoveredTiles != 2 || drill.CoveredAmount != 24100 {
		t.Fatalf("drill status and coverage: %+v", drill)
	}
	oil := result.Patches[1]
	if !oil.Infinite || oil.YieldPercent != 250 || oil.TotalAmount != 0 || !oil.ClippedByArea {
		t.Fatalf("infinite patch must report yield separately and the clip flag: %+v", oil)
	}
	if oil.NearbyDrills == nil || len(oil.NearbyDrills) != 0 {
		t.Fatalf("empty drills must decode as an empty list: %#v", oil.NearbyDrills)
	}
}

// Patch drills must be matched by mining area and patches must report clipping,
// infinite yield and drill status, so a patch entry never implies a drill that
// cannot mine it.
func TestWorldResourcesPatchCoverageLua(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":32,"rightBottomY":32},"drillRadius":10,"pageInfo":{"total":0,"limit":10,"offset":0,"hasMore":false},"resourceTotal":0,"obstacleTotal":0,"waterTotal":0,"worldEntries":{},"patches":{}}`)
	if _, err := worldResourcesQuery(r, &WorldResourcesInput{
		Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: 32, RightBottomY: 32},
		GroupPatches: true, Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		`infinite=props.infinite==true`,
		`local max_mining_radius=radius`,
		`local value=drill_proto.mining_drill_radius`,
		`local function drill_radius(d)`,
		`local mine_area={{d.position.x-r,d.position.y-r},{d.position.x+r,d.position.y+r}}`,
		`surface.find_entities_filtered{area=mine_area,type="resource",name=group.name}`,
		`if drill_tiles>0 then`,
		`status=status_names[d.status],coveredTiles=drill_tiles,coveredAmount=drill_amount`,
		`local clipped=(group.minx<=area[1][1]+0.5`,
		`infinite=group.infinite,yieldPercent=group.yieldPercent`,
		`coveredTiles=covered_tiles,coveredAmount=covered_amount,clippedByArea=clipped`,
		`group.infinite=true`,
		`group.sortAmount=(group.totalAmount>0) and group.totalAmount or group.yieldPercent`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("worldResources patch Lua missing %q\n%s", want, lua)
		}
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
		"local layers=proto.collision_mask.layers",
		"local build_check=defines.build_check_type.manual",
		"surface.can_place_entity{name=prototype,position=position,direction=direction,force=force,build_check_type=build_check}",
		`surface.find_entities_filtered{area={{x-w/2,y-h/2},{x+w/2,y+h/2}},collision_mask=layers}`,
		"local x0,x1=math.floor(x-w/2+0.001),math.ceil(x+w/2-0.001)-1",
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
