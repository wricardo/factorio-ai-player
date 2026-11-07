package main

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
)

// memgraph.chainCapacity: the nominal output of the chain feeding one machine
// and the stage that caps it. The supply chain (BELT resolution) says which
// machines and chests feed which; the recipe amounts and the nominal
// per-minute PRODUCES rates of the graph say how much each can deliver.
//
// Two passes over the producer graph:
//  1. top-down (consumers first): the root runs at its nominal crafts per
//     minute; each consumer's ingredient demand is allocated to its providers,
//     which become stages with a demand;
//  2. bottom-up (producers first): the rate each machine really reaches is its
//     nominal rate capped by what its providers can deliver, so the root's
//     output is the tightest ingredient bound, recursively.

// chainCapacityItemsCypher lists, for machines and chests in $units, the items
// they produce or contain: PRODUCES edges carry perMinute (machines) or
// via:'infinity' (infinity chests), CONTAINS edges carry the stocked count.
const chainCapacityItemsCypher = `MATCH (m) WHERE (m:Machine OR m:Chest) AND m.surface = $surface AND m.unit IN $units
OPTIONAL MATCH (m)-[e:PRODUCES|CONTAINS]->(i:Item)
RETURN m AS node, type(e) AS edge, i.name AS item, e.perMinute AS perMinute, e.via AS via
ORDER BY m.unit, item`

// chainCapacityRecipeCypher lists the per-craft amounts of the recipes the
// machines in $units run.
const chainCapacityRecipeCypher = `MATCH (m:Machine) WHERE m.surface = $surface AND m.unit IN $units
MATCH (m)-[:RUNS]->(:Recipe)-[c:CONSUMES|PRODUCES]->(i:Item)
RETURN m.unit AS unit, type(c) AS edge, i.name AS item, c.amount AS amount
ORDER BY m.unit, item`

const chainCapacityEpsilon = 1e-9

func validateChainCapacityInput(in MemgraphChainCapacityInput) error {
	if err := validateTraceTarget(in.UnitNumber, in.Surface); err != nil {
		return err
	}
	if in.MaxHops < 1 || in.MaxHops > supplyChainMaxHopsLimit {
		return fmt.Errorf("maxHops must be between 1 and %d", supplyChainMaxHopsLimit)
	}
	return nil
}

// chainCapacityEntity is one machine or chest of the chain with the facts the
// capacity arithmetic needs.
type chainCapacityEntity struct {
	node     *MemgraphTraceNode
	unit     int
	chest    bool
	rates    map[string]float64 // PRODUCES perMinute per item (machines)
	infinity map[string]bool    // PRODUCES via infinity per item (chests)
	contains map[string]bool    // CONTAINS per item (chests)
	makes    map[string]float64 // recipe amount produced per craft
	needs    map[string]float64 // recipe amount consumed per craft
	crafts   float64            // nominal crafts per minute
}

func newChainCapacityEntity(node *MemgraphTraceNode) *chainCapacityEntity {
	return &chainCapacityEntity{
		node: node, unit: unitOf(node), chest: node.Kind == "Chest",
		rates: map[string]float64{}, infinity: map[string]bool{}, contains: map[string]bool{},
		makes: map[string]float64{}, needs: map[string]float64{},
	}
}

func chainCapacitySortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// settle derives the nominal crafts per minute from the first product that
// has both a rate and a recipe amount.
func (e *chainCapacityEntity) settle() {
	for _, item := range chainCapacitySortedKeys(e.makes) {
		if rate, ok := e.rates[item]; ok && rate > 0 && e.makes[item] > 0 {
			e.crafts = rate / e.makes[item]
			return
		}
	}
}

// out is the nominal per-minute output of one item.
func (e *chainCapacityEntity) out(item string) float64 {
	if rate, ok := e.rates[item]; ok {
		return rate
	}
	return e.crafts * e.makes[item]
}

