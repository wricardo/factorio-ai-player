package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
)

// memgraph.cellSummary: groups machines into production cells.
//
// Cell rule (every count below follows it):
//
//   - Nodes are the machines, chests and inserters of the surface plus BELT
//     LINES. A belt line is a connected component of belt tiles over BELT_OUT
//     edges (read undirected, splitters included) and is one node.
//   - An inserter joins what it PICKS_FROM and what it DROPS_TO; a machine joins
//     the belt line it DROPS_ON. A cell is a connected component of that graph.
//   - MAIN BELT BOUNDARY: a belt line from which inserters feed three or more
//     distinct machines (cellMainBeltConsumers) is a main belt. It is not a node:
//     inserters stay with the entity on their other end, but nothing is joined
//     through the line and its tiles belong to no cell.
//   - A component with at least one machine and at least one other member (an
//     inserter, chest or belt line) is a cell. A machine whose component is only
//     itself is a lone machine. Components without a machine are dropped.
//   - Name: flow edges run source -> destination along each inserter and
//     DROPS_ON. The machine reached by the most distinct upstream machines inside
//     the cell is the most downstream one and its recipe names the cell; ties go
//     to the smaller recipe name. When no machine has an upstream machine the
//     most common recipe names the cell, ties by recipe name. A machine without a
//     recipe (a lab) stands in with its entity name.
//   - id is the smallest machine unit; cells sort by machine count descending,
//     then by id ascending.
//
// Topology and exported status only; positions are machine centres.

const (
	cellMainBeltConsumers = 3
	cellMaxLimit          = 500
)

const cellMachinesCypher = `MATCH (m:Machine) WHERE m.surface = $surface RETURN m.unit, m.name, m.recipe, m.status, m.x, m.y ORDER BY m.unit`

// cellInsertersCypher: one row per inserter with the kind and unit of each end;
// an end that is missing comes back null.
const cellInsertersCypher = `MATCH (i:Inserter) WHERE i.surface = $surface
OPTIONAL MATCH (i)-[:PICKS_FROM]->(s)
OPTIONAL MATCH (i)-[:DROPS_TO]->(d)
RETURN i.unit, labels(s), s.unit, labels(d), d.unit ORDER BY i.unit`

const cellDropsOnCypher = `MATCH (m:Machine)-[:DROPS_ON]->(b:Belt) WHERE m.surface = $surface RETURN m.unit, b.unit`

const cellBeltsCypher = `MATCH (b:Belt) WHERE b.surface = $surface RETURN b.unit`

const cellBeltEdgesCypher = `MATCH (a:Belt)-[:BELT_OUT]->(b:Belt) WHERE a.surface = $surface RETURN a.unit, b.unit`

type cellMachine struct {
	Unit   int
	Name   string
	Recipe string
	Status string
	X, Y   *float64
}

type cellEnd struct {
	Kind byte // 'M' machine, 'C' chest, 'B' belt; 0 for anything else or absent
	Unit int
}

type cellInserter struct {
	Unit     int
	Src, Dst cellEnd
}

type cellData struct {
	Machines  []cellMachine
	Inserters []cellInserter
	DropsOn   [][2]int // machine unit, belt unit
	Belts     []int
	BeltEdges [][2]int
}

type cellKey struct {
	Kind byte // 'M', 'C', 'I', 'L' (belt line, id = smallest belt unit)
	ID   int
}

func validateCellSummaryInput(in MemgraphCellSummaryInput) error {
	if !memgraphSurfacePattern.MatchString(in.Surface) {
		return fmt.Errorf("surface %q must be 1-64 letters, digits, '.', '_' or '-'", in.Surface)
	}
	if in.Limit < 1 || in.Limit > cellMaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", cellMaxLimit)
	}
	return nil
}

