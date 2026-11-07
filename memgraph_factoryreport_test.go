package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func reportFinding(code string, sev MemgraphAuditSeverity, id string, impact *int) *MemgraphAuditFinding {
	unit := id
	return &MemgraphAuditFinding{
		Code:     code,
		Severity: sev,
		Node:     &MemgraphTraceNode{ID: id, Kind: "Machine", UnitNumber: &unit},
		Impact:   impact,
	}
}

func fakeAudit(findings ...*MemgraphAuditFinding) func(context.Context, memgraphReader, MemgraphAuditInput) (*MemgraphAudit, error) {
	return func(_ context.Context, _ memgraphReader, _ MemgraphAuditInput) (*MemgraphAudit, error) {
		return finishAudit(findings, auditMaxLimit, false, nil), nil
	}
}

func failingAudit(msg string) func(context.Context, memgraphReader, MemgraphAuditInput) (*MemgraphAudit, error) {
	return func(context.Context, memgraphReader, MemgraphAuditInput) (*MemgraphAudit, error) {
		return nil, errors.New(msg)
	}
}

func useReportAudits(t *testing.T, runners ...factoryReportRunner) {
	t.Helper()
	old := factoryReportAudits
	factoryReportAudits = runners
	t.Cleanup(func() { factoryReportAudits = old })
}

func reportKeys(r *MemgraphFactoryReport) string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Code+":"+f.Node.ID+":"+*f.Audit)
	}
	return strings.Join(out, ",")
}

func TestFactoryReportDedupesByCodeAndNodeFirstWins(t *testing.T) {
	useReportAudits(t,
		factoryReportRunner{"sinkAudit", fakeAudit(
			reportFinding("NO_CONSUMER", MemgraphAuditSeverityConcern, "m1", nil),
			reportFinding("VOID_SINK", MemgraphAuditSeverityBlocker, "c1", nil),
		)},
		factoryReportRunner{"structure", fakeAudit(
			reportFinding("NO_CONSUMER", MemgraphAuditSeverityConcern, "m1", nil),
			reportFinding("NO_CONSUMER", MemgraphAuditSeverityConcern, "m2", nil),
		)},
	)
	r, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := "VOID_SINK:c1:sinkAudit,NO_CONSUMER:m1:sinkAudit,NO_CONSUMER:m2:structure"
	if got := reportKeys(r); got != want {
		t.Fatalf("findings = %s, want %s", got, want)
	}
	// Section counts are what each audit raised, before de-duplication.
	if r.Sections[0].FindingCount != 2 || r.Sections[1].FindingCount != 2 {
		t.Fatalf("sections = %+v %+v", r.Sections[0], r.Sections[1])
	}
	if r.Sections[0].Error != nil || r.Sections[1].Error != nil {
		t.Fatal("unexpected section error")
	}
	if r.Summary != "3 findings: 1 BLOCKER, 2 CONCERN; worst: VOID_SINK on c1" {
		t.Fatalf("summary = %q", r.Summary)
	}
}

func TestFactoryReportDoesNotMutateAuditFindings(t *testing.T) {
	f := reportFinding("A", MemgraphAuditSeverityInfo, "m1", nil)
	useReportAudits(t, factoryReportRunner{"powerAudit", fakeAudit(f)})
	if _, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if f.Audit != nil {
		t.Fatalf("source finding mutated: %v", *f.Audit)
	}
}

func TestFactoryReportPartialFailureKeepsOtherAudits(t *testing.T) {
	useReportAudits(t,
		factoryReportRunner{"powerAudit", failingAudit("boom")},
		factoryReportRunner{"fuelAudit", fakeAudit(reportFinding("NO_FUEL", MemgraphAuditSeverityBlocker, "m1", nil))},
	)
	r, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if reportKeys(r) != "NO_FUEL:m1:fuelAudit" {
		t.Fatalf("findings = %s", reportKeys(r))
	}
	bad := r.Sections[0]
	if bad.Name != "powerAudit" || bad.Error == nil || *bad.Error != "boom" || bad.FindingCount != 0 || bad.Summary != "" {
		t.Fatalf("failed section = %+v", bad)
	}
	if r.Sections[1].Error != nil {
		t.Fatalf("good section error = %v", *r.Sections[1].Error)
	}
	if !r.Truncated {
		t.Fatal("a failed audit must mark the report truncated")
	}
}

