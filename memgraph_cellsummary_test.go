package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// cellFixture builds the rows the five cellSummary statements return. Ends are
// written "M5", "C7", "B9" (kind letter + unit); "" is an absent end.
type cellFixture struct {
	machines  [][]any
	inserters [][]any
	dropsOn   [][]any
	belts     [][]any
	beltEdges [][]any
	truncated bool
}

func (f *cellFixture) machine(unit int, name, recipe, status string, x, y float64) *cellFixture {
	var rc any
	if recipe != "" {
		rc = recipe
	}
	f.machines = append(f.machines, []any{int64(unit), name, rc, status, x, y})
	return f
}

func cellEndRow(end string) (any, any) {
	if end == "" {
		return nil, nil
	}
	label := map[byte]string{'M': "Machine", 'C': "Chest", 'B': "Belt"}[end[0]]
	unit, _ := strconv.Atoi(end[1:])
	return []any{"Entity", label}, int64(unit)
}

func (f *cellFixture) inserter(unit int, src, dst string) *cellFixture {
	sl, su := cellEndRow(src)
	dl, du := cellEndRow(dst)
	f.inserters = append(f.inserters, []any{int64(unit), sl, su, dl, du})
	return f
}

func (f *cellFixture) belt(units ...int) *cellFixture {
	for i, u := range units {
		f.belts = append(f.belts, []any{int64(u)})
		if i > 0 {
			f.beltEdges = append(f.beltEdges, []any{int64(units[i-1]), int64(u)})
		}
	}
	return f
}

func (f *cellFixture) reader() memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		res := &MemgraphQueryResult{Truncated: f.truncated, Snapshot: &MemgraphSnapshotInfo{ID: "snap-test"}}
		switch in.Cypher {
		case cellMachinesCypher:
			res.Rows = f.machines
		case cellInsertersCypher:
			res.Rows = f.inserters
		case cellDropsOnCypher:
			res.Rows = f.dropsOn
		case cellBeltsCypher:
			res.Rows = f.belts
		case cellBeltEdgesCypher:
			res.Rows = f.beltEdges
		default:
			panic("unexpected statement: " + in.Cypher)
		}
		return res, nil
	}
}

