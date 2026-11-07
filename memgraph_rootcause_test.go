package main

import (
	"context"
	"strings"
	"testing"
)

func rootCauseNode(id, kind string, unit int64, props map[string]any) map[string]any {
	p := map[string]any{"surface": "nauvis"}
	for k, v := range props {
		p[k] = v
	}
	if unit != 0 {
		p["unit"] = unit
	}
	p["id"] = id
	return map[string]any{"id": "e" + id, "labels": []any{"Entity", kind}, "properties": p}
}

// rootCauseFake answers the root lookup, supply paths and coverage queries.
func rootCauseFake(root map[string]any, paths [][]any, coverage [][]any, truncated bool) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		switch {
		case strings.Contains(in.Cypher, "RETURN r AS node LIMIT 1"):
			return &MemgraphQueryResult{Rows: [][]any{{root}}}, nil
		case in.Cypher == ingredientCoverageCypher:
			return &MemgraphQueryResult{Rows: coverage}, nil
		}
		rows := make([][]any, len(paths))
		for i, p := range paths {
			rows[i] = []any{int64(len(p)), p}
		}
		return &MemgraphQueryResult{Rows: rows, Truncated: truncated}, nil
	}
}

func rootCauseDeadInputFixture() (map[string]any, [][]any, [][]any) {
	m370 := rootCauseNode("nauvis:370", "Machine", 370, map[string]any{"name": "assembling-machine-1", "recipe": "inserter", "status": "item_ingredient_shortage"})
	arm := rootCauseNode("nauvis:390", "Inserter", 390, nil)
	belt := rootCauseNode("nauvis:391", "Belt", 391, map[string]any{"name": "transport-belt"})
	paths := [][]any{{m370, arm, belt}}
	cov := [][]any{{m370, "iron-plate", int64(1), []any{int64(390)}}}
	return m370, paths, cov
}

