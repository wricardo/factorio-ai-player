package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func testBoltNode(id, kind string, unit int64, props map[string]any) map[string]any {
	p := map[string]any{"id": id}
	if unit != 0 {
		p["unit"] = unit
	}
	for k, v := range props {
		p[k] = v
	}
	return map[string]any{"id": "e" + id, "labels": []any{"Entity", kind}, "properties": p}
}

func testTraceNode(t *testing.T, id, kind string, unit int, name, recipe, status string) *MemgraphTraceNode {
	t.Helper()
	us := strconv.Itoa(unit)
	n := &MemgraphTraceNode{ID: id, Kind: kind, UnitNumber: &us, Name: &name}
	if recipe != "" {
		n.Recipe = &recipe
	}
	if status != "" {
		n.Status = &status
	}
	return n
}

func TestSupplyChainCypherShape(t *testing.T) {
	in := MemgraphSupplyChainInput{UnitNumber: "370", Surface: "nauvis", Resolution: MemgraphSupplyResolutionBelt, MaxHops: 3, MaxPaths: 10}
	rows := supplyChainCypher(in, false)
	if got := strings.Count(rows, "UNWIND (CASE WHEN size(ps)"); got != 3 {
		t.Fatalf("hop blocks = %d, want 3", got)
	}
	if !strings.Contains(rows, "{unit: $unit, surface: $surface}") || strings.Contains(rows, "370") {
		t.Fatalf("row query must use parameters, got:\n%s", rows)
	}
	if err := validateReadOnlyCypher(rows); err != nil {
		t.Fatalf("generated statement rejected by the read-only guard: %v", err)
	}
	graph := supplyChainCypher(in, true)
	if !strings.Contains(graph, `{unit: 370, surface: "nauvis"}`) || strings.Contains(graph, "$unit") || !strings.HasSuffix(graph, "RETURN DISTINCT p") {
		t.Fatalf("graph query must inline the root and return paths, got:\n%s", graph)
	}
	if err := validateReadOnlyCypher(graph); err != nil {
		t.Fatalf("graph statement rejected: %v", err)
	}
	in.Resolution = MemgraphSupplyResolutionStretch
	stretch := supplyChainCypher(in, false)
	if !strings.Contains(stretch, "IN_STRETCH") || !strings.Contains(stretch, "FEEDS_STRETCH") || strings.Contains(stretch, "BELT_OUT") {
		t.Fatalf("stretch resolution must walk stretch edges only, got:\n%s", stretch)
	}
}

func TestValidateSupplyChainInput(t *testing.T) {
	ok := MemgraphSupplyChainInput{UnitNumber: "1", Surface: "nauvis", Resolution: MemgraphSupplyResolutionBelt, MaxHops: 10, MaxPaths: 100}
	if err := validateSupplyChainInput(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*MemgraphSupplyChainInput){
		"unit":       func(in *MemgraphSupplyChainInput) { in.UnitNumber = "0" },
		"surface":    func(in *MemgraphSupplyChainInput) { in.Surface = `x"} DELETE (n) //` },
		"maxHops":    func(in *MemgraphSupplyChainInput) { in.MaxHops = supplyChainMaxHopsLimit + 1 },
		"maxPaths":   func(in *MemgraphSupplyChainInput) { in.MaxPaths = 0 },
		"resolution": func(in *MemgraphSupplyChainInput) { in.Resolution = "FAST" },
	}
	for name, mutate := range bad {
		in := ok
		mutate(&in)
		if err := validateSupplyChainInput(in); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: want an error naming the field, got %v", name, err)
		}
	}
}

