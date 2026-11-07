package main

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type iaNode = map[string]any

func iaBolt(kind string, unit int64, props map[string]any) iaNode {
	p := map[string]any{"id": "n:" + strconv.FormatInt(unit, 10), "unit": unit, "name": strings.ToLower(kind)}
	for k, v := range props {
		p[k] = v
	}
	return iaNode{"id": "e" + strconv.FormatInt(unit, 10), "labels": []any{"Entity", kind}, "properties": p}
}

type iaArm struct{ inserter, source, dest iaNode }

func iaInserter(unit int64, props map[string]any) iaNode { return iaBolt("Inserter", unit, props) }

func iaFilter(names ...string) []any {
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"index": int64(len(out) + 1), "name": n})
	}
	return out
}

// iaWorld is the canned graph one audit run reads.
type iaWorld struct {
	arms      []iaArm
	unfed     []int64            // belt units nothing feeds
	machItems map[int64][]string // machine and chest unit -> producible items
	beltOf    map[int64]string   // belt unit -> stretch id
	stretches map[string][]any   // stretch id -> itemsLeft
	truncated bool
}

func (w *iaWorld) reader(t *testing.T) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		switch in.Cypher {
		case inserterAuditArmsCypher:
			rows := make([][]any, 0, len(w.arms))
			for _, a := range w.arms {
				rows = append(rows, []any{a.inserter, a.source, a.dest})
			}
			return &MemgraphQueryResult{Rows: rows, Truncated: w.truncated}, nil
		case inserterAuditUnfedBeltsCypher:
			var rows [][]any
			for _, u := range w.unfed {
				rows = append(rows, []any{u})
			}
			return &MemgraphQueryResult{Rows: rows}, nil
		case removalImpactProducesCypher:
			var rows [][]any
			for u, items := range w.machItems {
				list := make([]any, 0, len(items))
				for _, it := range items {
					list = append(list, it)
				}
				rows = append(rows, []any{u, list})
			}
			return &MemgraphQueryResult{Rows: rows}, nil
		case inserterAuditBeltStretchCypher:
			var rows [][]any
			for u, id := range w.beltOf {
				rows = append(rows, []any{u, id})
			}
			return &MemgraphQueryResult{Rows: rows}, nil
		case stretchesCypher:
			var rows [][]any
			head := 1
			for id, left := range w.stretches {
				rows = append(rows, []any{stretchTestNode(id, head, 3, "deadend", left, nil, len(left), 0)})
				head++
			}
			return &MemgraphQueryResult{Rows: rows}, nil
		case stretchFlowsCypher, stretchPickersCypher:
			return &MemgraphQueryResult{}, nil
		}
		t.Fatalf("unexpected statement: %s", in.Cypher)
		return nil, nil
	}
}

func iaRun(t *testing.T, w *iaWorld, limit int) *MemgraphAudit {
	t.Helper()
	out, err := inserterAudit(context.Background(), w.reader(t), MemgraphAuditInput{Surface: "nauvis", Limit: limit})
	if err != nil {
		t.Fatalf("inserterAudit: %v", err)
	}
	return out
}

// iaCodes maps inserter unit to its sorted finding codes.
func iaCodes(out *MemgraphAudit) map[string][]string {
	m := map[string][]string{}
	for _, f := range out.Findings {
		u := *f.Node.UnitNumber
		m[u] = append(m[u], f.Code)
		sort.Strings(m[u])
	}
	return m
}

func iaExpect(t *testing.T, out *MemgraphAudit, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for u, codes := range iaCodes(out) {
		got[u] = strings.Join(codes, ",")
	}
	if len(got) != len(want) {
		t.Fatalf("findings per inserter = %v, want %v", got, want)
	}
	for u, codes := range want {
		if got[u] != codes {
			t.Fatalf("inserter %s findings = %q, want %q (all: %v)", u, got[u], codes, got)
		}
	}
}

func TestInserterAuditSelfLoop(t *testing.T) {
	m := iaBolt("Machine", 100, nil)
	w := &iaWorld{arms: []iaArm{
		{iaInserter(1, nil), m, m},                           // same node: fires
		{iaInserter(2, nil), m, iaBolt("Machine", 101, nil)}, // different nodes: silent
	}}
	out := iaRun(t, w, 10)
	iaExpect(t, out, map[string]string{"1": "SELF_LOOP"})
	f := out.Findings[0]
	if f.Severity != MemgraphAuditSeverityBlocker || len(f.Related) != 1 || f.Related[0].ID != "n:100" {
		t.Fatalf("self loop finding = %+v, want a BLOCKER relating the one shared node", f)
	}
}

