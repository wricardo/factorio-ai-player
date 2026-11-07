package main

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Supply-chain reads over the Memgraph factory graph: memgraph.supplyChain,
// memgraph.ingredientCoverage and memgraph.deadInputs. Each builds one
// read-only Cypher statement, runs it through queryMemgraph (so the read-only
// guard, timeout and snapshot read apply) and reshapes the rows into a compact,
// normalized result: a summary, a pre-rendered outline, nodes listed once and
// paths as id references.

const (
	supplyChainMaxHopsLimit = 16
	supplyChainMaxPathLimit = 10000
	supplyChainTimeoutMs    = 30000
)

// memgraphReader runs one read-only statement. Production wires it to
// queryMemgraph; tests inject canned rows.
type memgraphReader func(ctx context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error)

// memgraphReaderFor runs statements through queryMemgraph, so the read-only
// guard, deadline and snapshot read apply to every trace query.
func memgraphReaderFor(r *Resolver) memgraphReader {
	return func(ctx context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		return queryMemgraph(ctx, r, in)
	}
}

var memgraphSurfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// supplyTroubleStatuses are engine statuses worth surfacing in a summary.
// waiting_for_source_items and waiting_for_space_in_destination are left out
// deliberately: they are normal backpressure states, not faults.
var supplyTroubleStatuses = map[string]bool{
	"item_ingredient_shortage": true, "no_power": true, "low_power": true, "no_fuel": true,
	"no_minable_resources": true, "no_ingredients": true,
}

// downstreamTroubleStatuses adds output-side blockage: a machine that is full
// stalls its producers, which only matters when tracing what a node feeds.
var downstreamTroubleStatuses = map[string]bool{"full_output": true}

// A node comes back as a Bolt node ({id, labels, properties}). Cypher-built
// maps are avoided on purpose: Memgraph returned wrong values for a map
// literal over a list-comprehension variable, so properties are read in Go.

func validateSupplyChainInput(in MemgraphSupplyChainInput) error {
	if err := validateTraceTarget(in.UnitNumber, in.Surface); err != nil {
		return err
	}
	if in.MaxHops < 1 || in.MaxHops > supplyChainMaxHopsLimit {
		return fmt.Errorf("maxHops must be between 1 and %d", supplyChainMaxHopsLimit)
	}
	if in.MaxPaths < 1 || in.MaxPaths > supplyChainMaxPathLimit {
		return fmt.Errorf("maxPaths must be between 1 and %d", supplyChainMaxPathLimit)
	}
	if in.Resolution != MemgraphSupplyResolutionBelt && in.Resolution != MemgraphSupplyResolutionStretch {
		return fmt.Errorf("resolution must be BELT or STRETCH, got %q", in.Resolution)
	}
	return nil
}

// parseUnitNumber reads the UnitNumber scalar, a positive decimal string.
func parseUnitNumber(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("unitNumber %q must be a positive decimal unit number", s)
	}
	return n, nil
}

// validUnit returns the unit number of an input already accepted by
// validateTraceTarget; it is 0 for anything else.
func validUnit(s string) int {
	n, _ := parseUnitNumber(s)
	return n
}

func validateTraceTarget(unitNumber, surface string) error {
	if _, err := parseUnitNumber(unitNumber); err != nil {
		return err
	}
	if !memgraphSurfacePattern.MatchString(surface) {
		return fmt.Errorf("surface %q must be 1-64 letters, digits, '.', '_' or '-'", surface)
	}
	return nil
}

