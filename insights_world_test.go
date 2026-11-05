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
		`if group.tileKeys[key] then`,
		`group.tileKeys[math.floor(item.position.x)..":"..math.floor(item.position.y)]=true`,
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

// beltTiles must page one belt-family census with the true total, sort within
// the Lua, default to every belt type, and mark lane contamination in Go.
func TestBeltTilesPageAndMixed(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","area":{"leftTopX":-2,"leftTopY":-1,"rightBottomX":4,"rightBottomY":3},"pageInfo":{"total":3,"limit":2,"offset":0,"hasMore":true},"total":3,"tiles":[{"name":"transport-belt","type":"transport-belt","position":{"x":0.5,"y":0.5},"direction":4,"unitNumber":11,"lanes":[{"index":1,"lineName":"left_line","side":"left","items":[{"name":"iron-plate","count":2,"quality":"normal"},{"name":"copper-plate","count":1,"quality":"normal"}]},{"index":2,"lineName":"right_line","side":"right","items":[{"name":"iron-plate","count":3,"quality":"normal"}]}]},{"name":"underground-belt","type":"underground-belt","position":{"x":1.5,"y":0.5},"direction":4,"unitNumber":12,"lanes":{}}]}`)
	result, err := beltTilesQuery(r, &BeltTilesInput{
		Surface: "nauvis",
		Area:    &FactoryAreaInput{LeftTopX: -2, LeftTopY: -1, RightBottomX: 4, RightBottomY: 3},
		Limit:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.PageInfo == nil || result.PageInfo.Total != 3 || !result.PageInfo.HasMore {
		t.Fatalf("totals: %+v", result)
	}
	if len(result.Tiles) != 2 {
		t.Fatalf("tiles: %+v", result.Tiles)
	}
	belt := result.Tiles[0]
	if belt.UnitNumber == nil || *belt.UnitNumber != "11" || belt.Direction != 4 {
		t.Fatalf("belt identity: %+v", belt)
	}
	if len(belt.Lanes) != 2 || !belt.Lanes[0].Mixed || belt.Lanes[1].Mixed {
		t.Fatalf("lane mixed flags: %+v", belt.Lanes)
	}
	if result.Tiles[1].Lanes == nil || len(result.Tiles[1].Lanes) != 0 {
		t.Fatalf("empty lanes must decode as an empty list: %#v", result.Tiles[1].Lanes)
	}
	lua := <-commands
	wantTypes := "local type_list={" + strings.Join([]string{
		luaString("transport-belt"), luaString("underground-belt"), luaString("splitter"),
		luaString("loader"), luaString("loader-1x1"), luaString("linked-belt"),
	}, ",") + "}"
	for _, want := range []string{
		wantTypes,
		"local limit,offset=2,0",
		"if a.position.y~=b.position.y then return a.position.y<b.position.y end",
		"if a.position.x~=b.position.x then return a.position.x<b.position.x end",
		"return (a.unitNumber or 0)<(b.unitNumber or 0)",
		"transport_line_name(j)",
		"total=#matches,tiles=result",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("beltTiles Lua missing %q\n%s", want, lua)
		}
	}
}

// A requested type list must be passed as the engine type filter and validated
// in Go, so an unknown belt type never reaches RCON.
func TestBeltTilesTypeFilterAndValidation(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":4,"rightBottomY":4},"pageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"total":0,"tiles":{}}`)
	result, err := beltTilesQuery(r, &BeltTilesInput{
		Surface: "nauvis",
		Area:    &FactoryAreaInput{RightBottomX: 4, RightBottomY: 4},
		Types:   []string{"splitter"},
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tiles == nil || len(result.Tiles) != 0 {
		t.Fatalf("empty tiles must decode as an empty list: %#v", result.Tiles)
	}
	lua := <-commands
	if !strings.Contains(lua, "local type_list={"+luaString("splitter")+"}") {
		t.Fatalf("requested types must replace the default list:\n%s", lua)
	}
	if !strings.Contains(lua, "surface.find_entities_filtered{area=area,type=type_list}") {
		t.Fatalf("the requested types must be the engine filter:\n%s", lua)
	}

	static := &Resolver{addr: "127.0.0.1:1", password: "password"}
	base := BeltTilesInput{Surface: "nauvis", Area: &FactoryAreaInput{RightBottomX: 4, RightBottomY: 4}, Limit: 1}
	cases := []struct {
		name string
		edit func(*BeltTilesInput)
		want string
	}{
		{"unknown type", func(in *BeltTilesInput) { in.Types = []string{"inserter"} }, `unsupported belt type "inserter"`},
		{"zero limit", func(in *BeltTilesInput) { in.Limit = 0 }, "limit must be between 1 and 10000"},
		{"large limit", func(in *BeltTilesInput) { in.Limit = 10001 }, "limit must be between 1 and 10000"},
		{"negative offset", func(in *BeltTilesInput) { in.Offset = -1 }, "limit must be between 1 and 10000"},
		{"oversized area", func(in *BeltTilesInput) { in.Area = &FactoryAreaInput{RightBottomX: 2000, RightBottomY: 4} }, "no greater than 1024"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.edit(&in)
			if _, err := beltTilesQuery(static, &in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

// Underground halves must report their ground type and paired unit number on
// worldLogistics, and every other type must leave both null.
func TestWorldLogisticsUndergroundFields(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","area":{"leftTopX":-26,"leftTopY":79,"rightBottomX":-25,"rightBottomY":80},"pageInfo":{"total":2,"limit":10,"offset":0,"hasMore":false},"logisticsEntities":[{"name":"underground-belt","type":"underground-belt","position":{"x":-25.5,"y":79.5},"direction":4,"unitNumber":700,"lanes":[{"index":1,"lineName":"left_line","side":"left","items":[{"name":"iron-plate","count":1,"quality":"normal"}]}],"undergroundType":"INPUT","pairedUnitNumber":"701","fluids":{},"worldInventories":{}},{"name":"transport-belt","type":"transport-belt","position":{"x":-25.5,"y":78.5},"direction":4,"unitNumber":702,"lanes":{},"fluids":{},"worldInventories":{}}]}`)
	result, err := worldLogisticsQuery(r, &WorldLogisticsInput{
		Surface: "nauvis", Force: "player",
		Area:  &FactoryAreaInput{LeftTopX: -26, LeftTopY: 79, RightBottomX: -25, RightBottomY: 80},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.LogisticsEntities) != 2 {
		t.Fatalf("entities: %+v", result.LogisticsEntities)
	}
	half := result.LogisticsEntities[0]
	if half.UndergroundType == nil || *half.UndergroundType != UndergroundBeltTypeInput {
		t.Fatalf("undergroundType: %+v", half.UndergroundType)
	}
	if half.PairedUnitNumber == nil || *half.PairedUnitNumber != "701" {
		t.Fatalf("pairedUnitNumber: %+v", half.PairedUnitNumber)
	}
	plain := result.LogisticsEntities[1]
	if plain.UndergroundType != nil || plain.PairedUnitNumber != nil {
		t.Fatalf("non-underground entity must report null: %+v", plain)
	}
	lua := <-commands
	for _, want := range []string{
		"local underground_type,paired=underground_info(e)",
		"e.belt_to_ground_type",
		"e.neighbours",
		"obj.undergroundType=underground_type",
		"obj.pairedUnitNumber=(paired~=nil) and tostring(paired) or nil",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("worldLogistics underground Lua missing %q\n%s", want, lua)
		}
	}
}

