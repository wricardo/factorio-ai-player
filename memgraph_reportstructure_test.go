package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// rsWorld is the canned graph one structuralAudit run reads. Each field holds
// what the matching Cypher statement would return from the live graph.
type rsWorld struct {
	stretches []map[string]any // BeltStretch nodes
	noInput   []string         // stretch ids the no-inflow statement returns
	heads     []map[string]any // head Belt nodes
	noSource  []map[string]any
	noDest    []map[string]any
	noRecipe  []map[string]any
	ghosts    []map[string]any
	truncated bool
	queries   map[string]int
}

func rsEntity(kind string, unit int64, props map[string]any) map[string]any {
	p := map[string]any{"id": "nauvis:" + strconv.FormatInt(unit, 10), "unit": unit, "name": strings.ToLower(kind), "x": 1.5, "y": 2.5}
	for k, v := range props {
		p[k] = v
	}
	return map[string]any{"id": "e" + strconv.FormatInt(unit, 10), "labels": []any{"Entity", kind}, "properties": p}
}

func rsRows(nodes []map[string]any) [][]any {
	rows := make([][]any, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, []any{n})
	}
	return rows
}

func (w *rsWorld) reader(t *testing.T) memgraphReader {
	w.queries = map[string]int{}
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		w.queries[in.Cypher]++
		params, _ := in.Params.(map[string]any)
		if params["surface"] != "nauvis" {
			t.Fatalf("surface param = %v", params["surface"])
		}
		snap := &MemgraphSnapshotInfo{ID: "snap-1"}
		switch in.Cypher {
		case stretchesCypher:
			return &MemgraphQueryResult{Rows: rsRows(w.stretches), Snapshot: snap}, nil
		case stretchFlowsCypher, stretchPickersCypher:
			return &MemgraphQueryResult{}, nil
		case structuralNoInputCypher:
			rows := make([][]any, 0, len(w.noInput))
			for _, id := range w.noInput {
				rows = append(rows, []any{id})
			}
			return &MemgraphQueryResult{Rows: rows, Truncated: w.truncated, Snapshot: snap}, nil
		case structuralHeadBeltsCypher:
			if _, ok := params["units"].([]any); !ok {
				t.Fatalf("units param = %T, want []any", params["units"])
			}
			return &MemgraphQueryResult{Rows: rsRows(w.heads), Snapshot: snap}, nil
		case structuralNoSourceCypher:
			return &MemgraphQueryResult{Rows: rsRows(w.noSource), Snapshot: snap}, nil
		case structuralNoDestinationCypher:
			return &MemgraphQueryResult{Rows: rsRows(w.noDest), Snapshot: snap}, nil
		case structuralNoRecipeCypher:
			return &MemgraphQueryResult{Rows: rsRows(w.noRecipe), Snapshot: snap}, nil
		case structuralGhostsCypher:
			return &MemgraphQueryResult{Rows: rsRows(w.ghosts), Snapshot: snap}, nil
		}
		t.Fatalf("unexpected statement: %s", in.Cypher)
		return nil, nil
	}
}

func rsRun(t *testing.T, w *rsWorld, limit int) *MemgraphAudit {
	t.Helper()
	out, err := structuralAudit(context.Background(), w.reader(t), MemgraphAuditInput{Surface: "nauvis", Limit: limit})
	if err != nil {
		t.Fatalf("structuralAudit: %v", err)
	}
	return out
}

func rsByCode(out *MemgraphAudit) map[string][]*MemgraphAuditFinding {
	m := map[string][]*MemgraphAuditFinding{}
	for _, f := range out.Findings {
		m[f.Code] = append(m[f.Code], f)
	}
	return m
}

func rsGhost(unit int64, ghostName string) map[string]any {
	return rsEntity("Ghost", unit, map[string]any{"name": "entity-ghost", "ghostName": ghostName})
}

func TestStructuralAuditEmptyWorld(t *testing.T) {
	out := rsRun(t, &rsWorld{}, 10)
	if len(out.Findings) != 0 || out.Summary != "no findings" || out.Truncated {
		t.Fatalf("empty world gave %+v", out)
	}
}