// supplyHopBlock is one unrolled upstream hop. Cypher cannot alternate edge
// directions inside one variable-length pattern, so a hop is a path piece and
// the statement repeats the block maxHops times. Rows that find nothing new
// pass through with cur = null, which keeps shorter chains in the result.
func supplyHopBlock(res MemgraphSupplyResolution) string {
	const prefix = "WITH cur, chain, paths,\n  CASE WHEN cur IS NULL THEN []\n"
	const suffix = "  END AS ps\n" +
		"UNWIND (CASE WHEN size(ps) = 0 THEN [NULL] ELSE ps END) AS p\n" +
		"WITH CASE WHEN p IS NULL THEN NULL ELSE last(nodes(p)) END AS cur,\n" +
		"     chain + CASE WHEN p IS NULL THEN [] ELSE [last(nodes(p)).id] END AS chain,\n" +
		"     paths + CASE WHEN p IS NULL THEN [] ELSE [p] END AS paths\n"
	if res == MemgraphSupplyResolutionStretch {
		return prefix +
			"  WHEN cur:BeltStretch THEN\n" +
			"    [p = (cur)<-[:FLOWS_INTO*0..30]-(:BeltStretch)<-[:FEEDS_STRETCH]-(:Inserter)-[:PICKS_FROM]->(n) WHERE NOT n:Belt AND NOT n.id IN chain | p]\n" +
			"  + [p = (cur)<-[:FLOWS_INTO*0..30]-(:BeltStretch)<-[:FEEDS_STRETCH]-(:Inserter)-[:TAKES_FROM_STRETCH]->(n:BeltStretch) WHERE NOT n.id IN chain | p]\n" +
			"  ELSE\n" +
			"    [p = (cur)<-[:DROPS_TO]-(:Inserter)-[:PICKS_FROM]->(n) WHERE NOT n:Belt AND NOT n.id IN chain | p]\n" +
			"  + [p = (cur)<-[:DROPS_TO]-(:Inserter)-[:TAKES_FROM_STRETCH]->(n:BeltStretch) WHERE NOT n.id IN chain | p]\n" +
			suffix
	}
	return prefix +
		"  ELSE\n" +
		"    [p = (cur)<-[:BELT_OUT*0..400]-(:Belt)<-[:DROPS_TO]-(:Inserter)-[:PICKS_FROM]->(n) WHERE NOT n.id IN chain | p]\n" +
		"  + [p = (cur)<-[:DROPS_TO]-(:Inserter)-[:PICKS_FROM]->(n) WHERE NOT cur:Belt AND NOT n.id IN chain | p]\n" +
		suffix
}

// supplyChainCypher builds the unrolled upstream statement. With graph false it
// returns one ordered step list per path and references $unit and $surface as
// parameters; with graph true it returns Cypher path objects (for Memgraph
// Lab's Graph Results view) with the validated root inlined.
func supplyChainCypher(in MemgraphSupplyChainInput, graph bool) string {
	unitRef, surfaceRef := "$unit", "$surface"
	if graph {
		unitRef, surfaceRef = strconv.Itoa(validUnit(in.UnitNumber)), strconv.Quote(in.Surface)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MATCH (r {unit: %s, surface: %s}) WHERE r:Machine OR r:Chest OR r:Belt\n", unitRef, surfaceRef)
	if in.Resolution == MemgraphSupplyResolutionStretch {
		b.WriteString("OPTIONAL MATCH (r)-[:IN_STRETCH]->(s:BeltStretch)\n")
		b.WriteString("WITH CASE WHEN r:Belt THEN s ELSE r END AS cur, [CASE WHEN r:Belt THEN s.id ELSE r.id END] AS chain, [] AS paths\n")
	} else {
		b.WriteString("WITH r AS cur, [r.id] AS chain, [] AS paths\n")
	}
	block := supplyHopBlock(in.Resolution)
	for range in.MaxHops {
		b.WriteString(block)
	}
	return writeChainTail(&b, graph)
}

// writeChainTail appends the closing clauses shared by the upstream and
// downstream statements: with graph true, Cypher path objects for Memgraph
// Lab; otherwise one ordered, de-duplicated step list per path.
func writeChainTail(b *strings.Builder, graph bool) string {
	b.WriteString("WITH paths WHERE size(paths) > 0\n")
	if graph {
		b.WriteString("UNWIND paths AS p\nRETURN DISTINCT p")
		return b.String()
	}
	b.WriteString("WITH paths, reduce(s = [], x IN reduce(f = [], p IN paths | f + nodes(p)) |\n" +
		"  CASE WHEN size(s) > 0 AND last(s).id = x.id THEN s ELSE s + [x] END) AS flat\n")
	b.WriteString("RETURN size(paths) AS hops, flat AS steps\nORDER BY hops, size(flat)")
	return b.String()
}

func traceParams(unit int, surface string) map[string]any {
	return map[string]any{"unit": int64(unit), "surface": surface}
}

// supplyChain traces everything upstream of one Machine, Chest or Belt.
func supplyChain(ctx context.Context, read memgraphReader, in MemgraphSupplyChainInput) (*MemgraphSupplyChain, error) {
	if err := validateSupplyChainInput(in); err != nil {
		return nil, err
	}
	root, err := traceRoot(ctx, read, validUnit(in.UnitNumber), in.Surface, "Machine", "Chest", "Belt")
	if err != nil {
		return nil, err
	}
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: supplyChainCypher(in, false), Params: traceParams(validUnit(in.UnitNumber), in.Surface),
		Limit: in.MaxPaths, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, fmt.Errorf("supply chain of unit %s: %w", in.UnitNumber, err)
	}
	paths, err := decodeSupplyPaths(res.Rows)
	if err != nil {
		return nil, err
	}
	out := assembleSupplyChain(root, paths)
	out.Truncated = res.Truncated
	out.GraphCypher = supplyChainCypher(in, true)
	out.DurationMs = res.DurationMs
	out.Snapshot = res.Snapshot
	return out, nil
}