// markWorldLaneMixed must flag lane contamination by distinct name|quality
// combinations, including the same item in two qualities.
func TestMarkWorldLaneMixed(t *testing.T) {
	cases := []struct {
		name  string
		items []*InventoryStack
		want  bool
	}{
		{"empty", nil, false},
		{"one stack", []*InventoryStack{{Name: "iron-plate", Count: 4, Quality: "normal"}}, false},
		{"duplicate stack", []*InventoryStack{{Name: "iron-plate", Count: 4, Quality: "normal"}, {Name: "iron-plate", Count: 2, Quality: "normal"}}, false},
		{"two items", []*InventoryStack{{Name: "iron-plate", Count: 4, Quality: "normal"}, {Name: "copper-plate", Count: 2, Quality: "normal"}}, true},
		{"two qualities", []*InventoryStack{{Name: "iron-plate", Count: 4, Quality: "normal"}, {Name: "iron-plate", Count: 2, Quality: "uncommon"}}, true},
		{"nil stack ignored", []*InventoryStack{nil, {Name: "iron-plate", Count: 4, Quality: "normal"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lanes := []*WorldLane{nil, {Index: 1, Items: tc.items}}
			markWorldLaneMixed(lanes)
			if lanes[1].Mixed != tc.want {
				t.Fatalf("mixed = %v, want %v", lanes[1].Mixed, tc.want)
			}
		})
	}
}
