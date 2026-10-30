package main

import (
	"math"
	"net"
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
		"surface.find_tiles_filtered{area=area,collision_mask=layers}",
		"position={x=tx+0.5,y=ty+0.5}",
		"surface.can_place_entity{name=prototype",
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
		{"too wide", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Area: &FactoryAreaInput{RightBottomX: 257, RightBottomY: 2}}, "no greater than 256 tiles"},
		{"too many samples", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Area: &FactoryAreaInput{RightBottomX: 200, RightBottomY: 100}}, "above the 16384 tile limit"},
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
		{"zero max sites", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: PlacementProbeModeSites, SiteWidth: 1, SiteHeight: 1, MaxSites: 0, Area: area}, "maxSites must be between 1 and 200"},
		{"excess max sites", PlacementProbeInput{Surface: "nauvis", Force: "player", Prototype: "pipe", Mode: PlacementProbeModeSites, SiteWidth: 1, SiteHeight: 1, MaxSites: 201, Area: area}, "maxSites must be between 1 and 200"},
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