// chainCapacityProvider is something that can supply an item to a consumer.
type chainCapacityProvider struct {
	ent       *chainCapacityEntity
	unbounded bool // infinity chest, or a stocked chest with nothing upstream
	stock     bool // unbounded because the chest merely holds the item
}

// chainCapacityProviders finds what supplies item to the consumer: adjacent
// suppliers on the chain that produce it, looking through chests. A chest that
// produces the item (infinity) is unbounded; a chest that only holds the item
// is looked through to what fills it and, when nothing upstream produces the
// item, counted as an unbounded stock.
func chainCapacityProviders(ents map[int]*chainCapacityEntity, supp map[int][]int, consumer int, item string) []chainCapacityProvider {
	var out []chainCapacityProvider
	seen := map[int]bool{}
	var walk func(u int)
	walk = func(u int) {
		for _, s := range supp[u] {
			if seen[s] {
				continue
			}
			seen[s] = true
			e := ents[s]
			if e == nil {
				continue
			}
			if !e.chest {
				if e.makes[item] > 0 {
					out = append(out, chainCapacityProvider{ent: e})
				}
				continue
			}
			if e.infinity[item] {
				out = append(out, chainCapacityProvider{ent: e, unbounded: true})
				continue
			}
			before := len(out)
			walk(s)
			if len(out) == before && e.contains[item] {
				out = append(out, chainCapacityProvider{ent: e, unbounded: true, stock: true})
			}
		}
	}
	walk(consumer)
	return out
}

// chainCapacitySupplierEdges returns consumer -> suppliers for the machines
// and chests of the chain: two consecutive suppliers on a path are a consumer
// and the supplier feeding it.
func chainCapacitySupplierEdges(chain *MemgraphSupplyChain) (map[int][]int, []int) {
	byID := make(map[string]*MemgraphTraceNode, len(chain.Nodes))
	for _, n := range chain.Nodes {
		byID[n.ID] = n
	}
	supp := map[int][]int{}
	has := map[[2]int]bool{}
	units := map[int]bool{}
	for _, p := range chain.Paths {
		last := 0
		for i, id := range p.Steps {
			n := byID[id]
			if n == nil || (n.Kind != "Machine" && n.Kind != "Chest") || unitOf(n) == 0 {
				continue
			}
			u := unitOf(n)
			units[u] = true
			if i > 0 && last != 0 && last != u && !has[[2]int{last, u}] {
				has[[2]int{last, u}] = true
				supp[last] = append(supp[last], u)
			}
			last = u
		}
	}
	all := make([]int, 0, len(units))
	for u := range units {
		all = append(all, u)
	}
	sort.Ints(all)
	return supp, all
}

func chainCapacityRound(x float64) float64 {
	return math.Round(x*1e6) / 1e6
}

