package main

import (
	"context"
	"fmt"
	"strings"
)

const nominalRateMaxLimit = 500

// nominalRateCypher sums the nominal perMinute of every entity->Item
// PRODUCES/CONSUMES edge per item. Edges without perMinute (infinity chests)
// are counted as unbounded sources and never summed. Recipe nodes are not
// Entity nodes, so their per-craft amount edges are excluded.
const nominalRateCypher = `MATCH (e:Entity)-[r:PRODUCES|CONSUMES]->(i:Item)
WHERE e.surface = $surface AND ($item IS NULL OR i.name = $item)
WITH i.name AS name,
  sum(CASE WHEN type(r) = 'PRODUCES' AND r.perMinute IS NOT NULL THEN r.perMinute ELSE 0 END) AS produced,
  sum(CASE WHEN type(r) = 'CONSUMES' AND r.perMinute IS NOT NULL THEN r.perMinute ELSE 0 END) AS consumed,
  sum(CASE WHEN type(r) = 'PRODUCES' AND r.perMinute IS NOT NULL THEN 1 ELSE 0 END) AS producers,
  sum(CASE WHEN type(r) = 'CONSUMES' THEN 1 ELSE 0 END) AS consumers,
  sum(CASE WHEN type(r) = 'PRODUCES' AND r.perMinute IS NULL THEN 1 ELSE 0 END) AS unbounded
RETURN name, produced, consumed, producers, consumers, unbounded
ORDER BY produced - consumed ASC, name ASC
LIMIT $rows`

func validateNominalRateInput(in MemgraphNominalRateBalanceInput) error {
	if !memgraphSurfacePattern.MatchString(in.Surface) {
		return fmt.Errorf("surface %q is not a valid surface name", in.Surface)
	}
	if in.Limit < 1 || in.Limit > nominalRateMaxLimit {
		return fmt.Errorf("limit %d must be between 1 and %d", in.Limit, nominalRateMaxLimit)
	}
	return nil
}

func nominalRateNumber(v any) (float64, error) {
	if f := optionalFloat(v); f != nil {
		return *f, nil
	}
	switch x := v.(type) {
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	}
	return 0, fmt.Errorf("expected a number, got %T", v)
}

func nominalRateFmt(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}

// nominalRateDeficit explains why an item is short, or "" when it is not.
func nominalRateDeficit(r *MemgraphItemRate) string {
	switch {
	case r.Consumers > 0 && r.Producers == 0 && r.UnboundedSources == 0:
		return fmt.Sprintf("%s: consumed by %d machine(s) at %s/min, no producer", r.Name, r.Consumers, nominalRateFmt(r.ConsumedPerMinute))
	case r.Net < 0 && r.UnboundedSources > 0:
		return fmt.Sprintf("%s: net %s/min (produces %s, consumes %s) before %d unbounded source(s) not counted", r.Name, nominalRateFmt(r.Net), nominalRateFmt(r.ProducedPerMinute), nominalRateFmt(r.ConsumedPerMinute), r.UnboundedSources)
	case r.Net < 0:
		return fmt.Sprintf("%s: net %s/min (produces %s by %d, consumes %s by %d)", r.Name, nominalRateFmt(r.Net), nominalRateFmt(r.ProducedPerMinute), r.Producers, nominalRateFmt(r.ConsumedPerMinute), r.Consumers)
	}
	return ""
}

// nominalRateBalance sums nominal production and consumption per item.
func nominalRateBalance(ctx context.Context, read memgraphReader, in MemgraphNominalRateBalanceInput) (*MemgraphNominalRateBalance, error) {
	if err := validateNominalRateInput(in); err != nil {
		return nil, err
	}
	var item any
	if in.Item != nil && *in.Item != "" {
		item = *in.Item
	}
	res, err := read(ctx, MemgraphQueryInput{
		Cypher:    nominalRateCypher,
		Params:    map[string]any{"surface": in.Surface, "item": item, "rows": int64(in.Limit + 1)},
		Limit:     in.Limit + 1,
		TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, err
	}
	out := &MemgraphNominalRateBalance{Items: []*MemgraphItemRate{}, Deficits: []string{}, Snapshot: res.Snapshot}
	rows := res.Rows
	if len(rows) > in.Limit {
		rows, out.Truncated = rows[:in.Limit], true
	}
	for _, row := range rows {
		if len(row) < 6 {
			return nil, fmt.Errorf("nominal rate row has %d columns, want 6", len(row))
		}
		name, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("nominal rate item name is %T, want string", row[0])
		}
		var nums [5]float64
		for i := range nums {
			if nums[i], err = nominalRateNumber(row[i+1]); err != nil {
				return nil, fmt.Errorf("nominal rate %s column %d: %w", name, i+1, err)
			}
		}
		r := &MemgraphItemRate{Name: name, ProducedPerMinute: nums[0], ConsumedPerMinute: nums[1],
			Net: nums[0] - nums[1], Producers: int(nums[2]), Consumers: int(nums[3]), UnboundedSources: int(nums[4])}
		out.Items = append(out.Items, r)
		if d := nominalRateDeficit(r); d != "" {
			out.Deficits = append(out.Deficits, d)
		}
	}
	return out, nil
}