// TestSupplyOutline pins the outline contract: routes to the same supplier
// collapse, a supplier shared by two consumers is expanded once and then
// listed on one "also fed by" line, and belt runs collapse to a range.
func TestSupplyOutline(t *testing.T) {
	asm := testTraceNode(t, "n:1", "Machine", 1, "assembling-machine-1", "gear", "working")
	arm := testTraceNode(t, "n:10", "Inserter", 10, "inserter", "", "working")
	b1 := testTraceNode(t, "n:21", "Belt", 21, "transport-belt", "", "")
	b2 := testTraceNode(t, "n:22", "Belt", 22, "transport-belt", "", "")
	b3 := testTraceNode(t, "n:23", "Belt", 23, "transport-belt", "", "")
	arm2 := testTraceNode(t, "n:11", "Inserter", 11, "inserter", "", "waiting_for_source_items")
	furnace := testTraceNode(t, "n:2", "Machine", 2, "stone-furnace", "iron-plate", "working")
	armF := testTraceNode(t, "n:12", "Inserter", 12, "inserter", "", "waiting_for_space_in_destination")
	chest := testTraceNode(t, "n:3", "Chest", 3, "infinity-chest", "", "normal")
	armG := testTraceNode(t, "n:13", "Inserter", 13, "inserter", "", "working")
	gear := testTraceNode(t, "n:4", "Machine", 4, "assembling-machine-1", "inserter", "item_ingredient_shortage")
	dead := testTraceNode(t, "n:30", "Belt", 30, "transport-belt", "", "")
	armD := testTraceNode(t, "n:14", "Inserter", 14, "inserter", "", "waiting_for_source_items")

	paths := []tracePath{
		{asm, arm, b1, b2, b3, arm2, furnace},
		{asm, arm, b1, b3, arm2, furnace},
		{asm, arm, b1, b2, b3, arm2, furnace, armF, chest},
		{asm, armG, gear},
		{asm, armG, gear, arm, b1, arm2, furnace},
		{asm, armD, dead},
	}
	got := assembleSupplyChain(asm, paths)
	want := strings.Join([]string{
		"1 assembling-machine-1 (gear) [working]",
		"  <- arm 10 [working] <- belts 21..23 (3) <- arm 11 [waiting_for_source_items] <- 2 stone-furnace (iron-plate) [working] (+1 other routes)",
		"    <- arm 12 [waiting_for_space_in_destination] <- 3 infinity-chest [normal]",
		"  <- arm 13 [working] <- 4 assembling-machine-1 (inserter) [item_ingredient_shortage]",
		"    <- also fed by (expanded above): 2 stone-furnace (iron-plate)",
		"  <- arm 14 [waiting_for_source_items] <- belt 30",
	}, "\n")
	if got.Outline != want {
		t.Fatalf("outline mismatch\n got:\n%s\nwant:\n%s", got.Outline, want)
	}
	if got.Summary.PathCount != 6 || got.Summary.NodeCount != 13 {
		t.Fatalf("summary counts = %+v", got.Summary)
	}
	reasons := map[string]string{}
	for _, f := range got.Summary.Attention {
		reasons[f.Node.ID] = f.Reason
	}
	if reasons["n:4"] != "status:item_ingredient_shortage" || reasons["n:30"] != "origin:belt" || len(reasons) != 2 {
		t.Fatalf("attention = %v, want shortage on n:4 and a belt origin n:30 only", reasons)
	}
	if len(got.Origins) != 4 {
		t.Fatalf("origins = %d, want chest, furnace, gear assembler (path end) and dead belt", len(got.Origins))
	}
	for _, p := range got.Paths {
		for _, id := range p.Steps {
			if !strings.HasPrefix(id, "n:") {
				t.Fatalf("paths must hold node ids, got %q", id)
			}
		}
	}
}

func TestSupplyChainRootAndTruncation(t *testing.T) {
	root := testBoltNode("n:1", "Machine", 1, map[string]any{"name": "assembling-machine-1", "recipe": "gear"})
	reader := func(rows [][]any, truncated bool, seen *[]MemgraphQueryInput) memgraphReader {
		return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
			*seen = append(*seen, in)
			if strings.Contains(in.Cypher, "RETURN r AS node") {
				return &MemgraphQueryResult{Rows: [][]any{{root}}}, nil
			}
			return &MemgraphQueryResult{Rows: rows, Truncated: truncated}, nil
		}
	}
	in := MemgraphSupplyChainInput{UnitNumber: "1", Surface: "nauvis", Resolution: MemgraphSupplyResolutionBelt, MaxHops: 2, MaxPaths: 5}
	var seen []MemgraphQueryInput
	chest := testBoltNode("n:3", "Chest", 3, map[string]any{"name": "infinity-chest"})
	arm := testBoltNode("n:10", "Inserter", 10, map[string]any{"name": "inserter"})
	got, err := supplyChain(context.Background(), reader([][]any{{int64(1), []any{root, arm, chest}}}, true, &seen), in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Paths) != 1 || got.Paths[0].Hops != 1 || len(got.Origins) != 1 || *got.Origins[0].Name != "infinity-chest" {
		t.Fatalf("unexpected result %+v", got)
	}
	trace := seen[len(seen)-1]
	if trace.Limit != 5 || trace.Params.(map[string]any)["unit"] != int64(1) {
		t.Fatalf("trace statement must be capped by maxPaths and parameterized, got limit=%d params=%v", trace.Limit, trace.Params)
	}

	// A root that is not a Machine, Chest or Belt is rejected before the trace runs.
	armRoot := func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		return &MemgraphQueryResult{Rows: [][]any{{arm}}}, nil
	}
	if _, err := supplyChain(context.Background(), armRoot, in); err == nil || !strings.Contains(err.Error(), "an Inserter") {
		t.Fatalf("want a kind error naming Inserter, got %v", err)
	}
	// A unit that is not in the graph is an error, not an empty chain.
	none := func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		return &MemgraphQueryResult{Rows: [][]any{}}, nil
	}
	if _, err := supplyChain(context.Background(), none, in); err == nil || !strings.Contains(err.Error(), "no node with unit 1") {
		t.Fatalf("want a missing-node error, got %v", err)
	}
}

