package main

import (
	"context"
	"fmt"
	"strings"
)

const (
	routeBetweenMaxHopsLimit   = supplyChainMaxHopsLimit
	routeBetweenMaxRoutesLimit = 100
)

func validateRouteBetweenInput(in MemgraphRouteBetweenInput) error {
	if err := validateTraceTarget(in.From, in.Surface); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if err := validateTraceTarget(in.To, in.Surface); err != nil {
		return fmt.Errorf("to: %w", err)
	}
	if validUnit(in.From) == validUnit(in.To) {
		return fmt.Errorf("from and to are the same unit %s", in.From)
	}
	if in.MaxHops < 1 || in.MaxHops > routeBetweenMaxHopsLimit {
		return fmt.Errorf("maxHops must be between 1 and %d", routeBetweenMaxHopsLimit)
	}
	if in.MaxRoutes < 1 || in.MaxRoutes > routeBetweenMaxRoutesLimit {
		return fmt.Errorf("maxRoutes must be between 1 and %d", routeBetweenMaxRoutesLimit)
	}
	return nil
}

// routeBetweenHopBlock is downstreamHopBlock that stops expanding at the
// target ($toId), so every chain that reaches it ends there.
var routeBetweenHopBlock = strings.Replace(downstreamHopBlock,
	"CASE WHEN cur IS NULL THEN []\n", "CASE WHEN cur IS NULL OR cur.id = $toId THEN []\n", 1)

// routeBetweenCypher unrolls maxHops downstream hop blocks from `from` and
// keeps only the chains whose last node is the target.
func routeBetweenCypher(maxHops int) string {
	var b strings.Builder
	b.WriteString("MATCH (r {unit: $unit, surface: $surface}) WHERE r:Machine OR r:Chest OR r:Belt\n")
	b.WriteString("WITH r AS cur, [r.id] AS chain, [] AS paths\n")
	for range maxHops {
		b.WriteString(routeBetweenHopBlock)
	}
	b.WriteString("WITH paths WHERE size(paths) > 0 AND last(nodes(last(paths))).id = $toId\n")
	b.WriteString("WITH paths, reduce(s = [], x IN reduce(f = [], p IN paths | f + nodes(p)) |\n" +
		"  CASE WHEN size(s) > 0 AND last(s).id = x.id THEN s ELSE s + [x] END) AS flat\n")
	b.WriteString("RETURN DISTINCT size(paths) AS hops, flat AS steps\nORDER BY hops, size(flat)")
	return b.String()
}

// routeBetween finds the downstream routes from one node that reach another.
// No route is a normal result (connected false), not an error.
func routeBetween(ctx context.Context, read memgraphReader, in MemgraphRouteBetweenInput) (*MemgraphRouteBetween, error) {
	if err := validateRouteBetweenInput(in); err != nil {
		return nil, err
	}
	kinds := []string{"Machine", "Chest", "Belt"}
	from, err := traceRoot(ctx, read, validUnit(in.From), in.Surface, kinds...)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	to, err := traceRoot(ctx, read, validUnit(in.To), in.Surface, kinds...)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	params := traceParams(validUnit(in.From), in.Surface)
	params["toId"] = to.ID
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: routeBetweenCypher(in.MaxHops), Params: params,
		Limit: in.MaxRoutes, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, fmt.Errorf("route from unit %s to unit %s: %w", in.From, in.To, err)
	}
	paths, err := decodeSupplyPaths(res.Rows)
	if err != nil {
		return nil, err
	}
	out := &MemgraphRouteBetween{
		From: from, To: to, Connected: len(paths) > 0,
		Routes: []*MemgraphSupplyPath{}, Nodes: []*MemgraphTraceNode{},
		Truncated: res.Truncated, Snapshot: res.Snapshot,
	}
	if len(paths) == 0 {
		out.Outline = fmt.Sprintf("no route from %s to %s within %d hops", traceLabel(from, false), traceLabel(to, false), in.MaxHops)
		return out, nil
	}
	c := assembleChain(from, paths, true)
	out.Routes, out.Nodes, out.Outline = c.Paths, c.Nodes, c.Outline
	return out, nil
}
