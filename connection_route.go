package main

import (
	"container/heap"
	"math"
)

// Cost units of one planned step. They are intentionally coarse: they rank
// routes (fewer new tiles, fewer bends, tunnels only when they save tiles), not
// real item prices.
const (
	routeCostTile     = 1.0
	routeCostExisting = 0.25
	routeCostBend     = 0.25
	routeCostTunnel   = 2.0
)

// routeDirs are N, E, S, W as (dx, dy) tile offsets; engine direction is 4*index.
var routeDirs = [4][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}}

// routeGrid is the engine state the search needs for one bounded region. Index
// is (y-top)*cols + (x-left). Every slice has cols*rows entries except where
// noted.
type routeGrid struct {
	left, top  int
	cols, rows int
	// open is true when the route entity can be placed on the tile.
	open []bool
	// openTunnel is true when an underground half can be placed; nil disables
	// tunnel placement checks (every open tile counts).
	openTunnel []bool
	// fluid is true when a pipe-like entity touches the tile.
	fluid []bool
	// tunnel is true when an underground belt, linked belt or pipe-to-ground
	// sits on the tile; spans may not pass under one.
	tunnel []bool
	// avoid is true when the caller forbids entities on the tile.
	avoid []bool
	// existing maps a tile to the direction index 0..3 of a same-name belt, or
	// routeExistingPipe for a pipe.
	existing map[int]int
}

const routeExistingPipe = -1

func newRouteGrid(left, top, cols, rows int) *routeGrid {
	n := cols * rows
	return &routeGrid{
		left: left, top: top, cols: cols, rows: rows,
		open: make([]bool, n), fluid: make([]bool, n), tunnel: make([]bool, n), avoid: make([]bool, n),
		existing: map[int]int{},
	}
}

func (g *routeGrid) index(x, y int) int {
	if x < g.left || y < g.top || x >= g.left+g.cols || y >= g.top+g.rows {
		return -1
	}
	return (y-g.top)*g.cols + (x - g.left)
}

func (g *routeGrid) xy(index int) (int, int) {
	return g.left + index%g.cols, g.top + index/g.cols
}

type routeParams struct {
	pipe           bool
	fromX, fromY   int
	toX, toY       int
	allowTunnel    bool
	maxTunnel      int
	preferExisting bool
	allowAdjacency bool
	maxExpanded    int
}

type routeStep struct {
	x, y      int
	direction int // engine direction 0..15
	existing  bool
	tunnel    string // "", "input" or "output"
	cost      float64
}

type routeResult struct {
	steps    []routeStep
	expanded int
	cost     float64
	stop     ConnectionStopReason
}

// routeState identifies a search node: the tile, the direction of the move that
// reached it (4 means none, only for the start), and whether it is the exit half
// of an underground span. terminal marks a goal arrival whose role cost is
// already charged.
type routeState struct {
	index    int
	arrival  int
	exit     bool
	terminal bool
}

type routeVia struct {
	parent routeState
	kind   int // 0 start, 1 normal move, 2 tunnel jump
	dir    int // direction index of the move out of parent
	span   int // tile offset of a tunnel jump
	cost   float64
}

type routeItem struct {
	state routeState
	g, f  float64
	seq   int
}

type routeHeap []routeItem

