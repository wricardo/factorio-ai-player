package main

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const itemSourcesMaxLimit = 200

var itemSourcesItemPattern = regexp.MustCompile(`^[a-z0-9_-]{1,80}$`)

// Machines and chests with an entity-level PRODUCES edge to the item. Recipe
// nodes also PRODUCES items, so only Entity nodes qualify.
const itemSourcesProducesCypher = `MATCH (m:Entity)-[r:PRODUCES]->(:Item {name: $item})
WHERE m.surface = $surface AND (m:Machine OR m:Chest)
RETURN m AS node, r.perMinute AS perMinute, r.via AS via
ORDER BY m.unit`

const itemSourcesHoldsCypher = `MATCH (c:Chest)-[h:CONTAINS]->(:Item {name: $item})
WHERE c.surface = $surface
RETURN c AS node, h.count AS count
ORDER BY c.unit`

// itemsLeft/itemsRight elements are "item:count".
const itemSourcesBeltCypher = `MATCH (s:BeltStretch)
WHERE s.surface = $surface
  AND (any(e IN coalesce(s.itemsLeft, []) WHERE e STARTS WITH $prefix)
    OR any(e IN coalesce(s.itemsRight, []) WHERE e STARTS WITH $prefix))
RETURN s AS node, coalesce(s.itemsLeft, []) AS left, coalesce(s.itemsRight, []) AS right
ORDER BY s.headUnit`

func validateItemSourcesInput(in MemgraphItemSourcesInput) error {
	if !itemSourcesItemPattern.MatchString(in.Item) {
		return fmt.Errorf("item %q must be a lowercase item name matching %s", in.Item, itemSourcesItemPattern)
	}
	if !memgraphSurfacePattern.MatchString(in.Surface) {
		return fmt.Errorf("surface %q must match %s", in.Surface, memgraphSurfacePattern)
	}
	if in.Limit < 1 || in.Limit > itemSourcesMaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", itemSourcesMaxLimit)
	}
	if (in.NearX == nil) != (in.NearY == nil) {
		return fmt.Errorf("nearX and nearY must be given together")
	}
	for _, v := range []*float64{in.NearX, in.NearY} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
			return fmt.Errorf("nearX and nearY must be finite")
		}
	}
	return nil
}

// itemSourcesBeltCount totals the item across lane lists of "item:count".
func itemSourcesBeltCount(item string, lanes ...any) int {
	total := 0
	for _, lane := range lanes {
		list, _ := lane.([]any)
		for _, e := range list {
			s, ok := e.(string)
			if !ok {
				continue
			}
			i := strings.LastIndex(s, ":")
			if i < 0 || s[:i] != item {
				continue
			}
			if n, err := strconv.Atoi(s[i+1:]); err == nil {
				total += n
			}
		}
	}
	return total
}

