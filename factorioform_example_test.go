package main

import (
	"os"
	"strings"
	"testing"
)

const ffExampleFile = "testdata/factorioform/example.ftf"

// TestFFExampleManifest plans testdata/factorioform/example.ftf against an empty
// world: every resource is a CREATE, every step document is valid against the
// schema, and overriding the origin variable moves the whole layout.
func TestFFExampleManifest(t *testing.T) {
	src, err := os.ReadFile(ffExampleFile)
	if err != nil {
		t.Fatal(err)
	}
	tester := "tester"
	plan := ffPlanFixture(t, string(src), nil, nil, &ActorInput{Player: &tester})
	if plan.Manifest != "example" || plan.Status != "CHANGES" {
		t.Fatalf("manifest %q status %q", plan.Manifest, plan.Status)
	}
	// 3 assemblers + inserter + splitter + 6 belt tiles + 2 undergrounds + combinator + wire.
	if plan.Summary.Create != 15 || plan.Summary.Conflict != 0 {
		t.Fatalf("summary %+v", plan.Summary)
	}
	addrs := map[string]bool{}
	for _, d := range plan.Diffs {
		addrs[d.Address] = true
	}
	for _, want := range []string{"assembler.circuit[0]", "assembler.circuit[2]", "inserter.feed", "splitter.sort", "belt.bus.path[0]", "belt.bus.path[5]", "tunnel_in.bus", "tunnel_out.bus", "const.signal"} {
		if !addrs[want] {
			t.Fatalf("no row %s in %v", want, addrs)
		}
	}
	ffValidateSteps(t, plan, true)

	// The variable default places the first assembler at origin; overriding it
	// moves the layout.
	moved := strings.Replace(string(src), "x = 10.5, y = 10.5", "x = -12.5, y = 10.5", 1)
	plan2 := ffPlanFixture(t, moved, nil, nil, &ActorInput{Player: &tester})
	for _, d := range plan2.Diffs {
		if d.Address == "assembler.circuit[0]" && d.Position.X != -12.5 {
			t.Fatalf("origin override ignored: %+v", d.Position)
		}
	}
}
