package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	areaGraphMaxSide      = 1024
	areaGraphMaxNodeLimit = 10000
	areaGraphBoundaryRows = 5000
	areaGraphMachineList  = 8
)

// areaPowerEdges are left out by default: poles connect almost everything and
// hide the item flow.
var areaPowerEdges = []any{"POWERED_BY", "POWER_WIRE"}

const areaNodesCypher = `MATCH (a:Entity)
WHERE a.surface = $surface AND a.x >= $x1 AND a.x < $x2 AND a.y >= $y1 AND a.y < $y2
RETURN a ORDER BY a.unit, a.id`

const areaEdgesCypher = `MATCH (a:Entity)-[r]->(b:Entity)
WHERE a.id IN $ids AND b.id IN $ids AND ($power OR NOT type(r) IN $powerTypes)
RETURN a.id AS from, type(r) AS type, b.id AS to`

const areaBoundaryCypher = `MATCH (a:Entity)-[r]-(b:Entity)
WHERE a.id IN $ids AND NOT b.id IN $ids AND ($power OR NOT type(r) IN $powerTypes)
RETURN a.id AS inside, startNode(r).id = a.id AS outgoing, type(r) AS type, b AS outside`

func validateAreaGraphInput(in MemgraphAreaGraphInput) error {
	if !memgraphSurfacePattern.MatchString(in.Surface) {
		return fmt.Errorf("surface %q must be 1-64 letters, digits, '.', '_' or '-'", in.Surface)
	}
	if in.Area == nil {
		return fmt.Errorf("area is required")
	}
	a := in.Area
	for _, v := range []float64{a.LeftTopX, a.LeftTopY, a.RightBottomX, a.RightBottomY} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("area bounds must be finite")
		}
	}
	w, h := a.RightBottomX-a.LeftTopX, a.RightBottomY-a.LeftTopY
	if w <= 0 || h <= 0 {
		return fmt.Errorf("area must have positive size: right/bottom must exceed left/top")
	}
	if w > areaGraphMaxSide || h > areaGraphMaxSide {
		return fmt.Errorf("area sides are at most %d tiles, got %.0f x %.0f", areaGraphMaxSide, w, h)
	}
	if in.MaxNodes < 1 || in.MaxNodes > areaGraphMaxNodeLimit {
		return fmt.Errorf("maxNodes must be between 1 and %d", areaGraphMaxNodeLimit)
	}
	return nil
}

// areaGraph reads the entities inside a box, the edges among them, the
// connected components they form and the edges crossing the box edge. Three
// reads: nodes by position, edges among the returned ids, boundary edges.
func areaGraph(ctx context.Context, read memgraphReader, in MemgraphAreaGraphInput) (*MemgraphAreaGraph, error) {
	if err := validateAreaGraphInput(in); err != nil {
		return nil, err
	}
	a := in.Area
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: areaNodesCypher, Limit: in.MaxNodes + 1, TimeoutMs: supplyChainTimeoutMs,
		Params: map[string]any{"surface": in.Surface, "x1": a.LeftTopX, "x2": a.RightBottomX, "y1": a.LeftTopY, "y2": a.RightBottomY},
	})
	if err != nil {
		return nil, fmt.Errorf("area nodes: %w", err)
	}
	out := &MemgraphAreaGraph{Nodes: []*MemgraphTraceNode{}, Edges: []*MemgraphAreaEdge{}, Boundary: []*MemgraphAreaBoundaryEdge{}, Snapshot: res.Snapshot}
	rows := res.Rows
	if len(rows) > in.MaxNodes {
		rows, out.Truncated = rows[:in.MaxNodes], true
	}
	ids := make([]any, 0, len(rows))
	index := map[string]int{}
	for _, row := range rows {
		if len(row) < 1 {
			return nil, fmt.Errorf("area node row is empty")
		}
		n, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		if _, dup := index[n.ID]; dup {
			continue
		}
		index[n.ID] = len(out.Nodes)
		out.Nodes = append(out.Nodes, n)
		ids = append(ids, n.ID)
	}
	if len(ids) > 0 {
		params := map[string]any{"ids": ids, "power": in.IncludePower, "powerTypes": areaPowerEdges}
		eres, err := read(ctx, MemgraphQueryInput{Cypher: areaEdgesCypher, Params: params, Limit: memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs})
		if err != nil {
			return nil, fmt.Errorf("area edges: %w", err)
		}
		if eres.Truncated {
			out.Truncated = true
		}
		for _, row := range eres.Rows {
			if len(row) < 3 {
				return nil, fmt.Errorf("area edge row has %d columns, want 3", len(row))
			}
			from, _ := row[0].(string)
			typ, _ := row[1].(string)
			to, _ := row[2].(string)
			out.Edges = append(out.Edges, &MemgraphAreaEdge{From: from, To: to, Type: typ})
		}
		if in.IncludeBoundary {
			bres, err := read(ctx, MemgraphQueryInput{Cypher: areaBoundaryCypher, Params: params, Limit: areaGraphBoundaryRows, TimeoutMs: supplyChainTimeoutMs})
			if err != nil {
				return nil, fmt.Errorf("area boundary: %w", err)
			}
			if bres.Truncated {
				out.Truncated = true
			}
			for _, row := range bres.Rows {
				if len(row) < 4 {
					return nil, fmt.Errorf("area boundary row has %d columns, want 4", len(row))
				}
				outside, err := traceNodeFromAny(row[3])
				if err != nil {
					return nil, err
				}
				inside, _ := row[0].(string)
				typ, _ := row[2].(string)
				dir := MemgraphBoundaryDirectionIn
				if outgoing, _ := row[1].(bool); outgoing {
					dir = MemgraphBoundaryDirectionOut
				}
				out.Boundary = append(out.Boundary, &MemgraphAreaBoundaryEdge{Inside: inside, Direction: dir, Type: typ, Outside: outside})
			}
		}
	}
	out.Components = areaComponents(out.Nodes, out.Edges, index)
	kinds := map[string]int{}
	for _, n := range out.Nodes {
		kinds[n.Kind]++
	}
	out.Summary = &MemgraphAreaSummary{
		NodeCount: len(out.Nodes), EdgeCount: len(out.Edges), ComponentCount: len(out.Components),
		BoundaryCount: len(out.Boundary), KindCounts: memgraphCounts(kinds),
		Attention: traceAttention(out.Nodes, nil, "", false),
	}
	out.Outline = areaOutline(out)
	return out, nil
}

