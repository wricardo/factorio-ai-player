package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// factoryHealth sweeps the whole surface for the defects rootCause reports for
// one machine (NO_FEEDING_ARM, ORIGIN_BELT, MACHINE_TROUBLE), plus the
// NO_CONSUMER stretches of beltStretches, and ranks them by how many machines
// sit downstream of the defect.
//
// It does not call rootCause per machine. The graph is read in a handful of
// set-based statements (machines, troubled chests, inserter pairs, belt links,
// drop-on links, ingredient coverage in chunks, stretches) and the supply and
// downstream closures run in Go over those edges:
//
//   - NO_FEEDING_ARM is ingredientCoverageCypher's verdict (a recipe
//     ingredient no inserter feeding the machine can provide).
//   - MACHINE_TROUBLE is rootCauseFind's status rule: a Machine or Chest whose
//     status is in supplyTroubleStatuses, except item_ingredient_shortage (a
//     symptom of something upstream).
//   - ORIGIN_BELT is a belt that supplyChain would end a path at: reached
//     upstream from a Machine, and no inserter picks anything onto its line.
//   - Impact is the number of Machines downstream of the node, by
//     downstreamChain's rules (a belt line forward to the inserters picking from
//     it; a supplier through its picking inserters; a machine dropping onto a
//     belt), without downstreamChain's hop cap.
//
// Approximations: a machine dropping onto a belt line (DROPS_ON) counts as
// feeding it, which supplyChain does not follow, so drill-fed belts are not
// reported as origins; ORIGIN_BELT is reported once per unfed line, at its head
// belt, not once per belt an arm happens to pick from.

const (
	factoryHealthRelated        = 3
	factoryHealthCoverageChunk  = 100
	factoryHealthMachinesCypher = `MATCH (m:Machine) WHERE m.surface = $surface RETURN m ORDER BY m.unit`
	factoryHealthChestsCypher   = `MATCH (c:Chest) WHERE c.surface = $surface AND c.status IN $statuses RETURN c ORDER BY c.unit`
	// One row per distinct (source, destination) pair an inserter links.
	factoryHealthFlowsCypher = `MATCH (s)<-[:PICKS_FROM]-(i:Inserter)-[:DROPS_TO]->(d) WHERE i.surface = $surface
RETURN DISTINCT s.id AS src, labels(s) AS srcLabels, d.id AS dst, labels(d) AS dstLabels`
	factoryHealthBeltLinksCypher = `MATCH (a:Belt)-[:BELT_OUT]->(b:Belt) WHERE a.surface = $surface RETURN a.id AS beltFrom, b.id AS beltTo`
	factoryHealthDropsOnCypher   = `MATCH (x)-[:DROPS_ON]->(b:Belt) WHERE x.surface = $surface
RETURN DISTINCT x.id AS producer, labels(x) AS producerLabels, b.id AS onBelt`
	factoryHealthBeltsCypher = `MATCH (b:Belt) WHERE b.surface = $surface AND b.id IN $ids RETURN b ORDER BY b.unit`
)

// healthGraph is the item-flow subgraph of one surface, keyed by node id.
type healthGraph struct {
	kind      map[string]string
	beltNext  map[string][]string // belt -> next belts (BELT_OUT)
	beltPrev  map[string][]string
	flowDst   map[string][]string // source -> destinations of inserters picking from it
	flowSrc   map[string][]string // destination -> sources of inserters dropping into it
	dropsOn   map[string][]string // producer -> belts it drops onto
	droppedBy map[string][]string // belt -> producers dropping onto it
}

func newHealthGraph() *healthGraph {
	return &healthGraph{
		kind: map[string]string{}, beltNext: map[string][]string{}, beltPrev: map[string][]string{},
		flowDst: map[string][]string{}, flowSrc: map[string][]string{}, dropsOn: map[string][]string{}, droppedBy: map[string][]string{},
	}
}

