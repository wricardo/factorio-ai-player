package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator"
)

const ffPlanProtos = `{"entities":{
"assembling-machine-2":{"type":"assembling-machine","tile_width":3,"tile_height":3,"supports_direction":false,"place_item":"assembling-machine-2","module_inventory_size":2,"crafting_categories":{"crafting":true}},
"fast-inserter":{"type":"inserter","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"fast-inserter","filter_count":5,"crafting_categories":{}},
"vector-inserter":{"type":"inserter","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"vector-inserter","filter_count":5,"allow_custom_vectors":true,"crafting_categories":{}},
"splitter":{"type":"splitter","tile_width":2,"tile_height":1,"supports_direction":true,"place_item":"splitter","crafting_categories":{}},
"transport-belt":{"type":"transport-belt","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"transport-belt","crafting_categories":{}},
"underground-belt":{"type":"underground-belt","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"underground-belt","crafting_categories":{}},
"wooden-chest":{"type":"container","tile_width":1,"tile_height":1,"place_item":"wooden-chest","crafting_categories":{}},
"iron-chest":{"type":"container","tile_width":1,"tile_height":1,"place_item":"iron-chest","crafting_categories":{}},
"logistic-chest-requester":{"type":"logistic-container","tile_width":1,"tile_height":1,"place_item":"logistic-chest-requester","crafting_categories":{}},
"constant-combinator":{"type":"constant-combinator","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"constant-combinator","crafting_categories":{}},
"arithmetic-combinator":{"type":"arithmetic-combinator","tile_width":1,"tile_height":2,"supports_direction":true,"place_item":"arithmetic-combinator","crafting_categories":{}}},
"recipes":{"electronic-circuit":true},
"items":{"speed-module":true,"copper-cable":true,"iron-ore":true,"iron-plate":true},
"qualities":{"normal":true,"rare":true}}`

const ffPlanFreshManifest = `
factorioform {
  version = 1
  name    = "plan-test"
}
resource "assembler" "a" {
  entity   = "assembling-machine-2"
  position = { x = 10.5, y = 10.5 }
  quality  = "rare"
  recipe   = "electronic-circuit"
  modules  = ["speed-module", "speed-module@rare"]
}
resource "inserter" "feed" {
  entity    = "fast-inserter"
  position  = { x = 8.5, y = 10.5 }
  direction = direction.east
  inserter {
    filters             = ["copper-cable"]
    filter_mode         = "whitelist"
    stack_size_override = 2
  }
  circuit { set_filters = true }
}
resource "splitter" "sort" {
  entity    = "splitter"
  position  = { x = 20.5, y = 5 }
  direction = direction.east
  splitter {
    filter          = "iron-ore"
    output_priority = "right"
  }
}
resource "chest" "c" {
  entity   = "wooden-chest"
  position = { x = 30.5, y = 10.5 }
  container { allowed_slots = 4 }
}
resource "logi" "req" {
  entity   = "logistic-chest-requester"
  position = { x = 34.5, y = 10.5 }
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
  position = { x = 36.5, y = 10.5 }
  constant_combinator {
    section {
      slot {
        index  = 1
        signal = { name = "iron-plate" }
        count  = 5
      }
    }
  }
}
resource "calc" "m" {
  entity   = "arithmetic-combinator"
  position = { x = 38.5, y = 11 }
  combinator {
    arithmetic {
      first_signal    = { name = "iron-plate" }
      operation       = "*"
      second_constant = 2
      output_signal   = { name = "iron-plate" }
    }
  }
}
resource "tunnel" "in" {
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
}
wire {
  color          = "red"
  from           = resource.inserter.feed
  to             = resource.const.k
  from_connector = "circuit_red"
  to_connector   = "circuit_red"
}
`

const ffPlanChangedManifest = `
factorioform {
  version = 1
  name    = "plan-test"
}
resource "assembler" "a" {
  entity   = "assembling-machine-2"
  position = { x = 10.5, y = 10.5 }
  quality  = "rare"
  recipe   = "electronic-circuit"
  modules  = ["speed-module", "speed-module"]
}
resource "inserter" "feed" {
  entity    = "fast-inserter"
  position  = { x = 8.5, y = 10.5 }
  direction = direction.east
  inserter { filters = ["copper-cable"] }
}
resource "splitter" "sort" {
  entity    = "splitter"
  position  = { x = 20.5, y = 5 }
  direction = direction.east
  splitter {
    filter          = ""
    output_priority = "left"
  }
}
resource "chest" "c" {
  entity   = "wooden-chest"
  position = { x = 30.5, y = 10.5 }
  container { allowed_slots = 4 }
}
wire {
  color          = "green"
  from           = resource.assembler.a
  to             = resource.inserter.feed
  from_connector = "circuit_green"
  to_connector   = "circuit_green"
}
`

