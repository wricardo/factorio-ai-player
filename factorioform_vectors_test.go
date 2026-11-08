package main

import (
	"strings"
	"testing"
)

const ffVectorManifest = `
factorioform {
  version = 1
  name    = "vec-test"
}
resource "inserter" "arm" {
  entity    = "vector-inserter"
  position  = { x = 8.5, y = 10.5 }
  direction = direction.east
  inserter {
    pickup_offset = { x = 2, y = 0 }
    drop_offset   = { x = -1, y = 0.5 }
  }
}
`

func ffVectorLive(dir int, pick, drop map[string]any) []*ffLive {
	cfg := map[string]any{}
	if pick != nil {
		cfg["inserter.pickup_offset"] = pick
	}
	if drop != nil {
		cfg["inserter.drop_offset"] = drop
	}
	return []*ffLive{{UnitNumber: 2, Entity: "vector-inserter", Type: "inserter", Position: ffPoint{8.5, 10.5}, Direction: dir, Quality: "normal", Config: cfg}}
}

var ffVectorState = []ffStateEntry{{Address: "inserter.arm", Unit: 2, Entity: "vector-inserter", X: 8.5, Y: 10.5, Direction: 4}}

func TestFFInserterVectorManifestAccepted(t *testing.T) {
	plan := ffPlanFixture(t, ffVectorManifest, nil, nil, nil)
	ffValidateSteps(t, plan, false)
	var step *FactorioformPlanStep
	for _, s := range plan.Steps {
		if s.Operation == "setInserterSettings" {
			step = s
		}
	}
	if step == nil {
		t.Fatalf("no setInserterSettings step in %v", ffStepKinds(plan))
	}
	in := ffStepInput(step)
	if p := in["pickupVector"].(map[string]any); p["x"] != 2.0 || p["y"] != 0.0 {
		t.Fatalf("pickupVector %v", in["pickupVector"])
	}
	if d := in["dropVector"].(map[string]any); d["x"] != -1.0 || d["y"] != 0.5 {
		t.Fatalf("dropVector %v", in["dropVector"])
	}
}

func TestFFInserterVectorUpdateAndNoop(t *testing.T) {
	plan := ffPlanFixture(t, ffVectorManifest, ffVectorState, ffVectorLive(4, map[string]any{"x": 1.0, "y": 0.0}, map[string]any{"x": -1.0, "y": 0.5}), nil)
	ffValidateSteps(t, plan, false)
	if plan.Summary.Update != 1 || len(plan.Diffs) != 1 || len(plan.Diffs[0].Changes) != 1 || plan.Diffs[0].Changes[0].Path != "inserter.pickup_offset" {
		t.Fatalf("diff %+v summary %+v", plan.Diffs, plan.Summary)
	}
	if got := strings.Join(ffStepKinds(plan), ","); got != "config:setInserterSettings" {
		t.Fatalf("steps %s", got)
	}
	in := ffStepInput(plan.Steps[0])
	if _, ok := in["dropVector"]; ok || in["pickupVector"].(map[string]any)["x"] != 2.0 {
		t.Fatalf("update input %v", in)
	}
	same := ffPlanFixture(t, ffVectorManifest, ffVectorState, ffVectorLive(4, map[string]any{"x": 2.0, "y": 0.0}, map[string]any{"x": -1.0, "y": 0.5}), nil)
	if same.Status != ffStatusConverged {
		t.Fatalf("status %s diffs %+v", same.Status, same.Diffs)
	}
}

// A rotation rotates the engine vectors, so the plan rewrites them after setDirection.
func TestFFInserterVectorRewrittenAfterRotation(t *testing.T) {
	state := []ffStateEntry{{Address: "inserter.arm", Unit: 2, Entity: "vector-inserter", X: 8.5, Y: 10.5, Direction: 8}}
	plan := ffPlanFixture(t, ffVectorManifest, state, ffVectorLive(8, map[string]any{"x": 2.0, "y": 0.0}, map[string]any{"x": -1.0, "y": 0.5}), nil)
	ffValidateSteps(t, plan, false)
	if got := strings.Join(ffStepKinds(plan), ","); got != "config:setDirection,config:setInserterSettings" {
		t.Fatalf("steps %s", got)
	}
	in := ffStepInput(plan.Steps[1])
	if in["pickupVector"] == nil || in["dropVector"] == nil {
		t.Fatalf("vectors not rewritten: %v", in)
	}
}

func TestFFInserterVectorRejectedWithoutCustomVectors(t *testing.T) {
	src := strings.Replace(ffVectorManifest, "vector-inserter", "fast-inserter", 1)
	exp, diags := EvalFactorioform(map[string][]byte{"plan.ftf": []byte(src)}, ffEvalOptions{})
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	var protos ffPrototypes
	if err := jsonUnmarshalString(ffPlanProtos, &protos); err != nil {
		t.Fatal(err)
	}
	_, diags = ffNormalize(exp.Resources, exp.Wires, &protos)
	if !diags.HasErrors() || !strings.Contains(diags.Error(), "allow_custom_vectors=false") {
		t.Fatalf("diags %v", diags)
	}
}
