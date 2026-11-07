package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func fhNode(id, kind string, unit int64, name, recipe, status string) map[string]any {
	p := map[string]any{"id": id, "unit": unit, "name": name}
	if recipe != "" {
		p["recipe"] = recipe
	}
	if status != "" {
		p["status"] = status
	}
	return map[string]any{"id": "e" + id, "labels": []any{"Entity", kind}, "properties": p}
}

func fhLabels(kind string) []any { return []any{"Entity", kind} }

// fhGraph is the fixture: rows by statement kind.
//
//	m1 (no_power) -> m2 (gear, unfed) -> belt x1 -> x2 -> m3 (ingredient shortage, a symptom)
//	m1 -> belt c1 -> c2 -> m5                       (fed line: no ORIGIN_BELT)
//	b1 -> b2 -> m4                                  (nothing feeds b1: ORIGIN_BELT)
//	chest (no_power) -> m6
type fhGraph struct {
	machines, chests, flows, beltLinks, dropsOn, coverage, stretches, stretchFlows, stretchPickers [][]any
	trunc                                                                                          string // statement marker that reports truncated
	err                                                                                            string // statement marker that fails
	calls                                                                                          map[string]int
	coverageUnits                                                                                  [][]any
}

func fhFixture() *fhGraph {
	g := &fhGraph{calls: map[string]int{}}
	g.machines = [][]any{
		{fhNode("n:1", "Machine", 1, "assembling-machine-1", "gear", "no_power")},
		{fhNode("n:2", "Machine", 2, "assembling-machine-1", "inserter", "working")},
		{fhNode("n:3", "Machine", 3, "assembling-machine-1", "belt", "item_ingredient_shortage")},
		{fhNode("n:4", "Machine", 4, "stone-furnace", "iron-plate", "working")},
		{fhNode("n:5", "Machine", 5, "stone-furnace", "copper-plate", "working")},
		{fhNode("n:6", "Machine", 6, "stone-furnace", "stone-brick", "working")},
	}
	g.chests = [][]any{{fhNode("n:chest", "Chest", 50, "infinity-chest", "", "no_power")}}
	g.flows = [][]any{
		{"n:1", fhLabels("Machine"), "n:2", fhLabels("Machine")},
		{"n:2", fhLabels("Machine"), "n:x1", fhLabels("Belt")},
		{"n:x2", fhLabels("Belt"), "n:3", fhLabels("Machine")},
		{"n:1", fhLabels("Machine"), "n:c1", fhLabels("Belt")},
		{"n:c2", fhLabels("Belt"), "n:5", fhLabels("Machine")},
		{"n:b2", fhLabels("Belt"), "n:4", fhLabels("Machine")},
		{"n:chest", fhLabels("Chest"), "n:6", fhLabels("Machine")},
	}
	g.beltLinks = [][]any{{"n:x1", "n:x2"}, {"n:c1", "n:c2"}, {"n:b1", "n:b2"}}
	g.coverage = [][]any{
		{fhNode("n:2", "Machine", 2, "assembling-machine-1", "inserter", "working"), "iron-gear-wheel", 1.0, []any{}},
		{fhNode("n:2", "Machine", 2, "assembling-machine-1", "inserter", "working"), "iron-plate", 1.0, []any{int64(90)}},
		{fhNode("n:3", "Machine", 3, "assembling-machine-1", "belt", "item_ingredient_shortage"), "iron-plate", 1.0, []any{int64(91)}},
	}
	g.stretches = [][]any{
		{stretchTestNode("n:stretch:1", 1, 3, "deadend", []any{"iron-plate:30"}, nil, 30, 0)},
		{stretchTestNode("n:stretch:2", 2, 2, "deadend", []any{"coal:5"}, nil, 5, 0)},
	}
	g.stretchPickers = [][]any{{"n:stretch:2", int64(7)}}
	return g
}

