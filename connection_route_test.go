package main

import (
	"reflect"
	"strings"
	"testing"
)

// asciiRouteGrid builds a routing grid from rows of tiles:
//
//	. open        # blocked        S start (open)   G goal (open)
//	F foreign pipe-like entity (blocked, fluid)
//	p existing pipe (blocked, fluid)   U underground entity (blocked, tunnel)
//	> < ^ v existing belt facing E W N S (blocked)
//	A open tile the caller forbids
func asciiRouteGrid(t *testing.T, rows []string) (*routeGrid, routeParams) {
	t.Helper()
	grid := newRouteGrid(0, 0, len(rows[0]), len(rows))
	params := routeParams{fromX: -1, toX: -1, maxExpanded: 50000}
	for y, row := range rows {
		if len(row) != grid.cols {
			t.Fatalf("row %d has %d columns, want %d", y, len(row), grid.cols)
		}
		for x, c := range row {
			i := grid.index(x, y)
			switch c {
			case '.', 'S', 'G':
				grid.open[i] = true
			case 'A':
				grid.open[i] = true
				grid.avoid[i] = true
			case 'F':
				grid.fluid[i] = true
			case 'p':
				grid.fluid[i] = true
				grid.existing[i] = routeExistingPipe
			case 'U':
				grid.tunnel[i] = true
			case '^':
				grid.existing[i] = 0
			case '>':
				grid.existing[i] = 1
			case 'v':
				grid.existing[i] = 2
			case '<':
				grid.existing[i] = 3
			case '#':
			default:
				t.Fatalf("unknown tile %q", c)
			}
			if c == 'S' {
				params.fromX, params.fromY = x, y
			}
			if c == 'G' {
				params.toX, params.toY = x, y
			}
		}
	}
	if params.fromX < 0 || params.toX < 0 {
		t.Fatalf("grid needs S and G")
	}
	return grid, params
}

func routePositions(steps []routeStep) [][2]int {
	out := make([][2]int, len(steps))
	for i, s := range steps {
		out[i] = [2]int{s.x, s.y}
	}
	return out
}

// A clear corridor must produce a straight belt run facing the goal.
func TestRouteAStarClearPath(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{"S...G"})
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound || len(result.steps) != 5 {
		t.Fatalf("result: %+v", result)
	}
	for i, step := range result.steps {
		if step.x != i || step.y != 0 || step.direction != 4 || step.existing || step.tunnel != "" {
			t.Fatalf("step %d: %+v", i, step)
		}
	}
	if got := routeCostTotal(result.steps); got != 5 || result.cost != 5 {
		t.Fatalf("cost=%v total=%v, want 5", result.cost, got)
	}
}

// An obstacle must be bypassed without touching a blocked tile, and each bend
// must add its cost.
func TestRouteAStarObstacleDetour(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{
		"S.#..",
		"..#..",
		"..#.G",
		".....",
	})
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound {
		t.Fatalf("stop: %v", result.stop)
	}
	for _, p := range routePositions(result.steps) {
		if grid.open[grid.index(p[0], p[1])] == false {
			t.Fatalf("route crosses blocked tile %v: %v", p, routePositions(result.steps))
		}
	}
	if got := routeCostTotal(result.steps); got != result.cost {
		t.Fatalf("step costs %v disagree with search cost %v", got, result.cost)
	}
	last := result.steps[len(result.steps)-1]
	if last.x != 4 || last.y != 2 {
		t.Fatalf("route must end at the goal: %+v", last)
	}
	for i := 0; i+1 < len(result.steps); i++ {
		a, b := result.steps[i], result.steps[i+1]
		if abs(a.x-b.x)+abs(a.y-b.y) != 1 {
			t.Fatalf("surface steps must be adjacent: %+v -> %+v", a, b)
		}
	}
}