func TestInserterAuditVoidPickup(t *testing.T) {
	dest := iaBolt("Machine", 200, nil)
	inf := func(unit int64, flag any) iaNode {
		p := map[string]any{"type": "infinity-container", "name": "infinity-chest"}
		if flag != nil {
			p["removeUnfilteredItems"] = flag
		}
		return iaBolt("Chest", unit, p)
	}
	w := &iaWorld{arms: []iaArm{
		{iaInserter(1, nil), inf(300, true), dest},  // voiding infinity chest: fires
		{iaInserter(2, nil), inf(301, false), dest}, // keeps unfiltered items: silent
		{iaInserter(3, nil), inf(302, nil), dest},   // flag not exported: silent
		{iaInserter(4, nil), iaBolt("Chest", 303, map[string]any{"type": "container", "removeUnfilteredItems": true}), dest}, // not infinity
	}}
	out := iaRun(t, w, 10)
	iaExpect(t, out, map[string]string{"1": "VOID_PICKUP"})

	// A flagged chest that has filters (it provides items) is a normal supply
	// chest and is not a void pickup.
	w.arms = w.arms[:1]
	w.machItems = map[int64][]string{300: {"iron-plate"}}
	iaExpect(t, iaRun(t, w, 10), map[string]string{})
	w.machItems = nil
	f := out.Findings[0]
	if f.Severity != MemgraphAuditSeverityConcern || len(f.Related) != 2 || f.Related[0].ID != "n:300" || f.Related[1].ID != "n:200" {
		t.Fatalf("void pickup finding = %+v, want a CONCERN relating source then destination", f)
	}
}

func TestInserterAuditBeltToBelt(t *testing.T) {
	w := &iaWorld{arms: []iaArm{
		{iaInserter(1, nil), iaBolt("Belt", 400, map[string]any{"leftCount": int64(2)}), iaBolt("Belt", 401, nil)},
		{iaInserter(2, nil), iaBolt("Belt", 402, map[string]any{"leftCount": int64(2)}), iaBolt("Machine", 403, nil)},
		{iaInserter(3, nil), iaBolt("Machine", 404, nil), iaBolt("Belt", 405, nil)},
	}}
	out := iaRun(t, w, 10)
	iaExpect(t, out, map[string]string{"1": "BELT_TO_BELT"})
	if out.Findings[0].Severity != MemgraphAuditSeverityInfo {
		t.Fatalf("severity = %s, want INFO", out.Findings[0].Severity)
	}
}

func TestInserterAuditDeadSource(t *testing.T) {
	dest := iaBolt("Machine", 500, nil)
	w := &iaWorld{
		arms: []iaArm{
			{iaInserter(1, nil), iaBolt("Belt", 510, nil), dest},                                           // unfed and empty: fires
			{iaInserter(2, nil), iaBolt("Belt", 511, map[string]any{"leftCount": int64(3)}), dest},         // unfed but carries items
			{iaInserter(3, nil), iaBolt("Belt", 512, nil), dest},                                           // empty but fed
			{iaInserter(4, nil), iaBolt("Belt", 513, map[string]any{"left": []any{"iron-plate:1"}}), dest}, // lane list only counts as contents
			{iaInserter(5, nil), iaBolt("Machine", 514, nil), dest},                                        // not a belt
		},
		unfed: []int64{510, 511, 513},
	}
	out := iaRun(t, w, 10)
	iaExpect(t, out, map[string]string{"1": "DEAD_SOURCE"})
	if out.Findings[0].Severity != MemgraphAuditSeverityConcern {
		t.Fatalf("severity = %s, want CONCERN", out.Findings[0].Severity)
	}
}

