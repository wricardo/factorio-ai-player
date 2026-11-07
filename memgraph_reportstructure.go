package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// memgraph.factoryReport, structural section: defects that show in the shape of
// the factory graph alone, one pass of five set queries.
//
//   - STRETCH_NO_INPUT (CONCERN): a :BeltStretch none of whose belts has an
//     inflow: no BELT_OUT from a belt outside the stretch, no entity that
//     DROPS_ON or DROPS_TO one of its belts, and no other stretch FLOWS_INTO it.
//     Items it already carries are mentioned but do not raise the severity.
//   - MIXED_LANE (CONCERN): a stretch with two or more different items on one
//     lane (the beltStretches rule; left and right carrying different items is
//     a normal two-lane feed).
//   - INSERTER_NO_SOURCE / INSERTER_NO_DESTINATION (CONCERN): an :Inserter
//     without a PICKS_FROM / DROPS_TO edge. inserterAudit only reads inserters
//     with both edges, so these are disjoint from it. An inserter missing both
//     raises both codes.
//   - MACHINE_NO_RECIPE (CONCERN): a :Machine of engine type assembling-machine
//     (assembling machines, chemical plants, oil refineries, and the other
//     prototypes the engine types that way) with no recipe property. Labs and
//     furnaces are never flagged: a lab has no recipe and a furnace picks its
//     recipe from its input.
//   - GHOSTS_PENDING (INFO): one finding for all :Ghost nodes, the first ghost
//     as node, the commonest ghost names in the message.
//
// Topology and sampled lane contents only; a lane sample is a point in time.

const (
	// structuralNoInputCypher returns the id of every stretch without inflow.
	structuralNoInputCypher = `MATCH (b:Belt)-[:IN_STRETCH]->(s:BeltStretch) WHERE s.surface = $surface
OPTIONAL MATCH (x)-[r:BELT_OUT|DROPS_ON|DROPS_TO]->(b) WHERE NOT (x)-[:IN_STRETCH]->(s)
WITH s, count(r) AS inflow
WHERE inflow = 0 AND NOT ()-[:FLOWS_INTO]->(s)
RETURN s.id AS stretch ORDER BY s.headUnit`

	structuralHeadBeltsCypher = `MATCH (b:Belt) WHERE b.surface = $surface AND b.unit IN $units RETURN b ORDER BY b.unit`

	structuralNoSourceCypher = `MATCH (i:Inserter) WHERE i.surface = $surface AND NOT (i)-[:PICKS_FROM]->() RETURN i ORDER BY i.unit`

	structuralNoDestinationCypher = `MATCH (i:Inserter) WHERE i.surface = $surface AND NOT (i)-[:DROPS_TO]->() RETURN i ORDER BY i.unit`

	structuralNoRecipeCypher = `MATCH (m:Machine) WHERE m.surface = $surface AND m.type = 'assembling-machine' AND (m.recipe IS NULL OR m.recipe = '') RETURN m ORDER BY m.unit`

	structuralGhostsCypher = `MATCH (g:Ghost) WHERE g.surface = $surface RETURN g ORDER BY g.unit`

	// structuralListedGhosts caps the ghost names spelled out in a message.
	structuralListedGhosts = 5
)

// structuralNodes reads one statement whose first column is a node and decodes
// every row, together with the node properties.
func structuralNodes(ctx context.Context, read memgraphReader, cypher, what, surface string) ([]*MemgraphTraceNode, []map[string]any, *MemgraphQueryResult, error) {
	res, err := read(ctx, MemgraphQueryInput{Cypher: cypher, Params: map[string]any{"surface": surface}, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", what, err)
	}
	nodes := make([]*MemgraphTraceNode, 0, len(res.Rows))
	props := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) < 1 {
			return nil, nil, nil, fmt.Errorf("%s: row is empty", what)
		}
		n, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", what, err)
		}
		m, _ := row[0].(map[string]any)
		p, _ := m["properties"].(map[string]any)
		nodes = append(nodes, n)
		props = append(props, p)
	}
	return nodes, props, res, nil
}

// structuralStretchNode is the node a stretch finding points at: its head belt,
// or the stretch itself when the head belt is not in the graph.
func structuralStretchNode(s *stretchRow, heads map[string]*MemgraphTraceNode) *MemgraphTraceNode {
	if n := heads[s.HeadUnit]; n != nil {
		return n
	}
	name := "belt-stretch"
	return &MemgraphTraceNode{ID: s.ID, Kind: "BeltStretch", Name: &name, X: s.X, Y: s.Y}
}

func structuralGhostName(props map[string]any) string {
	if n, _ := props["ghostName"].(string); n != "" {
		return n
	}
	n, _ := props["name"].(string)
	return n
}

