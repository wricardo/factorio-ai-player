package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// memgraph.removalImpact: a pre-teardown check. Which downstream machines lose
// an ingredient if one machine, chest or belt is removed?
//
// Route model (the rule every count below follows):
//
//   - A ROUTE of machine M is one way an item can reach M: the supply path from
//     M through one feeding inserter ("arm", always the second node), the belts
//     it picks from (if any), up to the FIRST machine or chest on that path, the
//     route's SUPPLIER. Anything past the supplier belongs to the supplier's own
//     routes, not to M's. A path that ends on a belt nothing feeds has no
//     supplier and is not a route. Identical prefixes are one route.
//   - A route supplies ingredient X of M when its supplier produces X: a
//     machine whose recipe PRODUCES X, or a chest that CONTAINS or PRODUCES X
//     (infinity chests). Nothing else is attributed: sampled belt contents are
//     ignored, and so are recipe ingredients the supplier merely consumes.
//   - A route PASSES THROUGH the removed node when the node is any step of the
//     route: the arm, a belt tile between pick and drop point, or the supplier.
//   - M is affected for X when X has at least one route before removal and every
//     one of them passes through a removed node (the removed node is then a cut
//     vertex of M's feed of X). routesBefore is that route count. An ingredient
//     that has no route even before removal is not caused by the removal and is
//     not listed.
//   - Cascade: a machine that is affected for any ingredient is treated as
//     stopped, so its own downstream consumers are re-checked with it removed as
//     well, until nothing new falls over. Chests are never cascaded (they keep
//     their stock). Fuel is not an ingredient here: only recipe CONSUMES edges.
//
// Topology only: stock, throughput and belt buffers are not modelled.

// removalImpactSupplyHops is the supplier hops walked from each candidate: one
// hop reaches a machine or chest the arm picks from directly, a second reaches
// the first supplier behind the belt the arm picks from.
const removalImpactSupplyHops = 2

// removalImpactSummaryListed caps the machines spelled out in the summary.
const removalImpactSummaryListed = 5

// removalImpactIngredientsCypher lists the recipe ingredients of machines.
const removalImpactIngredientsCypher = `MATCH (m:Machine)-[:RUNS]->(:Recipe)-[:CONSUMES]->(it:Item)
WHERE m.surface = $surface AND m.unit IN $units
RETURN m.unit AS unit, collect(DISTINCT it.name) AS items`

// removalImpactProducesCypher lists, per supplier machine or chest, the items it
// can put on a route.
const removalImpactProducesCypher = `MATCH (s) WHERE s.surface = $surface AND s.unit IN $units AND (s:Machine OR s:Chest)
OPTIONAL MATCH (s)-[:RUNS]->(:Recipe)-[:PRODUCES]->(ri:Item)
OPTIONAL MATCH (s:Chest)-[:CONTAINS|PRODUCES]->(ci:Item)
RETURN s.unit AS unit, collect(DISTINCT ri.name) + collect(DISTINCT ci.name) AS items`

type removalImpactRoute struct {
	Steps    []string // node ids from the machine to the supplier, inclusive
	Supplier *MemgraphTraceNode
}

type removalImpactMachine struct {
	Node        *MemgraphTraceNode
	Ingredients []string
	Routes      []removalImpactRoute
}

func validateRemovalImpactInput(in MemgraphRemovalImpactInput) error {
	if err := validateTraceTarget(in.UnitNumber, in.Surface); err != nil {
		return err
	}
	if in.MaxHops < 1 || in.MaxHops > supplyChainMaxHopsLimit {
		return fmt.Errorf("maxHops must be between 1 and %d", supplyChainMaxHopsLimit)
	}
	return nil
}

