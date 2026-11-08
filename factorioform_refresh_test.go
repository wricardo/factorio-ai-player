package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const ffRefreshManifest = `
factorioform {
  version = 1
  name    = "plan-test"
}
resource "chest" "adopt" {
  entity   = "wooden-chest"
  position = { x = 30.5, y = 10.5 }
}
resource "chest" "same" {
  entity   = "wooden-chest"
  position = { x = 32.5, y = 10.5 }
}
resource "chest" "drift" {
  entity   = "wooden-chest"
  position = { x = 34.5, y = 10.5 }
}
resource "chest" "fresh" {
  entity   = "wooden-chest"
  position = { x = 40.5, y = 10.5 }
}
`

func ffRefreshFixture(t *testing.T) *ffPipelineResult {
	t.Helper()
	exp, diags := EvalFactorioform(map[string][]byte{"r.ftf": []byte(ffRefreshManifest)}, ffEvalOptions{})
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
	chest := func(u int64, x float64) *ffLive {
		return &ffLive{UnitNumber: u, Entity: "wooden-chest", Type: "container", Position: ffPoint{x, 10.5}, Quality: "normal", Config: map[string]any{},
			Footprint: ffBox{X1: x - 0.5, Y1: 10, X2: x + 0.5, Y2: 11}}
	}
	live := []*ffLive{chest(7, 30.5), chest(8, 32.5)}
	state := []ffStateEntry{
		{Address: "chest.same", Unit: 8, Entity: "wooden-chest", X: 32.5, Y: 10.5, Quality: "normal"},
		{Address: "chest.drift", Unit: 9, Entity: "wooden-chest", X: 34.5, Y: 10.5, Quality: "normal"},
		{Address: "chest.old", Unit: 10, Entity: "wooden-chest", X: 50.5, Y: 10.5, Quality: "normal"},
	}
	return &ffPipelineResult{
		Expanded: exp, Normalized: norm, State: &FactorioformManifestState{Name: "plan-test"}, StateEntries: state,
		Live: &ffLiveSnapshot{Tick: 99, Entities: live},
		Diff: ffDiff(ffDiffInput{Desired: norm, State: state, Live: live}),
	}
}

func TestFFRefreshClassification(t *testing.T) {
	p := ffRefreshCompute(ffRefreshFixture(t))
	if len(p.Result.Recorded) != 1 || p.Result.Recorded[0].Address != "chest.adopt" || p.Result.Recorded[0].UnitNumber != "7" {
		t.Fatalf("recorded = %+v", p.Result.Recorded)
	}
	if len(p.Result.Pruned) != 1 || p.Result.Pruned[0] != "chest.old" {
		t.Fatalf("pruned = %v", p.Result.Pruned)
	}
	un := map[string]string{}
	for _, u := range p.Result.Unmatched {
		un[u.Address] = u.Reason
	}
	if len(un) != 2 || un["chest.drift"] == "" || un["chest.fresh"] == "" {
		t.Fatalf("unmatched = %+v", p.Result.Unmatched)
	}
	if len(p.Remove) != 2 || p.Remove[0] != "chest.drift" || p.Remove[1] != "chest.old" || len(p.Upsert) != 1 {
		t.Fatalf("remove=%v upsert=%+v", p.Remove, p.Upsert)
	}
	if p.Result.Tick != 99 || p.Result.Status != ffStatusChanges {
		t.Fatalf("tick/status = %v %s", p.Result.Tick, p.Result.Status)
	}
}

func TestFFRefreshDryRunWritesNothingAndApplyWritesOnce(t *testing.T) {
	fake := newFakeRCON(t)
	got, err := ffRefreshApply(fake.resolver, ffRefreshFixture(t), true, nil)
	if err != nil || !got.DryRun || len(got.Recorded) != 1 {
		t.Fatalf("dry run = %+v err=%v", got, err)
	}
	if cmds := fake.commands(); len(cmds) != 0 {
		t.Fatalf("dry run emitted commands: %q", cmds)
	}
	fake = newFakeRCON(t, `{"tick":99}`, `{"tick":99}`)
	if _, err := ffRefreshApply(fake.resolver, ffRefreshFixture(t), false, nil); err != nil {
		t.Fatal(err)
	}
	cmds := fake.commands()
	puts := 0
	for _, c := range cmds {
		if strings.Contains(c, "ff_put(") {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("expected exactly one ff_put, got %d in %d commands", puts, len(cmds))
	}
}