// An underground pair must cross a full-height wall, pair at the cheapest
// offset and face the travel direction on both halves.
func TestRouteAStarUndergroundBeltCrossing(t *testing.T) {
	rows := []string{
		"S.#.G",
		"..#..",
		"..#..",
	}
	grid, params := asciiRouteGrid(t, rows)
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonNoPath {
		t.Fatalf("without tunnels the wall must be impassable: %+v", result)
	}
	params.allowTunnel = true
	params.maxTunnel = 5
	result := routeAStar(grid, params)
	// The shortest span that clears the wall wins: surface tile, a 2-tile
	// tunnel (2*1.05), the exit half and the goal belt.
	if result.stop != ConnectionStopReasonFound || len(result.steps) != 4 {
		t.Fatalf("tunnel route: %+v", result)
	}
	entrance, exit := result.steps[1], result.steps[2]
	if entrance.tunnel != "input" || entrance.x != 1 || entrance.direction != 4 || exit.tunnel != "output" || exit.x != 3 || exit.direction != 4 {
		t.Fatalf("tunnel halves: %+v %+v", entrance, exit)
	}
	if result.cost != 5.1 {
		t.Fatalf("cost=%v, want 1 + 2*1.05 + 1 + 1", result.cost)
	}

	// With a shorter span the entrance moves up to the first tile that can reach
	// the far side, and the goal tile stays an ordinary belt.
	params.maxTunnel = 3
	short := routeAStar(grid, params)
	if short.stop != ConnectionStopReasonFound {
		t.Fatalf("short span: %+v", short)
	}
	tunnels := 0
	for _, step := range short.steps {
		if step.tunnel != "" {
			tunnels++
		}
	}
	if tunnels != 2 || short.steps[len(short.steps)-1].tunnel != "" {
		t.Fatalf("expected one pair ending on an ordinary goal tile: %+v", short.steps)
	}
}

// A span cannot exceed the maximum offset, so a wall thicker than the reach is
// impassable.
func TestRouteAStarUndergroundDistanceBound(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{
		"S###G",
		"#####",
	})
	params.allowTunnel = true
	params.maxTunnel = 3
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonNoPath {
		t.Fatalf("span of 4 must exceed max offset 3: %+v", result)
	}
	params.maxTunnel = 4
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonFound {
		t.Fatalf("offset 4 must fit: %+v", result)
	}
}

// A span may not pass under another underground entity, which would pair with it.
func TestRouteAStarSpanBlockedByTunnelEntity(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{
		"S#U#G",
		"#####",
	})
	params.allowTunnel = true
	params.maxTunnel = 5
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonNoPath {
		t.Fatalf("span under an underground entity must be refused: %+v", result)
	}
}

// Avoided tiles hold no entities, but an underground span may pass beneath them.
func TestRouteAStarAvoidAreas(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{
		"SAAAG",
		"#####",
	})
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonNoPath {
		t.Fatalf("avoided tiles must not be used: %+v", result)
	}
	params.allowTunnel = true
	params.maxTunnel = 5
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound {
		t.Fatalf("span beneath the avoided area: %+v", result)
	}
	for _, step := range result.steps {
		if step.tunnel == "" && grid.avoid[grid.index(step.x, step.y)] {
			t.Fatalf("surface entity on an avoided tile: %+v", step)
		}
	}
}

// The search must stop at maxExpanded with a typed reason, and the same search
// must succeed with a larger limit.
func TestRouteAStarExpansionLimit(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{"S.............G"})
	params.maxExpanded = 3
	limited := routeAStar(grid, params)
	if limited.stop != ConnectionStopReasonExpansionLimit || limited.expanded != 3 || len(limited.steps) != 0 {
		t.Fatalf("limited: %+v", limited)
	}
	params.maxExpanded = 100
	if found := routeAStar(grid, params); found.stop != ConnectionStopReasonFound {
		t.Fatalf("unlimited: %+v", found)
	}
}

// preferExisting must route through a same-direction belt line at the existing
// cost, and must still refuse belts facing the wrong way.
func TestRouteAStarPreferExistingBelts(t *testing.T) {
	rows := []string{
		"S>>>G",
		".....",
	}
	grid, params := asciiRouteGrid(t, rows)
	plain := routeAStar(grid, params)
	if plain.stop != ConnectionStopReasonFound {
		t.Fatalf("plain: %+v", plain)
	}
	for _, step := range plain.steps {
		if step.existing {
			t.Fatalf("existing belts are obstacles unless preferExisting: %+v", plain.steps)
		}
	}
	params.preferExisting = true
	reuse := routeAStar(grid, params)
	if reuse.stop != ConnectionStopReasonFound || len(reuse.steps) != 5 {
		t.Fatalf("reuse: %+v", reuse)
	}
	for i := 1; i <= 3; i++ {
		if !reuse.steps[i].existing || reuse.steps[i].direction != 4 {
			t.Fatalf("step %d must reuse the east belt: %+v", i, reuse.steps[i])
		}
	}
	if reuse.cost >= plain.cost {
		t.Fatalf("reuse cost %v must beat the detour %v", reuse.cost, plain.cost)
	}

	opposed, params2 := asciiRouteGrid(t, []string{"S<<<G", "....."})
	params2.preferExisting = true
	wrong := routeAStar(opposed, params2)
	for _, step := range wrong.steps {
		if step.existing {
			t.Fatalf("a belt facing the other way must never be reused: %+v", wrong.steps)
		}
	}
}