func runCells(t *testing.T, f *cellFixture, limit int) *MemgraphCellSummary {
	t.Helper()
	out, err := cellSummary(context.Background(), f.reader(), MemgraphCellSummaryInput{Surface: "nauvis", Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func cellByID(t *testing.T, out *MemgraphCellSummary, id string) *MemgraphCell {
	t.Helper()
	for _, c := range out.Cells {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no cell %q in %v", id, cellIDs(out))
	return nil
}

func cellIDs(out *MemgraphCellSummary) string {
	ids := make([]string, len(out.Cells))
	for i, c := range out.Cells {
		ids[i] = c.ID
	}
	return strings.Join(ids, ",")
}

func TestCellSummaryGroupsMachinesAndCountsMembers(t *testing.T) {
	f := &cellFixture{}
	// Chain M1 -> M2 -> chest -> M3, one inserter per hop.
	f.machine(1, "stone-furnace", "iron-plate", "working", 10, 20)
	f.machine(2, "assembling-machine-1", "iron-gear-wheel", "full_output", 14, 22)
	f.machine(3, "assembling-machine-1", "electronic-circuit", "item_ingredient_shortage", 18, 18)
	f.inserter(10, "M1", "M2").inserter(11, "M2", "C20").inserter(12, "C20", "M3")
	// A second, unrelated cell: M50 drops onto a two-tile belt that M51 picks from.
	f.machine(50, "stone-furnace", "copper-plate", "working", 100, 100)
	f.machine(51, "assembling-machine-1", "copper-cable", "working", 104, 100)
	f.belt(100, 101)
	f.inserter(60, "M50", "B100").inserter(61, "B101", "M51")
	// A machine with no link at all.
	f.machine(99, "assembling-machine-1", "pipe", "no_power", 0, 0)

	out := runCells(t, f, 100)
	if len(out.Cells) != 2 || out.LoneMachines != 1 || out.Truncated {
		t.Fatalf("cells=%s lone=%d truncated=%v", cellIDs(out), out.LoneMachines, out.Truncated)
	}
	a := cellByID(t, out, "1")
	if a.MachineCount != 3 || a.InserterCount != 3 || a.ChestCount != 1 || a.BeltCount != 0 {
		t.Fatalf("cell 1 counts = %+v", a)
	}
	if got := strings.Join(a.Recipes, ","); got != "electronic-circuit,iron-gear-wheel,iron-plate" {
		t.Fatalf("recipes = %q", got)
	}
	if a.StalledCount != 2 {
		t.Fatalf("stalled = %d", a.StalledCount)
	}
	counts := map[string]int{}
	for _, c := range a.StatusCounts {
		counts[c.Name] = c.Count
	}
	if counts["working"] != 1 || counts["full_output"] != 1 || counts["item_ingredient_shortage"] != 1 {
		t.Fatalf("statusCounts = %v", counts)
	}
	if a.LeftTopX != 10 || a.LeftTopY != 18 || a.RightBottomX != 18 || a.RightBottomY != 22 {
		t.Fatalf("box = %v %v %v %v", a.LeftTopX, a.LeftTopY, a.RightBottomX, a.RightBottomY)
	}
	b := cellByID(t, out, "50")
	if b.MachineCount != 2 || b.BeltCount != 2 || b.InserterCount != 2 {
		t.Fatalf("cell 50 = %+v", b)
	}
	if out.Snapshot == nil || out.Snapshot.ID != "snap-test" {
		t.Fatalf("snapshot = %+v", out.Snapshot)
	}
}

func TestCellSummaryMainBeltIsABoundary(t *testing.T) {
	build := func(consumers int) *cellFixture {
		f := &cellFixture{}
		f.machine(59, "stone-furnace", "iron-plate", "working", 0, 0)
		f.belt(200, 201, 202)
		f.inserter(70, "M59", "B200")
		for i := range consumers {
			u := 60 + i
			f.machine(u, "assembling-machine-1", "iron-gear-wheel", "working", float64(10*i), 5)
			f.inserter(80+i, "B202", "M"+strconv.Itoa(u))
		}
		return f
	}

	// Two consumers: still a local belt, everything is one cell.
	two := runCells(t, build(2), 100)
	if len(two.Cells) != 1 || two.Cells[0].MachineCount != 3 || two.Cells[0].BeltCount != 3 {
		t.Fatalf("two consumers: %+v", two.Cells)
	}

	// Three consumers: the line ends every cell. Each machine keeps its own
	// inserter, the belt tiles belong to no cell.
	three := runCells(t, build(3), 100)
	if len(three.Cells) != 4 || three.LoneMachines != 0 {
		t.Fatalf("three consumers: cells=%s lone=%d", cellIDs(three), three.LoneMachines)
	}
	for _, c := range three.Cells {
		if c.MachineCount != 1 || c.InserterCount != 1 || c.BeltCount != 0 {
			t.Fatalf("cell %s = %+v", c.ID, c)
		}
	}
}

func TestCellSummaryMainBeltCountsDistinctMachines(t *testing.T) {
	// Three inserters into the same machine are one consumer, not a main belt.
	f := &cellFixture{}
	f.machine(1, "assembling-machine-1", "iron-gear-wheel", "working", 0, 0)
	f.belt(10, 11)
	f.inserter(20, "B10", "M1").inserter(21, "B11", "M1").inserter(22, "B11", "M1")
	out := runCells(t, f, 100)
	if len(out.Cells) != 1 || out.Cells[0].BeltCount != 2 || out.Cells[0].InserterCount != 3 {
		t.Fatalf("cells = %+v", out.Cells)
	}
}

func TestCellSummaryNaming(t *testing.T) {
	t.Run("most downstream machine names the cell", func(t *testing.T) {
		f := &cellFixture{}
		// Two parallel upstream machines feed one assembler through a belt.
		f.machine(1, "stone-furnace", "iron-plate", "working", 0, 0)
		f.machine(2, "stone-furnace", "iron-plate", "working", 0, 2)
		f.machine(3, "assembling-machine-1", "iron-gear-wheel", "working", 6, 0)
		f.belt(100, 101)
		f.inserter(10, "M1", "B100").inserter(11, "M2", "B100").inserter(12, "B101", "M3")
		out := runCells(t, f, 100)
		if len(out.Cells) != 1 || out.Cells[0].Name != "iron-gear-wheel" {
			t.Fatalf("cells = %+v", out.Cells)
		}
	})
	t.Run("most common recipe when nothing is downstream", func(t *testing.T) {
		f := &cellFixture{}
		f.machine(1, "stone-furnace", "iron-plate", "working", 0, 0)
		f.machine(2, "stone-furnace", "iron-plate", "working", 0, 2)
		f.machine(3, "assembling-machine-1", "iron-gear-wheel", "working", 6, 0)
		f.inserter(10, "M1", "C50").inserter(11, "M2", "C50").inserter(12, "M3", "C50")
		out := runCells(t, f, 100)
		if len(out.Cells) != 1 || out.Cells[0].Name != "iron-plate" {
			t.Fatalf("cells = %+v", out.Cells)
		}
	})
	t.Run("ties go to the smaller recipe name", func(t *testing.T) {
		f := &cellFixture{}
		f.machine(1, "assembling-machine-1", "zeta", "working", 0, 0)
		f.machine(2, "assembling-machine-1", "alpha", "working", 0, 2)
		f.inserter(10, "M1", "C50").inserter(11, "M2", "C50")
		out := runCells(t, f, 100)
		if len(out.Cells) != 1 || out.Cells[0].Name != "alpha" {
			t.Fatalf("no-downstream tie: %+v", out.Cells)
		}

		// Two final assemblers, each fed by one distinct furnace.
		g := &cellFixture{}
		g.machine(1, "stone-furnace", "iron-plate", "working", 0, 0)
		g.machine(2, "assembling-machine-1", "zeta", "working", 4, 0)
		g.machine(3, "assembling-machine-1", "alpha", "working", 4, 2)
		g.inserter(10, "M1", "M2").inserter(11, "M1", "M3")
		out = runCells(t, g, 100)
		if len(out.Cells) != 1 || out.Cells[0].Name != "alpha" {
			t.Fatalf("downstream tie: %+v", out.Cells)
		}
	})
	t.Run("a machine without a recipe falls back to its entity name", func(t *testing.T) {
		f := &cellFixture{}
		f.machine(1, "lab", "", "missing_science_packs", 0, 0)
		f.inserter(10, "C50", "M1")
		out := runCells(t, f, 100)
		if len(out.Cells) != 1 || out.Cells[0].Name != "lab" || len(out.Cells[0].Recipes) != 0 {
			t.Fatalf("cells = %+v", out.Cells)
		}
	})
}

func TestCellSummaryDropsOnJoinsMachineToBelt(t *testing.T) {
	f := &cellFixture{}
	f.machine(1, "stone-furnace", "iron-plate", "working", 0, 0)
	f.machine(2, "assembling-machine-1", "iron-gear-wheel", "working", 8, 0)
	f.belt(100, 101)
	f.dropsOn = append(f.dropsOn, []any{int64(1), int64(100)})
	f.inserter(10, "B101", "M2")
	out := runCells(t, f, 100)
	if len(out.Cells) != 1 || out.Cells[0].MachineCount != 2 || out.Cells[0].Name != "iron-gear-wheel" {
		t.Fatalf("cells = %+v", out.Cells)
	}
}

func TestCellSummaryLoneMachines(t *testing.T) {
	f := &cellFixture{}
	f.machine(1, "stone-furnace", "iron-plate", "working", 0, 0)
	f.machine(2, "stone-furnace", "iron-plate", "working", 4, 0)
	f.machine(3, "stone-furnace", "iron-plate", "working", 8, 0)
	// A dangling inserter with no ends touches nothing; only machine 3 is linked.
	f.inserter(10, "", "").inserter(11, "C50", "M3")
	out := runCells(t, f, 100)
	if out.LoneMachines != 2 || len(out.Cells) != 1 || out.Cells[0].ID != "3" {
		t.Fatalf("lone=%d cells=%s", out.LoneMachines, cellIDs(out))
	}
	if !strings.Contains(out.Summary, "2 lone machines") {
		t.Fatalf("summary = %q", out.Summary)
	}
}

func TestCellSummaryOrderLimitAndTruncated(t *testing.T) {
	f := &cellFixture{}
	// Cell 30 has two machines; cells 10 and 20 one each. Ids are the smallest unit.
	f.machine(30, "stone-furnace", "iron-plate", "working", 0, 0)
	f.machine(31, "stone-furnace", "iron-plate", "working", 2, 0)
	f.inserter(100, "M30", "M31")
	f.machine(20, "stone-furnace", "copper-plate", "working", 10, 0)
	f.inserter(101, "C50", "M20")
	f.machine(10, "stone-furnace", "stone-brick", "working", 20, 0)
	f.inserter(102, "C51", "M10")

	all := runCells(t, f, 100)
	if got := cellIDs(all); got != "30,10,20" || all.Truncated {
		t.Fatalf("order = %s truncated=%v", got, all.Truncated)
	}

	cut := runCells(t, f, 2)
	if got := cellIDs(cut); got != "30,10" || !cut.Truncated {
		t.Fatalf("limit 2: cells=%s truncated=%v", got, cut.Truncated)
	}
	if !strings.HasPrefix(cut.Summary, "3 cells") {
		t.Fatalf("summary must count every cell: %q", cut.Summary)
	}

	exact := runCells(t, f, 3)
	if exact.Truncated || len(exact.Cells) != 3 {
		t.Fatalf("limit 3: cells=%s truncated=%v", cellIDs(exact), exact.Truncated)
	}

	f.truncated = true
	if !runCells(t, f, 100).Truncated {
		t.Fatal("a read cut by the row cap must report truncated")
	}
}

func TestCellSummaryValidatesInput(t *testing.T) {
	f := &cellFixture{}
	for _, in := range []MemgraphCellSummaryInput{
		{Surface: "nauvis", Limit: 0},
		{Surface: "nauvis", Limit: 501},
		{Surface: "", Limit: 10},
		{Surface: "bad surface", Limit: 10},
	} {
		if _, err := cellSummary(context.Background(), f.reader(), in); err == nil {
			t.Fatalf("input %+v must be rejected", in)
		}
	}
	if _, err := cellSummary(context.Background(), f.reader(), MemgraphCellSummaryInput{Surface: "nauvis", Limit: 500}); err != nil {
		t.Fatal(err)
	}
}
