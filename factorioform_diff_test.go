package main

import (
	"encoding/json"
	"math/rand"
	"testing"
)

func ffBoxAt(x, y float64) ffBox { return ffBox{x - 0.5, y - 0.5, x + 0.5, y + 0.5} }

func ffRes(addr, entity, typ string, x, y float64, dir int) ffResource {
	return ffResource{Address: addr, Entity: entity, Type: typ, Position: ffPoint{x, y}, Direction: dir, Footprint: ffBoxAt(x, y), Config: map[string]any{}}
}

func ffLv(unit int64, entity, typ string, x, y float64, dir int) *ffLive {
	return &ffLive{UnitNumber: unit, Entity: entity, Type: typ, Position: ffPoint{x, y}, Direction: dir, Quality: "normal", Footprint: ffBoxAt(x, y), Config: map[string]any{}}
}

func ffRowFor(t *testing.T, r *ffDiffResult, addr string) ffDiffRow {
	t.Helper()
	for _, row := range r.Rows {
		if row.Address == addr {
			return row
		}
	}
	t.Fatalf("no row %s in %+v", addr, r.Rows)
	return ffDiffRow{}
}

func ffStateOf(addr string, unit uint64, entity string, x, y float64, dir int) ffStateEntry {
	return ffStateEntry{Address: addr, Unit: unit, Entity: entity, X: x, Y: y, Direction: dir, Quality: "normal"}
}