func (g *fhGraph) reader() memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		var rows [][]any
		var marker string
		switch {
		case strings.Contains(in.Cypher, "feedingArms"):
			marker, rows = "coverage", g.coverage
			g.coverageUnits = append(g.coverageUnits, in.Params.(map[string]any)["units"].([]any))
		case strings.Contains(in.Cypher, "MATCH (m:Machine)"):
			marker, rows = "machines", g.machines
		case strings.Contains(in.Cypher, "MATCH (c:Chest)"):
			marker, rows = "chests", g.chests
		case strings.Contains(in.Cypher, "AS src"):
			marker, rows = "flows", g.flows
		case strings.Contains(in.Cypher, "AS beltFrom"):
			marker, rows = "beltLinks", g.beltLinks
		case strings.Contains(in.Cypher, "AS producer"):
			marker, rows = "dropsOn", g.dropsOn
		case strings.Contains(in.Cypher, "MATCH (b:Belt)"):
			marker = "belts"
			for _, id := range in.Params.(map[string]any)["ids"].([]any) {
				rows = append(rows, []any{fhNode(id.(string), "Belt", 100, "transport-belt", "", "")})
			}
		case strings.Contains(in.Cypher, "FLOWS_INTO"):
			marker, rows = "stretchFlows", g.stretchFlows
		case strings.Contains(in.Cypher, "TAKES_FROM_STRETCH"):
			marker, rows = "stretchPickers", g.stretchPickers
		case strings.Contains(in.Cypher, "BeltStretch"):
			marker, rows = "stretches", g.stretches
		default:
			return nil, fmt.Errorf("unexpected statement: %s", in.Cypher)
		}
		g.calls[marker]++
		if g.err == marker {
			return nil, errors.New("boom")
		}
		return &MemgraphQueryResult{Rows: rows, Truncated: g.trunc == marker, Snapshot: &MemgraphSnapshotInfo{ID: "snap"}}, nil
	}
}

type fhKey struct {
	code, node string
	impact     int
}

func fhKeys(a *MemgraphAudit) []fhKey {
	var out []fhKey
	for _, f := range a.Findings {
		impact := -1
		if f.Impact != nil {
			impact = *f.Impact
		}
		out = append(out, fhKey{f.Code, f.Node.ID, impact})
	}
	return out
}

func TestFactoryHealthFindings(t *testing.T) {
	g := fhFixture()
	got, err := factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	// Blockers first by impact (ties by code, then node id), then concerns.
	want := []fhKey{
		{"MACHINE_TROUBLE", "n:1", 3},     // downstream: m2, m3 (through x1,x2), m5 (through c1,c2)
		{"MACHINE_TROUBLE", "n:chest", 1}, // chest feeds m6
		{"NO_FEEDING_ARM", "n:2", 1},      // m3 is downstream of m2
		{"ORIGIN_BELT", "n:b1", 1},        // the head of the unfed line, m4 takes from it
		{"NO_CONSUMER", "n:stretch:1", 0},
	}
	keys := fhKeys(got)
	if len(keys) != len(want) {
		t.Fatalf("findings = %+v, want %+v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("finding %d = %+v, want %+v (all %+v)", i, keys[i], want[i], keys)
		}
	}
	sev := map[string]MemgraphAuditSeverity{}
	for _, f := range got.Findings {
		sev[f.Code] = f.Severity
	}
	if sev["MACHINE_TROUBLE"] != MemgraphAuditSeverityBlocker || sev["NO_FEEDING_ARM"] != MemgraphAuditSeverityBlocker ||
		sev["ORIGIN_BELT"] != MemgraphAuditSeverityConcern || sev["NO_CONSUMER"] != MemgraphAuditSeverityConcern {
		t.Fatalf("severities = %v", sev)
	}
	if got.Truncated || got.Snapshot == nil || got.Snapshot.ID != "snap" {
		t.Fatalf("truncated %v snapshot %+v", got.Truncated, got.Snapshot)
	}
	// m1's related machines are the nearest downstream ones, at most three.
	var related []string
	for _, n := range got.Findings[0].Related {
		related = append(related, n.ID)
	}
	if strings.Join(related, ",") != "n:2,n:3,n:5" && strings.Join(related, ",") != "n:2,n:5,n:3" {
		t.Fatalf("related of n:1 = %v", related)
	}
}

// Negatives: a status that is a symptom, a covered ingredient, a fed line and a
// picked stretch each produce nothing.
func TestFactoryHealthNegatives(t *testing.T) {
	g := fhFixture()
	g.machines[0] = []any{fhNode("n:1", "Machine", 1, "assembling-machine-1", "gear", "working")} // no fault
	g.chests = nil
	g.coverage = g.coverage[1:]                                                                   // only covered ingredients
	g.flows = append(g.flows, []any{"n:m4feeder", fhLabels("Machine"), "n:b1", fhLabels("Belt")}) // b1 is now fed
	g.stretchPickers = append(g.stretchPickers, []any{"n:stretch:1", int64(8)})
	got, err := factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 0 || got.Summary != "no findings" {
		t.Fatalf("findings = %+v summary %q", fhKeys(got), got.Summary)
	}
	if g.calls["belts"] != 0 {
		t.Fatalf("no origin belt, yet belts were read %d times", g.calls["belts"])
	}
}