func TestInserterAuditFilterMismatch(t *testing.T) {
	dest := iaBolt("Machine", 600, nil)
	furnace := iaBolt("Machine", 601, nil)
	chest := iaBolt("Chest", 602, map[string]any{"type": "container"})
	emptyChest := iaBolt("Chest", 603, map[string]any{"type": "container"})
	ironBelt := iaBolt("Belt", 604, map[string]any{"leftCount": int64(1)})
	emptyBelt := iaBolt("Belt", 605, map[string]any{"leftCount": int64(1)})
	unknown := iaBolt("Splitter", 606, nil)
	filt := func(unit int64, mode string, names ...string) iaNode {
		p := map[string]any{"useFilters": true, "filters": iaFilter(names...)}
		if mode != "" {
			p["filterMode"] = mode
		}
		return iaInserter(unit, p)
	}
	w := &iaWorld{
		arms: []iaArm{
			{filt(1, "whitelist", "copper-plate"), furnace, dest},                                                     // whitelist, nothing matches: fires
			{filt(2, "whitelist", "iron-plate"), furnace, dest},                                                       // matches: silent
			{filt(3, "whitelist", "copper-plate", "iron-plate"), furnace, dest},                                       // one of two matches: silent
			{iaInserter(4, nil), furnace, dest},                                                                       // no filters never mismatch
			{filt(5, "blacklist", "copper-plate"), furnace, dest},                                                     // blacklist of something else: silent
			{filt(6, "blacklist", "iron-plate"), furnace, dest},                                                       // blacklist of everything it makes: fires
			{filt(7, "", "copper-plate"), chest, dest},                                                                // missing mode is a whitelist; chest CONTAINS: fires
			{filt(8, "whitelist", "coal"), emptyChest, dest},                                                          // nothing known about the source: silent
			{filt(9, "whitelist", "copper-plate"), ironBelt, dest},                                                    // belt lane items: fires
			{filt(10, "whitelist", "iron-plate"), ironBelt, dest},                                                     // belt carries it: silent
			{filt(11, "whitelist", "copper-plate"), emptyBelt, dest},                                                  // empty stretch: silent
			{filt(12, "whitelist", "copper-plate"), unknown, dest},                                                    // unknown source kind: silent
			{iaInserter(13, map[string]any{"useFilters": false, "filters": iaFilter("copper-plate")}), furnace, dest}, // filters switched off
		},
		machItems: map[int64][]string{601: {"iron-plate", "iron-plate"}, 602: {"iron-ore"}},
		beltOf:    map[int64]string{604: "n:stretch:1", 605: "n:stretch:2"},
		stretches: map[string][]any{"n:stretch:1": {"iron-plate:4"}, "n:stretch:2": nil},
	}
	out := iaRun(t, w, 20)
	iaExpect(t, out, map[string]string{"1": "FILTER_MISMATCH", "6": "FILTER_MISMATCH", "7": "FILTER_MISMATCH", "9": "FILTER_MISMATCH"})
	for _, f := range out.Findings {
		if f.Severity != MemgraphAuditSeverityConcern {
			t.Fatalf("%s severity = %s, want CONCERN", f.Code, f.Severity)
		}
	}
}

func TestInserterAuditFiltersAcceptPlainNames(t *testing.T) {
	got := inserterAuditFilters(map[string]any{"filters": []any{"b", map[string]any{"name": "a"}, "b", 3}})
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("filters = %v, want [a b]", got)
	}
	if inserterAuditFilters(map[string]any{}) != nil {
		t.Fatal("no filters property must give no filters")
	}
}

// Findings are ordered worst first, the limit cuts the list but counts cover
// every finding, and a read cap hit is reported as truncated.
func TestInserterAuditOrderLimitTruncated(t *testing.T) {
	m := iaBolt("Machine", 700, nil)
	w := &iaWorld{arms: []iaArm{
		{iaInserter(1, nil), iaBolt("Belt", 701, map[string]any{"leftCount": int64(1)}), iaBolt("Belt", 702, nil)}, // INFO
		{iaInserter(2, nil), m, m},                        // BLOCKER
		{iaInserter(3, nil), iaBolt("Belt", 703, nil), m}, // CONCERN
	}, unfed: []int64{703}}
	out := iaRun(t, w, 2)
	if len(out.Findings) != 2 || out.Findings[0].Code != "SELF_LOOP" || out.Findings[1].Code != "DEAD_SOURCE" {
		t.Fatalf("findings = %+v, want SELF_LOOP then DEAD_SOURCE", out.Findings)
	}
	if !out.Truncated {
		t.Fatal("limit cut findings, truncated must be true")
	}
	total := 0
	for _, c := range out.Counts {
		total += c.Count
	}
	if total != 3 {
		t.Fatalf("counts cover %d findings, want all 3 (%v)", total, out.Counts)
	}
	if !strings.Contains(out.Summary, "3 findings") || !strings.Contains(out.Summary, "SELF_LOOP") {
		t.Fatalf("summary = %q", out.Summary)
	}

	w.truncated = true
	if out := iaRun(t, w, 10); !out.Truncated {
		t.Fatal("a capped read must be reported as truncated")
	}
	w.truncated = false
	if out := iaRun(t, w, 10); out.Truncated {
		t.Fatal("nothing was cut, truncated must be false")
	}
}

func TestInserterAuditNoFindings(t *testing.T) {
	out := iaRun(t, &iaWorld{}, 10)
	if out.Summary != "no findings" || len(out.Findings) != 0 {
		t.Fatalf("empty graph audit = %+v", out)
	}
}

func TestInserterAuditInputValidation(t *testing.T) {
	read := (&iaWorld{}).reader(t)
	for _, in := range []MemgraphAuditInput{
		{Surface: "", Limit: 10},
		{Surface: "nauvis", Limit: 0},
		{Surface: "nauvis", Limit: auditMaxLimit + 1},
		{Surface: "na uvis'", Limit: 10},
	} {
		if _, err := inserterAudit(context.Background(), read, in); err == nil {
			t.Fatalf("input %+v must be rejected", in)
		}
	}
}