// ffPlanFixture builds a plan from a manifest and a hand-made world without
// touching an engine.
func ffPlanFixture(t *testing.T, src string, state []ffStateEntry, live []*ffLive, actor *ActorInput) *FactorioformPlan {
	t.Helper()
	exp, diags := EvalFactorioform(map[string][]byte{"plan.ftf": []byte(src)}, ffEvalOptions{})
	if diags.HasErrors() {
		t.Fatalf("eval: %v", diags)
	}
	var protos ffPrototypes
	if err := json.Unmarshal([]byte(ffPlanProtos), &protos); err != nil {
		t.Fatal(err)
	}
	norm, diags := ffNormalize(exp.Resources, exp.Wires, &protos)
	if diags.HasErrors() {
		t.Fatalf("normalize: %v", diags)
	}
	res := &ffPipelineResult{Expanded: exp, Normalized: norm, StateEntries: state, Live: &ffLiveSnapshot{Tick: 1234, Entities: live}}
	res.Diff = ffDiff(ffDiffInput{Desired: norm, State: state, Live: live})
	plan, err := ffBuildPlan(res, actor)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// ffValidateSteps parses every step document against the generated schema and
// coerces its variables, so each step is runnable as written.
func ffValidateSteps(t *testing.T, plan *FactorioformPlan, withActor bool) {
	t.Helper()
	schema := newGraphQLSchema("127.0.0.1:0", "x").Schema()
	for _, s := range plan.Steps {
		doc, errs := gqlparser.LoadQuery(schema, s.Document)
		if len(errs) > 0 {
			t.Fatalf("step %d (%s) document invalid: %v\n%s", s.Order, s.Operation, errs, s.Document)
		}
		if len(doc.Operations) != 1 || doc.Operations[0].Operation != ast.Mutation {
			t.Fatalf("step %d is not one mutation", s.Order)
		}
		vars := map[string]any{}
		raw, _ := json.Marshal(s.Variables)
		if err := json.Unmarshal(raw, &vars); err != nil {
			t.Fatal(err)
		}
		input := vars["input"].(map[string]any)
		if s.RequiresActor {
			input["actor"] = map[string]any{"player": "tester"}
		} else if _, ok := input["actor"]; !ok {
			t.Fatalf("step %d has neither actor nor requiresActor", s.Order)
		}
		if s.RequiresActor == withActor {
			t.Fatalf("step %d requiresActor=%v but plan actor given=%v", s.Order, s.RequiresActor, withActor)
		}
		if _, err := validator.VariableValues(schema, doc.Operations[0], vars); err != nil {
			t.Fatalf("step %d (%s) variables invalid: %v\n%s", s.Order, s.Operation, err, raw)
		}
	}
}

func ffStepKinds(plan *FactorioformPlan) []string {
	var out []string
	for _, s := range plan.Steps {
		out = append(out, s.Kind+":"+s.Operation)
	}
	return out
}

func ffStepInput(s *FactorioformPlanStep) map[string]any {
	return s.Variables.(map[string]any)["input"].(map[string]any)
}

func TestFFPlanFreshManifestSteps(t *testing.T) {
	plan := ffPlanFixture(t, ffPlanFreshManifest, nil, nil, nil)
	if plan.Status != ffStatusChanges || plan.FormatVersion != 1 || plan.Manifest != "plan-test" || plan.Tick != 1234 {
		t.Fatalf("plan header %+v", plan)
	}
	if plan.Summary.Create != 14 || plan.Summary.Noop != 0 {
		t.Fatalf("summary %+v", plan.Summary)
	}
	ffValidateSteps(t, plan, false)

	want := []string{
		"create:placeMany", "create:placeMany", "create:placeMany", "create:placeMany", "create:placeMany", "create:placeMany", "create:placeMany",
		"create-underground:place",
		"create-belts:placeBelts", "create-belts:placeBelts", "create-belts:placeBelts",
		// Config follows address order: assembler, calc, chest, const, inserter, logi, splitter.
		"config:setRecipe", "config:transfer", "config:transfer",
		"config:setCombinatorParameters", "config:setContainerBar", "config:setConstantCombinator",
		"config:setInserterSettings", "config:setCircuitBehavior", "config:setLogisticRequests",
		"config:setSplitterSettings",
		"connect-wire:connectWire",
	}
	got := ffStepKinds(plan)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("step order\n got %v\nwant %v", got, want)
	}
	for i, s := range plan.Steps {
		if s.Order != i+1 {
			t.Fatalf("step %d has order %d", i+1, s.Order)
		}
	}
	// Belt runs: east pair, north pair and a single east tile.
	var belts []map[string]any
	for _, s := range plan.Steps {
		if s.Operation == "placeBelts" {
			belts = append(belts, ffStepInput(s))
		}
	}
	if _, has := belts[0]["direction"]; has {
		t.Fatalf("multi-tile run must not carry direction: %v", belts[0])
	}
	if belts[2]["direction"] != 4 || belts[2]["routing"] != "DIRECT" {
		t.Fatalf("single tile run %v", belts[2])
	}
	// Created entities are addressed by name+position.
	for _, s := range plan.Steps {
		if s.Operation == "setRecipe" {
			target := ffStepInput(s)["target"].(map[string]any)
			if target["entity"] != "assembling-machine-2" || target["unitNumber"] != nil {
				t.Fatalf("create target %v", target)
			}
		}
	}
	mat := map[string]int{}
	for _, m := range plan.Materials {
		mat[m.Name+"@"+m.Quality] += m.Count
	}
	for k, n := range map[string]int{
		"assembling-machine-2@rare": 1, "speed-module@normal": 1, "speed-module@rare": 1, "fast-inserter@normal": 1,
		"splitter@normal": 1, "wooden-chest@normal": 1, "logistic-chest-requester@normal": 1, "constant-combinator@normal": 1,
		"arithmetic-combinator@normal": 1, "underground-belt@normal": 1, "transport-belt@normal": 5,
	} {
		if mat[k] != n {
			t.Errorf("material %s = %d, want %d (all %v)", k, mat[k], n, mat)
		}
	}
	if len(mat) != 11 {
		t.Errorf("materials %v", mat)
	}
}