func structuralAudit(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	if !memgraphSurfacePattern.MatchString(in.Surface) {
		return nil, fmt.Errorf("surface %q is not a valid surface name", in.Surface)
	}
	var findings []*MemgraphAuditFinding
	var snap *MemgraphSnapshotInfo
	truncated := false
	note := func(res *MemgraphQueryResult) {
		truncated = truncated || res.Truncated
		if snap == nil {
			snap = res.Snapshot
		}
	}

	// Stretches: mixed lanes and missing inflow.
	stretches, stretchSnap, stretchTrunc, err := loadStretches(ctx, read, MemgraphBeltStretchesInput{Surface: in.Surface})
	if err != nil {
		return nil, err
	}
	truncated = truncated || stretchTrunc
	if snap == nil {
		snap = stretchSnap
	}
	byID := make(map[string]*stretchRow, len(stretches))
	for _, s := range stretches {
		byID[s.ID] = s
	}
	noInputRes, err := read(ctx, MemgraphQueryInput{Cypher: structuralNoInputCypher, Params: map[string]any{"surface": in.Surface}, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
	if err != nil {
		return nil, fmt.Errorf("stretches without inflow: %w", err)
	}
	note(noInputRes)
	var noInput []*stretchRow
	seenStretch := map[string]bool{}
	for _, row := range noInputRes.Rows {
		if len(row) < 1 {
			return nil, fmt.Errorf("stretches without inflow: row is empty")
		}
		id, _ := row[0].(string)
		if s := byID[id]; s != nil && !seenStretch[id] {
			seenStretch[id] = true
			noInput = append(noInput, s)
		}
	}
	var mixed []*stretchRow
	for _, s := range stretches {
		if s.Mixed {
			mixed = append(mixed, s)
		}
	}

	heads := map[string]*MemgraphTraceNode{}
	if len(noInput)+len(mixed) > 0 {
		units := map[int]bool{}
		for _, list := range [][]*stretchRow{noInput, mixed} {
			for _, s := range list {
				u, err := strconv.Atoi(s.HeadUnit)
				if err != nil {
					return nil, fmt.Errorf("stretch %s: head unit %q is not a number", s.ID, s.HeadUnit)
				}
				units[u] = true
			}
		}
		ordered := make([]int, 0, len(units))
		for u := range units {
			ordered = append(ordered, u)
		}
		sort.Ints(ordered)
		params := make([]any, len(ordered))
		for i, u := range ordered {
			params[i] = int64(u)
		}
		res, err := read(ctx, MemgraphQueryInput{Cypher: structuralHeadBeltsCypher, Params: map[string]any{"surface": in.Surface, "units": params}, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
		if err != nil {
			return nil, fmt.Errorf("stretch head belts: %w", err)
		}
		note(res)
		for _, row := range res.Rows {
			if len(row) < 1 {
				continue
			}
			n, err := traceNodeFromAny(row[0])
			if err != nil {
				return nil, fmt.Errorf("stretch head belts: %w", err)
			}
			if n.UnitNumber != nil {
				heads[*n.UnitNumber] = n
			}
		}
	}
	for _, s := range noInput {
		msg := fmt.Sprintf("stretch %s (%d tiles, ends in a %s) has no inflow: no belt outside it feeds it, nothing drops onto it and no stretch flows into it",
			stretchTag(s.ID), s.Tiles, strings.ToLower(string(s.EndKind)))
		if s.carries() {
			msg += fmt.Sprintf("; it carries %d items that nothing replenishes (%s)", s.LeftCount+s.RightCount, stretchLaneText(s))
		} else {
			msg += "; it carries nothing"
		}
		findings = append(findings, &MemgraphAuditFinding{Code: "STRETCH_NO_INPUT", Severity: MemgraphAuditSeverityConcern, Node: structuralStretchNode(s, heads), Message: msg})
	}
	for _, s := range mixed {
		findings = append(findings, &MemgraphAuditFinding{Code: "MIXED_LANE", Severity: MemgraphAuditSeverityConcern, Node: structuralStretchNode(s, heads),
			Message: fmt.Sprintf("stretch %s has two or more different items on one lane (%s)", stretchTag(s.ID), stretchLaneText(s))})
	}

	// Inserters with a missing edge.
	for _, q := range []struct{ cypher, what, code, msg string }{
		{structuralNoSourceCypher, "inserters without source", "INSERTER_NO_SOURCE", "has no pickup source: nothing is at the tile it picks from"},
		{structuralNoDestinationCypher, "inserters without destination", "INSERTER_NO_DESTINATION", "has no drop destination: nothing is at the tile it drops onto"},
	} {
		nodes, _, res, err := structuralNodes(ctx, read, q.cypher, q.what, in.Surface)
		if err != nil {
			return nil, err
		}
		note(res)
		for _, n := range nodes {
			findings = append(findings, &MemgraphAuditFinding{Code: q.code, Severity: MemgraphAuditSeverityConcern, Node: n, Message: "inserter " + q.msg})
		}
	}

	// Crafting machines without a recipe.
	nodes, _, res, err := structuralNodes(ctx, read, structuralNoRecipeCypher, "machines without recipe", in.Surface)
	if err != nil {
		return nil, err
	}
	note(res)
	for _, n := range nodes {
		msg := "crafting machine has no recipe set, so it can never produce anything"
		if n.Status != nil {
			msg += " (status " + *n.Status + ")"
		}
		findings = append(findings, &MemgraphAuditFinding{Code: "MACHINE_NO_RECIPE", Severity: MemgraphAuditSeverityConcern, Node: n, Message: msg})
	}

	// Pending ghosts, one summary finding.
	ghosts, ghostProps, res, err := structuralNodes(ctx, read, structuralGhostsCypher, "ghosts", in.Surface)
	if err != nil {
		return nil, err
	}
	note(res)
	if len(ghosts) > 0 {
		counts := map[string]int{}
		for _, p := range ghostProps {
			counts[structuralGhostName(p)]++
		}
		top := stretchItemCounts(counts)
		listed := make([]string, 0, structuralListedGhosts)
		for i, c := range top {
			if i == structuralListedGhosts {
				listed = append(listed, fmt.Sprintf("and %d more", len(top)-structuralListedGhosts))
				break
			}
			listed = append(listed, fmt.Sprintf("%s x%d", c.Name, c.Count))
		}
		findings = append(findings, &MemgraphAuditFinding{Code: "GHOSTS_PENDING", Severity: MemgraphAuditSeverityInfo, Node: ghosts[0],
			Message: fmt.Sprintf("%d construction ghosts are waiting to be built: %s", len(ghosts), strings.Join(listed, ", "))})
	}
	return finishAudit(findings, in.Limit, truncated, snap), nil
}