func TestStructuralAuditValidatesInput(t *testing.T) {
	w := &rsWorld{}
	for _, in := range []MemgraphAuditInput{{Limit: 5}, {Surface: "nauvis"}, {Surface: "nauvis", Limit: auditMaxLimit + 1}, {Surface: "bad surface!", Limit: 5}} {
		if _, err := structuralAudit(context.Background(), w.reader(t), in); err == nil {
			t.Fatalf("input %+v was accepted", in)
		}
	}
}

// A stretch the no-inflow statement returns is STRETCH_NO_INPUT at its head
// belt; a stretch it does not return (fed mid-run, by an inserter, by a
// flowing stretch) is not, and an id the stretch read does not know is skipped.
func TestStructuralAuditStretchNoInput(t *testing.T) {
	w := &rsWorld{
		stretches: []map[string]any{
			stretchTestNode("nauvis:stretch:10", 10, 4, "deadend", []any{"iron-plate:5"}, []any{"copper-plate:3"}, 5, 3),
			stretchTestNode("nauvis:stretch:20", 20, 2, "splitter", nil, nil, 0, 0),
			stretchTestNode("nauvis:stretch:30", 30, 6, "deadend", []any{"coal:9"}, nil, 9, 0), // fed mid-run: not returned
		},
		noInput: []string{"nauvis:stretch:10", "nauvis:stretch:20", "nauvis:stretch:999"},
		heads:   []map[string]any{rsEntity("Belt", 10, map[string]any{"name": "transport-belt"})},
	}
	out := rsRun(t, w, 10)
	got := rsByCode(out)["STRETCH_NO_INPUT"]
	if len(got) != 2 || len(out.Findings) != 2 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	byNode := map[string]*MemgraphAuditFinding{}
	for _, f := range got {
		if f.Severity != MemgraphAuditSeverityConcern {
			t.Fatalf("%s severity = %s, want CONCERN even when it carries items", f.Node.ID, f.Severity)
		}
		byNode[f.Node.ID] = f
	}
	carrying := byNode["nauvis:10"]
	if carrying == nil || carrying.Node.Kind != "Belt" || *carrying.Node.UnitNumber != "10" {
		t.Fatalf("carrying stretch node = %+v, want head belt 10", carrying)
	}
	for _, want := range []string{"4 tiles", "deadend", "8 items", "iron-plate"} {
		if !strings.Contains(carrying.Message, want) {
			t.Fatalf("message %q lacks %q", carrying.Message, want)
		}
	}
	empty := byNode["nauvis:stretch:20"]
	if empty == nil || empty.Node.Kind != "BeltStretch" {
		t.Fatalf("stretch without a head belt row must fall back to the stretch node, got %+v (%v)", empty, byNode)
	}
	for _, want := range []string{"splitter", "carries nothing"} {
		if !strings.Contains(empty.Message, want) {
			t.Fatalf("message %q lacks %q", empty.Message, want)
		}
	}
}

func TestStructuralAuditNoInputStatementSkippedWhenNoStretches(t *testing.T) {
	w := &rsWorld{noInput: []string{"nauvis:stretch:1"}}
	out := rsRun(t, w, 10)
	if len(out.Findings) != 0 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	if w.queries[structuralHeadBeltsCypher] != 0 {
		t.Fatalf("head belts were read with nothing to report")
	}
}