func (h routeHeap) Len() int { return len(h) }
func (h routeHeap) Less(i, j int) bool {
	if h[i].f != h[j].f {
		return h[i].f < h[j].f
	}
	if h[i].g != h[j].g {
		return h[i].g > h[j].g
	}
	return h[i].seq < h[j].seq
}
func (h routeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *routeHeap) Push(x any)   { *h = append(*h, x.(routeItem)) }
func (h *routeHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func opposite(dir int) int { return (dir + 2) % 4 }

type router struct {
	grid   *routeGrid
	params routeParams
	from   int
	to     int
	hScale float64
}

// routeAStar finds the cheapest legal route inside the grid. The search is
// deterministic: neighbours are expanded in a fixed order and ties break by
// insertion order.
func routeAStar(grid *routeGrid, params routeParams) routeResult {
	r := &router{grid: grid, params: params, from: grid.index(params.fromX, params.fromY), to: grid.index(params.toX, params.toY)}
	if r.from < 0 || r.to < 0 {
		return routeResult{stop: ConnectionStopReasonEndpointBlocked}
	}
	if !r.endpointUsable(r.from) || !r.endpointUsable(r.to) {
		return routeResult{stop: ConnectionStopReasonEndpointBlocked}
	}
	if r.from == r.to {
		cost, ok := r.normalCost(r.from, 4, 4, true)
		if !ok {
			return routeResult{stop: ConnectionStopReasonEndpointBlocked}
		}
		_, existing := r.grid.existing[r.from]
		step := routeStep{x: params.fromX, y: params.fromY, existing: existing, cost: cost}
		return routeResult{steps: []routeStep{step}, cost: cost, stop: ConnectionStopReasonFound}
	}
	r.hScale = 1
	if params.preferExisting {
		r.hScale = routeCostExisting
	}
	if params.allowTunnel && params.maxTunnel >= 2 {
		if scale := 2 * routeCostTunnel / float64(params.maxTunnel); scale < r.hScale {
			r.hScale = scale
		}
	}

	start := routeState{index: r.from, arrival: 4}
	best := map[routeState]float64{start: 0}
	via := map[routeState]routeVia{start: {kind: 0}}
	queue := &routeHeap{}
	seq := 0
	push := func(state routeState, g float64) {
		seq++
		heap.Push(queue, routeItem{state: state, g: g, f: g + r.heuristic(state.index), seq: seq})
	}
	push(start, 0)

	expanded := 0
	for queue.Len() > 0 {
		item := heap.Pop(queue).(routeItem)
		if g, ok := best[item.state]; !ok || item.g > g {
			continue
		}
		if item.state.terminal {
			return routeResult{steps: r.reconstruct(item.state, via), expanded: expanded, cost: item.g, stop: ConnectionStopReasonFound}
		}
		if expanded >= params.maxExpanded {
			return routeResult{expanded: expanded, stop: ConnectionStopReasonExpansionLimit}
		}
		expanded++
		r.expand(item.state, item.g, best, via, push)
	}
	return routeResult{expanded: expanded, stop: ConnectionStopReasonNoPath}
}

func (r *router) heuristic(index int) float64 {
	x, y := r.grid.xy(index)
	return r.hScale * float64(abs(x-r.params.toX)+abs(y-r.params.toY))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// endpointUsable reports whether any role could occupy the endpoint tile.
func (r *router) endpointUsable(index int) bool {
	if r.grid.avoid[index] {
		return false
	}
	if _, ok := r.grid.existing[index]; ok {
		return true
	}
	if r.grid.open[index] {
		return true
	}
	return r.params.allowTunnel && r.tunnelOpen(index)
}

func (r *router) tunnelOpen(index int) bool {
	if r.grid.avoid[index] {
		return false
	}
	if _, ok := r.grid.existing[index]; ok {
		return false
	}
	if r.grid.openTunnel != nil {
		return r.grid.openTunnel[index]
	}
	return r.grid.open[index]
}

func (r *router) isEndpoint(index int) bool { return index == r.from || index == r.to }

// foreignFluidAt reports a pipe-like entity on a neighbour tile that is not an
// endpoint the route intends to join.
func (r *router) foreignFluidAt(x, y int) bool {
	index := r.grid.index(x, y)
	if index < 0 || !r.grid.fluid[index] {
		return false
	}
	return !(r.isEndpoint(index) && r.grid.existing[index] == routeExistingPipe && r.hasExisting(index))
}

func (r *router) hasExisting(index int) bool {
	_, ok := r.grid.existing[index]
	return ok
}

// pipeSidesClear is true when no foreign pipe-like entity touches a regular
// pipe tile.
func (r *router) pipeSidesClear(index int) bool {
	if !r.params.pipe || r.params.allowAdjacency {
		return true
	}
	x, y := r.grid.xy(index)
	for _, d := range routeDirs {
		if r.foreignFluidAt(x+d[0], y+d[1]) {
			return false
		}
	}
	return true
}

// faceClear is true when the tile a pipe-to-ground half connects to on the
// surface holds no foreign pipe-like entity. Only that one side connects.
func (r *router) faceClear(index, dir int) bool {
	if !r.params.pipe || r.params.allowAdjacency {
		return true
	}
	x, y := r.grid.xy(index)
	return !r.foreignFluidAt(x+routeDirs[dir][0], y+routeDirs[dir][1])
}

// normalCost returns the cost of using the tile as an ordinary route tile when
// leaving it along dOut (or arriving along dIn for the goal), and whether it is
// legal.
func (r *router) normalCost(index, arrival, out int, goal bool) (float64, bool) {
	if r.grid.avoid[index] {
		return 0, false
	}
	endpoint := r.isEndpoint(index)
	if dir, ok := r.grid.existing[index]; ok {
		reuse := endpoint || r.params.preferExisting
		if r.params.pipe {
			reuse = endpoint || (r.params.preferExisting && r.params.allowAdjacency)
		}
		if !reuse {
			return 0, false
		}
		if !r.params.pipe {
			switch {
			case index == r.from && index == r.to:
				// Single-tile route: the existing belt already satisfies it.
			case goal && index == r.to && index != r.from:
				// The route ends by feeding this belt; only a head-on belt is wrong.
				if arrival < 4 && dir == opposite(arrival) {
					return 0, false
				}
			case dir != out:
				return 0, false
			}
		}
		return routeCostExisting, true
	}
	if !r.grid.open[index] || !r.pipeSidesClear(index) {
		return 0, false
	}
	cost := routeCostTile
	if arrival < 4 && out < 4 && arrival != out {
		cost += routeCostBend
	}
	return cost, true
}

func (r *router) entranceOK(index, out, arrival int) bool {
	if !r.params.allowTunnel || !r.tunnelOpen(index) {
		return false
	}
	if arrival != 4 && arrival != out {
		return false
	}
	// A pipe-to-ground half connects on its surface face, the side behind the span.
	if r.params.pipe && arrival == 4 && !r.faceClear(index, opposite(out)) {
		return false
	}
	return true
}

func (r *router) exitOK(index, out int, goal bool) bool {
	if !r.tunnelOpen(index) {
		return false
	}
	if goal && r.params.pipe && !r.faceClear(index, out) {
		return false
	}
	return true
}

func (r *router) expand(state routeState, g float64, best map[routeState]float64, via map[routeState]routeVia, push func(routeState, float64)) {
	x, y := r.grid.xy(state.index)
	relax := func(next routeState, cost float64, v routeVia) {
		total := g + cost
		if prev, ok := best[next]; ok && prev <= total {
			return
		}
		best[next] = total
		v.parent = state
		v.cost = cost
		via[next] = v
		push(next, total)
	}

	// The exit half of a span outputs straight ahead, so it may only continue
	// along the span's direction; everything else may turn.
	for dir := range 4 {
		if state.exit && dir != state.arrival {
			continue
		}
		if state.arrival < 4 && dir == opposite(state.arrival) {
			continue
		}
		nx, ny := x+routeDirs[dir][0], y+routeDirs[dir][1]
		next := r.grid.index(nx, ny)
		if next < 0 {
			continue
		}
		var cost float64
		if state.exit {
			cost = routeCostTunnel
		} else {
			c, ok := r.normalCost(state.index, state.arrival, dir, false)
			if !ok {
				continue
			}
			cost = c
		}
		if next == r.to {
			// Arriving at the goal also pays the goal tile's own cost now.
			goalCost, ok := r.normalCost(next, dir, dir, true)
			if !ok {
				continue
			}
			relax(routeState{index: next, arrival: dir, terminal: true}, cost+goalCost, routeVia{kind: 1, dir: dir})
			continue
		}
		relax(routeState{index: next, arrival: dir}, cost, routeVia{kind: 1, dir: dir})
	}

	if !r.params.allowTunnel || state.exit {
		return
	}
	for dir := range 4 {
		if state.arrival < 4 && dir != state.arrival {
			continue
		}
		if !r.entranceOK(state.index, dir, state.arrival) {
			continue
		}
		if _, ok := r.grid.existing[state.index]; ok {
			continue
		}
		for span := 2; span <= r.params.maxTunnel; span++ {
			ex, ey := x+routeDirs[dir][0]*span, y+routeDirs[dir][1]*span
			exit := r.grid.index(ex, ey)
			if exit < 0 {
				break
			}
			if r.spanBlocked(x, y, dir, span) {
				break
			}
			goal := exit == r.to
			if !r.exitOK(exit, dir, goal) {
				continue
			}
			if goal {
				relax(routeState{index: exit, arrival: dir, exit: true, terminal: true}, routeCostTunnel+routeCostTunnel, routeVia{kind: 2, dir: dir, span: span})
				continue
			}
			relax(routeState{index: exit, arrival: dir, exit: true}, routeCostTunnel, routeVia{kind: 2, dir: dir, span: span})
		}
	}
}

// spanBlocked reports whether any tile strictly between the halves holds
// another underground entity, which would pair with or cut the span.
func (r *router) spanBlocked(x, y, dir, span int) bool {
	for i := 1; i < span; i++ {
		index := r.grid.index(x+routeDirs[dir][0]*i, y+routeDirs[dir][1]*i)
		if index < 0 || r.grid.tunnel[index] {
			return true
		}
	}
	return false
}

// reconstruct rebuilds the ordered steps from the goal back to the start.
func (r *router) reconstruct(goal routeState, via map[routeState]routeVia) []routeStep {
	var chain []routeState
	for state := goal; ; {
		chain = append(chain, state)
		v := via[state]
		if v.kind == 0 {
			break
		}
		state = v.parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	steps := make([]routeStep, 0, len(chain))
	for i, state := range chain {
		x, y := r.grid.xy(state.index)
		step := routeStep{x: x, y: y}
		last := i == len(chain)-1
		out := state.arrival
		jump := false
		if !last {
			v := via[chain[i+1]]
			out = v.dir
			jump = v.kind == 2
		}
		switch {
		case state.exit:
			step.tunnel = "output"
			step.direction = r.tunnelDirection(true, out)
			step.cost = routeCostTunnel
		case jump:
			step.tunnel = "input"
			step.direction = r.tunnelDirection(false, out)
			step.cost = routeCostTunnel
		default:
			cost, _ := r.normalCost(state.index, state.arrival, out, last)
			step.cost = cost
			dir, existing := r.grid.existing[state.index]
			step.existing = existing
			if !r.params.pipe {
				if existing {
					step.direction = dir * 4
				} else {
					step.direction = out * 4
				}
			}
		}
		steps = append(steps, step)
	}
	return steps
}

// tunnelDirection returns the engine direction of an underground half for a span
// travelling along travel. Belts face the travel direction on both halves;
// pipe-to-ground faces its surface connection, behind the span at the entrance
// and ahead of it at the exit.
func (r *router) tunnelDirection(exit bool, travel int) int {
	if !r.params.pipe || exit {
		return travel * 4
	}
	return opposite(travel) * 4
}

func routeCostTotal(steps []routeStep) float64 {
	total := 0.0
	for _, step := range steps {
		total += step.cost
	}
	return math.Round(total*1000) / 1000
}

// routeEndpointOK reports whether any role could occupy the tile; it lets a
// caller name the blocked endpoint after a search refuses to start.
func routeEndpointOK(grid *routeGrid, params routeParams, x, y int) bool {
	index := grid.index(x, y)
	if index < 0 {
		return false
	}
	r := &router{grid: grid, params: params, from: grid.index(params.fromX, params.fromY), to: grid.index(params.toX, params.toY)}
	return r.endpointUsable(index)
}
