package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// memgraph.fluidChain: the fluid network around one entity, along FLUID_LINK.
//
// The walk is undirected and goes through machines too (the export links a
// machine's fluid port to the adjacent pipe), so a refinery or chemical plant
// joins the networks on its ports into one result. Fluid contents are not in
// the graph and are not read here.
//
// Roles, from names and recipes:
//   - source: offshore-pump, pump, or a Machine whose recipe PRODUCES an Item
//     of kind fluid.
//   - sink: boiler, or a Machine whose recipe CONSUMES an Item of kind fluid.
//
// Findings: DEAD_END_PIPE (a pipe with exactly one distinct neighbour),
// ISOLATED_FLUID_ENTITY (the root has no FLUID_LINK) and NO_SOURCE (sinks but
// no source; not reported when the walk was cut by maxNodes, because the
// source may lie past the cut).

const fluidChainMaxNodes = 2000

// fluidChainRootCypher loads the root with its link count.
const fluidChainRootCypher = `MATCH (r {unit: $unit, surface: $surface})
OPTIONAL MATCH (r)-[l:FLUID_LINK]-()
RETURN r AS node, count(l) AS links LIMIT 1`

// fluidChainWalkCypher is one breadth-first walk; Memgraph visits each node
// once at its shortest distance, so loops in a pipe grid cost nothing extra.
const fluidChainWalkCypher = `MATCH p = (r {unit: $unit, surface: $surface})-[:FLUID_LINK *BFS 1..2000]-(n)
RETURN n AS node, size(relationships(p)) AS dist
ORDER BY dist, n.id`

// fluidChainLinksCypher lists the neighbours of the walked nodes, including
// the ones past a cut (they count toward a pipe's degree).
const fluidChainLinksCypher = `MATCH (a)-[:FLUID_LINK]-(b) WHERE a.id IN $ids
RETURN DISTINCT a.id AS a, b.id AS b`

// fluidChainFluidsCypher lists the fluid items of the recipes of machines.
const fluidChainFluidsCypher = `MATCH (m:Machine)-[:RUNS]->(:Recipe)-[c:CONSUMES|PRODUCES]->(it:Item)
WHERE m.id IN $ids AND it.kind = 'fluid'
RETURN DISTINCT m.id AS id, type(c) AS rel, it.name AS item`

func validateFluidChainInput(in MemgraphFluidChainInput) error {
	if err := validateTraceTarget(in.UnitNumber, in.Surface); err != nil {
		return err
	}
	if in.MaxNodes < 1 || in.MaxNodes > fluidChainMaxNodes {
		return fmt.Errorf("maxNodes must be between 1 and %d", fluidChainMaxNodes)
	}
	return nil
}

