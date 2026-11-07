package main

import (
	"context"
	"fmt"
	"sort"
)

// rateDeficitAudit raises NOMINAL_DEFICIT for every item whose nominal
// consumption exceeds its nominal production. It reuses nominalRateBalance and
// leaves out items with an unbounded (infinity chest) source: their supply has
// no rate, so no deficit can be stated. The rates are nominal (every machine
// working flat out), so this is a design-level concern, not a measured stall.
func rateDeficitAudit(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	bal, err := nominalRateBalance(ctx, read, MemgraphNominalRateBalanceInput{Surface: in.Surface, Limit: nominalRateMaxLimit})
	if err != nil {
		return nil, err
	}
	var findings []*MemgraphAuditFinding
	for _, r := range bal.Items {
		if r.Net >= 0 || r.UnboundedSources > 0 || r.Producers+r.Consumers == 0 {
			continue
		}
		name := r.Name
		findings = append(findings, &MemgraphAuditFinding{
			Code:     "NOMINAL_DEFICIT",
			Severity: MemgraphAuditSeverityConcern,
			Node:     &MemgraphTraceNode{ID: in.Surface + ":item:" + name, Kind: "Item", Name: &name},
			Message: fmt.Sprintf("%s: nominal consumption %s/min by %d machine(s) exceeds nominal production %s/min by %d machine(s), net %s/min. Nominal means every machine runs flat out, so actual rates are lower on both sides",
				name, nominalRateFmt(r.ConsumedPerMinute), r.Consumers, nominalRateFmt(r.ProducedPerMinute), r.Producers, nominalRateFmt(r.Net)),
			Related: []*MemgraphTraceNode{},
		})
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Node.ID < findings[j].Node.ID })
	return finishAudit(findings, in.Limit, bal.Truncated, bal.Snapshot), nil
}