func cellSummary(ctx context.Context, read memgraphReader, in MemgraphCellSummaryInput) (*MemgraphCellSummary, error) {
	if err := validateCellSummaryInput(in); err != nil {
		return nil, err
	}
	var data cellData
	var snap *MemgraphSnapshotInfo
	truncated := false
	run := func(name, cypher string) ([][]any, error) {
		res, err := read(ctx, MemgraphQueryInput{
			Cypher: cypher, Params: map[string]any{"surface": in.Surface},
			Limit: auditReadRows, TimeoutMs: auditReadTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if res.Truncated {
			truncated = true
		}
		if snap == nil {
			snap = res.Snapshot
		}
		return res.Rows, nil
	}

	rows, err := run("machines", cellMachinesCypher)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if len(r) < 6 {
			return nil, fmt.Errorf("machine row has %d columns, want 6", len(r))
		}
		u, ok := cellUnit(r[0])
		if !ok {
			continue
		}
		m := cellMachine{Unit: u, X: optionalFloat(r[4]), Y: optionalFloat(r[5])}
		m.Name, _ = r[1].(string)
		m.Recipe, _ = r[2].(string)
		m.Status, _ = r[3].(string)
		data.Machines = append(data.Machines, m)
	}

	if rows, err = run("inserters", cellInsertersCypher); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if len(r) < 5 {
			return nil, fmt.Errorf("inserter row has %d columns, want 5", len(r))
		}
		u, ok := cellUnit(r[0])
		if !ok {
			continue
		}
		ins := cellInserter{Unit: u, Src: cellEndOf(r[1], r[2]), Dst: cellEndOf(r[3], r[4])}
		data.Inserters = append(data.Inserters, ins)
	}

	if rows, err = run("drops-on", cellDropsOnCypher); err != nil {
		return nil, err
	}
	data.DropsOn = cellPairs(rows)
	if rows, err = run("belts", cellBeltsCypher); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if len(r) > 0 {
			if u, ok := cellUnit(r[0]); ok {
				data.Belts = append(data.Belts, u)
			}
		}
	}
	if rows, err = run("belt links", cellBeltEdgesCypher); err != nil {
		return nil, err
	}
	data.BeltEdges = cellPairs(rows)

	out := assembleCells(data, in.Limit)
	out.Truncated = out.Truncated || truncated
	out.Snapshot = snap
	return out, nil
}

func cellUnit(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

func cellPairs(rows [][]any) [][2]int {
	var out [][2]int
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		a, okA := cellUnit(r[0])
		b, okB := cellUnit(r[1])
		if okA && okB {
			out = append(out, [2]int{a, b})
		}
	}
	return out
}

func cellEndOf(labels, unit any) cellEnd {
	u, ok := cellUnit(unit)
	if !ok {
		return cellEnd{}
	}
	var names []string
	switch l := labels.(type) {
	case []any:
		for _, e := range l {
			if s, ok := e.(string); ok {
				names = append(names, s)
			}
		}
	case []string:
		names = l
	}
	for _, n := range names {
		switch n {
		case "Machine":
			return cellEnd{'M', u}
		case "Chest":
			return cellEnd{'C', u}
		case "Belt":
			return cellEnd{'B', u}
		}
	}
	return cellEnd{}
}

// cellUnionFind is a map-backed disjoint set keyed by cellKey.
type cellUnionFind struct{ parent map[cellKey]cellKey }

func (u *cellUnionFind) find(k cellKey) cellKey {
	p, ok := u.parent[k]
	if !ok {
		u.parent[k] = k
		return k
	}
	if p == k {
		return k
	}
	r := u.find(p)
	u.parent[k] = r
	return r
}

func (u *cellUnionFind) union(a, b cellKey) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}