func TestRootCauseOriginBelt(t *testing.T) {
	root, paths, cov := rootCauseDeadInputFixture()
	got, err := rootCause(context.Background(), rootCauseFake(root, paths, cov, false), MemgraphRootCauseInput{UnitNumber: "370", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stalled || len(got.Causes) != 1 {
		t.Fatalf("stalled=%v causes=%d, want stalled with one cause", got.Stalled, len(got.Causes))
	}
	c := got.Causes[0]
	if c.Reason != MemgraphCauseReasonOriginBelt || *c.Node.UnitNumber != "391" {
		t.Fatalf("cause = %s %v", c.Reason, c.Node)
	}
	if strings.Join(c.Route, ",") != "nauvis:370,nauvis:390,nauvis:391" || c.RouteText != "370 <- arm 390 <- belt 391" {
		t.Fatalf("route = %v / %q", c.Route, c.RouteText)
	}
	if !strings.Contains(got.Summary, "origin belt") || !strings.Contains(got.Summary, "370 <- arm 390 <- belt 391") {
		t.Fatalf("summary = %q", got.Summary)
	}
}

func TestRootCauseShortageIsNeverACause(t *testing.T) {
	m370 := rootCauseNode("nauvis:370", "Machine", 370, map[string]any{"name": "assembling-machine-1", "recipe": "inserter", "status": "item_ingredient_shortage"})
	arm := rootCauseNode("nauvis:237", "Inserter", 237, nil)
	m143 := rootCauseNode("nauvis:143", "Machine", 143, map[string]any{"name": "assembling-machine-1", "recipe": "electronic-circuit", "status": "item_ingredient_shortage"})
	cov := [][]any{
		{m370, "electronic-circuit", int64(3), []any{int64(237)}},
		{m143, "copper-cable", int64(3), []any{}},
	}
	got, err := rootCause(context.Background(), rootCauseFake(m370, [][]any{{m370, arm, m143}}, cov, false), MemgraphRootCauseInput{UnitNumber: "370", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Causes) != 1 || got.Causes[0].Reason != MemgraphCauseReasonNoFeedingArm || got.Causes[0].Detail != "copper-cable has no feeding arm" {
		t.Fatalf("causes = %+v", got.Causes)
	}
	if got.Causes[0].RouteText != "370 <- arm 237 <- 143" {
		t.Fatalf("routeText = %q", got.Causes[0].RouteText)
	}
}

func TestRootCauseMachineTroubleOrderingAndDedup(t *testing.T) {
	m1 := rootCauseNode("nauvis:1", "Machine", 1, map[string]any{"name": "stone-furnace", "recipe": "iron-plate", "status": "working"})
	a2 := rootCauseNode("nauvis:2", "Inserter", 2, nil)
	m3 := rootCauseNode("nauvis:3", "Machine", 3, map[string]any{"name": "stone-furnace", "recipe": "iron-plate", "status": "no_fuel"})
	b5 := rootCauseNode("nauvis:5", "Belt", 5, nil)
	b6 := rootCauseNode("nauvis:6", "Belt", 6, nil)
	b7 := rootCauseNode("nauvis:7", "Belt", 7, nil)
	paths := [][]any{
		{m1, a2, m3},
		{m1, a2, m3}, // same route twice must not duplicate
		{m1, a2, b5, b6, b7},
	}
	cov := [][]any{{m1, "iron-ore", int64(1), []any{int64(2)}}}
	got, err := rootCause(context.Background(), rootCauseFake(m1, paths, cov, true), MemgraphRootCauseInput{UnitNumber: "1", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Causes) != 2 || !got.Truncated {
		t.Fatalf("causes = %d truncated = %v", len(got.Causes), got.Truncated)
	}
	if got.Causes[0].Reason != MemgraphCauseReasonOriginBelt || got.Causes[1].Reason != MemgraphCauseReasonMachineTrouble {
		t.Fatalf("order = %s, %s", got.Causes[0].Reason, got.Causes[1].Reason)
	}
	if got.Causes[0].RouteText != "1 <- arm 2 <- belts 5..7 (3)" || got.Causes[1].Detail != "status no_fuel" {
		t.Fatalf("got %q / %q", got.Causes[0].RouteText, got.Causes[1].Detail)
	}
	if !strings.Contains(got.Summary, "truncated") {
		t.Fatalf("summary = %q", got.Summary)
	}
}

func TestRootCauseHealthyMachineNotStalled(t *testing.T) {
	m := rootCauseNode("nauvis:9", "Machine", 9, map[string]any{"name": "assembling-machine-1", "recipe": "iron-gear-wheel", "status": "working"})
	cov := [][]any{{m, "iron-plate", int64(2), []any{int64(8)}}}
	got, err := rootCause(context.Background(), rootCauseFake(m, [][]any{{m}}, cov, false), MemgraphRootCauseInput{UnitNumber: "9", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.Stalled || len(got.Causes) != 0 || got.Causes == nil || !strings.Contains(got.Summary, "not stalled") {
		t.Fatalf("got %+v", got)
	}
}

func TestRootCauseStalledWithoutCause(t *testing.T) {
	m := rootCauseNode("nauvis:9", "Machine", 9, map[string]any{"name": "assembling-machine-1", "recipe": "iron-gear-wheel", "status": "full_output"})
	got, err := rootCause(context.Background(), rootCauseFake(m, [][]any{{m}}, nil, false), MemgraphRootCauseInput{UnitNumber: "9", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stalled || len(got.Causes) != 0 || !strings.Contains(got.Summary, "no upstream cause") {
		t.Fatalf("got %+v", got)
	}
}

func TestRootCauseRootFaultIsCause(t *testing.T) {
	m := rootCauseNode("nauvis:9", "Machine", 9, map[string]any{"name": "stone-furnace", "recipe": "iron-plate", "status": "no_power"})
	got, err := rootCause(context.Background(), rootCauseFake(m, [][]any{{m}}, nil, false), MemgraphRootCauseInput{UnitNumber: "9", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Causes) != 1 || got.Causes[0].RouteText != "9" || len(got.Causes[0].Route) != 1 {
		t.Fatalf("causes = %+v", got.Causes)
	}
}

func TestRootCauseRejectsBadInput(t *testing.T) {
	chest := rootCauseNode("nauvis:5", "Chest", 5, nil)
	read := rootCauseFake(chest, nil, nil, false)
	cases := []struct {
		name string
		in   MemgraphRootCauseInput
		want string
	}{
		{"non-machine", MemgraphRootCauseInput{UnitNumber: "5", Surface: "nauvis", MaxHops: 10}, "is a Chest"},
		{"bad unit", MemgraphRootCauseInput{UnitNumber: "x", Surface: "nauvis", MaxHops: 10}, "positive decimal"},
		{"bad surface", MemgraphRootCauseInput{UnitNumber: "5", Surface: "na uvis", MaxHops: 10}, "surface"},
		{"hops low", MemgraphRootCauseInput{UnitNumber: "5", Surface: "nauvis", MaxHops: 0}, "maxHops"},
		{"hops high", MemgraphRootCauseInput{UnitNumber: "5", Surface: "nauvis", MaxHops: 17}, "maxHops"},
	}
	for _, tc := range cases {
		_, err := rootCause(context.Background(), read, tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
