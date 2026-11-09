//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const ffPlanLiveQuery = `query($input: FactorioformPlanInput!) {
  factorioformPlan(input: $input) {
    formatVersion manifest planId tick surface force status
    summary { create update replace delete drift adopt conflict pending noop }
    diffs { action address entity unitNumber reason changes { path before after } }
    steps { order kind addresses operation document variables requiresActor }
    materials { name quality count }
  }
}`

func ffLiveManifest(chestEntity, filter, modules string, inserterDir string, withCalc, withWire bool) string {
	wire := ""
	if withWire {
		wire = `
wire {
  color          = "red"
  from           = resource.inserter.feed
  to             = resource.const.k
  from_connector = "circuit_red"
  to_connector   = "circuit_red"
}
`
	}
	calc := ""
	if withCalc {
		calc = `
resource "calc" "m" {
  entity   = "arithmetic-combinator"
  position = { x = 26.5, y = 11 }
  combinator {
    arithmetic {
      first_signal    = { name = "iron-plate" }
      operation       = "*"
      second_constant = 2
      output_signal   = { name = "iron-plate" }
    }
  }
}`
	}
	return fmt.Sprintf(`
factorioform {
  version = 1
  name    = "plan-live"
  surface = %q
}
resource "assembler" "a" {
  entity   = "assembling-machine-2"
  position = { x = 10.5, y = 10.5 }
  quality  = "rare"
  recipe   = "electronic-circuit"
  modules  = %s
}
resource "inserter" "feed" {
  entity    = "fast-inserter"
  position  = { x = 8.5, y = 10.5 }
  direction = direction.%s
  inserter {
    filters     = [%q]
    filter_mode = "whitelist"
  }
  circuit { read_hand_contents = true }
}
resource "splitter" "sort" {
  entity    = "splitter"
  position  = { x = 20.5, y = 5 }
  direction = direction.east
  splitter {
    filter          = %q
    output_priority = "left"
  }
}
resource "chest" "c" {
  entity   = %q
  position = { x = 18.5, y = 10.5 }
  container {
    inventory     = "chest"
    allowed_slots = 4
  }
}
resource "logi" "req" {
  entity   = "requester-chest"
  position = { x = 22.5, y = 10.5 }
  logistic {
    section {
      slot {
        index = 1
        name  = "iron-plate"
        min   = 10
      }
    }
  }
}
resource "const" "k" {
  entity   = "constant-combinator"
  position = { x = 13.5, y = 10.5 }
  constant_combinator {
    section {
      slot {
        index  = 1
        signal = { name = "iron-plate" }
        count  = 5
      }
    }
  }
}%s
resource "tunnel" "out" {
  entity           = "underground-belt"
  position         = { x = 5.5, y = 5.5 }
  direction        = direction.east
  underground_type = "output"
}
resource "belt" "bus" {
  belt {
    entity = "transport-belt"
    path = [
      { x = 0.5, y = 0.5, direction = direction.east },
      { x = 1.5, y = 0.5, direction = direction.east },
      { x = 2.5, y = 0.5, direction = direction.north },
      { x = 2.5, y = -0.5, direction = direction.north },
      { x = 2.5, y = -1.5, direction = direction.east },
    ]
  }
}%s
`, integrationSurface, modules, inserterDir, filter, map[bool]string{true: "iron-ore", false: ""}[filter != "iron-plate"], chestEntity, calc, wire)
}

// TestIntegrationFactorioformPlan runs every step kind of a plan against a
// disposable engine: plan -> execute the steps -> verify the world -> import
// the adopted units -> plan a changed manifest and execute its update, replace,
// delete and wire steps too.
func TestIntegrationFactorioformPlan(t *testing.T) {
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })
	h.createTestSurface(t)
	character := h.spawnCharacter(t)
	t.Cleanup(func() { h.destroyCharacter(t, character.id) })
	h.grantItems(t, character.id, map[string]int{
		"fast-inserter": 2, "splitter": 2, "wooden-chest": 2, "iron-chest": 2,
		"requester-chest": 2, "constant-combinator": 2, "arithmetic-combinator": 2,
		"underground-belt": 2, "transport-belt": 10, "speed-module": 4,
	})
	h.lua(t, `storage.factorio_ai_player_factorioform=nil