// A machine that drops onto the belt line feeds it.
func TestFactoryHealthDropsOnFeedsLine(t *testing.T) {
	g := fhFixture()
	g.dropsOn = [][]any{{"n:drill", fhLabels("MiningDrill"), "n:b1"}}
	got, err := factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got.Findings {
		if f.Code == "ORIGIN_BELT" {
			t.Fatalf("drill-fed line reported as origin: %+v", fhKeys(got))
		}
	}
}

// An upstream chain through a chest and a belt-to-belt tap still ends at the
// unfed belt, and the impact follows the full downstream chain.
func TestFactoryHealthOriginThroughChest(t *testing.T) {
	g := fhFixture()
	g.machines = [][]any{
		{fhNode("n:4", "Machine", 4, "stone-furnace", "iron-plate", "working")},
		{fhNode("n:7", "Machine", 7, "assembling-machine-1", "gear", "working")},
	}
	g.chests, g.coverage, g.stretches = nil, nil, nil
	g.flows = [][]any{
		{"n:b2", fhLabels("Belt"), "n:chestA", fhLabels("Chest")}, // tap from the unfed line into a chest
		{"n:chestA", fhLabels("Chest"), "n:4", fhLabels("Machine")},
		{"n:4", fhLabels("Machine"), "n:b9", fhLabels("Belt")},
		{"n:b9", fhLabels("Belt"), "n:7", fhLabels("Machine")},
	}
	g.beltLinks = [][]any{{"n:b1", "n:b2"}}
	got, err := factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	want := []fhKey{{"ORIGIN_BELT", "n:b1", 2}}
	if keys := fhKeys(got); len(keys) != 1 || keys[0] != want[0] {
		t.Fatalf("findings = %+v, want %+v", keys, want)
	}
}

func TestFactoryHealthLimitAndTruncated(t *testing.T) {
	g := fhFixture()
	got, err := factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 2 || !got.Truncated || got.Findings[0].Node.ID != "n:1" {
		t.Fatalf("findings = %+v truncated %v", fhKeys(got), got.Truncated)
	}
	total := 0
	for _, c := range got.Counts {
		total += c.Count
	}
	if total != 5 {
		t.Fatalf("counts cover %d findings, want all 5: %+v", total, got.Counts)
	}

	for _, marker := range []string{"flows", "stretchPickers"} {
		g = fhFixture()
		g.trunc = marker
		got, err = factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Truncated {
			t.Fatalf("a capped %s read must set truncated", marker)
		}
	}
}

func TestFactoryHealthCoverageRunsInChunks(t *testing.T) {
	g := fhFixture()
	g.machines = nil
	for u := int64(1); u <= 205; u++ {
		g.machines = append(g.machines, []any{fhNode(fmt.Sprintf("n:%d", u), "Machine", u, "assembling-machine-1", "gear", "working")})
	}
	g.coverage = nil
	if _, err := factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 50}); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, u := range g.coverageUnits {
		sizes = append(sizes, len(u))
	}
	if fmt.Sprint(sizes) != "[100 100 5]" {
		t.Fatalf("coverage chunks = %v, want [100 100 5]", sizes)
	}
}

func TestFactoryHealthInputAndReadErrors(t *testing.T) {
	g := fhFixture()
	for _, in := range []MemgraphAuditInput{{Surface: "", Limit: 10}, {Surface: "nauvis", Limit: 0}, {Surface: "nauvis", Limit: auditMaxLimit + 1}} {
		if _, err := factoryHealth(context.Background(), g.reader(), in); err == nil {
			t.Fatalf("input %+v accepted", in)
		}
	}
	if len(g.calls) != 0 {
		t.Fatalf("invalid input must not read: %v", g.calls)
	}
	for _, marker := range []string{"machines", "flows", "coverage", "stretches"} {
		g = fhFixture()
		g.err = marker
		_, err := factoryHealth(context.Background(), g.reader(), MemgraphAuditInput{Surface: "nauvis", Limit: 10})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("%s read error = %v", marker, err)
		}
	}
}