// chainCapacity measures the nominal output of the chain feeding one machine.
func chainCapacity(ctx context.Context, read memgraphReader, in MemgraphChainCapacityInput) (*MemgraphChainCapacity, error) {
	if err := validateChainCapacityInput(in); err != nil {
		return nil, err
	}
	rootUnit := validUnit(in.UnitNumber)
	root, err := traceRoot(ctx, read, rootUnit, in.Surface, "Machine")
	if err != nil {
		return nil, err
	}
	out := &MemgraphChainCapacity{Root: root, Limiting: []*MemgraphStageRate{}, Stages: []*MemgraphStageRate{}, Notes: []string{}}
	if root.Recipe == nil || *root.Recipe == "" {
		out.Notes = append(out.Notes, fmt.Sprintf("unit %d has no recipe, so it produces nothing to measure", rootUnit))
		return out, nil
	}
	chain, err := supplyChain(ctx, read, MemgraphSupplyChainInput{
		UnitNumber: in.UnitNumber, Surface: in.Surface, Resolution: MemgraphSupplyResolutionBelt,
		MaxHops: in.MaxHops, MaxPaths: supplyChainMaxPathLimit,
	})
	if err != nil {
		return nil, err
	}
	out.Truncated = chain.Truncated
	out.Snapshot = chain.Snapshot
	supp, units := chainCapacitySupplierEdges(chain)
	if !slices.Contains(units, rootUnit) {
		units = append(units, rootUnit)
	}
	unitParams := make([]any, len(units))
	for i, u := range units {
		unitParams[i] = int64(u)
	}
	params := map[string]any{"surface": in.Surface, "units": unitParams}
	itemRes, err := read(ctx, MemgraphQueryInput{Cypher: chainCapacityItemsCypher, Params: params, Limit: memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs})
	if err != nil {
		return nil, fmt.Errorf("chain capacity items of unit %s: %w", in.UnitNumber, err)
	}
	recipeRes, err := read(ctx, MemgraphQueryInput{Cypher: chainCapacityRecipeCypher, Params: params, Limit: memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs})
	if err != nil {
		return nil, fmt.Errorf("chain capacity recipes of unit %s: %w", in.UnitNumber, err)
	}
	if itemRes.Snapshot != nil {
		out.Snapshot = itemRes.Snapshot
	}
	ents, err := chainCapacityEntities(itemRes.Rows, recipeRes.Rows)
	if err != nil {
		return nil, err
	}
	rootEnt := ents[rootUnit]
	if rootEnt == nil {
		rootEnt = newChainCapacityEntity(root)
		ents[rootUnit] = rootEnt
	}
	rootEnt.node = root
	products := chainCapacitySortedKeys(rootEnt.makes)
	if len(products) == 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("recipe %q of unit %d has no PRODUCES edge in the loaded graph", *root.Recipe, rootUnit))
		return out, nil
	}
	item := products[0]
	out.OutputItem = &item
	return chainCapacityCompute(out, ents, supp, rootEnt, item, len(products) > 1), nil
}

// chainCapacityEntities decodes the two result sets into entities by unit.
func chainCapacityEntities(itemRows, recipeRows [][]any) (map[int]*chainCapacityEntity, error) {
	ents := map[int]*chainCapacityEntity{}
	for _, row := range itemRows {
		if len(row) < 5 {
			return nil, fmt.Errorf("chain capacity item row has %d columns, want 5", len(row))
		}
		node, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		u := unitOf(node)
		if u == 0 {
			continue
		}
		e := ents[u]
		if e == nil {
			e = newChainCapacityEntity(node)
			ents[u] = e
		}
		edge, _ := row[1].(string)
		item, _ := row[2].(string)
		if item == "" {
			continue
		}
		switch edge {
		case "PRODUCES":
			if via, _ := row[4].(string); via == "infinity" {
				e.infinity[item] = true
			}
			if pm := optionalFloat(row[3]); pm != nil {
				e.rates[item] = *pm
			}
		case "CONTAINS":
			e.contains[item] = true
		}
	}
	for _, row := range recipeRows {
		if len(row) < 4 {
			return nil, fmt.Errorf("chain capacity recipe row has %d columns, want 4", len(row))
		}
		u64, ok := row[0].(int64)
		if !ok {
			continue
		}
		e := ents[int(u64)]
		item, _ := row[2].(string)
		amount := optionalFloat(row[3])
		if e == nil || item == "" || amount == nil {
			continue
		}
		switch row[1] {
		case "PRODUCES":
			e.makes[item] = *amount
		case "CONSUMES":
			e.needs[item] = *amount
		}
	}
	for _, e := range ents {
		e.settle()
	}
	return ents, nil
}

type chainCapacityKey struct {
	unit int
	item string
}

