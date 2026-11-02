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
		{"zero maxEntities", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, Limit: 5}, "maxEntities must be between 1 and 20000"},
		{"excess maxEntities", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, MaxEntities: maxBeltTraversal + 1, Limit: 5}, "maxEntities must be between 1 and 20000"},
		{"zero limit", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, MaxEntities: 10}, "limit must be between 1 and 1000"},
		{"excess limit", BeltComponentsInput{Surface: "nauvis", Force: "player", Area: area, MaxEntities: 10, Limit: maxBeltComponents + 1}, "limit must be between 1 and 1000"},
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
	if !strings.Contains(lua, "local max_entities,page_limit,page_offset,dead_only,include_line_links=1000,5,0,false,false") {
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
{"id":"1","dead":true,"belts":2,"truncated":false,"items":[{"name":"iron-plate","count":4,"quality":"normal"}],"itemCount":4,"mixed":true,"sharedPickupSources":2,"nodes":[{"name":"transport-belt","type":"transport-belt","surface":"nauvis","position":{"x":22.5,"y":-12.5},"unitNumber":"900","direction":12,"lanes":[{"index":1,"lineName":"left_line","side":"left","items":[{"name":"iron-plate","count":4,"quality":"normal"}]},{"index":2,"lineName":"right_line","side":"right","items":[]}]}],"edges":{},"sources":[{"name":"transport-belt","type":"transport-belt","surface":"nauvis","position":{"x":22.5,"y":-12.5},"unitNumber":"900","direction":12,"lanes":{}}],"sinks":[],"inserters":{},"stallEnds":[],"starveHeads":[]},
{"id":"2","dead":false,"belts":5,"truncated":true,"mixed":false,"sharedPickupSources":0,"items":{},"itemCount":0,"nodes":[],"edges":{},"sources":[],"sinks":[],"inserters":[{"inserter":{"name":"inserter","type":"inserter","surface":"nauvis","position":{"x":1.5,"y":2.5},"unitNumber":"77"},"pickup":null,"drop":null}],"stallEnds":[],"starveHeads":[]}]}`)
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
	if !dead.Mixed || dead.SharedPickupSources != 2 {
		t.Fatalf("lane contamination and contention flags: mixed=%v shared=%d", dead.Mixed, dead.SharedPickupSources)
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
		"box_area(area_box,2.5)",
		"for _,e in ipairs(scan_inserters(surface,box_area(area_box,2.5))) do",
		"surface.find_entities_filtered{area=box_area(area_box,2.5),force=force,type=machine_types}",
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

// beltComponentsFixtureSource assembles the production chunk exactly as
// topologyChunk does without the mod, so the fixture exercises the same Lua the
// query sends.
func beltComponentsFixtureSource(locals string) string {
	return transportLineLua + topologyCommonLua + "\n" + locals + beltComponentsCallLua +
		"\nrcon.print(helpers.table_to_json(payload))"
}

// An inserter arm reports a position offset about 0.2 tiles past the centre of
// the tile it reaches: the working mall inserter 2148 drops onto the belt at
// (29.5, 30.5) with a drop_position of (29.5, 30.699). The component join must
// resolve that arm to the tile the item lands on, because a surface point search
// never answers for a belt (live 2.0, tick 3062420: the 38-belt mall run read
// dead:true with inserters:[] while 2148 fed it and 2149 drained it).
func TestBeltComponentsJoinsOffsetInserters(t *testing.T) {
	// A stub surface whose point search answers nothing, exactly like the live
	// one for an arm that reaches a belt, and whose area search serves the
	// seeds, the inserter scan and the machine scan.
	const stubs = `
local surface={index=1,name="nauvis"}
local force={name="player"}
local world={}
local function box(cx,cy,size)
  local half=(size or 1)/2
  return {left_top={x=cx-half,y=cy-half},right_bottom={x=cx+half,y=cy+half}}
end
local function overlaps(e,area)
  if not area then return true end
  local bb=e.bounding_box
  return bb.left_top.x<=area[2][1] and bb.right_bottom.x>=area[1][1]
    and bb.left_top.y<=area[2][2] and bb.right_bottom.y>=area[1][2]
