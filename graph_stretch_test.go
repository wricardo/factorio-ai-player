package main

import (
	"sort"
	"testing"
)

// stretchFixture builds a builder out of hand-written belt nodes and BELT_OUT
// edges, so the stretch rules are tested without an engine.
type stretchFixture struct {
	b *graphBuilder
}

func newStretchFixture() *stretchFixture {
	return &stretchFixture{b: newGraphBuilder("snap-test", "player")}
}

// belt adds one entity node. dir is the engine direction (0 north, 4 east,
// 8 south, 12 west); shape is the engine belt_shape for transport belts.
func (f *stretchFixture) belt(unit int64, x, y float64, dir int, shape string) string {
	typ := "transport-belt"
	props := map[string]any{"id": "", "type": typ, "name": typ, "surface": "nauvis", "force": "player",
		"x": x, "y": y, "direction": dir, "unit": unit, "snapshot": "snap-test", "scope": "nauvis|player"}
	if shape != "" {
		props["beltShape"] = shape
	}
	id := "nauvis:" + itoa(unit)
	props["id"] = id
	f.b.owned[id] = &graphNodeOut{ID: id, Labels: []string{"Entity", "Belt"}, Props: props}
	f.b.types[id] = typ
	return id
}

func (f *stretchFixture) splitter(unit int64, x, y float64, dir int) string {
	id := f.belt(unit, x, y, dir, "")
	f.b.owned[id].Props["type"] = "splitter"
	f.b.types[id] = "splitter"
	return id
}

func (f *stretchFixture) underground(unit int64, x, y float64, dir int) string {
	id := f.belt(unit, x, y, dir, "")
	f.b.owned[id].Props["type"] = "underground-belt"
	f.b.types[id] = "underground-belt"
	return id
}

func (f *stretchFixture) link(from, to, kind string) {
	props := map[string]any{"kind": kind, "snapshot": "snap-test", "scope": "nauvis|player"}
	f.b.edge("BELT_OUT", from, "Entity", to, "Entity", kind, props, "nauvis")
}

func (f *stretchFixture) done(t *testing.T) {
	t.Helper()
	if err := f.b.buildStretches(); err != nil {
		t.Fatalf("buildStretches: %v", err)
	}
}

func (f *stretchFixture) stretchOf(t *testing.T, beltID string) *graphNodeOut {
	t.Helper()
	for _, e := range f.b.edges {
		if e.Type == "IN_STRETCH" && e.From == beltID {
			s := f.b.derived[e.To]
			if s == nil {
				t.Fatalf("no stretch node %s", e.To)
			}
			return s
		}
	}
	t.Fatalf("belt %s is in no stretch", beltID)
	return nil
}