// chainCapacityCompute runs the top-down demand pass and the bottom-up rate
// pass and fills the result.
func chainCapacityCompute(out *MemgraphChainCapacity, ents map[int]*chainCapacityEntity, supp map[int][]int, root *chainCapacityEntity, outItem string, multiProduct bool) *MemgraphChainCapacity {
	notes := []string{
		"Inserter and belt throughput are not modelled: a real chain can be lower than this nominal figure.",
		"Nominal means each machine running its recipe continuously at the per-minute rate exported on its PRODUCES edge; machine status, power and fuel are not considered.",
		"Every adjacent supplier on the chain whose recipe produces an ingredient is assumed to deliver it to the consumer.",
	}
	if multiProduct {
		notes = append(notes, fmt.Sprintf("The root recipe has several products; %s (first by name) is reported.", outItem))
	}

	// Providers per consumer and ingredient, plus a producer-first order. A
	// provider that is already being visited closes a cycle and is dropped.
	prov := map[chainCapacityKey][]chainCapacityProvider{}
	state := map[int]int{}
	var post []int
	var cycles []string
	var missing []string
	var visit func(e *chainCapacityEntity)
	visit = func(e *chainCapacityEntity) {
		state[e.unit] = 1
		for _, item := range chainCapacitySortedKeys(e.needs) {
			var kept []chainCapacityProvider
			for _, p := range chainCapacityProviders(ents, supp, e.unit, item) {
				if !p.ent.chest {
					switch state[p.ent.unit] {
					case 1:
						cycles = append(cycles, fmt.Sprintf("unit %d feeds %s back into unit %d", p.ent.unit, item, e.unit))
						continue
					case 0:
						visit(p.ent)
					}
				}
				kept = append(kept, p)
			}
			prov[chainCapacityKey{e.unit, item}] = kept
			if len(kept) == 0 && e.needs[item] > 0 {
				missing = append(missing, fmt.Sprintf("unit %d (%s) needs %s but nothing on the chain produces it, so its output is 0", e.unit, chainCapacityRecipe(e.node), item))
			}
		}
		state[e.unit] = 2
		post = append(post, e.unit)
	}
	visit(root)

	// Top-down demand: consumers come before the producers feeding them.
	demand := map[chainCapacityKey]float64{}
	shares := map[chainCapacityKey]map[int]float64{}
	required := map[int]float64{}
	var chestOrder []chainCapacityKey
	chestSeen := map[chainCapacityKey]bool{}
	hasInfinity, hasStock := false, false
	for i := len(post) - 1; i >= 0; i-- {
		e := ents[post[i]]
		rc := e.crafts
		if e != root {
			rc = 0
			for item, amount := range e.makes {
				if amount > 0 {
					rc = math.Max(rc, demand[chainCapacityKey{e.unit, item}]/amount)
				}
			}
		}
		required[e.unit] = rc
		for _, item := range chainCapacitySortedKeys(e.needs) {
			need := e.needs[item] * rc
			ps := prov[chainCapacityKey{e.unit, item}]
			bounded, unbounded := 0.0, 0
			for _, p := range ps {
				if p.unbounded {
					unbounded++
				} else {
					bounded += p.ent.out(item)
				}
			}
			take := need
			if unbounded > 0 {
				take = math.Min(need, bounded)
			}
			for _, p := range ps {
				var amt float64
				switch {
				case p.unbounded:
					amt = (need - take) / float64(unbounded)
				case bounded > 0:
					amt = take * p.ent.out(item) / bounded
				}
				k := chainCapacityKey{p.ent.unit, item}
				demand[k] += amt
				if shares[k] == nil {
					shares[k] = map[int]float64{}
				}
				shares[k][e.unit] += amt
				if p.unbounded {
					if p.stock {
						hasStock = true
					} else {
						hasInfinity = true
					}
					if !chestSeen[k] {
						chestSeen[k] = true
						chestOrder = append(chestOrder, k)
					}
				}
			}
		}
	}
	outKey := chainCapacityKey{root.unit, outItem}
	demand[outKey] = root.crafts * root.makes[outItem]

	// Bottom-up rates: producers come before the machines they feed.
	rate := map[int]float64{}
	for _, u := range post {
		e := ents[u]
		r := e.crafts
		for _, item := range chainCapacitySortedKeys(e.needs) {
			amount := e.needs[item]
			if amount <= 0 {
				continue
			}
			avail, inf := 0.0, false
			for _, p := range prov[chainCapacityKey{u, item}] {
				if p.unbounded {
					inf = true
					break
				}
				k := chainCapacityKey{p.ent.unit, item}
				if d := demand[k]; d > chainCapacityEpsilon {
					avail += rate[p.ent.unit] * p.ent.makes[item] * shares[k][u] / d
				}
			}
			if !inf {
				r = math.Min(r, avail/amount)
			}
		}
		rate[u] = r
	}
	out.OutputPerMinute = chainCapacityRound(rate[root.unit] * root.makes[outItem])

	// Stages: machines in consumer-first order, then the unbounded chests.
	for i := len(post) - 1; i >= 0; i-- {
		e := ents[post[i]]
		if e == root {
			out.Stages = append(out.Stages, &MemgraphStageRate{Node: e.node, Item: outItem, SupplyPerMinute: chainCapacityRound(e.out(outItem)), DemandPerMinute: chainCapacityRound(demand[outKey])})
			continue
		}
		for _, item := range chainCapacitySortedKeys(e.makes) {
			if d := demand[chainCapacityKey{e.unit, item}]; d > chainCapacityEpsilon {
				out.Stages = append(out.Stages, &MemgraphStageRate{Node: e.node, Item: item, SupplyPerMinute: chainCapacityRound(e.out(item)), DemandPerMinute: chainCapacityRound(d)})
			}
		}
	}
	sort.SliceStable(chestOrder, func(i, j int) bool {
		if chestOrder[i].unit != chestOrder[j].unit {
			return chestOrder[i].unit < chestOrder[j].unit
		}
		return chestOrder[i].item < chestOrder[j].item
	})
	for _, k := range chestOrder {
		d := chainCapacityRound(demand[k])
		out.Stages = append(out.Stages, &MemgraphStageRate{Node: ents[k.unit].node, Item: k.item, SupplyPerMinute: d, DemandPerMinute: d})
	}
	for _, s := range out.Stages {
		if s.SupplyPerMinute < s.DemandPerMinute-chainCapacityEpsilon {
			out.Limiting = append(out.Limiting, s)
		}
	}
	sort.SliceStable(out.Limiting, func(i, j int) bool {
		a, b := out.Limiting[i], out.Limiting[j]
		return a.SupplyPerMinute/a.DemandPerMinute < b.SupplyPerMinute/b.DemandPerMinute
	})

	if hasInfinity {
		notes = append(notes, "Infinity chests are treated as unbounded: their stage reports supply equal to the demand placed on it.")
	}
	if hasStock {
		notes = append(notes, "A chest that only holds an ingredient, with no producer upstream, is treated as an unbounded stock: a finite stock is not a rate.")
	}
	shared := false
	for _, m := range shares {
		if len(m) > 1 {
			shared = true
			break
		}
	}
	if shared {
		notes = append(notes, "A producer feeding several consumers is split in proportion to the demand each places on it when the root runs at its nominal rate.")
	}
	for _, u := range post {
		if e := ents[u]; e.crafts == 0 {
			notes = append(notes, fmt.Sprintf("Unit %d (%s) has no nominal per-minute rate in the graph and is counted as producing 0.", u, chainCapacityRecipe(e.node)))
		}
	}
	notes = append(notes, missing...)
	for _, c := range cycles {
		notes = append(notes, "Feed cycle ignored: "+c+".")
	}
	if out.Truncated {
		notes = append(notes, "The supply chain was cut by the path limit, so producers may be missing and the output understated.")
	}
	if len(out.Limiting) == 0 && out.OutputPerMinute > 0 {
		notes = append(notes, "No stage below the root is short: the output is the root's own nominal rate.")
	}
	out.Notes = notes
	return out
}

func chainCapacityRecipe(n *MemgraphTraceNode) string {
	if n != nil && n.Recipe != nil {
		return *n.Recipe
	}
	return "no recipe"
}
