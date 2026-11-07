package main

import (
	"context"
	"strings"
	"testing"
)

func sinkNode(kind string, unit int64, props map[string]any) map[string]any {
	return removalImpactBoltNode("n:"+kind+":"+string(rune('a'+unit%26))+string(rune('0'+unit/26)), kind, unit, props)
}

// sinkReader routes by statement; each argument is the row set of one query.
func sinkReader(void, full, buffer [][]any, seen *[]MemgraphQueryInput) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		if seen != nil {
			*seen = append(*seen, in)
		}
		switch in.Cypher {
		case sinkAuditVoidCypher:
			return &MemgraphQueryResult{Rows: void}, nil
		case sinkAuditFullOutputCypher:
			return &MemgraphQueryResult{Rows: full}, nil
		case sinkAuditBufferCypher:
			return &MemgraphQueryResult{Rows: buffer}, nil
		}
		panic("unexpected cypher")
	}
}

func sinkCodes(a *MemgraphAudit) []string {
	var out []string
	for _, f := range a.Findings {
		out = append(out, f.Code+":"+string(f.Severity)+":"+*f.Node.UnitNumber)
	}
	return out
}

func TestSinkAuditVoidSink(t *testing.T) {
	chest := sinkNode("Chest", 1, map[string]any{"name": "infinity-chest"})
	arm := sinkNode("Inserter", 2, map[string]any{"name": "inserter"})
	producing := sinkNode("Machine", 3, map[string]any{"name": "assembling-machine-1"})
	chest2 := sinkNode("Chest", 4, map[string]any{"name": "infinity-chest"})
	arm2 := sinkNode("Inserter", 5, map[string]any{"name": "inserter"})
	belt := sinkNode("Belt", 6, map[string]any{"name": "transport-belt"})
	a, err := sinkAudit(context.Background(), sinkReader([][]any{
		{chest, arm, producing, int64(2)},
		{chest2, arm2, belt, int64(0)},
	}, nil, nil, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sinkCodes(a), ",")
	if got != "VOID_SINK:BLOCKER:1,VOID_SINK:CONCERN:4" {
		t.Fatalf("findings = %s", got)
	}
	if len(a.Findings[0].Related) != 2 {
		t.Fatalf("related = %v", a.Findings[0].Related)
	}
}

func TestSinkAuditQueriesPassReadOnlyGuard(t *testing.T) {
	for _, q := range []string{sinkAuditVoidCypher, sinkAuditFullOutputCypher, sinkAuditBufferCypher} {
		if err := validateReadOnlyCypher(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSinkAuditFullOutputAndBuffer(t *testing.T) {
	m := sinkNode("Machine", 10, map[string]any{"name": "assembling-machine-1", "status": "full_output"})
	c := sinkNode("Chest", 11, map[string]any{"name": "wooden-chest"})
	arm := sinkNode("Inserter", 12, map[string]any{"name": "inserter"})
	arm2 := sinkNode("Inserter", 13, map[string]any{"name": "inserter"})
	a, err := sinkAudit(context.Background(), sinkReader(nil,
		[][]any{{m}}, [][]any{{c, arm}, {c, arm2}, {c, nil}}, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sinkCodes(a), ","); got != "FULL_OUTPUT_NO_CONSUMER:BLOCKER:10,BUFFER_FULL:INFO:11" {
		t.Fatalf("findings = %s", got)
	}
	if n := len(a.Findings[1].Related); n != 2 {
		t.Fatalf("buffer related = %d, want 2 arms deduped", n)
	}
}

func TestSinkAuditEmptyAndLimit(t *testing.T) {
	a, err := sinkAudit(context.Background(), sinkReader(nil, nil, nil, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 5})
	if err != nil || len(a.Findings) != 0 || a.Summary != "no findings" {
		t.Fatalf("%v %+v", err, a)
	}
	var full [][]any
	for i := int64(20); i < 23; i++ {
		full = append(full, []any{sinkNode("Machine", i, map[string]any{"name": "m"})})
	}
	a, err = sinkAudit(context.Background(), sinkReader(nil, full, nil, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 2})
	if err != nil || len(a.Findings) != 2 || !a.Truncated || a.Counts[0].Count != 3 {
		t.Fatalf("%v %+v", err, a)
	}
}

func TestSinkAuditValidatesInput(t *testing.T) {
	for _, in := range []MemgraphAuditInput{{Surface: "", Limit: 5}, {Surface: "nauvis", Limit: 0}, {Surface: "nauvis", Limit: 1001}} {
		if _, err := sinkAudit(context.Background(), sinkReader(nil, nil, nil, nil), in); err == nil {
			t.Fatalf("input %+v accepted", in)
		}
	}
}