func appendUnique(m map[string][]string, k, v string) {
	for _, x := range m[k] {
		if x == v {
			return
		}
	}
	m[k] = append(m[k], v)
}

func (g *healthGraph) sortEdges() {
	for _, m := range []map[string][]string{g.beltNext, g.beltPrev, g.flowDst, g.flowSrc, g.dropsOn, g.droppedBy} {
		for _, v := range m {
			sort.Strings(v)
		}
	}
}

// labelKind is the node kind of a labels() value: the first label that is not
// the shared Entity label.
func labelKind(v any) string {
	switch labels := v.(type) {
	case []any:
		for _, l := range labels {
			if s, ok := l.(string); ok && s != "Entity" {
				return s
			}
		}
	case []string:
		for _, s := range labels {
			if s != "Entity" {
				return s
			}
		}
	}
	return ""
}

// downstream lists the Machines downstream of id, nearest first (breadth first,
// ids sorted within a depth), id itself excluded.
func (g *healthGraph) downstream(id string) []string {
	seen := map[string]bool{id: true}
	queue := []string{id}
	var machines []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		var next []string
		if g.kind[cur] == "Belt" {
			next = append(next, g.beltNext[cur]...)
		}
		next = append(next, g.flowDst[cur]...)
		next = append(next, g.dropsOn[cur]...)
		sort.Strings(next)
		for _, n := range next {
			if seen[n] {
				continue
			}
			seen[n] = true
			queue = append(queue, n)
			if g.kind[n] == "Machine" {
				machines = append(machines, n)
			}
		}
	}
	return machines
}

