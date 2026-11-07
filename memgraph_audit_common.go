package main

import (
	"fmt"
	"sort"
)

// Shared plumbing of the memgraph audit fields (factoryHealth, powerAudit,
// fuelAudit, inserterAudit, sinkAudit): input validation and the single place
// that orders findings, counts them per code and applies the limit.

const (
	auditMaxLimit    = 1000
	auditReadRows    = 10000
	auditReadTimeout = 10000
)

func validateAuditInput(in MemgraphAuditInput) error {
	if in.Surface == "" {
		return fmt.Errorf("surface is required")
	}
	if in.Limit < 1 || in.Limit > auditMaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", auditMaxLimit)
	}
	return nil
}

func auditSeverityRank(s MemgraphAuditSeverity) int {
	switch s {
	case MemgraphAuditSeverityBlocker:
		return 0
	case MemgraphAuditSeverityConcern:
		return 1
	}
	return 2
}

// finishAudit orders findings (severity, then impact descending, then code,
// then node id), counts every finding per code, cuts the list to limit and
// writes the summary. truncated carries a read cap hit by the caller.
func finishAudit(findings []*MemgraphAuditFinding, limit int, truncated bool, snap *MemgraphSnapshotInfo) *MemgraphAudit {
	impact := func(f *MemgraphAuditFinding) int {
		if f.Impact == nil {
			return 0
		}
		return *f.Impact
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if ra, rb := auditSeverityRank(a.Severity), auditSeverityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if ia, ib := impact(a), impact(b); ia != ib {
			return ia > ib
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Node.ID < b.Node.ID
	})
	per := map[string]int{}
	for _, f := range findings {
		per[f.Code]++
	}
	counts := stretchItemCounts(per)
	out := &MemgraphAudit{Counts: counts, Findings: findings, Truncated: truncated, Snapshot: snap}
	if len(findings) > limit {
		out.Findings = findings[:limit]
		out.Truncated = true
	}
	switch {
	case len(findings) == 0:
		out.Summary = "no findings"
	default:
		top := findings[0]
		out.Summary = fmt.Sprintf("%d findings, worst: %s on %s", len(findings), top.Code, traceLabel(top.Node, false))
	}
	return out
}
