package main

import (
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
)

// serveLuaOnce answers one executeLua exchange and returns the resolver plus the
// captured Lua payload.
func serveLuaOnce(t *testing.T, response string) (*Resolver, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	commands := make(chan string, 1)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		if _, _, _, err = readPacket(conn); err != nil {
			serverErr <- err
			return
		}
		if err = writePacket(conn, 1, 2, ""); err != nil {
			serverErr <- err
			return
		}
		_, _, body, err := readPacket(conn)
		if err != nil {
			serverErr <- err
			return
		}
		commands <- body
		if err = writePacket(conn, 2, 0, response+"\n"); err != nil {
			serverErr <- err
			return
		}
		if err = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n"); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()
	t.Cleanup(func() {
		if err := <-serverErr; err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Errorf("fake server: %v", err)
		}
	})
	return &Resolver{addr: listener.Addr().String(), password: "password"}, commands
}

// The probe must sample tile centers once per tile in one RCON call, so the Lua
// carries the area, prototype, direction, and the prototype's own collision
// layers rather than a hardcoded mask.
func TestPlacementProbeLuaAndDecode(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"transport-belt","direction":4,"area":{"leftTopX":-3,"leftTopY":-4,"rightBottomX":7,"rightBottomY":6},"sampled":4,"placeable":3,"blocked":1,"blockedTiles":[{"position":{"x":0.5,"y":0.5},"blockers":["stone-furnace","long-handed-inserter"],"terrain":false,"tile":"grass-1"}]}`)
	result, err := placementProbeQuery(r, &PlacementProbeInput{
		Surface:   "nauvis",
		Force:     "player",
		Prototype: "transport-belt",
		Direction: 4,
		Area:      &FactoryAreaInput{LeftTopX: -3, LeftTopY: -4, RightBottomX: 7, RightBottomY: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		"local prototype=" + luaString("transport-belt"),
		"local direction=4",
		"local area,area_result={{-3,-4},{7,6}}",
		"local layers=proto.collision_mask.layers",
		"local build_check=defines.build_check_type.manual",
		"surface.find_tiles_filtered{area={{left-margin,top-margin},{right+margin,bottom+margin}},collision_mask=layers}",
		"local margin=math.max(math.ceil(w/2),math.ceil(h/2))",
		"position={x=tx+0.5,y=ty+0.5}",
		"surface.can_place_entity{name=prototype,position=position,direction=direction,force=force,build_check_type=build_check}",
		`surface.find_entities_filtered{area={{position.x-w/2,position.y-h/2},{position.x+w/2,position.y+h/2}},collision_mask=layers}`,
		"terrain=footprint_blocks(position.x,position.y)",
		"tx=left,right-1",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("lua missing %q:\n%s", want, lua)
		}
	}
	if result.Sampled != 4 || result.Placeable != 3 || result.Blocked != 1 {
		t.Fatalf("counts: %+v", result)
	}
	if result.Area == nil || result.Area.LeftTopX != -3 || result.Area.RightBottomY != 6 {
		t.Fatalf("area: %+v", result.Area)
	}
	if len(result.BlockedTiles) != 1 {
		t.Fatalf("blocked tiles: %+v", result.BlockedTiles)
	}
	tile := result.BlockedTiles[0]
	if tile.Position == nil || tile.Position.X != 0.5 || tile.Position.Y != 0.5 {
		t.Fatalf("tile position: %+v", tile.Position)
	}
	if tile.Terrain || tile.Tile != "grass-1" {
		t.Fatalf("tile facts: %+v", tile)
	}
	if len(tile.Blockers) != 2 || tile.Blockers[0] != "stone-furnace" || tile.Blockers[1] != "long-handed-inserter" {
		t.Fatalf("blockers: %+v", tile.Blockers)
	}
}

// An empty probe must decode blockedTiles as an empty list, not nil: Lua encodes
// an empty table as {} and the CLI normalizes only registered list fields.
func TestPlacementProbeEmptyBlockedTiles(t *testing.T) {
	r, _ := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"pipe","direction":0,"area":{"leftTopX":0,"leftTopY":0,"rightBottomX":2,"rightBottomY":2},"sampled":4,"placeable":4,"blocked":0,"blockedTiles":{}}`)
	result, err := placementProbeQuery(r, &PlacementProbeInput{
		Surface: "nauvis", Force: "player", Prototype: "pipe",
		Area: &FactoryAreaInput{RightBottomX: 2, RightBottomY: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BlockedTiles == nil || len(result.BlockedTiles) != 0 {
		t.Fatalf("blocked tiles: %#v", result.BlockedTiles)
	}
	if result.Blocked != 0 || result.Placeable != 4 {
		t.Fatalf("counts: %+v", result)
	}
}

// Invalid probes must fail before any RCON dial.
func TestPlacementProbeValidation(t *testing.T) {
	area := &FactoryAreaInput{RightBottomX: 2, RightBottomY: 2}
	cases := []struct {
		name  string
		input PlacementProbeInput
		want  string
	}{
		{"missing prototype", PlacementProbeInput{Surface: "nauvis", Force: "player", Area: area}, "surface, force and prototype are required"},
		{"missing surface", PlacementProbeInput{Force: "player", Prototype: "pipe", Area: area}, "surface, force and prototype are required"},
		{"negative direction", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Direction: -1, Area: area}, "direction must be between 0 and 15"},
		{"excess direction", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Direction: 16, Area: area}, "direction must be between 0 and 15"},
		{"missing area", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe"}, "area is required"},
		{"nan coordinate", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Area: &FactoryAreaInput{LeftTopX: math.NaN(), RightBottomX: 2, RightBottomY: 2}}, "area coordinates must be finite"},
		{"inverted area", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Area: &FactoryAreaInput{LeftTopX: 5, RightBottomX: 2, RightBottomY: 2}}, "area must have positive dimensions"},
		{"too wide", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Area: &FactoryAreaInput{RightBottomX: maxAreaTiles + 1, RightBottomY: 2}}, "no greater than 1024 tiles"},
		{"too many samples", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Area: &FactoryAreaInput{RightBottomX: 400, RightBottomY: 200}}, "above the 65536 tile limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Resolver{addr: "127.0.0.1:1", password: "password"}
			if _, err := placementProbeQuery(r, &tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
	if _, err := placementProbeQuery(&Resolver{}, nil); err == nil {
		t.Fatal("nil input must fail")
	}
}