func fluidChain(ctx context.Context, read memgraphReader, in MemgraphFluidChainInput) (*MemgraphFluidChain, error) {
	if err := validateFluidChainInput(in); err != nil {
		return nil, err
	}
	unit := validUnit(in.UnitNumber)
	params := traceParams(unit, in.Surface)

	rootRes, err := read(ctx, MemgraphQueryInput{Cypher: fluidChainRootCypher, Params: params, Limit: 1, TimeoutMs: supplyChainTimeoutMs})
	if err != nil {
		return nil, err
	}
	if len(rootRes.Rows) == 0 || len(rootRes.Rows[0]) < 2 {
		return nil, fmt.Errorf("no node with unit %d on surface %q in the loaded graph (it may not exist, or the snapshot predates it)", unit, in.Surface)
	}
	rawRoot, ok := rootRes.Rows[0][0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a node, got %T", rootRes.Rows[0][0])
	}
	root, err := traceNodeFromAny(rawRoot)
	if err != nil {
		return nil, err
	}
	rootLinks := fluidChainInt(rootRes.Rows[0][1])
	labels := fluidChainLabels(rawRoot)
	switch {
	case labels["FluidEntity"]:
	case labels["Machine"] && rootLinks > 0:
	case labels["Machine"]:
		return nil, fmt.Errorf("unit %d on surface %q is a machine with no fluid link; fluidChain needs a fluid entity (pipe, tank, pump, boiler, ...) or a machine wired to one", unit, in.Surface)
	default:
		return nil, fmt.Errorf("unit %d on surface %q is %s %s; fluidChain accepts a fluid entity or a machine with a fluid link", unit, in.Surface, indefiniteArticle(root.Kind), root.Kind)
	}

	out := &MemgraphFluidChain{
		Root: root, Nodes: []*MemgraphTraceNode{root}, Links: []*MemgraphFluidLink{},
		Sources: []*MemgraphTraceNode{}, Sinks: []*MemgraphTraceNode{}, Findings: []*MemgraphAuditFinding{},
		Snapshot: rootRes.Snapshot,
	}

	// The root takes one of the maxNodes places; the reader flags truncation
	// when a row exists past the limit.
	if room := in.MaxNodes - 1; room == 0 {
		out.Truncated = rootLinks > 0
	} else if rootLinks > 0 {
		walk, err := read(ctx, MemgraphQueryInput{Cypher: fluidChainWalkCypher, Params: params, Limit: room, TimeoutMs: supplyChainTimeoutMs})
		if err != nil {
			return nil, fmt.Errorf("walk fluid links: %w", err)
		}
		out.Truncated = walk.Truncated || len(walk.Rows) > room
		for i, row := range walk.Rows {
			if i >= room {
				break
			}
			if len(row) < 1 {
				return nil, fmt.Errorf("walk row has no node")
			}
			n, err := traceNodeFromAny(row[0])
			if err != nil {
				return nil, err
			}
			out.Nodes = append(out.Nodes, n)
		}
	}

	ids := make([]any, 0, len(out.Nodes))
	byID := map[string]*MemgraphTraceNode{}
	for _, n := range out.Nodes {
		ids = append(ids, n.ID)
		byID[n.ID] = n
	}

	neighbours, links, err := fluidChainLinks(ctx, read, ids, byID)
	if err != nil {
		return nil, err
	}
	out.Links = links

	produces, consumes, err := fluidChainRecipeFluids(ctx, read, out.Nodes, ids)
	if err != nil {
		return nil, err
	}
	for _, n := range out.Nodes {
		name := ""
		if n.Name != nil {
			name = *n.Name
		}
		if name == "offshore-pump" || name == "pump" || produces[n.ID] {
			out.Sources = append(out.Sources, n)
		}
		if name == "boiler" || consumes[n.ID] {
			out.Sinks = append(out.Sinks, n)
		}
	}

	findings := fluidChainFindings(out, neighbours)
	limit := len(findings)
	if limit < 1 {
		limit = 1
	}
	out.Findings = finishAudit(findings, limit, false, out.Snapshot).Findings
	return out, nil
}

// fluidChainLinks reads the neighbours of the walked nodes. neighbours maps a
// node id to its distinct neighbour ids (including ones outside the walk);
// links keeps each pair with both ends in the walk once, ordered by walk
// position.
func fluidChainLinks(ctx context.Context, read memgraphReader, ids []any, byID map[string]*MemgraphTraceNode) (map[string]map[string]bool, []*MemgraphFluidLink, error) {
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: fluidChainLinksCypher, Params: map[string]any{"ids": ids}, Limit: memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read fluid links: %w", err)
	}
	if res.Truncated {
		return nil, nil, fmt.Errorf("fluid link read hit the %d row cap; lower maxNodes", memgraphMaxRows)
	}
	order := make(map[string]int, len(ids))
	for i, id := range ids {
		order[id.(string)] = i
	}
	neighbours := map[string]map[string]bool{}
	type pair struct{ a, b string }
	seen := map[pair]bool{}
	var pairs []pair
	for _, row := range res.Rows {
		if len(row) < 2 {
			return nil, nil, fmt.Errorf("link row has %d columns, want 2", len(row))
		}
		a, _ := row[0].(string)
		b, _ := row[1].(string)
		if a == "" || b == "" || a == b {
			continue
		}
		if neighbours[a] == nil {
			neighbours[a] = map[string]bool{}
		}
		neighbours[a][b] = true
		if byID[a] == nil || byID[b] == nil {
			continue
		}
		p := pair{a, b}
		if order[b] < order[a] {
			p = pair{b, a}
		}
		if !seen[p] {
			seen[p] = true
			pairs = append(pairs, p)
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if oi, oj := order[pairs[i].a], order[pairs[j].a]; oi != oj {
			return oi < oj
		}
		return order[pairs[i].b] < order[pairs[j].b]
	})
	links := make([]*MemgraphFluidLink, 0, len(pairs))
	for _, p := range pairs {
		links = append(links, &MemgraphFluidLink{From: p.a, To: p.b})
	}
	return neighbours, links, nil
}