// areaComponents groups nodes joined by edges (undirected) with union-find and
// returns the groups largest first, ties by first member order.
func areaComponents(nodes []*MemgraphTraceNode, edges []*MemgraphAreaEdge, index map[string]int) []*MemgraphAreaComponent {
	parent := make([]int, len(nodes))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for _, e := range edges {
		f, okF := index[e.From]
		t, okT := index[e.To]
		if !okF || !okT {
			continue
		}
		if rf, rt := find(f), find(t); rf != rt {
			if rf < rt {
				parent[rt] = rf
			} else {
				parent[rf] = rt
			}
		}
	}
	groups := map[int][]int{}
	var order []int
	for i := range nodes {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], i)
	}
	sort.SliceStable(order, func(i, j int) bool { return len(groups[order[i]]) > len(groups[order[j]]) })
	out := make([]*MemgraphAreaComponent, 0, len(order))
	for rank, r := range order {
		members := groups[r]
		kinds := map[string]int{}
		ids := make([]string, len(members))
		var notable []*MemgraphTraceNode
		for i, m := range members {
			n := nodes[m]
			ids[i] = n.ID
			kinds[n.Kind]++
			if n.Kind == "Machine" || n.Kind == "Chest" {
				notable = append(notable, n)
			}
		}
		c := &MemgraphAreaComponent{Index: rank + 1, Size: len(members), Kinds: memgraphCounts(kinds), NodeIds: ids}
		c.Description = areaComponentDescription(c, notable)
		out = append(out, c)
	}
	return out
}

func areaComponentDescription(c *MemgraphAreaComponent, notable []*MemgraphTraceNode) string {
	parts := make([]string, len(c.Kinds))
	for i, k := range c.Kinds {
		parts[i] = fmt.Sprintf("%d %s", k.Count, k.Name)
	}
	s := strings.Join(parts, ", ")
	if len(notable) == 0 {
		return s
	}
	shown := notable
	if len(shown) > areaGraphMachineList {
		shown = shown[:areaGraphMachineList]
	}
	labels := make([]string, len(shown))
	for i, n := range shown {
		labels[i] = traceLabel(n, true)
	}
	s += ": " + strings.Join(labels, "; ")
	if len(notable) > len(shown) {
		s += fmt.Sprintf("; +%d more", len(notable)-len(shown))
	}
	return s
}

func areaOutline(g *MemgraphAreaGraph) string {
	if len(g.Nodes) == 0 {
		return "no entities in the area"
	}
	var b strings.Builder
	for _, c := range g.Components {
		fmt.Fprintf(&b, "#%d (%d nodes) %s\n", c.Index, c.Size, c.Description)
	}
	if len(g.Boundary) > 0 {
		in, out := 0, 0
		for _, e := range g.Boundary {
			if e.Direction == MemgraphBoundaryDirectionIn {
				in++
			} else {
				out++
			}
		}
		fmt.Fprintf(&b, "boundary: %d edges in, %d out", in, out)
	}
	return strings.TrimRight(b.String(), "\n")
}
