//go:build integration

package main

import (
	"context"
	"testing"
)

// TestIntegrationFactorioformLive reads a hand-built fixture through ffReadLive
// on a disposable engine: real quality, both wire connector names, recipe,
// modules, inserter filter, splitter filter, ghost and tree.
func TestIntegrationFactorioformLive(t *testing.T) {
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })
	h.createTestSurface(t)
	h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
local f=game.forces.player
local a=s.create_entity{name="assembling-machine-2",position={-10.5,-10.5},force=f,quality="rare"}
a.set_recipe("electronic-circuit")
local m=a.get_module_inventory(); m.insert{name="speed-module",count=2}
local i=s.create_entity{name="fast-inserter",position={-6.5,-10.5},direction=defines.direction.east,force=f}
i.use_filters=true; i.inserter_filter_mode="whitelist"; i.set_filter(1,"copper-cable")
local sp=s.create_entity{name="splitter",position={0,0.5},force=f}
sp.splitter_filter={name="iron-ore"}; sp.splitter_output_priority="right"
local c=s.create_entity{name="constant-combinator",position={-6.5,-8.5},force=f}
local w1=i.get_wire_connector(defines.wire_connector_id.circuit_red,true)
local w2=c.get_wire_connector(defines.wire_connector_id.circuit_red,true)
assert(w1.connect_to(w2,false))
s.create_entity{name="entity-ghost",inner_name="stone-furnace",position={10,10},force=f}
s.create_entity{name="tree-01",position={5.5,5.5}}
rcon.print("ok")`)
	snap, err := ffReadLive(context.Background(), &Resolver{addr: h.addr, password: h.password}, ffLiveQuery{Surface: integrationSurface, Area: ffBox{-16, -16, 16, 16}})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*ffLive{}
	for _, e := range snap.Entities {
		by[e.Entity] = e
	}
	a := by["assembling-machine-2"]
	if a == nil || a.Quality != "rare" || a.Config["recipe"] != "electronic-circuit" || len(a.Config["modules"].([]string)) != 1 {
		t.Fatalf("assembler %+v", a)
	}
	i := by["fast-inserter"]
	if i == nil || len(i.Wires) != 1 || i.Wires[0].Connector != "circuit_red" || i.Wires[0].PeerConnector != "circuit_red" || i.Wires[0].Color != "red" {
		t.Fatalf("inserter %+v", i)
	}
	if s := by["splitter"]; s == nil || s.Config["splitter.filter"] != "iron-ore" || s.Config["splitter.output_priority"] != "right" {
		t.Fatalf("splitter %+v", s)
	}
	if g := by["entity-ghost"]; g == nil || g.GhostName != "stone-furnace" {
		t.Fatalf("ghost %+v", g)
	}
	if tr := by["tree-01"]; tr == nil || tr.Type != "tree" {
		t.Fatalf("tree %+v", tr)
	}
	t.Logf("inserter=%+v", i)
}
