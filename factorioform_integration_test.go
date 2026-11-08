//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const ffE2EPlanQuery = `query($input: FactorioformPlanInput!) {
  factorioformPlan(input: $input) {
    planId status
    summary { create update replace delete drift adopt conflict pending noop }
    diffs { action address entity unitNumber reason changes { path before after } blockers { entity unitNumber } }
    steps { order kind addresses operation document variables requiresActor }
  }
}`

const ffE2ERefreshQuery = `mutation($input: FactorioformRefreshInput!) {
  factorioformRefresh(input: $input) { status recorded { address unitNumber } pruned unmatched { address reason } }
}`

// TestIntegrationFactorioformE2E proves the whole Factorioform loop on a
// disposable engine with testdata/factorioform/example.ftf: plan -> execute the
// step documents in order -> refresh -> plan reports CONVERGED with no steps,
// then every diff action (CREATE, UPDATE, REPLACE, DELETE, DRIFT, ADOPT,
// CONFLICT, PENDING, NOOP) is provoked by changing the manifest or the world.
func TestIntegrationFactorioformE2E(t *testing.T) {
	started := time.Now()
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })
	h.createTestSurface(t)
	character := h.spawnCharacter(t)
	t.Cleanup(func() { h.destroyCharacter(t, character.id) })
	h.grantItems(t, character.id, map[string]int{
		"assembling-machine-2": 3, "speed-module": 6, "fast-inserter": 2, "splitter": 2,
		"transport-belt": 6, "underground-belt": 2, "constant-combinator": 2,
		"wooden-chest": 2, "iron-chest": 2,
	})
	h.lua(t, `storage.factorio_ai_player_factorioform=nil