// The route may end on an existing belt it feeds, but not on one facing it.
func TestRouteAStarGoalOnExistingBelt(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{"S...G"})
	grid.existing[grid.index(4, 0)] = 0 // north: side-fed by the run
	grid.open[grid.index(4, 0)] = false
	side := routeAStar(grid, params)
	if side.stop != ConnectionStopReasonFound || !side.steps[len(side.steps)-1].existing {
		t.Fatalf("side-feeding an existing belt: %+v", side)
	}
	grid.existing[grid.index(4, 0)] = 3 // west: faces the run head-on
	if head := routeAStar(grid, params); head.stop != ConnectionStopReasonNoPath {
		t.Fatalf("a belt facing the run head-on must refuse it: %+v", head)
	}
}

// A single-tile route on an existing belt is already satisfied.
func TestRouteAStarSingleTileOnExistingBelt(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{"SG"})
	params.toX, params.toY = params.fromX, params.fromY
	grid.existing[grid.index(0, 0)] = 1
	grid.open[grid.index(0, 0)] = false
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound || len(result.steps) != 1 || !result.steps[0].existing {
		t.Fatalf("single existing belt tile: %+v", result)
	}
}

// Pipes must keep clear of foreign pipe-like entities, join one only at an
// endpoint, and use pipe-to-ground to pass one with the correct directions.
func TestRouteAStarPipeAdjacencyAndPipeToGround(t *testing.T) {
	rows := []string{
		"S...G",
		"..F..",
		".....",
	}
	grid, params := asciiRouteGrid(t, rows)
	params.pipe = true
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonNoPath {
		t.Fatalf("every row-0..2 route touches the foreign pipe: %+v", result)
	}
	params.allowAdjacency = true
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonFound {
		t.Fatalf("allowFluidAdjacency must accept touching: %+v", result)
	}
	params.allowAdjacency = false
	params.allowTunnel = true
	params.maxTunnel = 10
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound {
		t.Fatalf("pipe-to-ground must pass the foreign pipe: %+v", result)
	}
	var halves []routeStep
	for _, step := range result.steps {
		if step.tunnel != "" {
			halves = append(halves, step)
		}
	}
	if len(halves) != 2 || halves[0].tunnel != "input" || halves[1].tunnel != "output" {
		t.Fatalf("tunnel halves: %+v", result.steps)
	}
	if halves[0].direction != 12 || halves[1].direction != 4 {
		t.Fatalf("eastbound pipe-to-ground faces west at the entrance and east at the exit: %+v", halves)
	}
	for _, step := range result.steps {
		if step.direction != 0 && step.tunnel == "" {
			t.Fatalf("regular pipes carry no direction: %+v", step)
		}
	}
}

// A vertical pipe-to-ground span must face north at the southbound entrance.
func TestRouteAStarVerticalPipeToGroundDirections(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{
		"S",
		"#",
		"#",
		"G",
	})
	params.pipe = true
	params.allowTunnel = true
	params.maxTunnel = 10
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound || len(result.steps) != 2 {
		t.Fatalf("vertical span: %+v", result)
	}
	if result.steps[0].direction != 0 || result.steps[1].direction != 8 {
		t.Fatalf("southbound pipe-to-ground faces north then south: %+v", result.steps)
	}
}