// GRID mode must answer with a compact placeability map so a wide scan does not
// pay for per-tile blocker details.
func TestPlacementProbeGridMode(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"wooden-chest","direction":0,"area":{"leftTopX":0,"leftTopY":0,"rightBottomX":3,"rightBottomY":2},"sampled":6,"placeable":4,"blocked":2,"blockedTiles":{},"rows":["101","011"],"sites":{}}`)
	result, err := placementProbeQuery(r, &PlacementProbeInput{
		Surface: "nauvis", Force: "player", Prototype: "wooden-chest",
		Mode: PlacementProbeModeGrid,
		Area: &FactoryAreaInput{RightBottomX: 3, RightBottomY: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, "local collect_blocked=false") {
		t.Fatalf("grid mode must skip blocker collection:\n%s", lua)
	}
	if len(result.Rows) != 2 || result.Rows[0] != "101" || result.Rows[1] != "011" {
		t.Fatalf("rows: %#v", result.Rows)
	}
	if result.BlockedTiles == nil || len(result.BlockedTiles) != 0 {
		t.Fatalf("blocked tiles must be an empty list: %#v", result.BlockedTiles)
	}
	if result.Sites == nil || len(result.Sites) != 0 {
		t.Fatalf("sites must be an empty list: %#v", result.Sites)
	}
}

// SITES mode must carry the minimum rectangle size and the site cap into the Lua
// and decode the returned rectangles.
func TestPlacementProbeSitesMode(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","prototype":"wooden-chest","direction":0,"area":{"leftTopX":-8,"leftTopY":-4,"rightBottomX":8,"rightBottomY":4},"sampled":128,"placeable":96,"blocked":32,"blockedTiles":{},"rows":{},"sites":[{"origin":{"x":-3,"y":-2},"width":6,"height":4},{"origin":{"x":4,"y":-3},"width":2,"height":3}]}`)
	result, err := placementProbeQuery(r, &PlacementProbeInput{
		Surface: "nauvis", Force: "player", Prototype: "wooden-chest",
		Mode: PlacementProbeModeSites, SiteWidth: 2, SiteHeight: 3, MaxSites: 7,
		Area: &FactoryAreaInput{LeftTopX: -8, LeftTopY: -4, RightBottomX: 8, RightBottomY: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{"local minw,minh,maxsites=2,3,7", "local function largest_site()", "local collect_blocked=false"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("lua missing %q:\n%s", want, lua)
		}
	}
	if len(result.Sites) != 2 {
		t.Fatalf("sites: %+v", result.Sites)
	}
	first := result.Sites[0]
	if first.Origin == nil || first.Origin.X != -3 || first.Origin.Y != -2 || first.Width != 6 || first.Height != 4 {
		t.Fatalf("first site: %+v", first)
	}
	if result.Rows == nil || len(result.Rows) != 0 {
		t.Fatalf("rows must be an empty list: %#v", result.Rows)
	}
}

// Site geometry and mode values must be rejected before any RCON dial.
func TestPlacementProbeSiteValidation(t *testing.T) {
	area := &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8}
	cases := []struct {
		name  string
		input PlacementProbeInput
		want  string
	}{
		{"unknown mode", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: "PATCHWORK", SiteWidth: 1, SiteHeight: 1, MaxSites: 1, Area: area}, "unsupported placement probe mode"},
		{"zero width", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: PlacementProbeModeSites, SiteWidth: 0, SiteHeight: 1, MaxSites: 1, Area: area}, "site width and height must be finite and at least 1"},
		{"nan height", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: PlacementProbeModeSites, SiteWidth: 1, SiteHeight: math.NaN(), MaxSites: 1, Area: area}, "site width and height must be finite and at least 1"},
		{"site wider than area", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: PlacementProbeModeSites, SiteWidth: 9, SiteHeight: 1, MaxSites: 1, Area: area}, "must not exceed the probe area"},
		{"zero max sites", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: PlacementProbeModeSites, SiteWidth: 1, SiteHeight: 1, MaxSites: 0, Area: area}, "maxSites must be between 1 and 1000"},
		{"excess max sites", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: PlacementProbeModeSites, SiteWidth: 1, SiteHeight: 1, MaxSites: maxProbeSites + 1, Area: area}, "maxSites must be between 1 and 1000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Resolver{addr: "127.0.0.1:1", password: "password"}
			if _, err := placementProbeQuery(r, &tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

// A belt-occupied tile must be reported blocked for every probe direction.
// can_place_entity accepts the tile for every direction that differs from the
// belt's own one because the engine would fast-replace it, so the probe must
// not rely on that engine check alone (factorio-ai-player-0ge).
func TestPlacementProbeBeltOccupiedTileBlockedForEveryDirection(t *testing.T) {
	stubs := `
defines.build_check_type={manual=2}
prototypes={entity={["transport-belt"]={collision_mask={layers={object=true}},tile_width=1,tile_height=1}},item={},fluid={}}
local belt={name="transport-belt",type="transport-belt",valid=true,unit_number=1949,
  position={x=17.5,y=27.5},
  bounding_box={left_top={x=17,y=27},right_bottom={x=18,y=28}}}
local function overlaps_belt(area)
  return area[1][1]<18 and area[2][1]>17 and area[1][2]<28 and area[2][2]>27
end
local surface=game.surfaces["nauvis"]
surface.find_entities_filtered=function(filter)
  if filter.position then
    if filter.position.x==17.5 and filter.position.y==27.5 then return {belt} end
    return {}
  end
  if filter.area and overlaps_belt(filter.area) then return {belt} end
  return {}
end
surface.find_tiles_filtered=function() return {} end
-- The engine's manual check accepts a belt on a belt unless the direction
-- matches: that is the fast-replace acceptance this probe must override.
surface.can_place_entity=function(spec)
  local on_belt=spec.position.x==17.5 and spec.position.y==27.5
  return not (on_belt and spec.direction==0)
end
surface.get_tile=function(x,y) return {valid=true,name="grass-1"} end
`
	for _, direction := range []int{0, 4, 8, 12} {
		t.Run(strconv.Itoa(direction), func(t *testing.T) {
			r, commands := serveLuaOnce(t, "{}")
			if _, err := placementProbeQuery(r, &PlacementProbeInput{
				Surface: "nauvis", Force: "player", Prototype: "transport-belt",
				Direction: direction,
				Area:      &FactoryAreaInput{LeftTopX: 17, LeftTopY: 27, RightBottomX: 18, RightBottomY: 28},
			}); err != nil {
				t.Fatal(err)
			}
			source := <-commands
			assertions := `
assert(captured, "no payload captured")
assert(not captured.error, "probe failed: "..tostring(captured.error))
assert(captured.sampled==1, "sampled "..tostring(captured.sampled))
assert(captured.blocked==1, "blocked "..tostring(captured.blocked))
assert(captured.placeable==0, "placeable "..tostring(captured.placeable))
assert(#captured.blockedTiles==1, "blockedTiles "..#captured.blockedTiles)
local names=captured.blockedTiles[1].blockers
assert(#names==1 and names[1]=="transport-belt", "blockers "..tostring(names[1]))
`
			runLuaFixture(t, source, stubs, assertions)
		})
	}
}

// A resource tile stays placeable: belts are legal over ore, and the occupancy
// guard must keep excluding resources (and ghosts and item stacks) so a mining
// field does not read as a wall.
func TestPlacementProbeResourceTileStaysPlaceable(t *testing.T) {
	r, commands := serveLuaOnce(t, "{}")
	if _, err := placementProbeQuery(r, &PlacementProbeInput{
		Surface: "nauvis", Force: "player", Prototype: "transport-belt",
		Direction: 0,
		Area:      &FactoryAreaInput{LeftTopX: 30, LeftTopY: 30, RightBottomX: 31, RightBottomY: 31},
	}); err != nil {
		t.Fatal(err)
	}
	stubs := `
defines.build_check_type={manual=2}
prototypes={entity={["transport-belt"]={collision_mask={layers={object=true}},tile_width=1,tile_height=1}},item={},fluid={}}
local ore={name="iron-ore",type="resource",valid=true,
  position={x=30.5,y=30.5},
  bounding_box={left_top={x=30,y=30},right_bottom={x=31,y=31}}}
local ghost={name="transport-belt",type="entity-ghost",valid=true,
  position={x=30.5,y=30.5},
  bounding_box={left_top={x=30,y=30},right_bottom={x=31,y=31}}}
local stack={name="coal",type="item-entity",valid=true,
  position={x=30.5,y=30.5},
  bounding_box={left_top={x=30,y=30},right_bottom={x=31,y=31}}}
local surface=game.surfaces["nauvis"]
surface.find_entities_filtered=function(filter)
  if filter.position then return {ore,ghost,stack} end
  -- Resources, ghosts and item stacks are not on the prototype's collision
  -- layer, so the engine's collision scan finds nothing there.
  return {}
end
surface.find_tiles_filtered=function() return {} end
surface.can_place_entity=function() return true end
surface.get_tile=function(x,y) return {valid=true,name="grass-1"} end
`
	source := <-commands
	assertions := `
assert(captured, "no payload captured")
assert(not captured.error, "probe failed: "..tostring(captured.error))
assert(captured.sampled==1, "sampled "..tostring(captured.sampled))
assert(captured.placeable==1, "placeable "..tostring(captured.placeable))
assert(captured.blocked==0, "blocked "..tostring(captured.blocked))
assert(#captured.blockedTiles==0, "blockedTiles "..#captured.blockedTiles)
`
	runLuaFixture(t, source, stubs, assertions)
}