game.forces.player.recipes["electronic-circuit"].enabled=true
game.forces.player.recipes["iron-gear-wheel"].enabled=true
rcon.print("ok")`)

	raw, err := os.ReadFile("testdata/factorioform/example.ftf")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Replace(string(raw), `name    = "example"`, `name    = "example"
  surface = "`+integrationSurface+`"`, 1)
	if base == string(raw) {
		t.Fatal("example.ftf header changed; surface injection failed")
	}
	// The variable override proves manifest variables reach plan and refresh.
	origin := map[string]any{"x": -12.5, "y": 10.5}
	cut := strings.Index(base, `resource "const" "signal"`)
	if cut < 0 {
		t.Fatal("example.ftf has no const.signal resource")
	}
	withoutConst := base[:cut]

	run := func(args ...string) map[string]any {
		t.Helper()
		var out bytes.Buffer
		if err := runGraphQLInProcess(append([]string{"--addr", h.addr}, args...), &out, h.password); err != nil {
			t.Fatalf("%v failed: %v\n%s", args[0], err, out.String())
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("bad JSON %q: %v", out.String(), err)
		}
		return result
	}
	input := func(manifest string) string {
		vars, _ := json.Marshal(map[string]any{"input": map[string]any{
			"files":     []any{map[string]any{"name": "example.ftf", "content": manifest}},
			"variables": map[string]any{"origin": origin},
			"actor":     map[string]any{"character": character.id},
		}})
		return string(vars)
	}
	plan := func(manifest string) map[string]any {
		t.Helper()
		return jsonObject(t, run("query", "--format", "json", ffE2EPlanQuery, "--variables", input(manifest)), "data", "factorioformPlan")
	}
	refresh := func(manifest string) map[string]any {
		t.Helper()
		vars, _ := json.Marshal(map[string]any{"input": map[string]any{
			"files":     []any{map[string]any{"name": "example.ftf", "content": manifest}},
			"variables": map[string]any{"origin": origin},
		}})
		return jsonObject(t, run("mutation", "--format", "json", ffE2ERefreshQuery, "--variables", string(vars)), "data", "factorioformRefresh")
	}
	execute := func(p map[string]any) {
		t.Helper()
		for _, rawStep := range jsonArray(t, p, "steps") {
			step := rawStep.(map[string]any)
			vars, _ := json.Marshal(step["variables"])
			result := run("mutation", "--format", "json", jsonString(t, step, "document"), "--variables", string(vars))
			for _, v := range jsonObject(t, result, "data") {
				m, ok := v.(map[string]any)
				if !ok {
					continue
				}
				if errs, ok := m["errors"].([]any); ok && len(errs) > 0 {
					t.Fatalf("step %v %s per-entry errors: %v\nvariables: %s", step["order"], step["operation"], errs, vars)
				}
				if possible, ok := m["possible"].(bool); ok && !possible {
					t.Fatalf("step %v %s impossible: %v\nvariables: %s", step["order"], step["operation"], m, vars)
				}
			}
		}
	}
	row := func(p map[string]any, address string) map[string]any {
		t.Helper()
		for _, d := range jsonArray(t, p, "diffs") {
			if m := d.(map[string]any); m["address"] == address {
				return m
			}
		}
		t.Fatalf("plan has no row %s: %v", address, p["diffs"])
		return nil
	}
	wantAction := func(p map[string]any, address, action string) map[string]any {
		t.Helper()
		r := row(p, address)
		if r["action"] != action {
			t.Fatalf("%s: action %v, want %s\nrow: %v", address, r["action"], action, r)
		}
		return r
	}
	wantChange := func(r map[string]any, path string) {
		t.Helper()
		for _, c := range r["changes"].([]any) {
			if c.(map[string]any)["path"] == path {
				return
			}
		}
		t.Fatalf("row %v has no change at %s", r["address"], path)
	}
	// converge: plan -> execute -> refresh -> plan must be CONVERGED, no steps.
	converge := func(manifest, label string) {
		t.Helper()
		p := plan(manifest)
		if len(jsonArray(t, p, "steps")) == 0 {
			t.Fatalf("%s: nothing to execute: %v", label, p["summary"])
		}
		execute(p)
		rf := refresh(manifest)
		if un := jsonArray(t, rf, "unmatched"); len(un) != 0 {
			t.Logf("%s: refresh unmatched %v", label, un)
		}
		if rf["status"] != "CONVERGED" {
			// A DRIFT row only has its dead state entry dropped by the first
			// refresh; the recreated entity is an ADOPT row recorded by the second.
			rf = refresh(manifest)
		}
		final := plan(manifest)
		if jsonString(t, final, "status") != "CONVERGED" || len(jsonArray(t, final, "steps")) != 0 {
			t.Fatalf("%s: not converged after refresh: status=%v diffs=%v", label, final["status"], final["diffs"])
		}
		t.Logf("%s: converged (%v)", label, p["summary"])
	}
	probe := func(lua string) string {
		t.Helper()
		return strings.TrimSpace(h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
local function at(name,x,y) return s.find_entities_filtered{name=name,position={x,y},radius=0.4}[1] end
`+lua))
	}

	// CREATE: an empty world creates everything, then the loop converges.
	p := plan(base)
	if jsonString(t, p, "status") != "CHANGES" {
		t.Fatalf("initial status %v", p["status"])
	}
	if n := jsonNumber(t, jsonObject(t, p, "summary"), "create"); n != 15 {
		t.Fatalf("initial create count %v, want 15 (3 assemblers, inserter, splitter, 6 belts, 2 undergrounds, combinator, wire)", n)
	}
	wantAction(p, "assembler.circuit[1]", "CREATE")
	wantAction(p, "belt.bus.path[3]", "CREATE")
	execute(p)
	world := probe(`local a=at("assembling-machine-2",-8.5,10.5)
local i=at("fast-inserter",-14.5,10.5)
local sp=at("splitter",-12.5,5)
local wires=0 for _,cn in pairs(i.get_wire_connectors(true)) do wires=wires+cn.connection_count end
rcon.print(table.concat({a.get_recipe().name,a.get_module_inventory().get_item_count(),i.get_filter(1).name,sp.splitter_filter.name,sp.splitter_output_priority,wires,#s.find_entities_filtered{name="transport-belt"},#s.find_entities_filtered{name="underground-belt"}},"|"))`)
	if world != "electronic-circuit|2|copper-cable|iron-ore|right|1|6|2" {
		t.Fatalf("world after execute: %s", world)
	}
	rf := refresh(base)
	if n := len(jsonArray(t, rf, "recorded")); n != 14 {
		t.Fatalf("refresh recorded %d addresses, want 14 entities: %v", n, rf)
	}
	final := plan(base)
	if jsonString(t, final, "status") != "CONVERGED" || len(jsonArray(t, final, "steps")) != 0 {
		t.Fatalf("not converged after first refresh: %v", final["diffs"])
	}
	if n := jsonNumber(t, jsonObject(t, final, "summary"), "noop"); n != 15 {
		t.Fatalf("converged noop count %v, want 15", n)
	}

	// UPDATE: rotating the inserter.
	feed := strings.TrimSpace(probe(`rcon.print(at("fast-inserter",-14.5,10.5).unit_number)`))
	h.mutation(t, fmt.Sprintf(`mutation { setDirection(input:{actor:{character:%q},target:{unitNumber:%q,surface:%q},direction:12}) { entity } }`, character.id, feed, integrationSurface))
	p = plan(base)
	wantChange(wantAction(p, "inserter.feed", "UPDATE"), "direction")
	converge(base, "rotated inserter")

	// UPDATE: a different recipe on one assembler.
	gear := strings.TrimSpace(probe(`rcon.print(at("assembling-machine-2",-8.5,10.5).unit_number)`))
	h.mutation(t, fmt.Sprintf(`mutation { setRecipe(input:{actor:{character:%q},target:{unitNumber:%q,surface:%q},recipe:"iron-gear-wheel",drain:TO_ACTOR}) { entity } }`, character.id, gear, integrationSurface))
	p = plan(base)
	wantChange(wantAction(p, "assembler.circuit[1]", "UPDATE"), "recipe")
	converge(base, "changed recipe")

	// DRIFT: a managed entity vanished from the world.
	splitter := strings.TrimSpace(probe(`rcon.print(at("splitter",-12.5,5).unit_number)`))
	h.mutation(t, fmt.Sprintf(`mutation { remove(input:{actor:{character:%q},target:{unitNumber:%q,surface:%q},mode:DESTROY}) { entity } }`, character.id, splitter, integrationSurface))
	p = plan(base)
	if jsonString(t, p, "status") != "DRIFT_DETECTED" {
		t.Fatalf("status after destroying the splitter: %v", p["status"])
	}
	wantAction(p, "splitter.sort", "DRIFT")
	converge(base, "destroyed splitter")

	// ADOPT then NOOP: a pre-placed matching chest is adopted, imported, and
	// then reads as managed.
	adoptManifest := base + `
resource "chest" "pre" {
  entity   = "wooden-chest"
  position = { x = -20.5, y = 20.5 }
}
`
	h.lua(t, `assert(game.surfaces["`+integrationSurface+`"].create_entity{name="wooden-chest",position={-20.5,20.5},force="player"})
rcon.print("ok")`)
	p = plan(adoptManifest)
	adopt := wantAction(p, "chest.pre", "ADOPT")
	if len(jsonArray(t, p, "steps")) != 0 {
		t.Fatalf("ADOPT must not emit steps: %v", p["steps"])
	}
	h.mutation(t, fmt.Sprintf(`mutation { factorioformImport(input:{name:"example",surface:%q,address:"chest.pre",target:{entity:"wooden-chest",unitNumber:%q}}) { tick } }`, integrationSurface, adopt["unitNumber"]))
	p = plan(adoptManifest)
	wantAction(p, "chest.pre", "NOOP")
	if jsonString(t, p, "status") != "CONVERGED" {
		t.Fatalf("status after import: %v %v", p["status"], p["diffs"])
	}

	// REPLACE: the same address now wants a different prototype.
	replaceManifest := strings.Replace(adoptManifest, `entity   = "wooden-chest"`, `entity   = "iron-chest"`, 1)
	p = plan(replaceManifest)
	wantAction(p, "chest.pre", "REPLACE")
	converge(replaceManifest, "replaced chest")
	if got := probe(`rcon.print(tostring(at("wooden-chest",-20.5,20.5))..tostring(at("iron-chest",-20.5,20.5)~=nil))`); got != "niltrue" {
		t.Fatalf("chest replacement world: %s", got)
	}

	// CONFLICT and PENDING: a foreign entity on a desired tile, and a ghost of a
	// desired entity.
	conflictManifest := replaceManifest + `
resource "chest" "blocked" {
  entity   = "steel-chest"
  position = { x = -24.5, y = 20.5 }
}
resource "chest" "ghosted" {
  entity   = "wooden-chest"
  position = { x = -26.5, y = 20.5 }
}
`
	h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