func TestFFPlanActorInjection(t *testing.T) {
	player := "tester"
	plan := ffPlanFixture(t, ffPlanFreshManifest, nil, nil, &ActorInput{Player: &player})
	ffValidateSteps(t, plan, true)
	for _, s := range plan.Steps {
		if a := ffStepInput(s)["actor"].(map[string]any); a["player"] != "tester" {
			t.Fatalf("step %d actor %v", s.Order, a)
		}
	}
}

func TestFFPlanUpdateReplaceDeleteSteps(t *testing.T) {
	state := []ffStateEntry{
		{Address: "assembler.a", Unit: 1, Entity: "assembling-machine-2", X: 10.5, Y: 10.5, Quality: "rare"},
		{Address: "inserter.feed", Unit: 2, Entity: "fast-inserter", X: 8.5, Y: 10.5, Direction: 0},
		{Address: "splitter.sort", Unit: 3, Entity: "splitter", X: 20.5, Y: 5, Direction: 4},
		{Address: "chest.c", Unit: 4, Entity: "iron-chest", X: 30.5, Y: 10.5},
		{Address: "chest.old", Unit: 5, Entity: "wooden-chest", X: 50.5, Y: 10.5},
	}
	live := []*ffLive{
		{UnitNumber: 1, Entity: "assembling-machine-2", Type: "assembling-machine", Position: ffPoint{10.5, 10.5}, Quality: "rare",
			Config: map[string]any{"recipe": "electronic-circuit", "modules": []string{"speed-module:normal:1", "speed-module:rare:1"}},
			Wires:  []ffWire{{Color: "red", Connector: "circuit_red", PeerUnit: 2, PeerConnector: "circuit_red"}}},
		{UnitNumber: 2, Entity: "fast-inserter", Type: "inserter", Position: ffPoint{8.5, 10.5}, Direction: 0, Quality: "normal",
			Config: map[string]any{"inserter.filters": []string{""}},
			Wires:  []ffWire{{Color: "red", Connector: "circuit_red", PeerUnit: 1, PeerConnector: "circuit_red"}}},
		{UnitNumber: 3, Entity: "splitter", Type: "splitter", Position: ffPoint{20.5, 5}, Direction: 4, Quality: "normal",
			Config: map[string]any{"splitter.filter": "iron-ore", "splitter.output_priority": "right"}},
		{UnitNumber: 4, Entity: "iron-chest", Type: "container", Position: ffPoint{30.5, 10.5}, Quality: "normal", Config: map[string]any{}},
		{UnitNumber: 5, Entity: "wooden-chest", Type: "container", Position: ffPoint{50.5, 10.5}, Quality: "normal", Config: map[string]any{}},
	}
	plan := ffPlanFixture(t, ffPlanChangedManifest, state, live, nil)
	ffValidateSteps(t, plan, false)
	want := []string{
		"disconnect-wire:disconnectWire",
		"delete:removeMany",
		"replace-remove:removeMany",
		"create:placeMany",
		"config:transfer", "config:transfer",
		"config:setContainerBar",
		"config:setDirection", "config:setInserterSettings",
		"config:setSplitterSettings", "config:setSplitterSettings",
		"connect-wire:connectWire",
	}
	if got := ffStepKinds(plan); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("step order\n got %v\nwant %v\nsummary %+v", got, want, plan.Summary)
	}
	in := func(i int) map[string]any { return ffStepInput(plan.Steps[i]) }
	if in(0)["from"].(map[string]any)["target"].(map[string]any)["unitNumber"] != "1" {
		t.Fatalf("disconnect target %v", in(0))
	}
	if t0 := in(1)["targets"].([]any)[0].(map[string]any); t0["unitNumber"] != "5" || in(1)["mode"] != "MINE" {
		t.Fatalf("delete step %v", in(1))
	}
	if in(2)["targets"].([]any)[0].(map[string]any)["unitNumber"] != "4" {
		t.Fatalf("replace step %v", in(2))
	}
	if in(7)["target"].(map[string]any)["unitNumber"] != "2" {
		t.Fatalf("update target %v", in(4))
	}
	f := in(8)["filters"].([]any)[0].(map[string]any)
	if f["index"] != 1 || f["name"] != "copper-cable" {
		t.Fatalf("filters %v", in(8)["filters"])
	}
	// Module extras leave first, missing modules go in after.
	if in(4)["direction"] != "TO_ACTOR" || in(4)["quality"] != "rare" || in(5)["direction"] != "TO_ENTITY" || in(5)["quality"] != "normal" {
		t.Fatalf("module transfers %v %v", in(4), in(5))
	}
	// Splitter filter cleared first, then the left priority is re-applied.
	if in(9)["clear"] != true || in(10)["outputPriority"] != "left" {
		t.Fatalf("splitter steps %v %v", in(9), in(10))
	}
	mat := map[string]int{}
	for _, m := range plan.Materials {
		mat[m.Name+"@"+m.Quality] += m.Count
	}
	if mat["wooden-chest@normal"] != 1 || mat["speed-module@normal"] != 1 || len(mat) != 2 {
		t.Fatalf("materials %v", mat)
	}
}