func TestFfDiffActions(t *testing.T) {
	asm := ffRes("assembler.a", "assembling-machine-2", "assembling-machine", 0.5, 0.5, 0)
	asm.Config["recipe"] = "iron-gear-wheel"
	ins := ffRes("inserter.i", "inserter", "inserter", 3.5, 0.5, 4)
	chest := ffRes("chest.c", "wooden-chest", "container", 5.5, 0.5, 0)
	tests := []struct {
		name    string
		res     ffResource
		state   []ffStateEntry
		live    []*ffLive
		want    string
		path    string
		blocker string
	}{
		{name: "noop", res: chest, state: []ffStateEntry{ffStateOf("chest.c", 1, "wooden-chest", 5.5, 0.5, 0)}, live: []*ffLive{ffLv(1, "wooden-chest", "container", 5.5, 0.5, 0)}, want: ffActionNoop},
		{name: "create", res: chest, want: ffActionCreate},
		{name: "drift", res: chest, state: []ffStateEntry{ffStateOf("chest.c", 9, "wooden-chest", 5.5, 0.5, 0)}, want: ffActionDrift},
		{name: "adopt", res: chest, live: []*ffLive{ffLv(4, "wooden-chest", "container", 5.5, 0.5, 0)}, want: ffActionAdopt},
		{name: "adopt wrong rotatable direction conflicts", res: ins, live: []*ffLive{ffLv(4, "inserter", "inserter", 3.5, 0.5, 8)}, want: ffActionConflict},
		{name: "adopt ignores direction for non rotatable", res: chest, live: []*ffLive{ffLv(4, "wooden-chest", "container", 5.5, 0.5, 8)}, want: ffActionAdopt},
		{name: "pending ghost", res: chest, live: []*ffLive{{UnitNumber: 7, Entity: "entity-ghost", GhostName: "wooden-chest", Position: ffPoint{5.5, 0.5}, Footprint: ffBoxAt(5.5, 0.5), Config: map[string]any{}}}, want: ffActionPending},
		{name: "pending deconstruct", res: chest, state: []ffStateEntry{ffStateOf("chest.c", 1, "wooden-chest", 5.5, 0.5, 0)}, live: func() []*ffLive {
			l := ffLv(1, "wooden-chest", "container", 5.5, 0.5, 0)
			l.ToBeDeconstructed = true
			return []*ffLive{l}
		}(), want: ffActionPending},
		{name: "conflict tree", res: chest, live: []*ffLive{{Entity: "tree-01", Type: "tree", Position: ffPoint{5.4, 0.6}, Footprint: ffBoxAt(5.5, 0.5), Config: map[string]any{}}}, want: ffActionConflict, blocker: "tree-01"},
		{name: "replace entity", res: asm, state: []ffStateEntry{ffStateOf("assembler.a", 2, "assembling-machine-2", 0.5, 0.5, 0)}, live: []*ffLive{ffLv(2, "assembling-machine-1", "assembling-machine", 0.5, 0.5, 0)}, want: ffActionReplace, path: "entity"},
		{name: "replace position", res: chest, state: []ffStateEntry{ffStateOf("chest.c", 1, "wooden-chest", 5.5, 0.5, 0)}, live: []*ffLive{ffLv(1, "wooden-chest", "container", 6.5, 0.5, 0)}, want: ffActionReplace, path: "position"},
		{name: "replace quality", res: func() ffResource { r := chest; r.Quality = "rare"; return r }(), state: []ffStateEntry{ffStateOf("chest.c", 1, "wooden-chest", 5.5, 0.5, 0)}, live: []*ffLive{ffLv(1, "wooden-chest", "container", 5.5, 0.5, 0)}, want: ffActionReplace, path: "quality"},
		{name: "unmanaged quality ignored", res: chest, state: []ffStateEntry{ffStateOf("chest.c", 1, "wooden-chest", 5.5, 0.5, 0)}, live: func() []*ffLive {
			l := ffLv(1, "wooden-chest", "container", 5.5, 0.5, 0)
			l.Quality = "legendary"
			return []*ffLive{l}
		}(), want: ffActionNoop},
		{name: "replace underground type", res: func() ffResource {
			r := ffRes("u.a", "underground-belt", "underground-belt", 1.5, 1.5, 0)
			r.Config["underground_type"] = "output"
			return r
		}(), state: []ffStateEntry{ffStateOf("u.a", 3, "underground-belt", 1.5, 1.5, 0)}, live: func() []*ffLive {
			l := ffLv(3, "underground-belt", "underground-belt", 1.5, 1.5, 0)
			l.Config["underground_type"] = "input"
			return []*ffLive{l}
		}(), want: ffActionReplace, path: "underground_type"},
		{name: "update direction rotatable", res: ins, state: []ffStateEntry{ffStateOf("inserter.i", 5, "inserter", 3.5, 0.5, 4)}, live: []*ffLive{ffLv(5, "inserter", "inserter", 3.5, 0.5, 12)}, want: ffActionUpdate, path: "direction"},
		{name: "replace direction non rotatable", res: func() ffResource { r := chest; r.Direction = 4; return r }(), state: []ffStateEntry{ffStateOf("chest.c", 1, "wooden-chest", 5.5, 0.5, 0)}, live: []*ffLive{ffLv(1, "wooden-chest", "container", 5.5, 0.5, 0)}, want: ffActionReplace, path: "direction"},
		{name: "update config", res: asm, state: []ffStateEntry{ffStateOf("assembler.a", 2, "assembling-machine-2", 0.5, 0.5, 0)}, live: func() []*ffLive {
			l := ffLv(2, "assembling-machine-2", "assembling-machine", 0.5, 0.5, 0)
			l.Config["recipe"] = "copper-cable"
			return []*ffLive{l}
		}(), want: ffActionUpdate, path: "recipe"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{tc.res}}, State: tc.state, Live: tc.live})
			row := ffRowFor(t, r, tc.res.Address)
			if row.Action != tc.want {
				t.Fatalf("action = %s (%s), want %s", row.Action, row.Reason, tc.want)
			}
			if tc.path != "" && (len(row.Changes) == 0 || row.Changes[0].Path != tc.path) {
				t.Fatalf("changes = %+v, want path %s", row.Changes, tc.path)
			}
			if tc.blocker != "" && (len(row.Blockers) != 1 || row.Blockers[0].Entity != tc.blocker) {
				t.Fatalf("blockers = %+v", row.Blockers)
			}
		})
	}
}