end
surface.find_entities_filtered=function(spec)
  -- A point search is a collision-box probe, not a tile lookup: live 2.0 returns
  -- nothing for the belts an idle arm points at, so the join must not need it.
  if spec.position then return {} end
  local out={}
  for _,e in ipairs(world) do
    local ok=overlaps(e,spec.area)
    if ok and spec.type then
      local kinds=type(spec.type)=="table" and spec.type or {spec.type}
      local hit=false
      for _,kind in ipairs(kinds) do if e.type==kind then hit=true end end
      ok=hit
    end
    if ok and spec.force and e.force~=spec.force then ok=false end
    if ok then out[#out+1]=e end
  end
  return out
end
game={tick=7,surfaces={nauvis=surface},forces={player=force}}
local function belt(unit,cx,cy)
  local e={valid=true,unit_number=unit,type="transport-belt",name="transport-belt",surface=surface,
    force=force,position={x=cx,y=cy},direction=4,status=1,bounding_box=box(cx,cy),
    belt_neighbours={inputs={},outputs={}}}
  e.get_max_transport_line_index=function() return 1 end
  e.get_transport_line=function() return {owner=e,input_lines={},output_lines={},get_contents=function() return {} end} end
  world[#world+1]=e
  return e
end
local function inserter(unit,cx,cy,pickup,drop)
  local e={valid=true,unit_number=unit,type="inserter",name="inserter",surface=surface,force=force,
    position={x=cx,y=cy},direction=0,status=1,bounding_box=box(cx,cy),
    pickup_position=pickup,drop_position=drop}
  world[#world+1]=e
  return e
end
local feed=belt(11,0.5,0.5)
local drain=belt(12,1.5,0.5)
feed.belt_neighbours.outputs[1]=drain
drain.belt_neighbours.inputs[1]=feed
`
	const locals = `local surface_name,force_name="nauvis","player"
local area={{0.5,0.5},{1.5,0.5}}
local max_entities,page_limit,page_offset,dead_only,include_line_links=100,10,0,false,false
`
	source := beltComponentsFixtureSource(locals)
	arm := func(pickup, drop string) string {
		return stubs + "\ninserter(21,0.5,-0.5," + pickup + "," + drop + ")\n"
	}

	// (a) An arm whose offset drop lands on the feed belt joins the run, and the
	// run reads live, not dead.
	runLuaFixture(t, source,
		arm(`{x=0.5,y=-1.5}`, `{x=0.5,y=0.69921875}`),
		`
assert(captured and captured.components,"payload missing: "..tostring(captured and captured.error))
assert(#captured.components==1,"one component expected, got "..#captured.components)
local c=captured.components[1]
assert(c.dead==false,"a fed run must not read dead")
assert(#c.inserters==1,"the offset arm must join the run, got "..#c.inserters)
assert(c.inserters[1].drop and c.inserters[1].drop.unitNumber==11,"drop must resolve to the feed belt")
assert(#c.starveHeads==0,"the fed belt must not read starved")
`)

	// (b) The same offset on a tile with no belt joins nothing, so a genuinely
	// unwired run still reads dead.
	runLuaFixture(t, source,
		arm(`{x=0.5,y=-1.5}`, `{x=0.5,y=1.69921875}`),
		`
assert(#captured.components==1,"one component expected, got "..#captured.components)
local c=captured.components[1]
assert(#c.inserters==0,"an arm over an empty tile must not join, got "..#c.inserters)
assert(c.dead==true,"an unwired run must read dead")
`)

	// (c) A run with no inserter at all is still dead.
	runLuaFixture(t, source, stubs, `
assert(#captured.components==1,"one component expected, got "..#captured.components)
local c=captured.components[1]
assert(#c.inserters==0,"no inserter belongs to the run")
assert(c.dead==true,"a run with no inserter must read dead")
`)
}

// A splitter is one entity spanning two tiles, so the tile index must register
// both of them: an arm reaching the far tile still resolves the splitter, while
// an empty tile stays empty.
func TestBeltTileIndexCoversSplitterTiles(t *testing.T) {
	fixture := `
defines={entity_status={working=1}}
local surface={index=1,name="nauvis"}
local force={name="player"}
local function box(cx,cy,size)
  local half=(size or 1)/2
  return {left_top={x=cx-half,y=cy-half},right_bottom={x=cx+half,y=cy+half}}
end
-- A real splitter box is 1.8 by 0.8 tiles, so it covers two tiles in one axis.
local splitter={valid=true,unit_number=5,type="splitter",name="splitter",surface=surface,force=force,
  position={x=2.5,y=2.5},
  bounding_box={left_top={x=1.6,y=2.1},right_bottom={x=3.4,y=2.9}}}
local index=belt_tile_index({splitter})
`
	assertions := `
assert(index["1:2"]==splitter,"the splitter's left tile must be indexed")
assert(index["2:2"]==splitter,"the splitter's right tile must be indexed")
assert(index["0:2"]==nil and index["1:3"]==nil,"no other tile may be indexed")
local offset=belt_at_tile(index,{x=1.69921875,y=2.69921875})
assert(offset==splitter,"an arm offset inside the far tile must resolve the splitter")
assert(belt_at_tile(index,{x=2.5,y=3.69921875})==nil,"an empty tile must resolve nothing")
`
	source := transportLineLua + topologyCommonLua + fixture
	runLuaFixture(t, source, "", assertions)
}