func TestFFPlanConvergedHasNoSteps(t *testing.T) {
	src := "factorioform {\n version = 1\n name = \"plan-test\"\n}\n" + `
resource "chest" "c" {
  entity   = "wooden-chest"
  position = { x = 30.5, y = 10.5 }
}`
	state := []ffStateEntry{{Address: "chest.c", Unit: 4, Entity: "wooden-chest", X: 30.5, Y: 10.5}}
	live := []*ffLive{{UnitNumber: 4, Entity: "wooden-chest", Type: "container", Position: ffPoint{30.5, 10.5}, Quality: "normal"}}
	plan := ffPlanFixture(t, src, state, live, nil)
	if plan.Status != ffStatusConverged || len(plan.Steps) != 0 || plan.Summary.Noop != 1 {
		t.Fatalf("plan %+v steps %d", plan.Summary, len(plan.Steps))
	}
	first := plan.PlanID
	if again := ffPlanFixture(t, src, state, live, nil); again.PlanID != first {
		t.Fatal("planId must be stable for an unchanged world")
	}
	live[0].Position = ffPoint{30.5, 11.5}
	if changed := ffPlanFixture(t, src, state, live, nil); changed.PlanID == first {
		t.Fatal("planId must change when the world changes")
	}
}

func TestFFPlanDiagnosticsCarryLocation(t *testing.T) {
	_, diags := EvalFactorioform(map[string][]byte{"bad.ftf": []byte("factorioform {\n version = 2\n name = \"x\"\n}\n")}, ffEvalOptions{})
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics")
	}
	if err := ffDiagError(diags); !strings.Contains(err.Error(), "bad.ftf:2:") {
		t.Fatalf("error lacks file:line:col: %v", err)
	}
}