func TestFactoryReportAllFailedIsError(t *testing.T) {
	useReportAudits(t,
		factoryReportRunner{"powerAudit", failingAudit("boom")},
		factoryReportRunner{"fuelAudit", failingAudit("bang")},
	)
	_, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestFactoryReportLimitCutsWorstFirstCountsCoverAll(t *testing.T) {
	ten, five := 10, 5
	useReportAudits(t,
		factoryReportRunner{"a", fakeAudit(
			reportFinding("INFO_X", MemgraphAuditSeverityInfo, "m1", nil),
			reportFinding("CONCERN_X", MemgraphAuditSeverityConcern, "m2", &five),
		)},
		factoryReportRunner{"b", fakeAudit(
			reportFinding("CONCERN_X", MemgraphAuditSeverityConcern, "m3", &ten),
			reportFinding("BLOCK_X", MemgraphAuditSeverityBlocker, "m4", nil),
		)},
	)
	r, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reportKeys(r), "BLOCK_X:m4:b,CONCERN_X:m3:b"; got != want {
		t.Fatalf("findings = %s, want %s", got, want)
	}
	if !r.Truncated {
		t.Fatal("cut must set truncated")
	}
	counts := map[string]int{}
	for _, c := range r.Counts {
		counts[c.Name] = c.Count
	}
	if counts["INFO_X"] != 1 || counts["CONCERN_X"] != 2 || counts["BLOCK_X"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	if !strings.HasPrefix(r.Summary, "4 findings: 1 BLOCKER, 2 CONCERN, 1 INFO; worst: BLOCK_X") {
		t.Fatalf("summary = %q", r.Summary)
	}
}

func TestFactoryReportSectionTruncationAndSnapshot(t *testing.T) {
	snap := &MemgraphSnapshotInfo{ID: "snap-1"}
	useReportAudits(t,
		factoryReportRunner{"a", fakeAudit()},
		factoryReportRunner{"b", func(context.Context, memgraphReader, MemgraphAuditInput) (*MemgraphAudit, error) {
			return &MemgraphAudit{Summary: "s", Truncated: true, Snapshot: snap}, nil
		}},
	)
	r, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || r.Snapshot != snap {
		t.Fatalf("truncated=%v snapshot=%v", r.Truncated, r.Snapshot)
	}
	if r.Summary != "no findings" || len(r.Findings) != 0 {
		t.Fatalf("summary=%q findings=%d", r.Summary, len(r.Findings))
	}
}

func TestFactoryReportPassesSurfaceAndAuditLimit(t *testing.T) {
	var got MemgraphAuditInput
	useReportAudits(t, factoryReportRunner{"a", func(_ context.Context, _ memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
		got = in
		return finishAudit(nil, in.Limit, false, nil), nil
	}})
	if _, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 3}); err != nil {
		t.Fatal(err)
	}
	if got.Surface != "nauvis" || got.Limit != auditMaxLimit {
		t.Fatalf("audit input = %+v", got)
	}
}

func TestFactoryReportValidatesInput(t *testing.T) {
	useReportAudits(t, factoryReportRunner{"a", fakeAudit()})
	for _, in := range []MemgraphFactoryReportInput{
		{Surface: "", Limit: 10},
		{Surface: "nauvis", Limit: 0},
		{Surface: "nauvis", Limit: 2001},
	} {
		if _, err := factoryReport(context.Background(), nil, in); err == nil {
			t.Fatalf("input %+v accepted", in)
		}
	}
	if _, err := factoryReport(context.Background(), nil, MemgraphFactoryReportInput{Surface: "nauvis", Limit: 2000}); err != nil {
		t.Fatalf("limit 2000 rejected: %v", err)
	}
}