func TestFfDiffDriftCountsCreateAndStatus(t *testing.T) {
	chest := ffRes("chest.c", "wooden-chest", "container", 5.5, 0.5, 0)
	r := ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{chest}}, State: []ffStateEntry{ffStateOf("chest.c", 9, "wooden-chest", 5.5, 0.5, 0)}})
	if r.Summary.Drift != 1 || r.Summary.Create != 1 || r.Status != ffStatusDrift {
		t.Fatalf("%+v %s", r.Summary, r.Status)
	}
}

func TestFfDiffStatus(t *testing.T) {
	chest := ffRes("chest.c", "wooden-chest", "container", 5.5, 0.5, 0)
	st := []ffStateEntry{ffStateOf("chest.c", 1, "wooden-chest", 5.5, 0.5, 0)}
	lv := []*ffLive{ffLv(1, "wooden-chest", "container", 5.5, 0.5, 0)}
	if r := ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{chest}}, State: st, Live: lv}); r.Status != ffStatusConverged {
		t.Fatalf("status %s", r.Status)
	}
	if r := ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{chest}}}); r.Status != ffStatusChanges {
		t.Fatalf("status %s", r.Status)
	}
	other := ffRes("chest.d", "wooden-chest", "container", 9.5, 0.5, 0)
	blocker := &ffLive{Entity: "rock-huge", Position: ffPoint{9.5, 0.5}, Footprint: ffBoxAt(9.5, 0.5), Config: map[string]any{}}
	r := ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{chest, other}}, State: st, Live: append(lv, blocker)})
	if r.Status != ffStatusConflict || ffRowFor(t, r, "chest.c").Action != ffActionNoop {
		t.Fatalf("status %s rows %+v", r.Status, r.Rows)
	}
}

func TestFfDiffDeleteAndPrune(t *testing.T) {
	st := []ffStateEntry{ffStateOf("chest.old", 1, "wooden-chest", 1.5, 1.5, 0), ffStateOf("chest.gone", 2, "wooden-chest", 3.5, 1.5, 0)}
	lv := []*ffLive{ffLv(1, "wooden-chest", "container", 1.5, 1.5, 0)}
	r := ffDiff(ffDiffInput{Desired: &ffNormalized{}, State: st, Live: lv})
	row := ffRowFor(t, r, "chest.old")
	if row.Action != ffActionDelete || row.UnitNumber != 1 {
		t.Fatalf("%+v", row)
	}
	if len(r.Rows) != 1 || len(r.Pruned) != 1 || r.Pruned[0].Address != "chest.gone" || r.Pruned[0].Reason != "already gone" {
		t.Fatalf("rows %+v pruned %+v", r.Rows, r.Pruned)
	}
	if r.Summary.Delete != 1 || r.Status != ffStatusChanges {
		t.Fatalf("%+v", r.Summary)
	}
}

func TestFfDiffDeletedEntityDoesNotBlockCreate(t *testing.T) {
	want := ffRes("chest.new", "wooden-chest", "container", 1.5, 1.5, 0)
	st := []ffStateEntry{ffStateOf("chest.old", 1, "iron-chest", 1.5, 1.5, 0)}
	r := ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{want}}, State: st, Live: []*ffLive{ffLv(1, "iron-chest", "container", 1.5, 1.5, 0)}})
	if a := ffRowFor(t, r, "chest.new").Action; a != ffActionCreate {
		t.Fatalf("action %s", a)
	}
}

