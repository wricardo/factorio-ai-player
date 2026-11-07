package main

import (
	"context"
	"fmt"
	"sort"
)

// sinkAudit finds places where items end up destroyed or stuck: unfiltered
// infinity chests that swallow an arm's output, machines at full_output with
// no way out, and ordinary chests that fill with nothing taking items out.
//
// An infinity chest is a void only when its exported Chest property
// removeUnfilteredItems is true (absent means unknown and is not flagged) and
// it has no filters (no PRODUCES edge, via:'infinity').

const sinkAuditVoidCypher = `MATCH (a:Inserter {surface: $surface})-[:DROPS_TO]->(c:Chest {surface: $surface, type: 'infinity-container'})
WHERE c.removeUnfilteredItems = true AND NOT (c)-[:PRODUCES]->() AND NOT ()-[:PICKS_FROM]->(c)
OPTIONAL MATCH (a)-[:PICKS_FROM]->(src)
OPTIONAL MATCH (src)-[p:PRODUCES]->(:Item)
RETURN c, a, src, count(p)`

const sinkAuditFullOutputCypher = `MATCH (m:Machine {surface: $surface, status: 'full_output'})
WHERE NOT ()-[:PICKS_FROM]->(m) AND NOT (m)-[:DROPS_ON]->(:Belt)
RETURN m`

const sinkAuditBufferCypher = `MATCH (c:Chest {surface: $surface})
WHERE c.type <> 'infinity-container' AND c.type <> 'logistic-container' AND (c)-[:CONTAINS]->(:Item)
  AND NOT ()-[:PICKS_FROM]->(c) AND NOT (c)-[:IN_LOGISTIC_NETWORK]->()
OPTIONAL MATCH (a:Inserter)-[:DROPS_TO]->(c)
RETURN c, a`

func sinkAudit(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	run := func(cypher string) (*MemgraphQueryResult, error) {
		return read(ctx, MemgraphQueryInput{
			Cypher: cypher, Params: map[string]any{"surface": in.Surface},
			Limit: auditReadRows, TimeoutMs: auditReadTimeout,
		})
	}
	var (
		findings  []*MemgraphAuditFinding
		truncated bool
		snap      *MemgraphSnapshotInfo
	)
	steps := []func(*MemgraphQueryResult) ([]*MemgraphAuditFinding, error){
		sinkVoidFindings, sinkFullOutputFindings, sinkBufferFindings,
	}
	for i, cypher := range []string{sinkAuditVoidCypher, sinkAuditFullOutputCypher, sinkAuditBufferCypher} {
		res, err := run(cypher)
		if err != nil {
			return nil, err
		}
		fs, err := steps[i](res)
		if err != nil {
			return nil, err
		}
		findings = append(findings, fs...)
		truncated = truncated || res.Truncated
		if res.Snapshot != nil {
			snap = res.Snapshot
		}
	}
	return finishAudit(findings, in.Limit, truncated, snap), nil
}

func sinkRowNode(row []any, col int) (*MemgraphTraceNode, error) {
	if col >= len(row) {
		return nil, fmt.Errorf("sink audit row has %d columns, want more than %d", len(row), col)
	}
	if row[col] == nil {
		return nil, nil
	}
	return traceNodeFromAny(row[col])
}

func sinkVoidFindings(res *MemgraphQueryResult) ([]*MemgraphAuditFinding, error) {
	type group struct {
		chest   *MemgraphTraceNode
		related []*MemgraphTraceNode
		seen    map[string]bool
		produce bool
	}
	groups := map[string]*group{}
	var order []string
	for _, row := range res.Rows {
		chest, err := sinkRowNode(row, 0)
		if err != nil || chest == nil {
			return nil, fmt.Errorf("sink audit void row: chest: %v", err)
		}
		arm, err := sinkRowNode(row, 1)
		if err != nil {
			return nil, err
		}
		src, err := sinkRowNode(row, 2)
		if err != nil {
			return nil, err
		}
		g := groups[chest.ID]
		if g == nil {
			g = &group{chest: chest, seen: map[string]bool{}}
			groups[chest.ID] = g
			order = append(order, chest.ID)
		}
		for _, n := range []*MemgraphTraceNode{arm, src} {
			if n != nil && !g.seen[n.ID] {
				g.seen[n.ID] = true
				g.related = append(g.related, n)
			}
		}
		if src != nil && src.Kind == "Machine" && len(row) > 3 {
			if f := optionalFloat(row[3]); f != nil && *f > 0 {
				g.produce = true
			}
		}
	}
	sort.Strings(order)
	var out []*MemgraphAuditFinding
	for _, id := range order {
		g := groups[id]
		f := &MemgraphAuditFinding{Code: "VOID_SINK", Node: g.chest, Related: g.related}
		if g.produce {
			f.Severity = MemgraphAuditSeverityBlocker
			f.Message = "unfiltered infinity chest with removeUnfilteredItems true that an arm drops into: its source machine's output is destroyed"
		} else {
			f.Severity = MemgraphAuditSeverityConcern
			f.Message = "unfiltered infinity chest with removeUnfilteredItems true that an arm drops into; the source is not a machine with a known product, so nothing measurable is lost"
		}
		out = append(out, f)
	}
	return out, nil
}

func sinkFullOutputFindings(res *MemgraphQueryResult) ([]*MemgraphAuditFinding, error) {
	var out []*MemgraphAuditFinding
	for _, row := range res.Rows {
		m, err := sinkRowNode(row, 0)
		if err != nil || m == nil {
			return nil, fmt.Errorf("sink audit full_output row: machine: %v", err)
		}
		out = append(out, &MemgraphAuditFinding{
			Code: "FULL_OUTPUT_NO_CONSUMER", Severity: MemgraphAuditSeverityBlocker, Node: m,
			Message: "machine is at full_output and no inserter picks from it and it drops onto no belt, so nothing can leave it",
			Related: []*MemgraphTraceNode{},
		})
	}
	return out, nil
}

func sinkBufferFindings(res *MemgraphQueryResult) ([]*MemgraphAuditFinding, error) {
	type group struct {
		f    *MemgraphAuditFinding
		seen map[string]bool
	}
	groups := map[string]*group{}
	var order []string
	for _, row := range res.Rows {
		chest, err := sinkRowNode(row, 0)
		if err != nil || chest == nil {
			return nil, fmt.Errorf("sink audit buffer row: chest: %v", err)
		}
		arm, err := sinkRowNode(row, 1)
		if err != nil {
			return nil, err
		}
		g := groups[chest.ID]
		if g == nil {
			g = &group{seen: map[string]bool{}, f: &MemgraphAuditFinding{
				Code: "BUFFER_FULL", Severity: MemgraphAuditSeverityInfo, Node: chest,
				Message: "chest holds items and no inserter picks from it, so it only accumulates",
				Related: []*MemgraphTraceNode{},
			}}
			groups[chest.ID] = g
			order = append(order, chest.ID)
		}
		if arm != nil && !g.seen[arm.ID] {
			g.seen[arm.ID] = true
			g.f.Related = append(g.f.Related, arm)
		}
	}
	sort.Strings(order)
	out := make([]*MemgraphAuditFinding, 0, len(order))
	for _, id := range order {
		out = append(out, groups[id].f)
	}
	return out, nil
}