// Two items on one lane is MIXED_LANE; one item per lane, even different ones
// on the two lanes, is a normal two-lane feed.
func TestStructuralAuditMixedLane(t *testing.T) {
	w := &rsWorld{
		stretches: []map[string]any{
			stretchTestNode("nauvis:stretch:1", 1, 3, "deadend", []any{"coal:5", "iron-ore:4"}, nil, 9, 0),
			stretchTestNode("nauvis:stretch:2", 2, 3, "deadend", []any{"iron-plate:7"}, []any{"copper-plate:7"}, 7, 7),
			stretchTestNode("nauvis:stretch:3", 3, 3, "deadend", nil, []any{"coal:2", "stone:1"}, 0, 3),
		},
		heads: []map[string]any{rsEntity("Belt", 1, nil), rsEntity("Belt", 3, nil)},
	}
	out := rsRun(t, w, 10)
	got := rsByCode(out)["MIXED_LANE"]
	if len(got) != 2 || len(out.Findings) != 2 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	if *got[0].Node.UnitNumber != "1" || *got[1].Node.UnitNumber != "3" {
		t.Fatalf("mixed nodes = %s, %s, want head belts 1 and 3", *got[0].Node.UnitNumber, *got[1].Node.UnitNumber)
	}
	for _, f := range got {
		if f.Severity != MemgraphAuditSeverityConcern {
			t.Fatalf("severity = %s", f.Severity)
		}
	}
	if !strings.Contains(got[0].Message, "coal") || !strings.Contains(got[0].Message, "iron-ore") {
		t.Fatalf("message %q does not list the lane items", got[0].Message)
	}
}

// A stretch that is both without inflow and mixed raises both codes.
func TestStructuralAuditStretchWithTwoDefects(t *testing.T) {
	w := &rsWorld{
		stretches: []map[string]any{stretchTestNode("nauvis:stretch:1", 1, 3, "deadend", []any{"coal:5", "iron-ore:4"}, nil, 9, 0)},
		noInput:   []string{"nauvis:stretch:1"},
		heads:     []map[string]any{rsEntity("Belt", 1, nil)},
	}
	out := rsRun(t, w, 10)
	codes := rsByCode(out)
	if len(codes["STRETCH_NO_INPUT"]) != 1 || len(codes["MIXED_LANE"]) != 1 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	if w.queries[structuralHeadBeltsCypher] != 1 {
		t.Fatalf("head belts read %d times, want once", w.queries[structuralHeadBeltsCypher])
	}
}

func TestStructuralAuditInserters(t *testing.T) {
	w := &rsWorld{
		noSource: []map[string]any{rsEntity("Inserter", 390, map[string]any{"name": "inserter"}), rsEntity("Inserter", 391, nil)},
		noDest:   []map[string]any{rsEntity("Inserter", 391, nil), rsEntity("Inserter", 500, nil)},
	}
	out := rsRun(t, w, 10)
	codes := rsByCode(out)
	if len(codes["INSERTER_NO_SOURCE"]) != 2 || len(codes["INSERTER_NO_DESTINATION"]) != 2 || len(out.Findings) != 4 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	for _, f := range out.Findings {
		if f.Severity != MemgraphAuditSeverityConcern || f.Node.Kind != "Inserter" {
			t.Fatalf("finding %+v", f)
		}
	}
	// 391 has neither edge: both codes, one per code.
	both := 0
	for _, f := range out.Findings {
		if *f.Node.UnitNumber == "391" {
			both++
		}
	}
	if both != 2 {
		t.Fatalf("inserter 391 has %d findings, want 2", both)
	}
}

func TestStructuralAuditMachineNoRecipe(t *testing.T) {
	w := &rsWorld{noRecipe: []map[string]any{
		rsEntity("Machine", 700, map[string]any{"name": "assembling-machine-2", "status": "no_recipe"}),
		rsEntity("Machine", 701, map[string]any{"name": "chemical-plant"}),
	}}
	out := rsRun(t, w, 10)
	got := rsByCode(out)["MACHINE_NO_RECIPE"]
	if len(got) != 2 || len(out.Findings) != 2 {
		t.Fatalf("findings = %+v", out.Findings)
	}
	if got[0].Severity != MemgraphAuditSeverityConcern || *got[0].Node.UnitNumber != "700" || !strings.Contains(got[0].Message, "no_recipe") {
		t.Fatalf("finding 700 = %+v", got[0])
	}
	if strings.Contains(got[1].Message, "status") {
		t.Fatalf("a machine without status must not mention one: %q", got[1].Message)
	}
}