func TestFfDiffConfig(t *testing.T) {
	mk := func(cfg map[string]any, live map[string]any) *ffDiffResult {
		r := ffRes("assembler.a", "assembling-machine-3", "assembling-machine", 0.5, 0.5, 0)
		r.Config = cfg
		l := ffLv(2, "assembling-machine-3", "assembling-machine", 0.5, 0.5, 0)
		l.Config = live
		return ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{r}}, State: []ffStateEntry{ffStateOf("assembler.a", 2, "assembling-machine-3", 0.5, 0.5, 0)}, Live: []*ffLive{l}})
	}
	// Unmanaged live config is ignored.
	if r := mk(map[string]any{}, map[string]any{"recipe": "x", "modules": []string{"speed-module"}, "inserter.stack_size_override": 3}); r.Status != ffStatusConverged {
		t.Fatalf("unmanaged: %+v", r.Rows)
	}
	// Modules compare as multisets across the desired and live shapes.
	if r := mk(map[string]any{"modules": []string{"productivity-module:normal:1", "speed-module:normal:2"}}, map[string]any{"modules": []string{"speed-module", "productivity-module", "speed-module"}}); r.Status != ffStatusConverged {
		t.Fatalf("modules equal: %+v", r.Rows)
	}
	r := mk(map[string]any{"modules": []string{"speed-module:normal:2"}}, map[string]any{"modules": []string{"speed-module"}})
	if row := ffRowFor(t, r, "assembler.a"); row.Action != ffActionUpdate || row.Changes[0].Path != "modules" {
		t.Fatalf("modules differ: %+v", row)
	}
	// Floats use a tolerance; filters are ordered slots.
	if r := mk(map[string]any{"x": 1.0}, map[string]any{"x": 1.0000000001}); r.Status != ffStatusConverged {
		t.Fatalf("float: %+v", r.Rows)
	}
	if r := mk(map[string]any{"x": 1}, map[string]any{"x": 1.5}); r.Status != ffStatusChanges {
		t.Fatalf("float differ")
	}
	if r := mk(map[string]any{"inserter.filters": []string{"a", "b"}}, map[string]any{"inserter.filters": []string{"b", "a"}}); r.Status != ffStatusChanges {
		t.Fatalf("filter order ignored")
	}
	if r := mk(map[string]any{"inserter.filters": []string{"a", ""}}, map[string]any{"inserter.filters": []string{"a"}}); r.Status != ffStatusConverged {
		t.Fatalf("trailing empty filter slots differ")
	}
	r = mk(map[string]any{"recipe": "gear"}, map[string]any{})
	ch := ffRowFor(t, r, "assembler.a").Changes
	if len(ch) != 1 || ch[0].Before != nil || ch[0].After != "gear" {
		t.Fatalf("%+v", ch)
	}
}