// An existing pipe at an endpoint is the join target, so its own fluid must not
// block the route.
func TestRouteAStarPipeEndpointJoinsExistingPipe(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{"S...G"})
	params.pipe = true
	goal := grid.index(4, 0)
	grid.existing[goal] = routeExistingPipe
	grid.fluid[goal] = true
	grid.open[goal] = false
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound || !result.steps[len(result.steps)-1].existing {
		t.Fatalf("join an existing pipe at the endpoint: %+v", result)
	}

	// An existing pipe between the endpoints is another network: never reuse it
	// unless fluid adjacency is allowed.
	mid, midParams := asciiRouteGrid(t, []string{"S.p.G", "....."})
	midParams.pipe = true
	midParams.preferExisting = true
	route := routeAStar(mid, midParams)
	for _, step := range route.steps {
		if step.existing {
			t.Fatalf("interior existing pipe reused without allowFluidAdjacency: %+v", route.steps)
		}
	}
}

// Endpoints that cannot hold the entity must stop before any search.
func TestRouteAStarEndpointBlocked(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{"S...G"})
	grid.open[grid.index(0, 0)] = false
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonEndpointBlocked || result.expanded != 0 {
		t.Fatalf("blocked start: %+v", result)
	}
}

// A zero-length route is one tile.
func TestRouteAStarSingleTile(t *testing.T) {
	grid := newRouteGrid(0, 0, 1, 1)
	grid.open[0] = true
	result := routeAStar(grid, routeParams{fromX: 0, fromY: 0, toX: 0, toY: 0, maxExpanded: 10})
	if result.stop != ConnectionStopReasonFound || len(result.steps) != 1 || result.steps[0].direction != 0 || result.cost != 1 {
		t.Fatalf("single tile: %+v", result)
	}
}

// Repeated searches must return identical routes, so plans are reproducible and
// plan and apply agree.
func TestRouteAStarIsDeterministic(t *testing.T) {
	rows := []string{
		"S....#....",
		".....#....",
		".....#....",
		"..........",
		".....#...G",
	}
	grid, params := asciiRouteGrid(t, rows)
	first := routeAStar(grid, params)
	for i := 0; i < 5; i++ {
		again := routeAStar(grid, params)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("search %d differs:\n%+v\n%+v", i, first, again)
		}
	}
	if first.stop != ConnectionStopReasonFound {
		t.Fatalf("stop: %v", first.stop)
	}
}

// A new underground half must not land inside an existing same-name span on the
// same axis: the engine would re-pair it with the span's other half and silently
// break the existing line. Surface belts over a span stay legal.
func TestRouteAStarRejectsHalvesInsideExistingSpan(t *testing.T) {
	build := func() (*routeGrid, routeParams) {
		grid := newRouteGrid(0, 0, 5, 1)
		grid.openTunnel = make([]bool, 5)
		// Only the span exit at tile 2 can be placed on the surface layer; the
		// rest of the route is walkable.
		grid.open[0], grid.open[3], grid.open[4] = true, true, true
		grid.openTunnel[0], grid.openTunnel[2] = true, true
		grid.spanX = make([]bool, 5)
		grid.spanX[2] = true
		return grid, routeParams{fromX: 0, fromY: 0, toX: 4, toY: 0, allowTunnel: true, maxTunnel: 2, maxExpanded: 50000}
	}
	grid, params := build()
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonNoPath {
		t.Fatalf("a half inside an existing span must be refused, got %+v", result)
	}
	// The same span on the perpendicular axis does not re-pair, so it is allowed.
	grid, params = build()
	grid.spanX[2] = false
	grid.spanY = make([]bool, 5)
	grid.spanY[2] = true
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound {
		t.Fatalf("a perpendicular span must stay usable, got %+v", result)
	}
	tunnels := 0
	for _, step := range result.steps {
		if step.tunnel != "" {
			tunnels++
		}
	}
	if tunnels != 2 {
		t.Fatalf("expected one underground pair, got %+v", result.steps)
	}
}