func (f *stretchFixture) edgesOf(typ string) []*graphEdgeOut {
	var out []*graphEdgeOut
	for _, e := range f.b.edges {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func itoa(v int64) string {
	return itemList(map[string]int64{"x": v})[0][2:]
}

// A straight run joined by an underground pair is one stretch, and the index
// keeps running across the tunnel.
func TestBuildStretchesThroughUnderground(t *testing.T) {
	f := newStretchFixture()
	a := f.belt(1, 0.5, 0.5, 4, "straight")
	b := f.underground(2, 1.5, 0.5, 4)
	c := f.underground(3, 5.5, 0.5, 4)
	d := f.belt(4, 6.5, 0.5, 4, "straight")
	f.link(a, b, "belt")
	f.link(b, c, "underground")
	f.link(c, d, "belt")
	f.done(t)

	s := f.stretchOf(t, a)
	for _, id := range []string{a, b, c, d} {
		if got := f.stretchOf(t, id); got.ID != s.ID {
			t.Fatalf("belt %s in stretch %s, want %s", id, got.ID, s.ID)
		}
	}
	if got := s.Props["belts"]; got != 4 {
		t.Fatalf("belts=%v, want 4", got)
	}
	if got := s.Props["undergrounds"]; got != 2 {
		t.Fatalf("undergrounds=%v, want 2", got)
	}
	if got := s.Props["endKind"]; got != "deadend" {
		t.Fatalf("endKind=%v, want deadend", got)
	}
	indexes := map[string]any{}
	for _, e := range f.edgesOf("IN_STRETCH") {
		indexes[e.From] = e.Props["index"]
	}
	if indexes[c] != 2 || indexes[d] != 3 {
		t.Fatalf("IN_STRETCH order=%v, want c=2 d=3", indexes)
	}
}

// A splitter ends every run entering it and starts one per output port.
func TestBuildStretchesCutAtSplitter(t *testing.T) {
	f := newStretchFixture()
	in := f.belt(10, 0.5, 0.5, 4, "straight")
	sp := f.splitter(11, 1.5, 0.5, 4)
	up := f.belt(12, 2.5, 0.5, 4, "straight")
	down := f.belt(13, 2.5, 1.5, 4, "straight")
	f.link(in, sp, "belt")
	f.b.edge("BELT_OUT", sp, "Entity", up, "Entity", "belt", map[string]any{"kind": "belt", "fromPort": "left", "snapshot": "snap-test", "scope": "nauvis|player"}, "nauvis")
	f.b.edge("BELT_OUT", sp, "Entity", down, "Entity", "belt", map[string]any{"kind": "belt", "fromPort": "right", "snapshot": "snap-test", "scope": "nauvis|player"}, "nauvis")
	f.done(t)

	inStretch := f.stretchOf(t, in)
	if got := inStretch.Props["endKind"]; got != "splitter" {
		t.Fatalf("in endKind=%v, want splitter", got)
	}
	if upStretch := f.stretchOf(t, up); upStretch.ID == inStretch.ID {
		t.Fatalf("belt after splitter joined the incoming run")
	}
	if downStretch := f.stretchOf(t, down); downStretch.ID == f.stretchOf(t, up).ID {
		t.Fatalf("both splitter outputs share one stretch")
	}
	if spStretch := f.edgesOf("IN_STRETCH"); len(spStretch) != 3 {
		t.Fatalf("IN_STRETCH=%d, want 3 (splitter is not a member)", len(spStretch))
	}
	flows := f.edgesOf("FLOWS_INTO")
	if len(flows) != 2 {
		t.Fatalf("FLOWS_INTO=%d, want 2 splitter branches", len(flows))
	}
	ports := map[string]bool{}
	for _, e := range flows {
		ports[e.Props["fromPort"].(string)] = true
	}
	if !ports["left"] || !ports["right"] {
		t.Fatalf("splitter branches=%v, want left and right", ports)
	}
	// The splitter port is the transferring endpoint, never the belt behind it.
	for _, e := range f.edgesOf("FEEDS_STRETCH") {
		if e.From != sp {
			t.Fatalf("FEEDS_STRETCH from %s, want the splitter port", e.From)
		}
	}
	if takes := f.edgesOf("TAKES_FROM_STRETCH"); len(takes) != 1 || takes[0].From != sp {
		t.Fatalf("TAKES_FROM_STRETCH=%v, want one edge from the splitter", takes)
	}
}

// A side-load ends the loading run and links it into the main one; the main run
// stays continuous.
func TestBuildStretchesSideload(t *testing.T) {
	f := newStretchFixture()
	main1 := f.belt(20, 0.5, 0.5, 4, "straight")
	main2 := f.belt(21, 1.5, 0.5, 4, "straight")
	main3 := f.belt(22, 2.5, 0.5, 4, "straight")
	side := f.belt(23, 1.5, -0.5, 8, "straight") // south-facing, loads onto main2
	f.link(main1, main2, "belt")
	f.link(main2, main3, "belt")
	f.link(side, main2, "belt")
	f.done(t)

	main := f.stretchOf(t, main1)
	if f.stretchOf(t, main2).ID != main.ID || f.stretchOf(t, main3).ID != main.ID {
		t.Fatalf("side-load broke the main run")
	}
	if got := main.Props["belts"]; got != 3 {
		t.Fatalf("main belts=%v, want 3", got)
	}
	sideStretch := f.stretchOf(t, side)
	if sideStretch.ID == main.ID {
		t.Fatalf("side-loading run merged into the main run")
	}
	if got := sideStretch.Props["endKind"]; got != "sideload" {
		t.Fatalf("side endKind=%v, want sideload", got)
	}
	flows := f.edgesOf("FLOWS_INTO")
	if len(flows) != 1 || flows[0].From != sideStretch.ID || flows[0].To != main.ID {
		t.Fatalf("FLOWS_INTO=%v, want side -> main", flows)
	}
	if side, _ := flows[0].Props["side"].(string); side != "left" {
		t.Fatalf("sideload side=%v, want left (side belt is north of an eastbound main)", flows[0].Props["side"])
	}
}

// A T-junction has no belt directly behind it, so it starts a new stretch and
// every input run ends there, even when only one non-splitter input exists.
func TestBuildStretchesTJunction(t *testing.T) {
	f := newStretchFixture()
	north := f.belt(30, 1.5, -0.5, 8, "straight")
	head := f.belt(31, 1.5, 0.5, 4, "straight") // eastbound, nothing behind it
	f.link(north, head, "belt")
	f.done(t)

	if f.stretchOf(t, head).ID == f.stretchOf(t, north).ID {
		t.Fatalf("a belt fed only from the side was merged into that run")
	}
	flows := f.edgesOf("FLOWS_INTO")
	if len(flows) != 1 || flows[0].Props["via"] != "sideload" {
		t.Fatalf("FLOWS_INTO=%v, want one sideload join", flows)
	}
}

// A splitter output behind the belt plus one side-load: the side run ends and
// the belt starts a new stretch (a splitter output never continues a run).
func TestBuildStretchesSplitterBehindWithSideload(t *testing.T) {
	f := newStretchFixture()
	sp := f.splitter(40, 0.5, 0.5, 4)
	out := f.belt(41, 1.5, 0.5, 4, "straight")
	side := f.belt(42, 1.5, -0.5, 8, "straight")
	f.b.edge("BELT_OUT", sp, "Entity", out, "Entity", "belt", map[string]any{"kind": "belt", "fromPort": "left", "snapshot": "snap-test", "scope": "nauvis|player"}, "nauvis")
	f.link(side, out, "belt")
	f.done(t)

	outStretch := f.stretchOf(t, out)
	if outStretch.ID == f.stretchOf(t, side).ID {
		t.Fatalf("the side run continued into the belt after a splitter")
	}
	if got := outStretch.Props["belts"]; got != 1 {
		t.Fatalf("belts=%v, want the single belt after the splitter", got)
	}
}

// A curved belt continues from its single input side (belt_shape), and a closed
// loop still gets exactly one stretch.
func TestBuildStretchesCurveAndLoop(t *testing.T) {
	f := newStretchFixture()
	// East run turning south: the south-facing belt is the curve.
	in := f.belt(50, 0.5, 0.5, 4, "straight")
	curve := f.belt(51, 1.5, 0.5, 8, "left")
	after := f.belt(52, 1.5, 1.5, 8, "straight")
	f.link(in, curve, "belt")
	f.link(curve, after, "belt")
	// A two-belt loop with no head.
	l1 := f.belt(60, 10.5, 0.5, 4, "left")
	l2 := f.belt(61, 11.5, 0.5, 12, "left")
	f.link(l1, l2, "belt")
	f.link(l2, l1, "belt")
	f.done(t)

	if f.stretchOf(t, in).ID != f.stretchOf(t, curve).ID || f.stretchOf(t, curve).ID != f.stretchOf(t, after).ID {
		t.Fatalf("curve did not continue the run")
	}
	if l1Stretch := f.stretchOf(t, l1); l1Stretch.ID != f.stretchOf(t, l2).ID {
		t.Fatalf("loop split into two stretches")
	} else if got := l1Stretch.Props["endKind"]; got != "loop" {
		t.Fatalf("loop endKind=%v, want loop", got)
	}
}

// Only transferring entities link to a stretch: the inserter itself, never the
// machine behind it.
func TestBuildStretchesTransferEndpointsAreInserters(t *testing.T) {
	f := newStretchFixture()
	belt := f.belt(70, 0.5, 0.5, 4, "straight")
	inserter := "nauvis:71"
	f.b.owned[inserter] = &graphNodeOut{ID: inserter, Labels: []string{"Entity", "Inserter"},
		Props: map[string]any{"id": inserter, "type": "inserter", "name": "inserter", "surface": "nauvis", "x": 0.5, "y": -0.5, "unit": int64(71), "snapshot": "snap-test", "scope": "nauvis|player"}}
	f.b.types[inserter] = "inserter"
	machine := "nauvis:72"
	f.b.owned[machine] = &graphNodeOut{ID: machine, Labels: []string{"Entity", "Machine"},
		Props: map[string]any{"id": machine, "type": "furnace", "name": "electric-furnace", "surface": "nauvis", "x": 0.5, "y": -2.5, "unit": int64(72), "snapshot": "snap-test", "scope": "nauvis|player"}}
	f.b.types[machine] = "furnace"
	dropProps := map[string]any{"side": "right", "snapshot": "snap-test", "scope": "nauvis|player"}
	f.b.edge("DROPS_TO", inserter, "Entity", belt, "Entity", "", dropProps, "nauvis")
	// picks-from-belt: finish() stores PICKS_FROM as inserter -> source belt.
	picker := "nauvis:73"
	f.b.owned[picker] = &graphNodeOut{ID: picker, Labels: []string{"Entity", "Inserter"},
		Props: map[string]any{"id": picker, "type": "inserter", "name": "inserter", "surface": "nauvis", "x": 0.5, "y": 1.5, "unit": int64(73), "snapshot": "snap-test", "scope": "nauvis|player"}}
	f.b.types[picker] = "inserter"
	f.b.edge("PICKS_FROM", picker, "Entity", belt, "Entity", "", map[string]any{"snapshot": "snap-test", "scope": "nauvis|player"}, "nauvis")
	f.done(t)

	feeds := f.edgesOf("FEEDS_STRETCH")
	if len(feeds) != 1 || feeds[0].From != inserter {
		t.Fatalf("FEEDS_STRETCH=%v, want one edge from the inserter", feeds)
	}
	if side, _ := feeds[0].Props["side"].(string); side != "right" {
		t.Fatalf("FEEDS_STRETCH side=%v, want the drop side", feeds[0].Props["side"])
	}
	takes := f.edgesOf("TAKES_FROM_STRETCH")
	if len(takes) != 1 || takes[0].From != picker {
		t.Fatalf("TAKES_FROM_STRETCH=%v, want one edge from the picking inserter", takes)
	}
	for _, e := range takes {
		if e.From == machine {
			t.Fatalf("a machine linked to a stretch directly")
		}
	}
}