func TestFfDiffWires(t *testing.T) {
	a := ffRes("a.a", "constant-combinator", "constant-combinator", 0.5, 0.5, 0)
	b := ffRes("b.b", "arithmetic-combinator", "arithmetic-combinator", 3.5, 0.5, 0)
	st := []ffStateEntry{ffStateOf("a.a", 1, "constant-combinator", 0.5, 0.5, 0), ffStateOf("b.b", 2, "arithmetic-combinator", 3.5, 0.5, 0)}
	mkLive := func() (*ffLive, *ffLive) {
		return ffLv(1, "constant-combinator", "constant-combinator", 0.5, 0.5, 0), ffLv(2, "arithmetic-combinator", "arithmetic-combinator", 3.5, 0.5, 0)
	}
	dw := ffDesiredWire{Address: "wire:a.a->b.b:red", Color: "red", From: "a.a", FromConnector: "circuit_red", To: "b.b", ToConnector: "combinator_input_red"}
	run := func(wires []ffDesiredWire, la, lb *ffLive, extra ...*ffLive) *ffDiffResult {
		return ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: []ffResource{a, b}, Wires: wires}, State: st, Live: append([]*ffLive{la, lb}, extra...)})
	}

	// Add.
	la, lb := mkLive()
	r := run([]ffDesiredWire{dw}, la, lb)
	if row := ffRowFor(t, r, dw.Address); row.Action != ffActionCreate || row.Wire.FromUnit != 1 || row.Wire.ToUnit != 2 {
		t.Fatalf("%+v", row)
	}
	// Present, recorded from the peer side only.
	la, lb = mkLive()
	lb.Wires = []ffWire{{Color: "red", Connector: "combinator_input_red", PeerUnit: 1, PeerConnector: "circuit_red"}}
	if r := run([]ffDesiredWire{dw}, la, lb); ffRowFor(t, r, dw.Address).Action != ffActionNoop || r.Status != ffStatusConverged {
		t.Fatalf("%+v", r.Rows)
	}
	// Extra live wire between managed entities is deleted.
	la, lb = mkLive()
	la.Wires = []ffWire{{Color: "green", Connector: "circuit_green", PeerUnit: 2, PeerConnector: "combinator_input_green"}}
	r = run(nil, la, lb)
	row := ffRowFor(t, r, "wire:a.a->b.b:green")
	if row.Action != ffActionDelete || row.Wire.FromUnit != 1 || row.Wire.ToUnit != 2 {
		t.Fatalf("%+v", row)
	}
	// Wire to an unmanaged entity is ignored.
	la, lb = mkLive()
	la.Wires = []ffWire{{Color: "green", Connector: "circuit_green", PeerUnit: 99, PeerConnector: "circuit_green"}}
	if r := run(nil, la, lb, ffLv(99, "small-lamp", "lamp", 20.5, 0.5, 0)); r.Status != ffStatusConverged {
		t.Fatalf("%+v", r.Rows)
	}
	// Replaced endpoint: wire must be recreated and old wire is not deleted.
	la, lb = mkLive()
	lb.Entity = "decider-combinator"
	lb.Wires = []ffWire{{Color: "red", Connector: "combinator_input_red", PeerUnit: 1, PeerConnector: "circuit_red"}}
	r = run([]ffDesiredWire{dw}, la, lb)
	if ffRowFor(t, r, "b.b").Action != ffActionReplace || ffRowFor(t, r, dw.Address).Action != ffActionCreate || r.Summary.Delete != 0 {
		t.Fatalf("%+v", r.Rows)
	}
}

func TestFfDiffDeterministic(t *testing.T) {
	var res []ffResource
	var st []ffStateEntry
	var live []*ffLive
	for i := 0; i < 12; i++ {
		x := float64(i*2) + 0.5
		addr := "chest.c" + string(rune('a'+i))
		r := ffRes(addr, "wooden-chest", "container", x, 0.5, 0)
		res = append(res, r)
		switch i % 4 {
		case 0:
			st = append(st, ffStateOf(addr, uint64(100+i), "wooden-chest", x, 0.5, 0))
			live = append(live, ffLv(int64(100+i), "wooden-chest", "container", x, 0.5, 0))
		case 1:
			live = append(live, ffLv(int64(100+i), "wooden-chest", "container", x, 0.5, 0))
		case 2:
			live = append(live, &ffLive{Entity: "tree-01", Position: ffPoint{x, 0.5}, Footprint: ffBoxAt(x, 0.5), Config: map[string]any{}})
		}
	}
	st = append(st, ffStateOf("old.x", 500, "iron-chest", 90.5, 0.5, 0))
	live = append(live, ffLv(500, "iron-chest", "container", 90.5, 0.5, 0))
	enc := func(rng *rand.Rand) string {
		r2 := append([]ffResource(nil), res...)
		s2 := append([]ffStateEntry(nil), st...)
		l2 := append([]*ffLive(nil), live...)
		rng.Shuffle(len(r2), func(i, j int) { r2[i], r2[j] = r2[j], r2[i] })
		rng.Shuffle(len(s2), func(i, j int) { s2[i], s2[j] = s2[j], s2[i] })
		rng.Shuffle(len(l2), func(i, j int) { l2[i], l2[j] = l2[j], l2[i] })
		b, err := json.Marshal(ffDiff(ffDiffInput{Desired: &ffNormalized{Resources: r2}, State: s2, Live: l2}))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first := enc(rand.New(rand.NewSource(1)))
	for seed := int64(2); seed < 12; seed++ {
		if got := enc(rand.New(rand.NewSource(seed))); got != first {
			t.Fatalf("seed %d differs:\n%s\n%s", seed, got, first)
		}
	}
}
