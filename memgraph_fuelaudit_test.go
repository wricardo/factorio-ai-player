package main

import (
	"context"
	"strings"
	"testing"
)

func fuelNode(id, kind string, unit int64, props map[string]any) map[string]any {
	p := map[string]any{"id": id, "unit": unit, "name": id}
	for k, v := range props {
		p[k] = v
	}
	return map[string]any{"id": "e" + id, "labels": []any{"Entity", kind}, "properties": p}
}

func fuelReader(empty, feeders [][]any) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		if strings.Contains(in.Cypher, "DROPS_TO") {
			return &MemgraphQueryResult{Rows: feeders}, nil
		}
		return &MemgraphQueryResult{Rows: empty}, nil
	}
}

func fuelCodes(a *MemgraphAudit) map[string]string {
	out := map[string]string{}
	for _, f := range a.Findings {
		out[*f.Node.UnitNumber] = f.Code
	}
	return out
}

func TestFuelAuditCodes(t *testing.T) {
	f1 := fuelNode("n:1", "Machine", 1, map[string]any{"fuelCount": int64(0)}) // no arm
	f2 := fuelNode("n:2", "Machine", 2, map[string]any{"fuelCount": int64(0)}) // belt with coal
	f3 := fuelNode("n:3", "Machine", 3, map[string]any{"fuelCount": int64(0)}) // arm, ore only
	f4 := fuelNode("n:4", "Machine", 4, map[string]any{"fuelCount": int64(0)}) // chest with wood
	f5 := fuelNode("n:5", "Machine", 5, map[string]any{"fuelCount": int64(0)}) // stretch lane coal
	i2 := fuelNode("n:12", "Inserter", 12, nil)
	i3 := fuelNode("n:13", "Inserter", 13, nil)
	i4 := fuelNode("n:14", "Inserter", 14, nil)
	i5 := fuelNode("n:15", "Inserter", 15, nil)
	belt := fuelNode("n:20", "Belt", 20, map[string]any{"lanes": []any{"1|left_line|left|coal:4", "2|right_line|right|iron-ore:4"}})
	ore := fuelNode("n:21", "Belt", 21, map[string]any{"left": []any{"iron-ore:4"}})
	chest := fuelNode("n:22", "Chest", 22, map[string]any{"items": []any{"wood@good:3"}})
	belt2 := fuelNode("n:23", "Belt", 23, nil)
	feeders := [][]any{
		{"n:2", i2, belt, nil, nil, nil},
		{"n:3", i3, ore, nil, nil, nil},
		{"n:4", i4, chest, nil, nil, nil},
		{"n:5", i5, belt2, nil, []any{"coal:9"}, nil},
	}
	a, err := fuelAuditAssemble([][]any{{f1}, {f2}, {f3}, {f4}, {f5}}, feeders, false, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := fuelCodes(a)
	want := map[string]string{"1": "NO_FUEL_ARM", "2": "FUEL_NOT_ARRIVING", "4": "FUEL_NOT_ARRIVING", "5": "FUEL_NOT_ARRIVING"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("unit %s: got %q want %q (all %v)", k, got[k], v, got)
		}
	}
	if a.Findings[0].Code != "NO_FUEL_ARM" || a.Findings[0].Severity != MemgraphAuditSeverityBlocker {
		t.Fatalf("blocker must sort first: %+v", a.Findings[0])
	}
	for _, f := range a.Findings {
		if !strings.Contains(f.Message, "sample") || !strings.Contains(f.Message, "heuristic") {
			t.Fatalf("message lacks caveat: %q", f.Message)
		}
	}
}

// Fueled burners and electric machines are never returned by the empty-fuel
// query; the reader below returns nothing for them and no finding results.
func TestFuelAuditNeverFlagsFueledOrElectric(t *testing.T) {
	var queries []string
	read := func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		queries = append(queries, in.Cypher)
		return &MemgraphQueryResult{}, nil
	}
	a, err := fuelAudit(context.Background(), read, MemgraphAuditInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 0 || a.Summary != "no findings" {
		t.Fatalf("unexpected %+v", a)
	}
	if !strings.Contains(queries[0], "fuelCount = 0") {
		t.Fatalf("empty burners must be selected on fuelCount = 0 only: %s", queries[0])
	}
}

func TestFuelAuditLimitAndValidation(t *testing.T) {
	var empty [][]any
	for i := int64(1); i <= 3; i++ {
		empty = append(empty, []any{fuelNode("n:"+string(rune('0'+i)), "Machine", i, map[string]any{"fuelCount": int64(0)})})
	}
	a, err := fuelAudit(context.Background(), fuelReader(empty, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 2 || !a.Truncated || a.Counts[0].Count != 3 {
		t.Fatalf("limit not applied: %+v", a)
	}
	for _, in := range []MemgraphAuditInput{{Surface: "", Limit: 5}, {Surface: "nauvis", Limit: 0}} {
		if _, err := fuelAudit(context.Background(), fuelReader(nil, nil), in); err == nil {
			t.Fatalf("input %+v accepted", in)
		}
	}
}