// A foreign belt pointing into a new BELT tile side-loads its items into the
// run. The search must refuse that tile unless the caller accepts the merge.
func TestRouteAStarRejectsForeignBeltFeed(t *testing.T) {
	build := func() (*routeGrid, routeParams) {
		grid := newRouteGrid(0, 0, 5, 1)
		for x := 0; x < 5; x++ {
			grid.open[x] = true
		}
		grid.feed = make([]bool, 5)
		// A foreign belt points into the middle tile, which is otherwise open and
		// the only way through.
		grid.feed[2] = true
		return grid, routeParams{fromX: 0, fromY: 0, toX: 4, toY: 0, maxExpanded: 50000}
	}
	grid, params := build()
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonNoPath {
		t.Fatalf("a fed tile must be refused by default, got %+v", result)
	}
	grid, params = build()
	params.allowBeltAdjacency = true
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound {
		t.Fatalf("allowBeltAdjacency must accept the merge, got %+v", result)
	}
	// Pipes do not move items, so the feed check never applies to them.
	grid, params = build()
	params.pipe = true
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonFound {
		t.Fatalf("pipe routes must ignore belt feeds, got %+v", result)
	}
}

// An open straight must stay a line of belts: a tunnel is priced above the
// surface tiles it skips, so it is only chosen when the surface is blocked.
func TestRouteAStarOpenStraightStaysOnSurface(t *testing.T) {
	grid, params := asciiRouteGrid(t, []string{"S......G"})
	params.allowTunnel = true
	params.maxTunnel = 5
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonFound || len(result.steps) != 8 {
		t.Fatalf("open straight: %+v", result)
	}
	for _, step := range result.steps {
		if step.tunnel != "" {
			t.Fatalf("open ground must not tunnel: %+v", result.steps)
		}
	}
}

// A goal tile fed by a foreign belt fails at once with the feeder named, not
// as an expansion-limit search blow-up.
func TestRouteAStarFedGoalIsTypedBeltAdjacency(t *testing.T) {
	grid := newRouteGrid(0, 0, 5, 3)
	for i := range grid.open {
		grid.open[i] = true
	}
	grid.feed = make([]bool, len(grid.open))
	grid.feed[grid.index(4, 1)] = true
	grid.open[grid.index(3, 1)] = false
	grid.existing[grid.index(3, 1)] = 1 // east-facing belt feeding the goal
	params := routeParams{fromX: 0, fromY: 1, toX: 4, toY: 1, maxExpanded: 3}
	result := routeAStar(grid, params)
	if result.stop != ConnectionStopReasonEndpointBlocked {
		t.Fatalf("fed goal must stop before searching, got %+v", result)
	}
	conflict := routeFailureConflict(connectionRequest{item: "transport-belt"}, grid, params, result, 3)
	if conflict.Kind != ConnectionConflictKindBeltAdjacency || conflict.Position.X != 4.5 || !strings.Contains(conflict.Message, "3,1") {
		t.Fatalf("conflict: %+v", conflict)
	}
	params.allowBeltAdjacency = true
	params.maxExpanded = 50000
	if result := routeAStar(grid, params); result.stop != ConnectionStopReasonFound {
		t.Fatalf("allowBeltAdjacency must accept the merge: %+v", result)
	}
}

// A single-tile run has no route to derive a heading from, so the direction the
// caller validated must reach the step. Before this the ASTAR branch dropped it
// and every one-tile placement came out north-facing.
func TestRouteAStarSingleTileKeepsRequestedDirection(t *testing.T) {
	for _, want := range []int{0, 4, 8, 12} {
		grid := newRouteGrid(0, 0, 1, 1)
		grid.open[0] = true
		direction := want
		result := routeAStar(grid, routeParams{
			fromX: 0, fromY: 0, toX: 0, toY: 0, maxExpanded: 50000, direction: &direction,
		})
		if result.stop != ConnectionStopReasonFound || len(result.steps) != 1 {
			t.Fatalf("direction %d: %+v", want, result)
		}
		if got := result.steps[0]; got.direction != want || got.x != 0 || got.y != 0 {
			t.Fatalf("direction %d: step %+v", want, got)
		}
	}
}

// Without an explicit direction the single-tile step keeps the engine default so
// the planner stays usable outside a validated API call.
func TestRouteAStarSingleTileWithoutDirection(t *testing.T) {
	grid := newRouteGrid(0, 0, 1, 1)
	grid.open[0] = true
	result := routeAStar(grid, routeParams{fromX: 0, fromY: 0, toX: 0, toY: 0, maxExpanded: 50000})
	if result.stop != ConnectionStopReasonFound || len(result.steps) != 1 {
		t.Fatalf("result: %+v", result)
	}
	if got := result.steps[0].direction; got != 0 {
		t.Fatalf("direction %d, want the 0 default", got)
	}
}