func TestAssembleCoverageGaps(t *testing.T) {
	root := testBoltNode("n:143", "Machine", 143, map[string]any{"name": "assembling-machine-1", "recipe": "electronic-circuit"})
	other := testBoltNode("n:144", "Machine", 144, map[string]any{"name": "assembling-machine-1", "recipe": "copper-cable"})
	rows := [][]any{
		{other, "copper-plate", float64(1), []any{int64(151), int64(454)}},
		{root, "copper-cable", float64(3), []any{int64(455)}},
		{root, "iron-plate", float64(1), []any{}},
	}
	rootNode, _ := traceNodeFromAny(root)
	got, err := assembleCoverage(rootNode, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Machines) != 2 || got.Machines[0].Machine.ID != "n:143" {
		t.Fatalf("root machine must come first, got %+v", got.Machines)
	}
	if len(got.Gaps) != 1 || got.Gaps[0] != "143 electronic-circuit: iron-plate has no feeding arm" {
		t.Fatalf("gaps = %v", got.Gaps)
	}
	if feed := got.Machines[1].Ingredients[0]; !feed.Covered || len(feed.FeedingArms) != 2 || feed.FeedingArms[0] != "151" {
		t.Fatalf("feed = %+v, want covered with sorted arms", feed)
	}
}

// TestDownstreamChain pins the downstream contract: the statement walks
// consumers only (never PICKS_FROM toward a source), the outline points away
// from the root and flags dead-end belts as sinks, and inputs are validated.
func TestDownstreamChain(t *testing.T) {
	in := MemgraphDownstreamChainInput{UnitNumber: "1", Surface: "nauvis", MaxHops: 2, MaxPaths: 5}
	cy := downstreamChainCypher(in, false)
	if strings.Contains(cy, "-[:PICKS_FROM]->") || strings.Contains(cy, "<-[:DROPS_TO]-") {
		t.Fatalf("downstream statement walks upstream edges:\n%s", cy)
	}
	if strings.Count(cy, "WHEN cur:Belt THEN") != 2 || !strings.Contains(downstreamChainCypher(in, true), "RETURN DISTINCT p") {
		t.Fatalf("unexpected statement shape:\n%s", cy)
	}
	for name, mut := range map[string]func(*MemgraphDownstreamChainInput){
		"unit":     func(i *MemgraphDownstreamChainInput) { i.UnitNumber = "x" },
		"maxHops":  func(i *MemgraphDownstreamChainInput) { i.MaxHops = 99 },
		"maxPaths": func(i *MemgraphDownstreamChainInput) { i.MaxPaths = 0 },
	} {
		bad := in
		mut(&bad)
		if err := validateDownstreamInput(bad); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}

	src := testTraceNode(t, "n:1", "Machine", 1, "stone-furnace", "iron-plate", "working")
	arm := testTraceNode(t, "n:10", "Inserter", 10, "inserter", "", "working")
	b1 := testTraceNode(t, "n:21", "Belt", 21, "transport-belt", "", "")
	b2 := testTraceNode(t, "n:22", "Belt", 22, "transport-belt", "", "")
	arm2 := testTraceNode(t, "n:11", "Inserter", 11, "inserter", "", "working")
	asm := testTraceNode(t, "n:2", "Machine", 2, "assembling-machine-1", "gear", "item_ingredient_shortage")
	dead := testTraceNode(t, "n:30", "Belt", 30, "transport-belt", "", "")
	armD := testTraceNode(t, "n:12", "Inserter", 12, "inserter", "", "working")
	got := assembleDownstreamChain(src, []tracePath{
		{src, arm, b1, b2, arm2, asm},
		{src, armD, dead},
	})
	want := strings.Join([]string{
		"1 stone-furnace (iron-plate) [working]",
		"  -> arm 10 [working] -> belts 21..22 (2) -> arm 11 [working] -> 2 assembling-machine-1 (gear) [item_ingredient_shortage]",
		"  -> arm 12 [working] -> belt 30",
	}, "\n")
	if got.Outline != want {
		t.Fatalf("outline mismatch\n got:\n%s\nwant:\n%s", got.Outline, want)
	}
	if len(got.Sinks) != 2 {
		t.Fatalf("sinks = %d, want the assembler and the dead belt", len(got.Sinks))
	}
	reasons := map[string]string{}
	for _, f := range got.Summary.Attention {
		reasons[f.Node.ID] = f.Reason
	}
	if reasons["n:2"] != "status:item_ingredient_shortage" || reasons["n:30"] != "sink:belt" || len(reasons) != 2 {
		t.Fatalf("attention = %v", reasons)
	}
}