// line is every belt that can feed x along BELT_OUT, x included.
func (g *healthGraph) line(x string) []string {
	seen := map[string]bool{x: true}
	out := []string{x}
	for i := 0; i < len(out); i++ {
		for _, p := range g.beltPrev[out[i]] {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// feeders are the nodes supplyChain would step to from x. A belt is fed by
// whatever inserters pick from onto any tile of its line (and, as an
// approximation, machines dropping onto it); anything else by the sources of
// the inserters dropping into it. The head belt of the line comes second.
func (g *healthGraph) feeders(x string) (feeders []string, head string) {
	if g.kind[x] != "Belt" {
		return g.flowSrc[x], ""
	}
	line := g.line(x)
	seen := map[string]bool{x: true}
	for _, t := range line {
		for _, src := range g.flowSrc[t] {
			if !seen[src] {
				seen[src] = true
				feeders = append(feeders, src)
			}
		}
		for _, p := range g.droppedBy[t] {
			if !seen[p] {
				seen[p] = true
				feeders = append(feeders, p)
			}
		}
	}
	sort.Strings(feeders)
	for _, t := range line {
		if len(g.beltPrev[t]) == 0 && (head == "" || t < head) {
			head = t
		}
	}
	if head == "" {
		head = line[0]
		for _, t := range line {
			if t < head {
				head = t
			}
		}
	}
	return feeders, head
}

// originHeads walks upstream from every machine and returns the head belt of
// each belt line nothing feeds, sorted.
func (g *healthGraph) originHeads(machines []string) []string {
	seen := map[string]bool{}
	queue := append([]string(nil), machines...)
	for _, m := range machines {
		seen[m] = true
	}
	heads := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		fs, head := g.feeders(cur)
		if g.kind[cur] == "Belt" && len(fs) == 0 {
			heads[head] = true
		}
		for _, f := range fs {
			if !seen[f] {
				seen[f] = true
				queue = append(queue, f)
			}
		}
	}
	out := make([]string, 0, len(heads))
	for h := range heads {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func factoryHealthTroubleStatuses() []any {
	out := []any{}
	for s := range supplyTroubleStatuses {
		if s != "item_ingredient_shortage" {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
	return out
}

type healthReads struct {
	read      memgraphReader
	ctx       context.Context
	params    map[string]any
	truncated bool
	snapshot  *MemgraphSnapshotInfo
}

func (h *healthReads) run(what, cypher string, params map[string]any) ([][]any, error) {
	res, err := h.read(h.ctx, MemgraphQueryInput{Cypher: cypher, Params: params, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
	if err != nil {
		return nil, fmt.Errorf("factory health: %s: %w", what, err)
	}
	if res.Truncated {
		h.truncated = true
	}
	if h.snapshot == nil {
		h.snapshot = res.Snapshot
	}
	return res.Rows, nil
}

func factoryHealth(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	h := &healthReads{read: read, ctx: ctx}
	params := map[string]any{"surface": in.Surface}

	g := newHealthGraph()
	nodes := map[string]*MemgraphTraceNode{}
	var machines, troubled []*MemgraphTraceNode

	rows, err := h.run("machines", factoryHealthMachinesCypher, params)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if len(row) < 1 {
			return nil, fmt.Errorf("factory health: machine row is empty")
		}
		n, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		nodes[n.ID], g.kind[n.ID] = n, "Machine"
		machines = append(machines, n)
		troubled = append(troubled, n)
	}
	rows, err = h.run("chests", factoryHealthChestsCypher, map[string]any{"surface": in.Surface, "statuses": factoryHealthTroubleStatuses()})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if len(row) < 1 {
			return nil, fmt.Errorf("factory health: chest row is empty")
		}
		n, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		nodes[n.ID], g.kind[n.ID] = n, "Chest"
		troubled = append(troubled, n)
	}

	rows, err = h.run("inserter links", factoryHealthFlowsCypher, params)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if len(row) < 4 {
			return nil, fmt.Errorf("factory health: inserter link row has %d columns, want 4", len(row))
		}
		src, _ := row[0].(string)
		dst, _ := row[2].(string)
		if src == "" || dst == "" {
			continue
		}
		g.kind[src], g.kind[dst] = labelKind(row[1]), labelKind(row[3])
		appendUnique(g.flowDst, src, dst)
		appendUnique(g.flowSrc, dst, src)
	}
	rows, err = h.run("belt links", factoryHealthBeltLinksCypher, params)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("factory health: belt link row has %d columns, want 2", len(row))
		}
		a, _ := row[0].(string)
		b, _ := row[1].(string)
		if a == "" || b == "" {
			continue
		}
		g.kind[a], g.kind[b] = "Belt", "Belt"
		appendUnique(g.beltNext, a, b)
		appendUnique(g.beltPrev, b, a)
	}
	rows, err = h.run("drop-on links", factoryHealthDropsOnCypher, params)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if len(row) < 3 {
			return nil, fmt.Errorf("factory health: drop-on row has %d columns, want 3", len(row))
		}
		p, _ := row[0].(string)
		b, _ := row[2].(string)
		if p == "" || b == "" {
			continue
		}
		if g.kind[p] == "" {
			g.kind[p] = labelKind(row[1])
		}
		g.kind[b] = "Belt"
		appendUnique(g.dropsOn, p, b)
		appendUnique(g.droppedBy, b, p)
	}
	g.sortEdges()

	impacts := map[string][]string{}
	downstream := func(id string) []string {
		d, ok := impacts[id]
		if !ok {
			d = g.downstream(id)
			impacts[id] = d
		}
		return d
	}
	var findings []*MemgraphAuditFinding
	add := func(code string, sev MemgraphAuditSeverity, node *MemgraphTraceNode, msg string) {
		down := downstream(node.ID)
		related := []*MemgraphTraceNode{}
		for _, id := range down {
			if len(related) == factoryHealthRelated {
				break
			}
			if n := nodes[id]; n != nil {
				related = append(related, n)
			}
		}
		impact := len(down)
		findings = append(findings, &MemgraphAuditFinding{
			Code: code, Severity: sev, Node: node, Message: msg, Related: related, Impact: &impact,
		})
	}

	// MACHINE_TROUBLE: rootCauseFind's status rule over every supplier.
	for _, n := range troubled {
		if n.Status != nil && supplyTroubleStatuses[*n.Status] && *n.Status != "item_ingredient_shortage" {
			add("MACHINE_TROUBLE", MemgraphAuditSeverityBlocker, n,
				fmt.Sprintf("%s has status %s", traceLabel(n, false), *n.Status))
		}
	}

	// NO_FEEDING_ARM: ingredientCoverageCypher over every machine, in chunks.
	units := make([]any, 0, len(machines))
	for _, m := range machines {
		if u := unitOf(m); u > 0 {
			units = append(units, int64(u))
		}
	}
	for start := 0; start < len(units); start += factoryHealthCoverageChunk {
		end := min(start+factoryHealthCoverageChunk, len(units))
		rows, err := h.run("ingredient coverage", ingredientCoverageCypher, map[string]any{"surface": in.Surface, "units": units[start:end]})
		if err != nil {
			return nil, err
		}
		cov, err := assembleCoverage(&MemgraphTraceNode{}, rows)
		if err != nil {
			return nil, err
		}
		for _, mc := range cov.Machines {
			node := nodes[mc.Machine.ID]
			if node == nil {
				node = mc.Machine
			}
			for _, ing := range mc.Ingredients {
				if !ing.Covered {
					add("NO_FEEDING_ARM", MemgraphAuditSeverityBlocker, node,
						fmt.Sprintf("%s: %s has no feeding arm", traceLabel(node, false), ing.Item))
				}
			}
		}
	}

	// ORIGIN_BELT: unfed belt lines reached upstream from a machine.
	machineIDs := make([]string, len(machines))
	for i, m := range machines {
		machineIDs[i] = m.ID
	}
	if heads := g.originHeads(machineIDs); len(heads) > 0 {
		ids := make([]any, len(heads))
		for i, id := range heads {
			ids[i] = id
		}
		rows, err := h.run("origin belts", factoryHealthBeltsCypher, map[string]any{"surface": in.Surface, "ids": ids})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if len(row) < 1 {
				return nil, fmt.Errorf("factory health: belt row is empty")
			}
			b, err := traceNodeFromAny(row[0])
			if err != nil {
				return nil, err
			}
			nodes[b.ID] = b
		}
		for _, id := range heads {
			b := nodes[id]
			if b == nil {
				continue
			}
			add("ORIGIN_BELT", MemgraphAuditSeverityConcern, b,
				fmt.Sprintf("the belt line starting at %s is fed by no inserter or machine, yet %d machine(s) downstream take from it", traceLabel(b, false), len(downstream(id))))
		}
	}

	// NO_CONSUMER: stretches carrying items that nothing picks from or flows on.
	stretches, snap, stretchTruncated, err := loadStretches(ctx, read, MemgraphBeltStretchesInput{Surface: in.Surface})
	if err != nil {
		return nil, err
	}
	if h.snapshot == nil {
		h.snapshot = snap
	}
	zero := 0
	for _, s := range stretches {
		if !s.noConsumer() {
			continue
		}
		name := "belt-stretch"
		node := &MemgraphTraceNode{ID: s.ID, Kind: "BeltStretch", Name: &name, X: s.X, Y: s.Y}
		items := make([]string, 0, len(s.tileItems))
		for _, c := range stretchItemCounts(s.tileItems) {
			items = append(items, c.Name+":"+strconv.Itoa(c.Count))
		}
		findings = append(findings, &MemgraphAuditFinding{
			Code: "NO_CONSUMER", Severity: MemgraphAuditSeverityConcern, Node: node, Related: []*MemgraphTraceNode{}, Impact: &zero,
			Message: fmt.Sprintf("%s carries %s on %d tiles; no inserter picks from it and it flows into no other stretch",
				traceLabel(node, false), strings.Join(items, ", "), s.Tiles),
		})
	}

	if findings == nil {
		findings = []*MemgraphAuditFinding{}
	}
	return finishAudit(findings, in.Limit, h.truncated || stretchTruncated, h.snapshot), nil
}