// fluidChainRecipeFluids returns the ids of walked machines whose recipe
// produces, and consumes, a fluid item.
func fluidChainRecipeFluids(ctx context.Context, read memgraphReader, nodes []*MemgraphTraceNode, ids []any) (produces, consumes map[string]bool, err error) {
	produces, consumes = map[string]bool{}, map[string]bool{}
	hasMachine := false
	for _, n := range nodes {
		if n.Kind == "Machine" {
			hasMachine = true
			break
		}
	}
	if !hasMachine {
		return produces, consumes, nil
	}
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: fluidChainFluidsCypher, Params: map[string]any{"ids": ids}, Limit: memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read recipe fluids: %w", err)
	}
	for _, row := range res.Rows {
		if len(row) < 2 {
			return nil, nil, fmt.Errorf("recipe fluid row has %d columns, want at least 2", len(row))
		}
		id, _ := row[0].(string)
		switch rel, _ := row[1].(string); rel {
		case "PRODUCES":
			produces[id] = true
		case "CONSUMES":
			consumes[id] = true
		}
	}
	return produces, consumes, nil
}

// fluidChainFindings applies the three rules to the walked network.
func fluidChainFindings(chain *MemgraphFluidChain, neighbours map[string]map[string]bool) []*MemgraphAuditFinding {
	var findings []*MemgraphAuditFinding
	if len(chain.Nodes) == 1 && len(neighbours[chain.Root.ID]) == 0 {
		findings = append(findings, &MemgraphAuditFinding{
			Code: "ISOLATED_FLUID_ENTITY", Severity: MemgraphAuditSeverityConcern, Node: chain.Root, Related: []*MemgraphTraceNode{},
			Message: fmt.Sprintf("%s has no FLUID_LINK: no pipe or machine port touches it", traceLabel(chain.Root, false)),
		})
	}
	for _, n := range chain.Nodes {
		if n.Name == nil || *n.Name != "pipe" || len(neighbours[n.ID]) != 1 {
			continue
		}
		findings = append(findings, &MemgraphAuditFinding{
			Code: "DEAD_END_PIPE", Severity: MemgraphAuditSeverityInfo, Node: n, Related: []*MemgraphTraceNode{},
			Message: fmt.Sprintf("%s has exactly one link: a dead end (fine at a machine port, a leftover stub otherwise)", traceLabel(n, false)),
		})
	}
	if len(chain.Sinks) > 0 && len(chain.Sources) == 0 && !chain.Truncated {
		names := make([]string, 0, len(chain.Sinks))
		for _, s := range chain.Sinks {
			names = append(names, traceLabel(s, false))
		}
		findings = append(findings, &MemgraphAuditFinding{
			Code: "NO_SOURCE", Severity: MemgraphAuditSeverityBlocker, Node: chain.Root, Related: chain.Sinks,
			Message: fmt.Sprintf("%d fluid consumers (%s) and no source in the network: no offshore pump, pump or machine producing a fluid", len(chain.Sinks), strings.Join(names, ", ")),
		})
	}
	return findings
}

func fluidChainLabels(node map[string]any) map[string]bool {
	out := map[string]bool{}
	switch l := node["labels"].(type) {
	case []any:
		for _, v := range l {
			if s, ok := v.(string); ok {
				out[s] = true
			}
		}
	case []string:
		for _, s := range l {
			out[s] = true
		}
	}
	return out
}

func fluidChainInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}
