package main

import (
	"io"
	"math"
	"strings"
	"testing"
)

// Invalid input must fail before any RCON dial, so a bad request never spends a
// game round trip or a Lua scan of a large area.
func TestBeltComponentsValidation(t *testing.T) {
	area := &FactoryAreaInput{RightBottomX: 10, RightBottomY: 10}
	cases := []struct {
		name  string
		input BeltComponentsInput
		want  string
	}{
		{"missing surface", BeltComponentsInput{Force: "player", Area: area, MaxEntities: 10, Limit: 5}, "surface and force are required"},
		{"missing force", BeltComponentsInput{Surface: "nauvis", Area: area, MaxEntities: 10, Limit: 5}, "surface and force are required"},
		{"missing area", BeltComponentsInput{Surface: "nauvis", Force: "player", MaxEntities: 10, Limit: 5}, "area is required"},
		{"zero maxEntities", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, Limit: 5}, "maxEntities must be between 1 and 5000"},
		{"excess maxEntities", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, MaxEntities: 5001, Limit: 5}, "maxEntities must be between 1 and 5000"},
		{"zero limit", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, MaxEntities: 10}, "limit must be between 1 and 100"},
		{"excess limit", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, MaxEntities: 10, Limit: 101}, "limit must be between 1 and 100"},
		{"negative offset", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, MaxEntities: 10, Limit: 5, Offset: -1}, "offset must be non-negative"},
		{"inverted area", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{LeftTopX: 5, RightBottomX: 2, RightBottomY: 2}, MaxEntities: 10, Limit: 5}, "area must have positive dimensions"},
		{"nan area", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{LeftTopX: math.NaN(), RightBottomX: 2, RightBottomY: 2}, MaxEntities: 10, Limit: 5}, "area coordinates must be finite"},
		{"area beyond analysis limit", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: 1025, RightBottomY: 10}, MaxEntities: 10, Limit: 5}, "no greater than 1024 tiles"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Resolver{addr: "127.0.0.1:1", password: "password"}
			if _, err := beltComponentsQuery(r, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

// The read-only analysis is the one query that accepts areas wider than the
// 256-tile paging cap, and it must pass that bound to the scan.
func TestBeltComponentsAcceptsWideArea(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","area":{"leftTopX":-512,"leftTopY":-512,"rightBottomX":256,"rightBottomY":0},"beltTotal":0,"truncated":false,"pageInfo":{"total":0,"limit":5,"offset":0,"hasMore":false},"components":{}}`)
	result, err := beltComponentsQuery(r, BeltComponentsInput{
		Surface: "nauvis", Force: "player",
		Area:        &FactoryAreaInput{LeftTopX: -512, LeftTopY: -512, RightBottomX: 256, RightBottomY: 0},
		MaxEntities: 1000, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Components == nil || len(result.Components) != 0 {
		t.Fatalf("components: %#v", result.Components)
	}
	if result.BeltTotal != 0 || result.PageInfo == nil || result.PageInfo.Total != 0 {
		t.Fatalf("page: %+v", result)
	}
	lua := <-commands
	if !strings.Contains(lua, "local max_entities,page_limit,page_offset,dead_only=1000,5,0,false") {
		t.Fatalf("bounds must reach Lua:\n%s", lua)
	}
	if !strings.Contains(lua, "type=belt_types") {
		t.Fatal("scan must use the belt prototype set")
	}
}

// A dead component must keep its items, terminals and flags after decoding;
// empty Lua tables must come back as empty lists, not null.
func TestBeltComponentsDecodesComponents(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":99,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":16,"rightBottomY":16},"beltTotal":7,"truncated":false,"pageInfo":{"total":2,"limit":10,"offset":0,"hasMore":false},"components":[
{"id":"1","dead":true,"belts":2,"truncated":false,"items":[{"name":"iron-plate","count":4,"quality":"normal"}],"itemCount":4,"nodes":[{"name":"transport-belt","type":"transport-belt","surface":"nauvis","position":{"x":22.5,"y":-12.5},"unitNumber":"900","direction":12,"lanes":[{"index":1,"lineName":"left_line","side":"left","items":[{"name":"iron-plate","count":4,"quality":"normal"}]},{"index":2,"lineName":"right_line","side":"right","items":[]}]}],"edges":{},"sources":[{"name":"transport-belt","type":"transport-belt","surface":"nauvis","position":{"x":22.5,"y":-12.5},"unitNumber":"900","direction":12,"lanes":{}}],"sinks":[],"inserters":{},"stallEnds":[],"starveHeads":[]},
{"id":"2","dead":false,"belts":5,"truncated":true,"items":{},"itemCount":0,"nodes":[],"edges":{},"sources":[],"sinks":[],"inserters":[{"inserter":{"name":"inserter","type":"inserter","surface":"nauvis","position":{"x":1.5,"y":2.5},"unitNumber":"77"},"pickup":null,"drop":null}],"stallEnds":[],"starveHeads":[]}]}`)
	result, err := beltComponentsQuery(r, BeltComponentsInput{
		Surface: "nauvis", Force: "player",
		Area:        &FactoryAreaInput{RightBottomX: 16, RightBottomY: 16},
		MaxEntities: 500, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Components) != 2 || result.PageInfo.Total != 2 || result.BeltTotal != 7 {
		t.Fatalf("page: %+v", result)
	}
	dead := result.Components[0]
	if !dead.Dead || dead.Belts != 2 || dead.ItemCount != 4 {
		t.Fatalf("dead component: %+v", dead)
	}
	if len(dead.Items) != 1 || dead.Items[0].Name != "iron-plate" || dead.Items[0].Count != 4 || dead.Items[0].Quality != "normal" {
		t.Fatalf("items: %#v", dead.Items)
	}
	if len(dead.Nodes) != 1 || dead.Nodes[0].Direction == nil || *dead.Nodes[0].Direction != 12 {
		t.Fatalf("nodes: %#v", dead.Nodes)
	}
	if len(dead.Nodes[0].Lanes) != 2 || len(dead.Nodes[0].Lanes[0].Items) != 1 || dead.Nodes[0].Lanes[0].LineName != "left_line" {
		t.Fatalf("lanes: %#v", dead.Nodes[0].Lanes)
	}
	for name, list := range map[string][]*LogisticsEndpoint{"sinks": dead.Sinks, "stallEnds": dead.StallEnds, "starveHeads": dead.StarveHeads} {
		if list == nil {
			t.Fatalf("%s must decode as an empty list", name)
		}
	}
	if dead.Edges == nil || len(dead.Edges) != 0 {
		t.Fatalf("edges: %#v", dead.Edges)
	}
	live := result.Components[1]
	if live.Dead || !live.Truncated || live.Items == nil || len(live.Items) != 0 {
		t.Fatalf("live component: %+v", live)
	}
	if len(live.Inserters) != 1 || live.Inserters[0].Inserter == nil || live.Inserters[0].Pickup != nil {
		t.Fatalf("inserters: %#v", live.Inserters)
	}
	lua := <-commands
	for _, needle := range []string{
		"local components,component_of={},{}",
		"type=\"inserter\"",
		"local machine_types=",
		`{{pickup,"pickup"},{drop,"drop"}}`,
		"c.picked[key(target)]=true",
		"c.inserter_seen[inserter_key]",
		"table.sort(out,function(a,b)",
		"dead=(#c.inserters==0 and c.machine_links==0)",
	} {
		if !strings.Contains(lua, needle) {
			t.Fatalf("missing %q in lua:\n%s", needle, lua)
		}
	}
}

// The inspect snapshot must report the belt direction, so callers stop paying a
// second entity query to learn which tile is the successor.
func TestInspectLuaReportsDirection(t *testing.T) {
	lua, err := productionLua("inspect", []string{"--player=wricardo", "--entity=transport-belt", "--x=1.5", "--y=2.5"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lua, "result.direction=direction") {
		t.Fatalf("inspect lua must report direction:\n%s", lua)
	}
}
