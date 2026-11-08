package main

import (
	"context"
	"strings"
	"testing"
)

const ffLiveCanned = `{"tick":500,"scanned":6,"absent":[77],"entities":[
{"node":{"id":"nauvis:10","unit":10,"name":"assembling-machine-2","type":"assembling-machine","x":10.5,"y":10.5,"direction":0,"quality":"normal","recipe":"electronic-circuit","recipeQuality":"normal","modules":["speed-module:normal:1","speed-module:normal:1"]},"box":[9.15,9.15,11.85,11.85]},
{"node":{"id":"nauvis:11","unit":11,"name":"fast-inserter","type":"inserter","x":8.5,"y":10.5,"direction":4,"quality":"normal","useFilters":true,"filterMode":"whitelist","stackSizeOverride":4,"filters":[{"index":2,"name":"copper-cable","quality":"normal"}]},"box":[8.15,10.15,8.85,10.85],"settings":{"circuit":{"behaviorType":4,"setFilters":true,"handReadMode":1}}},
{"node":{"id":"nauvis:12","unit":12,"name":"splitter","type":"splitter","x":12,"y":20.5,"direction":0,"quality":"normal","inputPriority":"left","outputPriority":"right","filters":[{"index":1,"name":"iron-ore","quality":"normal"}]},"box":[11.1,20.1,12.9,20.9]},
{"node":{"id":"nauvis:13","unit":13,"name":"entity-ghost","type":"entity-ghost","x":30.5,"y":30.5,"direction":0,"quality":"normal","ghostName":"assembling-machine-1","ghostType":"assembling-machine","ghostRecipe":"iron-gear-wheel"},"box":[29.15,29.15,31.85,31.85],"deconstruct":true},
{"neutral":true,"name":"tree-01","type":"tree","x":5.2,"y":6.3,"box":[5,6,5.4,6.6]},
{"node":{"id":"nauvis:14","unit":14,"name":"small-electric-pole","type":"electric-pole","x":1.5,"y":1.5,"direction":0,"quality":"normal"},"box":[1.3,1.3,1.7,1.7]}
],"wires":[
{"kind":"red","from":"nauvis:11","to":"nauvis:14","fromConnector":"circuit_red","toConnector":"circuit_red"},
{"kind":"red","from":"nauvis:14","to":"nauvis:11","fromConnector":"circuit_red","toConnector":"circuit_red"}]}`

func TestFFReadLiveDecodes(t *testing.T) {
	fake := newFakeRCON(t, ffLiveCanned)
	snap, err := ffReadLive(context.Background(), fake.resolver, ffLiveQuery{Area: ffBox{0, 0, 40, 40}, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*ffLive{}
	for _, e := range snap.Entities {
		by[e.Entity] = e
	}
	if snap.Tick != 500 || len(snap.Tiles) != 1 || len(snap.Absent) != 1 || snap.Absent[0] != 77 {
		t.Fatalf("snapshot %+v", snap)
	}
	asm := by["assembling-machine-2"]
	if asm.Config["recipe"] != "electronic-circuit" || len(asm.Config["modules"].([]string)) != 2 || asm.Footprint != (ffBox{9, 9, 12, 12}) {
		t.Fatalf("assembler %+v", asm)
	}
	ins := by["fast-inserter"]
	f := ins.Config["inserter.filters"].([]string)
	if len(f) != 2 || f[0] != "" || f[1] != "copper-cable" || ins.Config["inserter.filter_mode"] != "whitelist" || ins.Config["inserter.stack_size_override"] != int64(4) || ins.Config["circuit.set_filters"] != true || ins.Direction != 4 {
		t.Fatalf("inserter %+v", ins)
	}
	sp := by["splitter"]
	if sp.Config["splitter.filter"] != "iron-ore" || sp.Config["splitter.input_priority"] != "left" || sp.Config["splitter.output_priority"] != "right" {
		t.Fatalf("splitter %+v", sp)
	}
	g := by["entity-ghost"]
	if g.GhostName != "assembling-machine-1" || !g.ToBeDeconstructed || g.Config["recipe"] != "iron-gear-wheel" {
		t.Fatalf("ghost %+v", g)
	}
	tr := by["tree-01"]
	if tr.Type != "tree" || tr.UnitNumber != 0 || tr.Footprint != (ffBox{5, 6, 6, 7}) {
		t.Fatalf("tree %+v", tr)
	}
	w := ins.Wires
	if len(w) != 1 || w[0] != (ffWire{"red", "circuit_red", 14, "circuit_red"}) || len(by["small-electric-pole"].Wires) != 1 {
		t.Fatalf("wires %+v", w)
	}
}

func TestFFLiveCommandsTile(t *testing.T) {
	cmds, err := ffLiveCommands(ffLiveQuery{Area: ffBox{0, 0, 2000, 50}, StateUnits: make([]int64, ffLiveUnitBatch+1)})
	if err != nil {
		t.Fatal(err)
	}
	tiles, lookups := 0, 0
	for _, c := range cmds {
		if c.Area != nil {
			tiles++
			if c.Area[2]-c.Area[0] > ffLiveTileSide || c.Area[3]-c.Area[1] > ffLiveTileSide {
				t.Fatalf("oversized tile %v", *c.Area)
			}
		} else {
			lookups++
		}
		if !strings.Contains(c.Lua, "graph_node(") {
			t.Fatal("missing prelude")
		}
	}
	if want := (2000 + ffLiveTileSide - 1) / ffLiveTileSide; tiles != want || lookups != 2 {
		t.Fatalf("tiles=%d lookups=%d", tiles, lookups)
	}
	if _, err := ffLiveCommands(ffLiveQuery{Area: ffBox{0, 0, ffLiveMaxSide + 1, 10}}); err == nil {
		t.Fatal("expected oversize error")
	}
}

func TestFFReadLiveTileTicks(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":100,"entities":[],"wires":[],"absent":[]}`, `{"tick":105,"entities":[],"wires":[],"absent":[]}`)
	snap, err := ffReadLive(context.Background(), fake.resolver, ffLiveQuery{Area: ffBox{0, 0, 100, 10}, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tiles) != 2 || snap.Tiles[0].Tick+snap.Tiles[1].Tick != 205 || snap.Tiles[0].Tick == snap.Tiles[1].Tick || snap.Tick != 105 {
		t.Fatalf("%+v", snap.Tiles)
	}
}