// assembleCells is the pure part: it applies the rule documented at the top of
// the file to the decoded graph.
func assembleCells(data cellData, limit int) *MemgraphCellSummary {
	// Belt lines: components over BELT_OUT. A belt seen only through an
	// inserter, DROPS_ON or edge still counts as a tile.
	belts := cellUnionFind{parent: map[cellKey]cellKey{}}
	tile := func(u int) cellKey {
		k := cellKey{'B', u}
		belts.find(k)
		return k
	}
	for _, u := range data.Belts {
		tile(u)
	}
	for _, e := range data.BeltEdges {
		belts.union(tile(e[0]), tile(e[1]))
	}
	for _, ins := range data.Inserters {
		for _, end := range []cellEnd{ins.Src, ins.Dst} {
			if end.Kind == 'B' {
				tile(end.Unit)
			}
		}
	}
	for _, d := range data.DropsOn {
		tile(d[1])
	}
	// Smallest unit names a line; tiles are counted per line.
	lineID := map[cellKey]int{}
	for k := range belts.parent {
		r := belts.find(k)
		if cur, ok := lineID[r]; !ok || k.ID < cur {
			lineID[r] = k.ID
		}
	}
	lineOf := func(u int) cellKey { return cellKey{'L', lineID[belts.find(cellKey{'B', u})]} }
	lineTiles := map[cellKey]int{}
	for k := range belts.parent {
		lineTiles[lineOf(k.ID)]++
	}

	// Main belts: lines whose inserters feed >= cellMainBeltConsumers machines.
	feeds := map[cellKey]map[int]bool{}
	for _, ins := range data.Inserters {
		if ins.Src.Kind == 'B' && ins.Dst.Kind == 'M' {
			l := lineOf(ins.Src.Unit)
			if feeds[l] == nil {
				feeds[l] = map[int]bool{}
			}
			feeds[l][ins.Dst.Unit] = true
		}
	}
	main := map[cellKey]bool{}
	for l, ms := range feeds {
		if len(ms) >= cellMainBeltConsumers {
			main[l] = true
		}
	}
	node := func(e cellEnd) (cellKey, bool) {
		switch e.Kind {
		case 'M', 'C':
			return cellKey{e.Kind, e.Unit}, true
		case 'B':
			l := lineOf(e.Unit)
			return l, !main[l]
		}
		return cellKey{}, false
	}

	uf := cellUnionFind{parent: map[cellKey]cellKey{}}
	flow := map[cellKey][]cellKey{} // destination -> sources
	link := func(from, to cellKey) {
		uf.union(from, to)
		flow[to] = append(flow[to], from)
	}
	machineByUnit := map[int]*cellMachine{}
	for i := range data.Machines {
		m := &data.Machines[i]
		machineByUnit[m.Unit] = m
		uf.find(cellKey{'M', m.Unit})
	}
	for _, ins := range data.Inserters {
		ik := cellKey{'I', ins.Unit}
		uf.find(ik)
		src, okS := node(ins.Src)
		dst, okD := node(ins.Dst)
		if okS {
			uf.union(ik, src)
		}
		if okD {
			uf.union(ik, dst)
		}
		if okS && okD {
			flow[dst] = append(flow[dst], src)
		}
	}
	for _, d := range data.DropsOn {
		if m, ok := node(cellEnd{'M', d[0]}); ok {
			if l, ok := node(cellEnd{'B', d[1]}); ok {
				link(m, l)
			}
		}
	}

	type group struct {
		machines  []*cellMachine
		inserters int
		chests    int
		belts     int
		other     bool // any member besides the machines themselves
	}
	groups := map[cellKey]*group{}
	get := func(k cellKey) *group {
		r := uf.find(k)
		g := groups[r]
		if g == nil {
			g = &group{}
			groups[r] = g
		}
		return g
	}
	for k := range uf.parent {
		switch k.Kind {
		case 'M':
			if m := machineByUnit[k.ID]; m != nil {
				g := get(k)
				g.machines = append(g.machines, m)
			}
		case 'I':
			g := get(k)
			g.inserters++
			g.other = true
		case 'C':
			g := get(k)
			g.chests++
			g.other = true
		case 'L':
			g := get(k)
			g.belts += lineTiles[k]
			g.other = true
		}
	}

	out := &MemgraphCellSummary{Cells: []*MemgraphCell{}}
	var cells []*MemgraphCell
	for _, g := range groups {
		if len(g.machines) == 0 {
			continue
		}
		if !g.other {
			out.LoneMachines += len(g.machines)
			continue
		}
		cells = append(cells, assembleCell(g.machines, g.inserters, g.chests, g.belts, flow))
	}
	sort.Slice(cells, func(i, j int) bool {
		if cells[i].MachineCount != cells[j].MachineCount {
			return cells[i].MachineCount > cells[j].MachineCount
		}
		a, _ := strconv.Atoi(cells[i].ID)
		b, _ := strconv.Atoi(cells[j].ID)
		return a < b
	})

	stalled := 0
	for _, c := range cells {
		if c.StalledCount > 0 {
			stalled++
		}
	}
	total := len(cells)
	if total > limit {
		cells = cells[:limit]
		out.Truncated = true
	}
	out.Cells = append(out.Cells, cells...)
	switch {
	case total == 0:
		out.Summary = fmt.Sprintf("no production cells, %s", cellPlural(out.LoneMachines, "lone machine"))
	default:
		out.Summary = fmt.Sprintf("%s (%d with stalled machines), %s; largest: %s (%s)",
			cellPlural(total, "cell"), stalled, cellPlural(out.LoneMachines, "lone machine"), out.Cells[0].Name, cellPlural(out.Cells[0].MachineCount, "machine"))
	}
	return out
}

