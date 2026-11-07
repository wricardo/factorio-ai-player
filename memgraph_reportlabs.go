package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// labFeedAudit raises LAB_PACK_UNREACHABLE for every (lab, science pack) pair
// where the factory makes the pack but no arm of the lab can deliver it.
//
// Labs are Machine nodes named lab; they have no recipe, so the graph holds
// no CONSUMES edge for them and every lab is assumed to want every pack the
// factory makes. A pack is made when a machine's recipe produces it or an
// infinity chest holds it as a filter. An arm (an inserter dropping to the
// lab) reaches a pack when its pickup source is a chest holding it, a machine
// running a recipe that produces it, a belt whose own lane samples carry it,
// or a belt whose line has, upstream along BELT_OUT, a dropper (inserter
// DROPS_TO a belt of the line, or a machine DROPS_ON it) whose own source
// provides it.
//
// Approximations: the pickup belt's lane sample is counted, but the upstream
// walk is topology only (BELT_OUT, droppers); BeltStretch lane items and
// FLOWS_INTO sideloads are deliberately not used, so a pack parked on a line
// that never joins the pickup line is still flagged. One dropper hop is
// followed, not a chain of machines behind it.

// labFeedMadeCypher lists the science packs the factory makes.
const labFeedMadeCypher = `MATCH (i:Item {surface: $surface}) WHERE i.name ENDS WITH '-science-pack' AND coalesce(i.kind, 'item') = 'item'
  AND (EXISTS { MATCH (:Machine)-[:RUNS]->(:Recipe)-[:PRODUCES]->(i) } OR EXISTS { MATCH (:Chest {type: 'infinity-container'})-[:PRODUCES|CONTAINS]->(i) })
RETURN i.name
ORDER BY i.name`

// labFeedArmsCypher lists, per lab arm, its pickup source and every item that
// source can provide. "Provides" is computed once per arm, not per pack:
// re-walking the belt line per pack exceeds the statement deadline. A lab
// with no arm comes back once with null arm, source and an empty provides.
const labFeedArmsCypher = `MATCH (m:Machine {surface: $surface, name: 'lab'})
OPTIONAL MATCH (m)<-[:DROPS_TO]-(i:Inserter)-[:PICKS_FROM]->(s)
OPTIONAL MATCH (s:Belt)<-[:BELT_OUT*0..400]-(:Belt)<-[:DROPS_TO]-(:Inserter)-[:PICKS_FROM]->(p)
OPTIONAL MATCH (s:Belt)<-[:BELT_OUT*0..400]-(:Belt)<-[:DROPS_ON]-(pm:Machine)
WITH m, i, s, collect(DISTINCT p) + collect(DISTINCT pm) AS ups
OPTIONAL MATCH (s)-[:RUNS]->(:Recipe)-[:PRODUCES]->(si:Item)
OPTIONAL MATCH (s)-[:CONTAINS|PRODUCES]->(ci:Item)
WITH m, i, s, ups, collect(DISTINCT si.name) + collect(DISTINCT ci.name)
  + [x IN coalesce(s.left, []) + coalesce(s.right, []) | split(x, ':')[0]] AS own
UNWIND (CASE WHEN size(ups) = 0 THEN [NULL] ELSE ups END) AS u
OPTIONAL MATCH (u)-[:RUNS]->(:Recipe)-[:PRODUCES]->(ui:Item)
OPTIONAL MATCH (u)-[:CONTAINS|PRODUCES]->(uc:Item)
WITH m, i, s, own, collect(DISTINCT ui.name) + collect(DISTINCT uc.name) AS upstream
WITH m, i, s, own + reduce(a = [], x IN collect(upstream) | a + x) AS provides
RETURN m AS lab, i.unit AS arm, s AS source, provides
ORDER BY m.unit, arm`

func labFeedAudit(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	run := func(cypher string) (*MemgraphQueryResult, error) {
		return read(ctx, MemgraphQueryInput{
			Cypher: cypher, Params: map[string]any{"surface": in.Surface},
			Limit: auditReadRows, TimeoutMs: auditReadTimeout,
		})
	}
	madeRes, err := run(labFeedMadeCypher)
	if err != nil {
		return nil, err
	}
	made := make([]string, 0, len(madeRes.Rows))
	for _, row := range madeRes.Rows {
		name, ok := "", false
		if len(row) > 0 {
			name, ok = row[0].(string)
		}
		if !ok {
			return nil, fmt.Errorf("lab feed audit made-pack row is %v, want a pack name", row)
		}
		made = append(made, name)
	}
	armRes, err := run(labFeedArmsCypher)
	if err != nil {
		return nil, err
	}
	findings, err := labFeedFindings(made, armRes.Rows)
	if err != nil {
		return nil, err
	}
	snap := armRes.Snapshot
	if snap == nil {
		snap = madeRes.Snapshot
	}
	return finishAudit(findings, in.Limit, madeRes.Truncated || armRes.Truncated, snap), nil
}

func labFeedFindings(made []string, rows [][]any) ([]*MemgraphAuditFinding, error) {
	type lab struct {
		node     *MemgraphTraceNode
		sources  []*MemgraphTraceNode
		seen     map[string]bool
		provides map[string]bool
	}
	labs := map[string]*lab{}
	var order []string
	for _, row := range rows {
		if len(row) < 4 {
			return nil, fmt.Errorf("lab feed audit arm row has %d columns, want 4", len(row))
		}
		node, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, fmt.Errorf("lab feed audit arm row: lab: %w", err)
		}
		l := labs[node.ID]
		if l == nil {
			l = &lab{node: node, seen: map[string]bool{}, provides: map[string]bool{}}
			labs[node.ID] = l
			order = append(order, node.ID)
		}
		if row[2] != nil {
			src, err := traceNodeFromAny(row[2])
			if err != nil {
				return nil, fmt.Errorf("lab feed audit arm row: source: %w", err)
			}
			if !l.seen[src.ID] {
				l.seen[src.ID] = true
				l.sources = append(l.sources, src)
			}
		}
		items, _ := row[3].([]any)
		for _, it := range items {
			if s, ok := it.(string); ok {
				l.provides[s] = true
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return unitOf(labs[order[i]].node) < unitOf(labs[order[j]].node) })
	var out []*MemgraphAuditFinding
	for _, id := range order {
		l := labs[id]
		var can []string
		for _, p := range made {
			if l.provides[p] {
				can = append(can, p)
			}
		}
		have := "none"
		if len(can) > 0 {
			have = strings.Join(can, ", ")
		}
		for _, p := range made {
			if l.provides[p] {
				continue
			}
			related := l.sources
			if related == nil {
				related = []*MemgraphTraceNode{}
			}
			out = append(out, &MemgraphAuditFinding{
				Code: "LAB_PACK_UNREACHABLE", Severity: MemgraphAuditSeverityBlocker, Node: l.node, Related: related,
				Message: fmt.Sprintf("the factory makes %s but no arm of this lab can deliver it (no chest, machine or upstream belt dropper that provides it); the lab can get: %s. Every lab is assumed to want every made pack; the check is structural, a pack lying on a belt does not count", p, have),
			})
		}
	}
	return out, nil
}
