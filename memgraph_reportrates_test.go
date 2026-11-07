package main

import (
	"context"
	"strings"
	"testing"
)

func rateAuditRows() [][]any {
	return [][]any{
		{"petroleum-gas", 540.0, 3000.0, int64(1), int64(2), int64(0)},
		{"iron-ore", 0.0, 206.25, int64(0), int64(11), int64(1)},   // unbounded source: silent
		{"gear", 60.0, 0.0, int64(1), int64(0), int64(0)},          // surplus: silent
		{"balanced", 10.0, 10.0, int64(1), int64(1), int64(0)},     // balanced: silent
		{"infinity-chest", 0.0, 0.0, int64(0), int64(0), int64(1)}, // nothing: silent
		{"copper-cable", 0.0, 90.0, int64(0), int64(2), int64(0)},
	}
}

func TestRateDeficitAuditFlagsOnlyBoundedDeficits(t *testing.T) {
	var seen MemgraphQueryInput
	a, err := rateDeficitAudit(context.Background(), nominalRateReader(rateAuditRows(), &seen), MemgraphAuditInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 2 || a.Counts[0].Name != "NOMINAL_DEFICIT" || a.Counts[0].Count != 2 {
		t.Fatalf("findings = %v counts = %v", a.Findings, a.Counts)
	}
	var ids []string
	for _, f := range a.Findings {
		if f.Severity != MemgraphAuditSeverityConcern || f.Node.Kind != "Item" || f.Node.UnitNumber != nil || f.Impact != nil {
			t.Errorf("bad finding %+v node %+v", f, f.Node)
		}
		ids = append(ids, f.Node.ID)
	}
	if strings.Join(ids, ",") != "nauvis:item:copper-cable,nauvis:item:petroleum-gas" {
		t.Errorf("ids = %v", ids)
	}
	if m := a.Findings[1].Message; !strings.Contains(m, "3000/min") || !strings.Contains(m, "540/min") || !strings.Contains(m, "nominal") {
		t.Errorf("message = %q", m)
	}
	if seen.Params.(map[string]any)["surface"] != "nauvis" {
		t.Errorf("params = %v", seen.Params)
	}
}

func TestRateDeficitAuditLimitAndValidation(t *testing.T) {
	a, err := rateDeficitAudit(context.Background(), nominalRateReader(rateAuditRows(), nil), MemgraphAuditInput{Surface: "nauvis", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 1 || !a.Truncated || a.Counts[0].Count != 2 {
		t.Fatalf("findings=%d truncated=%v counts=%v", len(a.Findings), a.Truncated, a.Counts)
	}
	for _, in := range []MemgraphAuditInput{{Surface: "", Limit: 5}, {Surface: "nauvis", Limit: 0}, {Surface: "nauvis", Limit: 1001}} {
		if _, err := rateDeficitAudit(context.Background(), nominalRateReader(nil, nil), in); err == nil {
			t.Errorf("%+v: want error", in)
		}
	}
	a, err = rateDeficitAudit(context.Background(), nominalRateReader([][]any{{"gear", 60.0, 0.0, int64(1), int64(0), int64(0)}}, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 5})
	if err != nil || len(a.Findings) != 0 || a.Summary != "no findings" {
		t.Fatalf("clean audit = %+v err=%v", a, err)
	}
}