func cellLabel(m *cellMachine) string {
	if m.Recipe != "" {
		return m.Recipe
	}
	return m.Name
}

func assembleCell(machines []*cellMachine, inserters, chests, belts int, flow map[cellKey][]cellKey) *MemgraphCell {
	sort.Slice(machines, func(i, j int) bool { return machines[i].Unit < machines[j].Unit })
	c := &MemgraphCell{
		ID: strconv.Itoa(machines[0].Unit), MachineCount: len(machines),
		InserterCount: inserters, ChestCount: chests, BeltCount: belts,
		Recipes: []string{},
	}
	recipes := map[string]bool{}
	statuses := map[string]int{}
	var box [4]float64
	boxed := false
	inCell := map[cellKey]bool{}
	for _, m := range machines {
		inCell[cellKey{'M', m.Unit}] = true
		if m.Recipe != "" {
			recipes[m.Recipe] = true
		}
		st := m.Status
		if st == "" {
			st = "unknown"
		}
		statuses[st]++
		if st != "working" {
			c.StalledCount++
		}
		if m.X != nil && m.Y != nil {
			if !boxed {
				box = [4]float64{*m.X, *m.Y, *m.X, *m.Y}
				boxed = true
			}
			box[0], box[1] = min(box[0], *m.X), min(box[1], *m.Y)
			box[2], box[3] = max(box[2], *m.X), max(box[3], *m.Y)
		}
	}
	for r := range recipes {
		c.Recipes = append(c.Recipes, r)
	}
	sort.Strings(c.Recipes)
	c.StatusCounts = stretchItemCounts(statuses)
	c.LeftTopX, c.LeftTopY, c.RightBottomX, c.RightBottomY = box[0], box[1], box[2], box[3]
	c.Name = cellName(machines, flow)
	return c
}

// cellName implements the naming rule: the machine with the most distinct
// upstream machines, else the most common recipe.
func cellName(machines []*cellMachine, flow map[cellKey][]cellKey) string {
	best, bestUp := (*cellMachine)(nil), 0
	for _, m := range machines {
		up := cellUpstreamMachines(cellKey{'M', m.Unit}, flow)
		if up == 0 {
			continue
		}
		if best == nil || up > bestUp || (up == bestUp && cellLabel(m) < cellLabel(best)) {
			best, bestUp = m, up
		}
	}
	if best != nil {
		return cellLabel(best)
	}
	common := map[string]int{}
	for _, m := range machines {
		common[cellLabel(m)]++
	}
	name, n := "", 0
	for l, c := range common {
		if c > n || (c == n && l < name) {
			name, n = l, c
		}
	}
	return name
}

// cellUpstreamMachines counts the distinct machines (other than start) that can
// reach start along flow edges.
func cellUpstreamMachines(start cellKey, flow map[cellKey][]cellKey) int {
	seen := map[cellKey]bool{start: true}
	queue := []cellKey{start}
	machines := 0
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		for _, s := range flow[k] {
			if seen[s] {
				continue
			}
			seen[s] = true
			if s.Kind == 'M' {
				machines++
			}
			queue = append(queue, s)
		}
	}
	return machines
}

func cellPlural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