func removalImpact(ctx context.Context, read memgraphReader, in MemgraphRemovalImpactInput) (*MemgraphRemovalImpact, error) {
	if err := validateRemovalImpactInput(in); err != nil {
		return nil, err
	}
	root, err := traceRoot(ctx, read, validUnit(in.UnitNumber), in.Surface, "Machine", "Chest", "Belt")
	if err != nil {
		return nil, err
	}
	down, err := downstreamChain(ctx, read, MemgraphDownstreamChainInput{
		UnitNumber: in.UnitNumber, Surface: in.Surface, MaxHops: in.MaxHops, MaxPaths: supplyChainMaxPathLimit,
	})
	if err != nil {
		return nil, err
	}
	out := &MemgraphRemovalImpact{Root: root, Affected: []*MemgraphAffectedMachine{}, Truncated: down.Truncated, Snapshot: down.Snapshot}

	var candidates []*MemgraphTraceNode
	for _, n := range down.Nodes {
		if n.Kind == "Machine" && n.ID != root.ID && n.Recipe != nil && unitOf(n) > 0 {
			candidates = append(candidates, n)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return unitOf(candidates[i]) < unitOf(candidates[j]) })

	machines := make([]removalImpactMachine, 0, len(candidates))
	supplierUnits := map[int]string{} // supplier unit -> node id
	for _, m := range candidates {
		chain, err := supplyChain(ctx, read, MemgraphSupplyChainInput{
			UnitNumber: *m.UnitNumber, Surface: in.Surface, Resolution: MemgraphSupplyResolutionBelt,
			MaxHops: removalImpactSupplyHops, MaxPaths: supplyChainMaxPathLimit,
		})
		if err != nil {
			return nil, err
		}
		if chain.Truncated {
			out.Truncated = true
		}
		paths, err := removalImpactPaths(chain, m)
		if err != nil {
			return nil, err
		}
		routes := removalImpactRoutes(paths)
		for _, r := range routes {
			supplierUnits[unitOf(r.Supplier)] = r.Supplier.ID
		}
		machines = append(machines, removalImpactMachine{Node: m, Routes: routes})
	}

	ingredients, err := removalImpactItems(ctx, read, removalImpactIngredientsCypher, in.Surface, candidates)
	if err != nil {
		return nil, fmt.Errorf("ingredients of downstream machines: %w", err)
	}
	var suppliers []*MemgraphTraceNode
	for unit, id := range supplierUnits {
		u := strconv.Itoa(unit)
		suppliers = append(suppliers, &MemgraphTraceNode{ID: id, UnitNumber: &u})
	}
	producedByUnit, err := removalImpactItems(ctx, read, removalImpactProducesCypher, in.Surface, suppliers)
	if err != nil {
		return nil, fmt.Errorf("items produced by suppliers: %w", err)
	}
	produces := map[string]map[string]bool{}
	for unit, items := range producedByUnit {
		set := map[string]bool{}
		for _, it := range items {
			set[it] = true
		}
		produces[supplierUnits[unit]] = set
	}
	for i := range machines {
		machines[i].Ingredients = ingredients[unitOf(machines[i].Node)]
		sort.Strings(machines[i].Ingredients)
	}

	out.Affected = removalImpactCompute(root.ID, machines, produces)
	out.Safe = len(out.Affected) == 0
	out.Summary = removalImpactSummary(root, len(machines), out.Affected, out.Truncated)
	return out, nil
}

// removalImpactPaths decodes the steps of a supply chain back into node paths:
// the chain lists nodes once and paths as ids.
func removalImpactPaths(chain *MemgraphSupplyChain, m *MemgraphTraceNode) ([]tracePath, error) {
	byID := make(map[string]*MemgraphTraceNode, len(chain.Nodes)+1)
	byID[m.ID] = m
	for _, n := range chain.Nodes {
		byID[n.ID] = n
	}
	paths := make([]tracePath, 0, len(chain.Paths))
	for _, p := range chain.Paths {
		path := make(tracePath, 0, len(p.Steps))
		for _, id := range p.Steps {
			n, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("supply chain of unit %s references unknown node %q", beltUnit(m), id)
			}
			path = append(path, n)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// removalImpactItems runs a statement returning (unit, [item names]) rows for
// the units of nodes and returns the lists keyed by unit.
func removalImpactItems(ctx context.Context, read memgraphReader, cypher, surface string, nodes []*MemgraphTraceNode) (map[int][]string, error) {
	out := map[int][]string{}
	if len(nodes) == 0 {
		return out, nil
	}
	units := make([]any, 0, len(nodes))
	for _, n := range nodes {
		units = append(units, int64(unitOf(n)))
	}
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: cypher, Params: map[string]any{"surface": surface, "units": units},
		Limit: memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("row has %d columns, want 2", len(row))
		}
		unit, ok := row[0].(int64)
		if !ok {
			return nil, fmt.Errorf("unit is %T, want an integer", row[0])
		}
		raw, _ := row[1].([]any)
		items := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok {
				items = append(items, s)
			}
		}
		out[int(unit)] = items
	}
	return out, nil
}

