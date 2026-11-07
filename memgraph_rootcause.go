package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// rootCauseStallStatuses are root statuses that mean the machine is not
// producing. Unlike supplyTroubleStatuses, waiting and full states count: the
// question "why is it stalled" applies to them too.
var rootCauseStallStatuses = map[string]bool{
	"item_ingredient_shortage": true, "waiting_for_source_items": true, "waiting_for_space_in_destination": true,
	"full_output": true, "no_power": true, "low_power": true, "no_fuel": true,
	"no_minable_resources": true, "no_ingredients": true,
}

// rootCauseSummaryCauses is how many causes the summary sentence names.
const rootCauseSummaryCauses = 2

// rootCause explains why one machine is stalled: the supply chain paths are
// reduced to the few nodes that actually explain it. item_ingredient_shortage
// on a supplier is a symptom of something further upstream and is never a
// cause on its own.
func rootCause(ctx context.Context, read memgraphReader, in MemgraphRootCauseInput) (*MemgraphRootCause, error) {
	if err := validateTraceTarget(in.UnitNumber, in.Surface); err != nil {
		return nil, err
	}
	if in.MaxHops < 1 || in.MaxHops > supplyChainMaxHopsLimit {
		return nil, fmt.Errorf("maxHops must be between 1 and %d", supplyChainMaxHopsLimit)
	}
	unit := validUnit(in.UnitNumber)
	if _, err := traceRoot(ctx, read, unit, in.Surface, "Machine"); err != nil {
		return nil, err
	}
	chain, err := supplyChain(ctx, read, MemgraphSupplyChainInput{
		UnitNumber: in.UnitNumber, Surface: in.Surface, Resolution: MemgraphSupplyResolutionBelt,
		MaxHops: in.MaxHops, MaxPaths: supplyChainMaxPathLimit,
	})
	if err != nil {
		return nil, err
	}

	units := []any{int64(unit)}
	var upstream []int
	for _, n := range chain.Nodes {
		if n.Kind == "Machine" && n.UnitNumber != nil && n.ID != chain.Root.ID {
			upstream = append(upstream, unitOf(n))
		}
	}
	sort.Ints(upstream)
	for _, u := range upstream {
		units = append(units, int64(u))
	}
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: ingredientCoverageCypher,
		Params: map[string]any{"surface": in.Surface, "units": units},
		Limit:  memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, fmt.Errorf("root cause of unit %s: ingredient coverage: %w", in.UnitNumber, err)
	}
	coverage, err := assembleCoverage(chain.Root, res.Rows)
	if err != nil {
		return nil, err
	}

	causes := rootCauseFind(chain, coverage)
	out := &MemgraphRootCause{
		Root: chain.Root, Causes: causes, Truncated: chain.Truncated, Snapshot: chain.Snapshot,
	}
	if out.Snapshot == nil {
		out.Snapshot = res.Snapshot
	}
	// Stalled follows the root's own exported status. Causes found upstream
	// of a working machine are findings (a spare dead input), not a stall.
	out.Stalled = chain.Root.Status != nil && rootCauseStallStatuses[*chain.Root.Status]
	out.Summary = rootCauseSummary(chain.Root, out.Stalled, causes, chain.Truncated)
	return out, nil
}