func TestFactorioformCLIUsageAndErrors(t *testing.T) {
	var out strings.Builder
	if err := runWithCredentials([]string{"factorioform", "help"}, &out, ""); err != nil || !strings.Contains(out.String(), "--actor-player") {
		t.Fatalf("help: %v %q", err, out.String())
	}
	for args, want := range map[string]string{
		"plan":                         "FACTORIO_RCON_PASSWORD",
		"bogus":                        "unknown factorioform command",
		"plan --file /nonexistent.ftf": "FACTORIO_RCON_PASSWORD",
	} {
		out.Reset()
		err := runWithCredentials(append([]string{"--addr", "127.0.0.1:0", "factorioform"}, strings.Fields(args)...), &out, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", args, err, want)
		}
	}
	out.Reset()
	err := runWithCredentials([]string{"--addr", "127.0.0.1:0", "factorioform", "plan"}, &out, "pw")
	if err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("plan without files: %v", err)
	}
	err = runWithCredentials([]string{"--addr", "127.0.0.1:0", "factorioform", "plan", "--file", "x.ftf", "--var", "novalue"}, &out, "pw")
	if err == nil {
		t.Fatal("expected error for unreadable file or malformed --var")
	}
}
func TestFactorioformCLIPositionalDir(t *testing.T) {
	dir := t.TempDir()
	one := "factorioform {\n  version = 1\n  name    = \"plan-test\"\n}\nvariable \"origin\" {\n  type    = object({ x = number, y = number })\n  default = { x = 10.5, y = 10.5 }\n}\nresource \"chest\" \"c\" {\n  entity   = \"wooden-chest\"\n  position = var.origin\n}\n"
	two := "resource \"chest\" \"d\" {\n  entity   = \"wooden-chest\"\n  position = { x = var.origin.x + 2, y = var.origin.y }\n}\n"
	for name, content := range map[string]string{"one.ftf": one, "two.ftf": two} {
		if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Without a live server the password error must fire after DIR parsing.
	var out strings.Builder
	err := runWithCredentials([]string{"--addr", "127.0.0.1:0", "factorioform", "plan", dir}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "FACTORIO_RCON_PASSWORD") {
		t.Fatalf("positional DIR: %v", err)
	}
	err = runWithCredentials([]string{"--addr", "127.0.0.1:0", "factorioform", "plan", dir, dir}, &out, "pw")
	if err == nil || !strings.Contains(err.Error(), "at most one DIR") {
		t.Fatalf("two DIRs: %v", err)
	}
	err = runWithCredentials([]string{"--addr", "127.0.0.1:0", "factorioform", "plan", "--dir", dir, dir}, &out, "pw")
	if err == nil || !strings.Contains(err.Error(), "DIR or --dir") {
		t.Fatalf("positional + --dir: %v", err)
	}
	// Both files merge into one manifest: EvalFactorioform sees one plan-test.
	merged, diags := EvalFactorioform(map[string][]byte{"one.ftf": []byte(one), "two.ftf": []byte(two)}, ffEvalOptions{})
	if diags.HasErrors() {
		t.Fatalf("merge diags: %v", ffDiagLines(diags))
	}
	if merged == nil || len(merged.Resources) != 2 {
		t.Fatalf("expected 2 resources from merged dir, got %+v", merged)
	}
}

func TestFFPlanJSONFillsNullableFields(t *testing.T) {
	plan := ffPlanFixture(t, ffPlanFreshManifest, nil, nil, nil)
	raw, err := ffPlanJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Diffs []map[string]any `json:"diffs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, d := range doc.Diffs {
		for _, k := range []string{"entity", "quality", "unitNumber", "reason", "wire"} {
			if _, ok := d[k]; !ok {
				t.Fatalf("diff %v lacks %s", d["address"], k)
			}
		}
	}
}