func itemSourcesInt(v any) (int, bool) {
	switch x := v.(type) {
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

func itemSourcesRun(ctx context.Context, read memgraphReader, cypher string, params map[string]any, what string) (*MemgraphQueryResult, error) {
	res, err := read(ctx, MemgraphQueryInput{Cypher: cypher, Params: params, Limit: memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs})
	if err != nil {
		return nil, fmt.Errorf("item sources (%s): %w", what, err)
	}
	return res, nil
}

func itemSources(ctx context.Context, read memgraphReader, in MemgraphItemSourcesInput) (*MemgraphItemSources, error) {
	if err := validateItemSourcesInput(in); err != nil {
		return nil, err
	}
	params := map[string]any{"item": in.Item, "surface": in.Surface, "prefix": in.Item + ":"}
	out := &MemgraphItemSources{Item: in.Item, Sources: []*MemgraphItemSource{}}
	truncated := false
	seen := map[string]bool{} // chest ids already listed (infinity wins over CONTAINS)
	var all []*MemgraphItemSource

	res, err := itemSourcesRun(ctx, read, itemSourcesProducesCypher, params, "producers")
	if err != nil {
		return nil, err
	}
	truncated = truncated || res.Truncated
	out.Snapshot = res.Snapshot
	for _, row := range res.Rows {
		if len(row) < 3 {
			return nil, fmt.Errorf("item sources: producer row has %d columns, want 3", len(row))
		}
		node, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		via, _ := row[2].(string)
		src := &MemgraphItemSource{Node: node}
		switch {
		case node.Kind == "Chest" && via == "infinity":
			src.Kind = MemgraphItemSourceKindInfinityChest
			seen[node.ID] = true
		case node.Kind == "Machine":
			src.Kind = MemgraphItemSourceKindProducer
			src.PerMinute = optionalFloat(row[1])
		default:
			continue // a non-infinity chest PRODUCES edge is not a source; CONTAINS covers it
		}
		all = append(all, src)
	}

	res, err = itemSourcesRun(ctx, read, itemSourcesHoldsCypher, params, "chests")
	if err != nil {
		return nil, err
	}
	truncated = truncated || res.Truncated
	if out.Snapshot == nil {
		out.Snapshot = res.Snapshot
	}
	for _, row := range res.Rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("item sources: chest row has %d columns, want 2", len(row))
		}
		node, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		if seen[node.ID] {
			continue
		}
		seen[node.ID] = true
		src := &MemgraphItemSource{Kind: MemgraphItemSourceKindChest, Node: node}
		if n, ok := itemSourcesInt(row[1]); ok {
			src.Count = &n
		}
		all = append(all, src)
	}

	res, err = itemSourcesRun(ctx, read, itemSourcesBeltCypher, params, "belts")
	if err != nil {
		return nil, err
	}
	truncated = truncated || res.Truncated
	if out.Snapshot == nil {
		out.Snapshot = res.Snapshot
	}
	for _, row := range res.Rows {
		if len(row) < 3 {
			return nil, fmt.Errorf("item sources: belt row has %d columns, want 3", len(row))
		}
		node, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		n := itemSourcesBeltCount(in.Item, row[1], row[2])
		all = append(all, &MemgraphItemSource{Kind: MemgraphItemSourceKindBelt, Node: node, Count: &n})
	}

	if in.NearX != nil {
		for _, s := range all {
			if s.Node.X != nil && s.Node.Y != nil {
				d := math.Hypot(*s.Node.X-*in.NearX, *s.Node.Y-*in.NearY)
				s.Distance = &d
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return itemSourcesLess(all[i], all[j], in.NearX != nil) })

	if len(all) > in.Limit {
		all = all[:in.Limit]
		truncated = true
	}
	out.Sources = append(out.Sources, all...)
	out.Truncated = truncated
	return out, nil
}

func itemSourcesIsBelt(s *MemgraphItemSource) bool { return s.Kind == MemgraphItemSourceKindBelt }

// itemSourcesUnit is the unit number, or the head unit parsed from a stretch
// id (surface:stretch:head), which has no unit number of its own.
func itemSourcesUnit(s *MemgraphItemSource) int {
	if !itemSourcesIsBelt(s) {
		return unitOf(s.Node)
	}
	id := s.Node.ID
	n, _ := strconv.Atoi(id[strings.LastIndex(id, ":")+1:])
	return n
}

// itemSourcesLess orders by distance when near is set (unpositioned last),
// otherwise machines/chests by unit before belts by head unit. Ties: unit, id.
func itemSourcesLess(a, b *MemgraphItemSource, near bool) bool {
	if near {
		switch {
		case a.Distance != nil && b.Distance == nil:
			return true
		case a.Distance == nil && b.Distance != nil:
			return false
		case a.Distance != nil && *a.Distance != *b.Distance:
			return *a.Distance < *b.Distance
		}
	} else if itemSourcesIsBelt(a) != itemSourcesIsBelt(b) {
		return !itemSourcesIsBelt(a)
	}
	if ua, ub := itemSourcesUnit(a), itemSourcesUnit(b); ua != ub {
		return ua < ub
	}
	return a.Node.ID < b.Node.ID
}