assert(s.create_entity{name="wooden-chest",position={-24.5,20.5},force="player"})
assert(s.create_entity{name="entity-ghost",inner_name="wooden-chest",position={-26.5,20.5},force="player"})
rcon.print("ok")`)
	p = plan(conflictManifest)
	if jsonString(t, p, "status") != "CONFLICT" {
		t.Fatalf("conflict status: %v", p["status"])
	}
	conflict := wantAction(p, "chest.blocked", "CONFLICT")
	named := false
	for _, b := range conflict["blockers"].([]any) {
		named = named || b.(map[string]any)["entity"] == "wooden-chest"
	}
	if !named {
		t.Fatalf("CONFLICT does not name the wooden-chest: %v", conflict)
	}
	wantAction(p, "chest.ghosted", "PENDING")

	// DELETE: resources removed from the manifest are removed from the world.
	h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
for _,e in pairs(s.find_entities_filtered{position={-24.5,20.5},radius=0.4}) do e.destroy() end
for _,e in pairs(s.find_entities_filtered{position={-26.5,20.5},radius=0.4}) do e.destroy() end
rcon.print("ok")`)
	p = plan(withoutConst)
	wantAction(p, "const.signal", "DELETE")
	wantAction(p, "chest.pre", "DELETE")
	converge(withoutConst, "removed combinator")
	if got := probe(`rcon.print(#s.find_entities_filtered{name="constant-combinator"})`); got != "0" {
		t.Fatalf("combinator still present: %s", got)
	}
	t.Logf("Factorioform end-to-end subtest runtime: %s", time.Since(started).Round(time.Second))
}
