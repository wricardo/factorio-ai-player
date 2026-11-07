package main

import (
	"context"
	"fmt"
	"strings"
)

const factoryReportMaxLimit = 2000

type factoryReportRunner struct {
	name string
	run  func(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error)
}

// factoryReportAudits are the audits factoryReport merges, in report order.
// Tests replace it to control what each audit returns.
var factoryReportAudits = []factoryReportRunner{
	{"factoryHealth", factoryHealth},
	{"powerAudit", powerAudit},
	{"fuelAudit", fuelAudit},
	{"inserterAudit", inserterAudit},
	{"sinkAudit", sinkAudit},
	{"structure", structuralAudit},
	{"labFeed", labFeedAudit},
	{"rateDeficits", rateDeficitAudit},
}

// factoryReport runs every audit sequentially (they share one reader), merges
// their findings, de-duplicates by (code, node id), ranks them like any other
// audit and cuts to the limit. An audit that fails is recorded in its section
// and does not fail the report unless every audit failed.
func factoryReport(ctx context.Context, read memgraphReader, in MemgraphFactoryReportInput) (*MemgraphFactoryReport, error) {
	if in.Surface == "" {
		return nil, fmt.Errorf("surface is required")
	}
	if in.Limit < 1 || in.Limit > factoryReportMaxLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", factoryReportMaxLimit)
	}
	auditIn := MemgraphAuditInput{Surface: in.Surface, Limit: auditMaxLimit}

	sections := make([]*MemgraphReportSection, 0, len(factoryReportAudits))
	var merged []*MemgraphAuditFinding
	var snap *MemgraphSnapshotInfo
	seen := map[string]bool{}
	truncated := false
	failed := 0
	var firstErr string

	for _, r := range factoryReportAudits {
		a, err := r.run(ctx, read, auditIn)
		if err == nil && a == nil {
			err = fmt.Errorf("audit returned no result")
		}
		if err != nil {
			msg := err.Error()
			if failed == 0 {
				firstErr = r.name + ": " + msg
			}
			failed++
			truncated = true
			sections = append(sections, &MemgraphReportSection{Name: r.name, Error: &msg})
			continue
		}
		raised := 0
		for _, c := range a.Counts {
			raised += c.Count
		}
		if raised < len(a.Findings) {
			raised = len(a.Findings)
		}
		sections = append(sections, &MemgraphReportSection{Name: r.name, Summary: a.Summary, FindingCount: raised})
		truncated = truncated || a.Truncated
		if snap == nil {
			snap = a.Snapshot
		}
		for _, f := range a.Findings {
			if f == nil || f.Node == nil {
				continue
			}
			key := f.Code + "\x00" + f.Node.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			c := *f
			name := r.name
			c.Audit = &name
			merged = append(merged, &c)
		}
	}
	if failed == len(factoryReportAudits) {
		return nil, fmt.Errorf("every audit failed; first: %s", firstErr)
	}

	bySeverity := map[MemgraphAuditSeverity]int{}
	for _, f := range merged {
		bySeverity[f.Severity]++
	}
	total := len(merged)
	done := finishAudit(merged, in.Limit, truncated, snap)

	report := &MemgraphFactoryReport{
		Counts:    done.Counts,
		Findings:  done.Findings,
		Sections:  sections,
		Truncated: done.Truncated,
		Snapshot:  done.Snapshot,
	}
	if total == 0 {
		report.Summary = "no findings"
		return report, nil
	}
	var parts []string
	for _, s := range []MemgraphAuditSeverity{MemgraphAuditSeverityBlocker, MemgraphAuditSeverityConcern, MemgraphAuditSeverityInfo} {
		if n := bySeverity[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	top := done.Findings[0]
	report.Summary = fmt.Sprintf("%d findings: %s; worst: %s on %s", total, strings.Join(parts, ", "), top.Code, traceLabel(top.Node, false))
	return report, nil
}