// traceRoot loads the root node and checks it is one of the allowed kinds.
func traceRoot(ctx context.Context, read memgraphReader, unit int, surface string, allowed ...string) (*MemgraphTraceNode, error) {
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: "MATCH (r {unit: $unit, surface: $surface}) RETURN r AS node LIMIT 1",
		Params: traceParams(unit, surface), Limit: 1, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return nil, fmt.Errorf("no node with unit %d on surface %q in the loaded graph (it may not exist, or the snapshot predates it)", unit, surface)
	}
	node, err := traceNodeFromAny(res.Rows[0][0])
	if err != nil {
		return nil, err
	}
	for _, kind := range allowed {
		if node.Kind == kind {
			return node, nil
		}
	}
	return nil, fmt.Errorf("unit %d on surface %q is %s %s; this query accepts %s", unit, surface, indefiniteArticle(node.Kind), node.Kind, strings.Join(allowed, ", "))
}

func traceNodeFromAny(v any) (*MemgraphTraceNode, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a node, got %T", v)
	}
	props, _ := m["properties"].(map[string]any)
	labels, _ := m["labels"].([]string)
	if rawLabels, ok := m["labels"].([]any); ok {
		for _, l := range rawLabels {
			if s, ok := l.(string); ok {
				labels = append(labels, s)
			}
		}
	}
	n := &MemgraphTraceNode{}
	n.ID, _ = props["id"].(string)
	for _, l := range labels {
		if l != "Entity" {
			n.Kind = l
			break
		}
	}
	if n.ID == "" || n.Kind == "" {
		return nil, fmt.Errorf("node without id or kind: %v", m)
	}
	if u, ok := props["unit"].(int64); ok {
		unit := strconv.FormatInt(u, 10)
		n.UnitNumber = &unit
	}
	if n.Kind == "BeltStretch" {
		name := "belt-stretch"
		n.Name = &name
		n.X, n.Y = optionalFloat(props["headX"]), optionalFloat(props["headY"])
	} else {
		n.Name = optionalString(props["name"])
		n.X, n.Y = optionalFloat(props["x"]), optionalFloat(props["y"])
	}
	n.Recipe = optionalString(props["recipe"])
	n.Status = optionalString(props["status"])
	return n, nil
}

func optionalString(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func optionalFloat(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case int64:
		f := float64(x)
		return &f
	}
	return nil
}

type tracePath []*MemgraphTraceNode

