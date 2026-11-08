//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestIntegrationFactorioformModules proves the module loop on a disposable
// engine: a module instantiated twice plans, executes, refreshes and
// converges under module-prefixed addresses, a wire joins resources exposed
// by the two instances, and a resource moved to a new address is adopted after
// factorioformForget instead of being rebuilt.
func TestIntegrationFactorioformModules(t *testing.T) {
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })
	h.createTestSurface(t)
	character := h.spawnCharacter(t)
	t.Cleanup(func() { h.destroyCharacter(t, character.id) })
	h.grantItems(t, character.id, map[string]int{"constant-combinator": 2, "wooden-chest": 2})
	h.lua(t, `storage.factorio_ai_player_factorioform=nil
rcon.print("ok")`)

	root := `
factorioform {
  version = 1
  name    = "modules-live"
  surface = "` + integrationSurface + `"
}
module "a" {
  source = "./sig"
  origin = { x = 4.5, y = 4.5 }
}
module "b" {
  source = "./sig"
  origin = { x = 12.5, y = 4.5 }
}
wire {
  color          = "red"
  from           = module.a.signal
  to             = module.b.signal
  from_connector = "circuit_red"
  to_connector   = "circuit_red"
}
`
	module := `
variable "origin" {
  type = object({ x = number, y = number })
}
resource "chest" "box" {
  entity   = "wooden-chest"
  position = var.origin
}
resource "const" "signal" {
  entity   = "constant-combinator"
  position = offset(var.origin, 2, 0)
}
output "signal" {
  value = resource.const.signal
}
`
	files := []any{
		map[string]any{"name": "main.ftf", "content": root},
		map[string]any{"name": "sig/main.ftf", "content": module},
	}
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
	plan := func() map[string]any {
		t.Helper()
		vars, _ := json.Marshal(map[string]any{"input": map[string]any{"files": files, "actor": map[string]any{"character": character.id}}})
		return jsonObject(t, run("query", "--format", "json", ffE2EPlanQuery, "--variables", string(vars)), "data", "factorioformPlan")
	}
	refresh := func() map[string]any {
		t.Helper()
		vars, _ := json.Marshal(map[string]any{"input": map[string]any{"files": files}})
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
	actions := func(p map[string]any) map[string]string {
		got := map[string]string{}
		for _, d := range jsonArray(t, p, "diffs") {
			m := d.(map[string]any)
			got[m["address"].(string)] = m["action"].(string)
		}
		return got
	}

	first := plan()
	got := actions(first)
	for _, addr := range []string{"module.a.chest.box", "module.a.const.signal", "module.b.chest.box", "module.b.const.signal"} {
		if got[addr] != "CREATE" {
			t.Fatalf("%s: %q, want CREATE; plan rows %v", addr, got[addr], got)
		}
	}
	wireRows := 0
	for addr, action := range got {
		if strings.HasPrefix(addr, "wire:module.a.const.signal->module.b.const.signal") && action == "CREATE" {
			wireRows++
		}
	}
	if wireRows != 1 {
		t.Fatalf("want one CREATE wire row between the module instances, rows %v", got)
	}
	execute(first)
	refresh()
	if p := plan(); p["status"] != "CONVERGED" || len(jsonArray(t, p, "steps")) != 0 {
		t.Fatalf("after execute+refresh: status %v rows %v", p["status"], actions(p))
	}
	if n := h.entityCount(t, "constant-combinator"); n != 2 {
		t.Fatalf("constant-combinator count = %v, want 2", n)
	}

	// Rename the module instance: the state address is gone, so the plan would
	// rebuild; forgetting the old state makes the live entities ADOPT instead.
	renamed := strings.NewReplacer(`module "a"`, `module "left"`, "module.a.", "module.left.").Replace(root)
	files[0] = map[string]any{"name": "main.ftf", "content": renamed}
	if got := actions(plan()); got["module.left.chest.box"] != "CREATE" || got["module.a.chest.box"] != "DELETE" {
		t.Fatalf("renamed module without forget: %v", got)
	}
	forget, _ := json.Marshal(map[string]any{"input": map[string]any{"name": "modules-live", "addresses": []string{}}})
	run("mutation", "--format", "json", `mutation($input: FactorioformForgetInput!) { factorioformForget(input: $input) { remaining } }`, "--variables", string(forget))
	adopt := plan()
	got = actions(adopt)
	for _, addr := range []string{"module.left.chest.box", "module.left.const.signal", "module.b.chest.box", "module.b.const.signal"} {
		if got[addr] != "ADOPT" {
			t.Fatalf("%s: %q, want ADOPT after forget; rows %v", addr, got[addr], got)
		}
	}
	if len(jsonArray(t, adopt, "steps")) != 0 {
		t.Fatalf("ADOPT rows need no steps: %v", adopt["steps"])
	}
	refresh()
	if p := plan(); p["status"] != "CONVERGED" {
		t.Fatalf("after adopt+refresh: status %v rows %v", p["status"], actions(p))
	}
	if n := h.entityCount(t, "constant-combinator"); n != 2 {
		t.Fatalf("adoption rebuilt entities: constant-combinator count = %v, want 2", n)
	}
}