// removalImpactRoutes cuts each supply path of a machine at its first machine
// or chest after the machine itself (path[0]) and drops duplicates. Paths with
// no supplier (they end on a belt nothing feeds) yield no route.
func removalImpactRoutes(paths []tracePath) []removalImpactRoute {
	var routes []removalImpactRoute
	seen := map[string]bool{}
	for _, path := range paths {
		for i := 1; i < len(path); i++ {
			n := path[i]
			if n.Kind != "Machine" && n.Kind != "Chest" {
				continue
			}
			steps := make([]string, i+1)
			for j := range steps {
				steps[j] = path[j].ID
			}
			if key := strings.Join(steps, "\x00"); !seen[key] {
				seen[key] = true
				routes = append(routes, removalImpactRoute{Steps: steps, Supplier: n})
			}
			break
		}
	}
	return routes
}

// removalImpactCompute applies the route rule documented at the top of the
// file. produces maps a supplier node id to the items it can supply. The result
// is ordered by machine unit, then ingredient.
func removalImpactCompute(rootID string, machines []removalImpactMachine, produces map[string]map[string]bool) []*MemgraphAffectedMachine {
	removed := map[string]bool{rootID: true}
	var affected []*MemgraphAffectedMachine
	for changed := true; changed; {
		changed = false
		var newlyRemoved []string
		for _, m := range machines {
			if removed[m.Node.ID] {
				continue
			}
			lost := false
			for _, item := range m.Ingredients {
				before, alive := 0, 0
				for _, r := range m.Routes {
					if !produces[r.Supplier.ID][item] {
						continue
					}
					before++
					if !removalImpactCrosses(r, removed) {
						alive++
					}
				}
				if before > 0 && alive == 0 {
					affected = append(affected, &MemgraphAffectedMachine{Machine: m.Node, Ingredient: item, RoutesBefore: before})
					lost = true
				}
			}
			if lost {
				// Every ingredient lost at this stage was found in this pass.
				newlyRemoved = append(newlyRemoved, m.Node.ID)
			}
		}
		for _, id := range newlyRemoved {
			removed[id] = true
			changed = true
		}
	}
	sort.SliceStable(affected, func(i, j int) bool {
		if a, b := unitOf(affected[i].Machine), unitOf(affected[j].Machine); a != b {
			return a < b
		}
		return affected[i].Ingredient < affected[j].Ingredient
	})
	if affected == nil {
		affected = []*MemgraphAffectedMachine{}
	}
	return affected
}

func removalImpactCrosses(r removalImpactRoute, removed map[string]bool) bool {
	for _, id := range r.Steps {
		if removed[id] {
			return true
		}
	}
	return false
}

func removalImpactSummary(root *MemgraphTraceNode, checked int, affected []*MemgraphAffectedMachine, truncated bool) string {
	label := traceLabel(root, false)
	var s string
	switch {
	case len(affected) == 0 && checked == 0:
		s = fmt.Sprintf("Removing %s is safe: no machine with a recipe is downstream of it.", label)
	case len(affected) == 0:
		s = fmt.Sprintf("Removing %s is safe: all %d downstream machine(s) keep a route for every ingredient.", label, checked)
	default:
		byMachine := map[string][]string{}
		var order []*MemgraphTraceNode
		for _, a := range affected {
			if _, ok := byMachine[a.Machine.ID]; !ok {
				order = append(order, a.Machine)
			}
			byMachine[a.Machine.ID] = append(byMachine[a.Machine.ID], a.Ingredient)
		}
		var parts []string
		for i, m := range order {
			if i == removalImpactSummaryListed {
				parts = append(parts, fmt.Sprintf("+%d more", len(order)-i))
				break
			}
			parts = append(parts, fmt.Sprintf("%s loses %s", traceLabel(m, false), strings.Join(byMachine[m.ID], ", ")))
		}
		s = fmt.Sprintf("Removing %s cuts off %d of %d downstream machine(s): %s.", label, len(order), checked, strings.Join(parts, "; "))
	}
	if truncated {
		s += " Result is incomplete: a trace hit the path limit."
	}
	return s
}