// rootCauseFind classifies the chain into causes, deduped, most upstream first.
func rootCauseFind(chain *MemgraphSupplyChain, coverage *MemgraphIngredientCoverage) []*MemgraphCause {
	byID := make(map[string]*MemgraphTraceNode, len(chain.Nodes))
	for _, n := range chain.Nodes {
		byID[n.ID] = n
	}
	causes := []*MemgraphCause{}
	seen := map[string]bool{}
	add := func(node *MemgraphTraceNode, reason MemgraphCauseReason, detail string) {
		key := string(reason) + "|" + node.ID + "|" + detail
		if seen[key] {
			return
		}
		route := rootCauseRoute(chain, node.ID, reason == MemgraphCauseReasonOriginBelt)
		if route == nil {
			route = []string{chain.Root.ID}
			if node.ID != chain.Root.ID {
				route = append(route, node.ID)
			}
		}
		seen[key] = true
		causes = append(causes, &MemgraphCause{
			Node: node, Reason: reason, Detail: detail, Route: route, RouteText: rootCauseRouteText(route, byID),
		})
	}

	for _, n := range chain.Origins {
		if n.Kind == "Belt" {
			add(n, MemgraphCauseReasonOriginBelt, "no belt, inserter or machine feeds this belt line")
		}
	}
	for _, mc := range coverage.Machines {
		node := byID[mc.Machine.ID]
		if node == nil {
			node = mc.Machine
		}
		for _, ing := range mc.Ingredients {
			if !ing.Covered {
				add(node, MemgraphCauseReasonNoFeedingArm, ing.Item+" has no feeding arm")
			}
		}
	}
	for _, n := range chain.Nodes {
		if (n.Kind == "Machine" || n.Kind == "Chest") && n.Status != nil &&
			supplyTroubleStatuses[*n.Status] && *n.Status != "item_ingredient_shortage" {
			add(n, MemgraphCauseReasonMachineTrouble, "status "+*n.Status)
		}
	}

	sort.SliceStable(causes, func(i, j int) bool {
		a, b := causes[i], causes[j]
		if len(a.Route) != len(b.Route) {
			return len(a.Route) > len(b.Route)
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		if ua, ub := unitOf(a.Node), unitOf(b.Node); ua != ub {
			return ua < ub
		}
		return a.Detail < b.Detail
	})
	return causes
}

// rootCauseRoute is the shortest chain path to id, cut at id, or the whole
// path when the node must be the path end (origins). nil when no path
// reaches it (the root itself yields [root]).
func rootCauseRoute(chain *MemgraphSupplyChain, id string, endOnly bool) []string {
	if id == chain.Root.ID {
		return []string{id}
	}
	var best []string
	for _, p := range chain.Paths {
		for i, s := range p.Steps {
			if s != id || endOnly && i != len(p.Steps)-1 {
				continue
			}
			if best == nil || i+1 < len(best) {
				best = p.Steps[:i+1]
			}
			break
		}
	}
	if best == nil {
		return nil
	}
	return append([]string(nil), best...)
}

// rootCauseRouteText renders a route as `370 <- arm 390 <- belt 391`; runs of
// belts collapse to `belts FIRST..LAST (count)`.
func rootCauseRouteText(route []string, byID map[string]*MemgraphTraceNode) string {
	var parts []string
	for i := 0; i < len(route); {
		n := byID[route[i]]
		if n == nil {
			parts = append(parts, route[i])
			i++
			continue
		}
		if n.Kind != "Belt" {
			parts = append(parts, rootCauseLabel(n))
			i++
			continue
		}
		j := i
		for j+1 < len(route) && byID[route[j+1]] != nil && byID[route[j+1]].Kind == "Belt" {
			j++
		}
		if j == i {
			parts = append(parts, rootCauseLabel(n))
		} else {
			parts = append(parts, fmt.Sprintf("belts %s..%s (%d)", beltUnit(n), beltUnit(byID[route[j]]), j-i+1))
		}
		i = j + 1
	}
	return strings.Join(parts, " <- ")
}

func rootCauseLabel(n *MemgraphTraceNode) string {
	switch n.Kind {
	case "Inserter":
		return "arm " + beltUnit(n)
	case "Belt":
		return "belt " + beltUnit(n)
	case "BeltStretch":
		return "stretch " + stretchTag(n.ID)
	}
	return beltUnit(n)
}

func rootCauseSummary(root *MemgraphTraceNode, stalled bool, causes []*MemgraphCause, truncated bool) string {
	subject := "Machine " + traceLabel(root, false)
	status := ""
	if root.Status != nil {
		status = " [" + *root.Status + "]"
	}
	var b strings.Builder
	switch {
	case len(causes) == 0 && !stalled:
		fmt.Fprintf(&b, "%s%s is not stalled and no upstream fault was found.", subject, status)
	case len(causes) == 0:
		fmt.Fprintf(&b, "%s%s is stalled but no upstream cause was found in the topology (it may be blocked on its output).", subject, status)
	default:
		lead := "has %d cause(s), most upstream first"
		if !stalled {
			lead = "is not stalled but has %d upstream finding(s), most upstream first (they may be spare or non-blocking inputs)"
		}
		n := len(causes)
		if n > rootCauseSummaryCauses {
			n = rootCauseSummaryCauses
		}
		parts := make([]string, n)
		for i, c := range causes[:n] {
			parts[i] = fmt.Sprintf("%s %s (%s) via %s", strings.ToLower(strings.ReplaceAll(string(c.Reason), "_", " ")),
				rootCauseLabel(c.Node), c.Detail, c.RouteText)
		}
		more := ""
		if len(causes) > n {
			more = "; " + strconv.Itoa(len(causes)-n) + " more in causes"
		}
		fmt.Fprintf(&b, "%s%s "+lead+": %s%s.", subject, status, len(causes), strings.Join(parts, "; "), more)
	}
	if truncated {
		b.WriteString(" The supply chain was truncated, so causes may be incomplete.")
	}
	return b.String()
}