func TestStructuralAuditGhosts(t *testing.T) {
	w := &rsWorld{ghosts: []map[string]any{
		rsGhost(93, "electric-energy-interface"), rsGhost(94, "small-electric-pole"), rsGhost(95, "small-electric-pole"),
		rsGhost(96, "a"), rsGhost(97, "b"), rsGhost(98, "c"), rsGhost(99, "d"),
	}}
	out := rsRun(t, w, 10)
	got := rsByCode(out)["GHOSTS_PENDING"]
	if len(got) != 1 || len(out.Findings) != 1 {
		t.Fatalf("one summary finding expected, got %+v", out.Findings)
	}
	f := got[0]
	if f.Severity != MemgraphAuditSeverityInfo || f.Node.Kind != "Ghost" || *f.Node.UnitNumber != "93" {
		t.Fatalf("finding = %+v node %+v", f, f.Node)
	}
	for _, want := range []string{"7 construction ghosts", "small-electric-pole x2", "built: small-electric-pole x2", "and 1 more"} {
		if !strings.Contains(f.Message, want) {
			t.Fatalf("message %q lacks %q", f.Message, want)
		}
	}
}

func TestStructuralAuditGhostsFallBackToEntityName(t *testing.T) {
	w := &rsWorld{ghosts: []map[string]any{rsEntity("Ghost", 1, map[string]any{"name": "tile-ghost"})}}
	out := rsRun(t, w, 10)
	if len(out.Findings) != 1 || !strings.Contains(out.Findings[0].Message, "tile-ghost x1") {
		t.Fatalf("findings = %+v", out.Findings)
	}
}

// Severity first (CONCERN before INFO), counts cover every finding, the limit
// cuts the list and marks it truncated, a read cap marks it truncated too.
func TestStructuralAuditOrderingLimitAndTruncation(t *testing.T) {
	w := &rsWorld{
		noSource: []map[string]any{rsEntity("Inserter", 390, nil), rsEntity("Inserter", 389, nil)},
		noRecipe: []map[string]any{rsEntity("Machine", 700, nil)},
		ghosts:   []map[string]any{rsGhost(93, "x")},
	}
	out := rsRun(t, w, 10)
	if len(out.Findings) != 4 || out.Truncated {
		t.Fatalf("findings = %d truncated = %v", len(out.Findings), out.Truncated)
	}
	if last := out.Findings[3]; last.Code != "GHOSTS_PENDING" {
		t.Fatalf("last finding = %s, want the INFO one", last.Code)
	}
	if out.Findings[0].Code != "INSERTER_NO_SOURCE" || *out.Findings[0].Node.UnitNumber != "389" {
		t.Fatalf("first finding = %+v, want inserter 389 (code, then node id order)", out.Findings[0])
	}
	if out.Snapshot == nil || out.Snapshot.ID != "snap-1" {
		t.Fatalf("snapshot = %+v", out.Snapshot)
	}

	cut := rsRun(t, w, 2)
	if len(cut.Findings) != 2 || !cut.Truncated {
		t.Fatalf("limit 2: findings = %d truncated = %v", len(cut.Findings), cut.Truncated)
	}
	total := 0
	for _, c := range cut.Counts {
		total += c.Count
	}
	if total != 4 {
		t.Fatalf("counts cover %d findings, want all 4: %+v", total, cut.Counts)
	}

	w.truncated = true
	w.stretches = []map[string]any{stretchTestNode("nauvis:stretch:1", 1, 1, "deadend", nil, nil, 0, 0)}
	if !rsRun(t, w, 10).Truncated {
		t.Fatalf("a truncated read must mark the audit truncated")
	}
}

func TestStructuralAuditPropagatesReadErrors(t *testing.T) {
	for _, fail := range []string{stretchesCypher, structuralNoInputCypher, structuralNoSourceCypher, structuralNoDestinationCypher, structuralNoRecipeCypher, structuralGhostsCypher} {
		w := &rsWorld{stretches: []map[string]any{stretchTestNode("nauvis:stretch:1", 1, 1, "deadend", nil, nil, 0, 0)}}
		inner := w.reader(t)
		read := func(ctx context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
			if in.Cypher == fail {
				return nil, context.DeadlineExceeded
			}
			return inner(ctx, in)
		}
		if _, err := structuralAudit(context.Background(), read, MemgraphAuditInput{Surface: "nauvis", Limit: 5}); err == nil {
			t.Fatalf("a failing read of %q was swallowed", fail)
		}
	}
}