local inv=game.get_entity_by_unit_number(`+character.id+`).get_inventory(defines.inventory.character_main)
assert(inv.insert{name="assembling-machine-2",quality="rare",count=2}==2)
assert(inv.insert{name="speed-module",quality="rare",count=4}==4)
game.forces.player.recipes["electronic-circuit"].enabled=true
rcon.print("ok")`)

	run := func(args ...string) (map[string]any, error) {
		var out bytes.Buffer
		err := runGraphQLInProcess(append([]string{"--addr", h.addr}, args...), &out, h.password)
		var result map[string]any
		if out.Len() > 0 {
			_ = json.Unmarshal(out.Bytes(), &result)
		}
		return result, err
	}
	plan := func(manifest string) map[string]any {
		t.Helper()
		vars, _ := json.Marshal(map[string]any{"input": map[string]any{
			"files": []any{map[string]any{"name": "live.ftf", "content": manifest}},
			"actor": map[string]any{"character": character.id},
		}})
		result, err := run("query", "--format", "json", ffPlanLiveQuery, "--variables", string(vars))
		if err != nil {
			t.Fatalf("factorioformPlan: %v\n%s", err, manifest)
		}
		return jsonObject(t, result, "data", "factorioformPlan")
	}
	execute := func(p map[string]any) {
		t.Helper()
		for _, raw := range jsonArray(t, p, "steps") {
			step := raw.(map[string]any)
			vars, _ := json.Marshal(step["variables"])
			result, err := run("mutation", "--format", "json", jsonString(t, step, "document"), "--variables", string(vars))
			if err != nil {
				t.Fatalf("step %v %s failed: %v\nvariables: %s", step["order"], step["operation"], err, vars)
			}
			// Batch operations continue past per-entry failures; surface them.
			data := jsonObject(t, result, "data")
			for _, v := range data {
				if m, ok := v.(map[string]any); ok {
					if errs, ok := m["errors"].([]any); ok && len(errs) > 0 {
						t.Fatalf("step %v %s reported per-entry errors: %v", step["order"], step["operation"], errs)
					}
					if possible, ok := m["possible"].(bool); ok && !possible {
						t.Fatalf("step %v %s impossible: %v", step["order"], step["operation"], m)
					}
				}
			}
		}
	}
	probe := func(lua string) string {
		t.Helper()
		return h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
local function at(name,x,y) return s.find_entities_filtered{name=name,position={x,y},radius=0.4}[1] end
`+lua)
	}

	// Phase 1: everything is created.
	p1 := plan(ffLiveManifest("wooden-chest", "copper-cable", `["speed-module", "speed-module@rare"]`, "east", true, true))
	if jsonString(t, p1, "status") != "CHANGES" {
		t.Fatalf("status %v", p1["status"])
	}
	execute(p1)
	got := probe(`local a=at("assembling-machine-2",10.5,10.5)
local i=at("fast-inserter",8.5,10.5)
local sp=at("splitter",20.5,5)
local c=at("wooden-chest",18.5,10.5)
local k=at("constant-combinator",13.5,10.5)
local mods=a.get_module_inventory().get_item_count()
local wires=0 for _,cn in pairs(i.get_wire_connectors(true)) do wires=wires+cn.connection_count end
rcon.print(table.concat({a.get_recipe().name,a.quality.name,mods,i.get_filter(1).name,i.direction,sp.splitter_filter.name,sp.splitter_output_priority,c.get_inventory(defines.inventory.chest).get_bar(),#at("requester-chest",22.5,10.5).get_logistic_point(0).sections,wires,#s.find_entities_filtered{name="transport-belt"},k.get_or_create_control_behavior().sections_count,at("arithmetic-combinator",26.5,11).get_control_behavior().parameters.operation},"|"))`)
	t.Logf("phase 1 probe: %s", got)
	if !strings.HasPrefix(got, "electronic-circuit|rare|2|copper-cable|4|iron-ore|left|5|") {
		t.Fatalf("phase 1 world: %s", got)
	}

	// Replanning the same manifest adopts every unmanaged-but-matching entity.
	p1b := plan(ffLiveManifest("wooden-chest", "copper-cable", `["speed-module", "speed-module@rare"]`, "east", true, true))
	adopt := 0
	for _, raw := range jsonArray(t, p1b, "diffs") {
		d := raw.(map[string]any)
		if d["action"] == "ADOPT" {
			adopt++
			h.mutation(t, fmt.Sprintf(`mutation { factorioformImport(input:{name:"plan-live",surface:%q,address:%q,target:{entity:%q,unitNumber:%q}}) { tick } }`,
				integrationSurface, d["address"], d["entity"], d["unitNumber"]))
		}
	}
	if adopt != 13 {
		t.Fatalf("adopted %d rows: %v", adopt, p1b["summary"])
	}

	// Phase 2: update, replace, delete.
	p2 := plan(ffLiveManifest("iron-chest", "iron-plate", `["speed-module@rare", "speed-module@rare"]`, "west", false, true))
	t.Logf("phase 2 summary: %v", p2["summary"])
	kinds := map[string]bool{}
	for _, raw := range jsonArray(t, p2, "steps") {
		kinds[jsonString(t, raw.(map[string]any), "kind")] = true
	}
	for _, k := range []string{"delete", "replace-remove", "create", "config"} {
		if !kinds[k] {
			t.Fatalf("phase 2 plan lacks %s steps: %v", k, kinds)
		}
	}
	execute(p2)
	got = probe(`local a=at("assembling-machine-2",10.5,10.5)
local i=at("fast-inserter",8.5,10.5)
local sp=at("splitter",20.5,5)
local mods={} for name,count in pairs(a.get_module_inventory().get_contents()) do mods[#mods+1]=type(count)=="table" and (count.name..":"..count.quality..":"..count.count) or (name..":"..count) end
table.sort(mods)
rcon.print(table.concat({table.concat(mods,","),i.get_filter(1).name,i.direction,tostring(sp.splitter_filter),sp.splitter_output_priority,tostring(at("wooden-chest",18.5,10.5)),tostring(at("iron-chest",18.5,10.5)~=nil),tostring(at("arithmetic-combinator",26.5,11))},"|"))`)
	t.Logf("phase 2 probe: %s", got)
	if !strings.Contains(got, "|iron-plate|12|nil|left|nil|true|nil") || !strings.Contains(got, "speed-module:rare:2") {
		t.Fatalf("phase 2 world: %s", got)
	}

	// Phase 3: the replaced chest has a new unit and calc.m left the manifest;
	// record both, then drop the wire from the manifest to run disconnectWire.
	unit := strings.TrimSpace(probe(`rcon.print(at("iron-chest",18.5,10.5).unit_number)`))
	h.mutation(t, fmt.Sprintf(`mutation { factorioformImport(input:{name:"plan-live",surface:%q,address:"chest.c",overwrite:true,target:{entity:"iron-chest",unitNumber:%q}}) { tick } }`, integrationSurface, unit))
	h.mutation(t, `mutation { factorioformForget(input:{name:"plan-live",addresses:["calc.m"]}) { remaining } }`)
	p3 := plan(ffLiveManifest("iron-chest", "iron-plate", `["speed-module@rare", "speed-module@rare"]`, "west", false, false))
	if jsonString(t, p3, "status") != "CHANGES" || len(jsonArray(t, p3, "steps")) != 1 || jsonString(t, jsonArray(t, p3, "steps")[0].(map[string]any), "kind") != "disconnect-wire" {
		t.Fatalf("phase 3 plan: %v", p3)
	}
	execute(p3)
	if got := probe(`local i=at("fast-inserter",8.5,10.5) local n=0 for _,cn in pairs(i.get_wire_connectors(true)) do n=n+cn.connection_count end rcon.print(n)`); strings.TrimSpace(got) != "0" {
		t.Fatalf("wire still connected: %s", got)
	}
	if p4 := plan(ffLiveManifest("iron-chest", "iron-plate", `["speed-module@rare", "speed-module@rare"]`, "west", false, false)); jsonString(t, p4, "status") != "CONVERGED" || len(jsonArray(t, p4, "steps")) != 0 {
		t.Fatalf("phase 4 should converge: %v", p4["summary"])
	}

	// Phase 5: an omitted direction is unmanaged, and a container bar the
	// manifest manages converges against the live inventory it names. Both are
	// the P0 regressions: a plain int direction replaced every non-north
	// entity, and container.inventory was diffed as a field no live read
	// reported, so the plan never settled.
	omitted := strings.Replace(ffLiveManifest("iron-chest", "iron-plate", `["speed-module@rare", "speed-module@rare"]`, "west", false, false),
		"  direction = direction.west\n", "", 1)
	if p5 := plan(omitted); jsonString(t, p5, "status") != "CONVERGED" || len(jsonArray(t, p5, "steps")) != 0 {
		t.Fatalf("omitted direction must not churn the plan: %v %v", p5["status"], p5["summary"])
	}
	// The inserter still faces west in the world, proving the omission was
	// treated as unmanaged rather than as north.
	if got := strings.TrimSpace(probe(`rcon.print(at("fast-inserter",8.5,10.5).direction)`)); got != "12" {
		t.Fatalf("inserter direction = %s, want 12 (west)", got)
	}
	// The container bar that the manifest manages is the one it named, and it
	// matches the world, so the chest row stays NOOP.
	for _, raw := range jsonArray(t, plan(ffLiveManifest("iron-chest", "iron-plate", `["speed-module@rare", "speed-module@rare"]`, "west", false, false)), "diffs") {
		d := raw.(map[string]any)
		if d["address"] == "chest.c" && d["action"] != "NOOP" {
			t.Fatalf("container row must be NOOP: %v", d)
		}
	}
}