func decodeSupplyPaths(rows [][]any) ([]tracePath, error) {
	paths := make([]tracePath, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("supply chain row has %d columns, want 2", len(row))
		}
		raw, ok := row[1].([]any)
		if !ok {
			return nil, fmt.Errorf("supply chain steps are %T, want a list", row[1])
		}
		path := make(tracePath, 0, len(raw))
		for _, step := range raw {
			n, err := traceNodeFromAny(step)
			if err != nil {
				return nil, err
			}
			path = append(path, n)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// assembleSupplyChain normalizes decoded paths: nodes once, paths as ids,
// origins, summary and the outline.
func assembleSupplyChain(root *MemgraphTraceNode, paths []tracePath) *MemgraphSupplyChain {
	return assembleChain(root, paths, false)
}

// assembleChain normalizes decoded paths: nodes once, paths as ids, path
// ends (origins upstream, sinks downstream), summary and the outline.
func assembleChain(root *MemgraphTraceNode, paths []tracePath, down bool) *MemgraphSupplyChain {
	out := &MemgraphSupplyChain{Root: root, Nodes: []*MemgraphTraceNode{root}, Paths: []*MemgraphSupplyPath{}, Origins: []*MemgraphTraceNode{}}
	seen := map[string]bool{root.ID: true}
	origin := map[string]bool{}
	maxHops := 0
	for _, path := range paths {
		ids := make([]string, len(path))
		hops := 0
		for i, n := range path {
			ids[i] = n.ID
			if !seen[n.ID] {
				seen[n.ID] = true
				out.Nodes = append(out.Nodes, n)
			}
			if i > 0 && traceSignificant(n, i == len(path)-1) {
				hops++
			}
		}
		if hops > maxHops {
			maxHops = hops
		}
		out.Paths = append(out.Paths, &MemgraphSupplyPath{Hops: hops, Steps: ids})
		if last := path[len(path)-1]; last.ID != root.ID && !origin[last.ID] {
			origin[last.ID] = true
			out.Origins = append(out.Origins, last)
		}
	}
	kinds := map[string]int{}
	for _, n := range out.Nodes {
		kinds[n.Kind]++
	}
	reason := "origin:belt"
	if down {
		reason = "sink:belt"
	}
	out.Summary = &MemgraphSupplyChainSummary{
		PathCount: len(out.Paths), NodeCount: len(out.Nodes), MaxHops: maxHops,
		KindCounts: memgraphCounts(kinds), Attention: traceAttention(out.Nodes, origin, reason, down),
	}
	out.Outline = supplyOutline(root, paths, down)
	return out
}

// assembleDownstreamChain is assembleSupplyChain for paths that run away from
// the root: the same normalization, with sinks (where items leave the chain)
// in place of origins.
func assembleDownstreamChain(root *MemgraphTraceNode, paths []tracePath) *MemgraphDownstreamChain {
	c := assembleChain(root, paths, true)
	return &MemgraphDownstreamChain{Root: c.Root, Summary: c.Summary, Outline: c.Outline, Nodes: c.Nodes, Paths: c.Paths, Sinks: c.Origins}
}

// traceSignificant reports whether a path step is a supplier (machine or
// chest) or the end of the path, as opposed to the inserters and belts that
// connect suppliers.
func traceSignificant(n *MemgraphTraceNode, last bool) bool {
	return n.Kind == "Machine" || n.Kind == "Chest" || last
}

// traceAttention flags nodes in trouble: machines or chests with a trouble
// status, and path ends that are belts (origin:belt upstream, sink:belt
// downstream: nothing feeds, or nothing takes from, that line).
func traceAttention(nodes []*MemgraphTraceNode, ends map[string]bool, endReason string, down bool) []*MemgraphTraceFinding {
	out := []*MemgraphTraceFinding{}
	for _, n := range nodes {
		switch {
		case (n.Kind == "Machine" || n.Kind == "Chest") && n.Status != nil && (supplyTroubleStatuses[*n.Status] || down && downstreamTroubleStatuses[*n.Status]):
			out = append(out, &MemgraphTraceFinding{Node: n, Reason: "status:" + *n.Status})
		case ends[n.ID] && (n.Kind == "Belt" || n.Kind == "BeltStretch"):
			out = append(out, &MemgraphTraceFinding{Node: n, Reason: endReason})
		}
	}
	return out
}

func traceLabel(n *MemgraphTraceNode, withStatus bool) string {
	var parts []string
	switch n.Kind {
	case "Belt":
		parts = append(parts, "belt")
	case "BeltStretch":
		parts = append(parts, "stretch "+stretchTag(n.ID))
	}
	if n.UnitNumber != nil {
		parts = append(parts, *n.UnitNumber)
	}
	if n.Kind != "Belt" && n.Kind != "BeltStretch" && n.Name != nil {
		parts = append(parts, *n.Name)
	}
	label := strings.Join(parts, " ")
	if n.Recipe != nil {
		label += " (" + *n.Recipe + ")"
	}
	if withStatus && n.Status != nil {
		label += " [" + *n.Status + "]"
	}
	return label
}

// stretchTag is the part of a stretch id after the surface and "stretch:".
func stretchTag(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

type outlineEdge struct {
	vias  []string
	child *outlineNode
}

type outlineNode struct {
	node  *MemgraphTraceNode
	edges []*outlineEdge
}

// supplyOutline renders the paths as an indented tree, one edge per
// (node, child). Several routes to the same child (different belts or
// arms) collapse to the first route plus a count. A child is expanded where
// it first appears; later parents list it once, in a single "also fed by"
// (downstream: "also feeds") line, so a shared furnace bank does not repeat
// under every consumer. With down false the arrows point toward the root
// (supplier <- consumer reading); with down true they point away from it.
func supplyOutline(root *MemgraphTraceNode, paths []tracePath, down bool) string {
	arrow, sharedVerb, sameNoun := "<- ", "fed by", "suppliers"
	if down {
		arrow, sharedVerb, sameNoun = "-> ", "feeds", "consumers"
	}
	byID := map[string]*outlineNode{root.ID: {node: root}}
	for _, path := range paths {
		cur := byID[root.ID]
		var via []*MemgraphTraceNode
		for i := 1; i < len(path); i++ {
			step := path[i]
			if !traceSignificant(step, i == len(path)-1) {
				via = append(via, step)
				continue
			}
			child, ok := byID[step.ID]
			if !ok {
				child = &outlineNode{node: step}
				byID[step.ID] = child
			}
			desc := describeVia(via, down)
			var edge *outlineEdge
			for _, e := range cur.edges {
				if e.child == child {
					edge = e
					break
				}
			}
			if edge == nil {
				edge = &outlineEdge{child: child}
				cur.edges = append(cur.edges, edge)
			}
			if !slices.Contains(edge.vias, desc) {
				edge.vias = append(edge.vias, desc)
			}
			cur, via = child, nil
		}
	}
	var b strings.Builder
	b.WriteString(traceLabel(root, true) + "\n")
	expanded := map[string]bool{root.ID: true}
	var walk func(n *outlineNode, depth int)
	sameAs := map[string]string{}
	walk = func(n *outlineNode, depth int) {
		indent := strings.Repeat("  ", depth)
		if len(n.edges) > 0 {
			ids := make([]string, len(n.edges))
			for i, e := range n.edges {
				ids[i] = e.child.node.ID
			}
			sort.Strings(ids)
			key := strings.Join(ids, "|")
			if first, ok := sameAs[key]; ok {
				b.WriteString(indent + arrow + "same " + sameNoun + " as " + first + "\n")
				return
			}
			sameAs[key] = traceLabel(n.node, false)
		}
		var shared []string
		for _, e := range n.edges {
			if expanded[e.child.node.ID] {
				shared = append(shared, traceLabel(e.child.node, false))
				continue
			}
			expanded[e.child.node.ID] = true
			b.WriteString(indent + arrow)
			if e.vias[0] != "" {
				b.WriteString(e.vias[0] + " " + strings.TrimSpace(arrow) + " ")
			}
			b.WriteString(traceLabel(e.child.node, true))
			if len(e.vias) > 1 {
				fmt.Fprintf(&b, " (+%d other routes)", len(e.vias)-1)
			}
			b.WriteString("\n")
			walk(e.child, depth+1)
		}
		if len(shared) > 0 {
			b.WriteString(indent + arrow + "also " + sharedVerb + " (expanded above): " + strings.Join(shared, "; ") + "\n")
		}
	}
	walk(byID[root.ID], 1)
	return strings.TrimRight(b.String(), "\n")
}

// describeVia renders the inserters and belts between two suppliers, collapsing
// each run of belts to its first and last unit and a count.
func describeVia(via []*MemgraphTraceNode, down bool) string {
	var parts []string
	for i := 0; i < len(via); {
		n := via[i]
		switch n.Kind {
		case "Belt":
			j := i
			for j < len(via) && via[j].Kind == "Belt" {
				j++
			}
			first, last := beltUnit(via[i]), beltUnit(via[j-1])
			if j-i == 1 {
				parts = append(parts, "belt "+first)
			} else {
				parts = append(parts, fmt.Sprintf("belts %s..%s (%d)", first, last, j-i))
			}
			i = j
			continue
		case "BeltStretch":
			parts = append(parts, "stretch "+stretchTag(n.ID))
		default:
			label := n.Kind
			if n.Kind == "Inserter" {
				label = "arm"
			}
			if n.UnitNumber != nil {
				label += " " + *n.UnitNumber
			}
			if n.Status != nil {
				label += " [" + *n.Status + "]"
			}
			parts = append(parts, label)
		}
		i++
	}
	if down {
		return strings.Join(parts, " -> ")
	}
	return strings.Join(parts, " <- ")
}

func beltUnit(n *MemgraphTraceNode) string {
	if n.UnitNumber == nil {
		return "?"
	}
	return *n.UnitNumber
}

func validateIngredientCoverageInput(in MemgraphIngredientCoverageInput) error {
	if err := validateTraceTarget(in.UnitNumber, in.Surface); err != nil {
		return err
	}
	if in.MaxHops < 1 || in.MaxHops > supplyChainMaxHopsLimit {
		return fmt.Errorf("maxHops must be between 1 and %d", supplyChainMaxHopsLimit)
	}
	return nil
}

// ingredientCoverageCypher lists, for each machine in $units, each ingredient
// of its recipe and the inserters whose source can provide it. Per-arm
// "provides" is computed once; fluid ingredients are skipped because they
// arrive through pipes, never an inserter; re-walking the belt line per ingredient exceeds
// the statement deadline.
const ingredientCoverageCypher = `MATCH (m:Machine) WHERE m.surface = $surface AND m.unit IN $units
OPTIONAL MATCH (m)<-[:DROPS_TO]-(i:Inserter)-[:PICKS_FROM]->(s)
OPTIONAL MATCH (s:Belt)<-[:BELT_OUT*0..400]-(:Belt)<-[:DROPS_TO]-(:Inserter)-[:PICKS_FROM]->(p)
OPTIONAL MATCH (s)-[:RUNS]->(:Recipe)-[:PRODUCES]->(si:Item)
OPTIONAL MATCH (p)-[:RUNS]->(:Recipe)-[:PRODUCES]->(pi:Item)
OPTIONAL MATCH (s)-[:CONTAINS|PRODUCES]->(ci:Item)
WITH m, i, collect(DISTINCT si.name) + collect(DISTINCT pi.name) + collect(DISTINCT ci.name)
     + [x IN coalesce(s.left, []) + coalesce(s.right, []) | split(x, ':')[0]] AS provides
WITH m, collect({arm: i.unit, provides: provides}) AS arms
MATCH (m)-[:RUNS]->(:Recipe)-[c:CONSUMES]->(it:Item)
WHERE coalesce(it.kind, 'item') <> 'fluid'
RETURN m AS machine, it.name AS item, c.amount AS amount,
       [a IN arms WHERE a.arm IS NOT NULL AND it.name IN a.provides | a.arm] AS feedingArms
ORDER BY m.unit, item`

func ingredientCoverage(ctx context.Context, read memgraphReader, in MemgraphIngredientCoverageInput) (*MemgraphIngredientCoverage, error) {
	if err := validateIngredientCoverageInput(in); err != nil {
		return nil, err
	}
	root, err := traceRoot(ctx, read, validUnit(in.UnitNumber), in.Surface, "Machine")
	if err != nil {
		return nil, err
	}
	units := []any{int64(validUnit(in.UnitNumber))}
	if in.Upstream {
		chain, err := supplyChain(ctx, read, MemgraphSupplyChainInput{
			UnitNumber: in.UnitNumber, Surface: in.Surface, Resolution: MemgraphSupplyResolutionBelt,
			MaxHops: in.MaxHops, MaxPaths: supplyChainMaxPathLimit,
		})
		if err != nil {
			return nil, err
		}
		if chain.Truncated {
			return nil, fmt.Errorf("the upstream trace of unit %s exceeds %d paths, so the machine set is incomplete; lower maxHops or set upstream:false", in.UnitNumber, supplyChainMaxPathLimit)
		}
		var upstream []int
		for _, n := range chain.Nodes {
			if n.Kind == "Machine" && n.UnitNumber != nil && validUnit(*n.UnitNumber) != validUnit(in.UnitNumber) {
				upstream = append(upstream, validUnit(*n.UnitNumber))
			}
		}
		sort.Ints(upstream)
		for _, u := range upstream {
			units = append(units, int64(u))
		}
	}
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: ingredientCoverageCypher,
		Params: map[string]any{"surface": in.Surface, "units": units},
		Limit:  memgraphMaxRows, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, fmt.Errorf("ingredient coverage of unit %s: %w", in.UnitNumber, err)
	}
	out, err := assembleCoverage(root, res.Rows)
	if err != nil {
		return nil, err
	}
	out.Snapshot = res.Snapshot
	return out, nil
}

// assembleCoverage groups ingredient rows by machine, root first and then by
// unit, and lists the ingredients with no feeding arm as one-line gaps.
func assembleCoverage(root *MemgraphTraceNode, rows [][]any) (*MemgraphIngredientCoverage, error) {
	out := &MemgraphIngredientCoverage{Machines: []*MemgraphMachineCoverage{}, Gaps: []string{}}
	byID := map[string]*MemgraphMachineCoverage{}
	for _, row := range rows {
		if len(row) < 4 {
			return nil, fmt.Errorf("coverage row has %d columns, want 4", len(row))
		}
		node, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		item, _ := row[1].(string)
		amount := 0.0
		if f := optionalFloat(row[2]); f != nil {
			amount = *f
		}
		arms := []int{}
		if raw, ok := row[3].([]any); ok {
			for _, a := range raw {
				if u, ok := a.(int64); ok {
					arms = append(arms, int(u))
				}
			}
		}
		sort.Ints(arms)
		mc := byID[node.ID]
		if mc == nil {
			mc = &MemgraphMachineCoverage{Machine: node, Ingredients: []*MemgraphIngredientFeed{}}
			byID[node.ID] = mc
			out.Machines = append(out.Machines, mc)
		}
		covered := len(arms) > 0
		armUnits := make([]string, len(arms))
		for i, a := range arms {
			armUnits[i] = strconv.Itoa(a)
		}
		mc.Ingredients = append(mc.Ingredients, &MemgraphIngredientFeed{Item: item, Amount: amount, FeedingArms: armUnits, Covered: covered})
		if !covered {
			recipe := ""
			if node.Recipe != nil {
				recipe = *node.Recipe
			}
			unit := "?"
			if node.UnitNumber != nil {
				unit = *node.UnitNumber
			}
			out.Gaps = append(out.Gaps, fmt.Sprintf("%s %s: %s has no feeding arm", unit, recipe, item))
		}
	}
	sort.SliceStable(out.Machines, func(i, j int) bool {
		a, b := out.Machines[i].Machine, out.Machines[j].Machine
		if (a.ID == root.ID) != (b.ID == root.ID) {
			return a.ID == root.ID
		}
		return unitOf(a) < unitOf(b)
	})
	return out, nil
}

func unitOf(n *MemgraphTraceNode) int {
	if n.UnitNumber == nil {
		return 0
	}
	return validUnit(*n.UnitNumber)
}

// downstreamHopBlock is the downstream mirror of supplyHopBlock (BELT
// resolution only). From a belt it follows the line forward to every inserter
// picking from it; from a machine or chest it follows the inserters that pick
// from it, directly into the next machine or chest, or onto a belt line that
// is then followed forward; machines dropping straight onto a belt (DROPS_ON)
// are followed the same way. Only belts after the drop point are searched, so
// the order of drops and picks along a line is respected.
const downstreamHopBlock = "WITH cur, chain, paths,\n  CASE WHEN cur IS NULL THEN []\n" +
	"  WHEN cur:Belt THEN\n" +
	"    [p = (cur)-[:BELT_OUT*0..400]->(:Belt)<-[:PICKS_FROM]-(:Inserter)-[:DROPS_TO]->(n) WHERE NOT n.id IN chain | p]\n" +
	"  ELSE\n" +
	"    [p = (cur)<-[:PICKS_FROM]-(:Inserter)-[:DROPS_TO]->(n) WHERE NOT n:Belt AND NOT n.id IN chain | p]\n" +
	"  + [p = (cur)<-[:PICKS_FROM]-(:Inserter)-[:DROPS_TO]->(:Belt)-[:BELT_OUT*0..400]->(:Belt)<-[:PICKS_FROM]-(:Inserter)-[:DROPS_TO]->(n) WHERE NOT n.id IN chain | p]\n" +
	"  + [p = (cur)-[:DROPS_ON]->(:Belt)-[:BELT_OUT*0..400]->(:Belt)<-[:PICKS_FROM]-(:Inserter)-[:DROPS_TO]->(n) WHERE NOT n.id IN chain | p]\n" +
	"  END AS ps\n" +
	"UNWIND (CASE WHEN size(ps) = 0 THEN [NULL] ELSE ps END) AS p\n" +
	"WITH CASE WHEN p IS NULL THEN NULL ELSE last(nodes(p)) END AS cur,\n" +
	"     chain + CASE WHEN p IS NULL THEN [] ELSE [last(nodes(p)).id] END AS chain,\n" +
	"     paths + CASE WHEN p IS NULL THEN [] ELSE [p] END AS paths\n"

func validateDownstreamInput(in MemgraphDownstreamChainInput) error {
	if err := validateTraceTarget(in.UnitNumber, in.Surface); err != nil {
		return err
	}
	if in.MaxHops < 1 || in.MaxHops > supplyChainMaxHopsLimit {
		return fmt.Errorf("maxHops must be between 1 and %d", supplyChainMaxHopsLimit)
	}
	if in.MaxPaths < 1 || in.MaxPaths > supplyChainMaxPathLimit {
		return fmt.Errorf("maxPaths must be between 1 and %d", supplyChainMaxPathLimit)
	}
	return nil
}

// downstreamChainCypher builds the unrolled downstream statement; graph has
// the same meaning as in supplyChainCypher.
func downstreamChainCypher(in MemgraphDownstreamChainInput, graph bool) string {
	unitRef, surfaceRef := "$unit", "$surface"
	if graph {
		unitRef, surfaceRef = strconv.Itoa(validUnit(in.UnitNumber)), strconv.Quote(in.Surface)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MATCH (r {unit: %s, surface: %s}) WHERE r:Machine OR r:Chest OR r:Belt\n", unitRef, surfaceRef)
	b.WriteString("WITH r AS cur, [r.id] AS chain, [] AS paths\n")
	for range in.MaxHops {
		b.WriteString(downstreamHopBlock)
	}
	return writeChainTail(&b, graph)
}

// downstreamChain traces everything downstream of one Machine, Chest or Belt:
// what its output reaches, through inserters and belts, down to the sinks.
func downstreamChain(ctx context.Context, read memgraphReader, in MemgraphDownstreamChainInput) (*MemgraphDownstreamChain, error) {
	if err := validateDownstreamInput(in); err != nil {
		return nil, err
	}
	root, err := traceRoot(ctx, read, validUnit(in.UnitNumber), in.Surface, "Machine", "Chest", "Belt")
	if err != nil {
		return nil, err
	}
	res, err := read(ctx, MemgraphQueryInput{
		Cypher: downstreamChainCypher(in, false), Params: traceParams(validUnit(in.UnitNumber), in.Surface),
		Limit: in.MaxPaths, TimeoutMs: supplyChainTimeoutMs,
	})
	if err != nil {
		return nil, fmt.Errorf("downstream chain of unit %s: %w", in.UnitNumber, err)
	}
	paths, err := decodeSupplyPaths(res.Rows)
	if err != nil {
		return nil, err
	}
	out := assembleDownstreamChain(root, paths)
	out.Truncated = res.Truncated
	out.GraphCypher = downstreamChainCypher(in, true)
	out.DurationMs = res.DurationMs
	out.Snapshot = res.Snapshot
	return out, nil
}

// indefiniteArticle returns "an" before a vowel-initial word, else "a".
func indefiniteArticle(word string) string {
	if word != "" && strings.ContainsRune("AEIOUaeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}
