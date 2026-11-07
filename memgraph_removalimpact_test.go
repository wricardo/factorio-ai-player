package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func removalImpactNode(t *testing.T, id, kind string, unit int, name, recipe string) *MemgraphTraceNode {
	t.Helper()
	return testTraceNode(t, id, kind, unit, name, recipe, "working")
}

func removalImpactBoltNode(id, kind string, unit int64, props map[string]any) map[string]any {
	p := map[string]any{"id": id}
	if unit != 0 {
		p["unit"] = unit
	}
	for k, v := range props {
		p[k] = v
	}
	return map[string]any{"id": "e" + id, "labels": []any{"Entity", kind}, "properties": p}
}

func TestRemovalImpactRoutes(t *testing.T) {
	m := removalImpactNode(t, "n:1", "Machine", 1, "assembling-machine-1", "gear")
	arm := removalImpactNode(t, "n:10", "Inserter", 10, "inserter", "")
	b1 := removalImpactNode(t, "n:21", "Belt", 21, "transport-belt", "")
	b2 := removalImpactNode(t, "n:22", "Belt", 22, "transport-belt", "")
	arm2 := removalImpactNode(t, "n:11", "Inserter", 11, "inserter", "")
	furnace := removalImpactNode(t, "n:2", "Machine", 2, "stone-furnace", "iron-plate")
	armF := removalImpactNode(t, "n:12", "Inserter", 12, "inserter", "")
	chest := removalImpactNode(t, "n:3", "Chest", 3, "infinity-chest", "")
	dead := removalImpactNode(t, "n:30", "Belt", 30, "transport-belt", "")
	armD := removalImpactNode(t, "n:14", "Inserter", 14, "inserter", "")

	routes := removalImpactRoutes([]tracePath{
		{m, arm, b1, b2, arm2, furnace},
		{m, arm, b1, b2, arm2, furnace, armF, chest}, // continues past the supplier: same route
		{m, armD, dead},                              // orphan belt: no supplier, no route
		{m},                                          // no arm at all
	})
	if len(routes) != 1 {
		t.Fatalf("routes = %+v, want exactly one", routes)
	}
	if got := strings.Join(routes[0].Steps, ","); got != "n:1,n:10,n:21,n:22,n:11,n:2" || routes[0].Supplier != furnace {
		t.Fatalf("route steps %q supplier %v", got, routes[0].Supplier)
	}
}

// ri builds a machine with explicit routes: each route is a list of ids with
// the supplier last.
type riRoute struct {
	supplier *MemgraphTraceNode
	via      []string
}

func riMachine(m *MemgraphTraceNode, ingredients []string, routes ...riRoute) removalImpactMachine {
	out := removalImpactMachine{Node: m, Ingredients: ingredients}
	for _, r := range routes {
		steps := append([]string{m.ID}, r.via...)
		steps = append(steps, r.supplier.ID)
		out.Routes = append(out.Routes, removalImpactRoute{Steps: steps, Supplier: r.supplier})
	}
	return out
}

func affectedKeys(a []*MemgraphAffectedMachine) []string {
	var out []string
	for _, x := range a {
		out = append(out, *x.Machine.UnitNumber+":"+x.Ingredient+":"+strconv.Itoa(x.RoutesBefore))
	}
	return out
}

func TestRemovalImpactComputeCutVertex(t *testing.T) {
	asm := removalImpactNode(t, "m:370", "Machine", 370, "assembling-machine-1", "inserter")
	gear := removalImpactNode(t, "m:346", "Machine", 346, "assembling-machine-1", "iron-gear-wheel")
	circ := removalImpactNode(t, "m:143", "Machine", 143, "assembling-machine-1", "electronic-circuit")
	f1 := removalImpactNode(t, "m:4", "Machine", 4, "stone-furnace", "iron-plate")
	f2 := removalImpactNode(t, "m:359", "Machine", 359, "stone-furnace", "iron-plate")
	produces := map[string]map[string]bool{
		gear.ID: {"iron-gear-wheel": true}, circ.ID: {"electronic-circuit": true},
		f1.ID: {"iron-plate": true}, f2.ID: {"iron-plate": true},
	}
	machines := []removalImpactMachine{
		// Gear and circuits through single lines (belt b400 on the gear line);
		// iron plate through two redundant furnaces on separate belts.
		riMachine(asm, []string{"electronic-circuit", "iron-gear-wheel", "iron-plate"},
			riRoute{gear, []string{"a390", "b400"}},
			riRoute{circ, []string{"a391", "b401"}},
			riRoute{f1, []string{"a392", "b402"}},
			riRoute{f2, []string{"a393", "b403"}},
		),
	}

	t.Run("belt on the single gear line", func(t *testing.T) {
		got := affectedKeys(removalImpactCompute("b400", machines, produces))
		if len(got) != 1 || got[0] != "370:iron-gear-wheel:1" {
			t.Fatalf("affected = %v", got)
		}
	})
	t.Run("one of two redundant furnaces", func(t *testing.T) {
		if got := removalImpactCompute(f1.ID, machines, produces); len(got) != 0 {
			t.Fatalf("affected = %v, want none: the other furnace still feeds iron-plate", affectedKeys(got))
		}
	})
	t.Run("both furnaces lost one at a time via their belts only cuts nothing", func(t *testing.T) {
		if got := removalImpactCompute("b402", machines, produces); len(got) != 0 {
			t.Fatalf("affected = %v", affectedKeys(got))
		}
	})
	t.Run("sole supplier machine", func(t *testing.T) {
		got := affectedKeys(removalImpactCompute(circ.ID, machines, produces))
		if len(got) != 1 || got[0] != "370:electronic-circuit:1" {
			t.Fatalf("affected = %v", got)
		}
	})
	t.Run("node on no route", func(t *testing.T) {
		if got := removalImpactCompute("b999", machines, produces); len(got) != 0 || got == nil {
			t.Fatalf("affected = %#v, want empty non-nil", got)
		}
	})
	t.Run("route counted per ingredient with two suppliers on one shared arm", func(t *testing.T) {
		shared := []removalImpactMachine{riMachine(asm, []string{"iron-plate"},
			riRoute{f1, []string{"a1", "bShared"}}, riRoute{f2, []string{"a1", "bShared"}})}
		got := affectedKeys(removalImpactCompute("bShared", shared, produces))
		if len(got) != 1 || got[0] != "370:iron-plate:2" {
			t.Fatalf("affected = %v, want both routes lost through the shared belt", got)
		}
	})
}

func TestRemovalImpactComputeRules(t *testing.T) {
	asm := removalImpactNode(t, "m:1", "Machine", 1, "assembling-machine-1", "inserter")
	gear := removalImpactNode(t, "m:2", "Machine", 2, "assembling-machine-1", "iron-gear-wheel")
	furnace := removalImpactNode(t, "m:3", "Machine", 3, "stone-furnace", "iron-plate")
	other := removalImpactNode(t, "m:4", "Machine", 4, "stone-furnace", "copper-plate")
	produces := map[string]map[string]bool{gear.ID: {"iron-gear-wheel": true}, furnace.ID: {"iron-plate": true}, other.ID: {"copper-plate": true}}

	t.Run("ingredient with no route before removal is not blamed on the removal", func(t *testing.T) {
		machines := []removalImpactMachine{riMachine(asm, []string{"copper-plate", "iron-plate"}, riRoute{furnace, []string{"a", "b"}})}
		got := affectedKeys(removalImpactCompute("b", machines, produces))
		if len(got) != 1 || got[0] != "1:iron-plate:1" {
			t.Fatalf("affected = %v, want only iron-plate", got)
		}
	})
	t.Run("supplier that does not produce the ingredient does not count", func(t *testing.T) {
		// The only copper route's supplier makes copper; the iron-plate route
		// is a copper furnace and so is no iron-plate route at all.
		machines := []removalImpactMachine{riMachine(asm, []string{"iron-plate"}, riRoute{other, []string{"a", "b"}})}
		if got := removalImpactCompute("b", machines, produces); len(got) != 0 {
			t.Fatalf("affected = %v", affectedKeys(got))
		}
	})
	t.Run("cascade through a machine that falls over", func(t *testing.T) {
		// furnace -> gear assembler -> inserter assembler. Removing the furnace
		// starves the gear assembler, which then starves the inserter assembler.
		machines := []removalImpactMachine{
			riMachine(gear, []string{"iron-plate"}, riRoute{furnace, []string{"ag", "bg"}}),
			riMachine(asm, []string{"iron-gear-wheel"}, riRoute{gear, []string{"a1", "b1"}}),
		}
		got := affectedKeys(removalImpactCompute(furnace.ID, machines, produces))
		if len(got) != 2 || got[0] != "1:iron-gear-wheel:1" || got[1] != "2:iron-plate:1" {
			t.Fatalf("affected = %v", got)
		}
	})
	t.Run("no cascade when the consumer keeps another supplier", func(t *testing.T) {
		gear2 := removalImpactNode(t, "m:5", "Machine", 5, "assembling-machine-1", "iron-gear-wheel")
		p := map[string]map[string]bool{gear.ID: {"iron-gear-wheel": true}, gear2.ID: {"iron-gear-wheel": true}, furnace.ID: {"iron-plate": true}}
		machines := []removalImpactMachine{
			riMachine(gear, []string{"iron-plate"}, riRoute{furnace, []string{"ag", "bg"}}),
			riMachine(asm, []string{"iron-gear-wheel"}, riRoute{gear, []string{"a1", "b1"}}, riRoute{gear2, []string{"a2", "b2"}}),
		}
		got := affectedKeys(removalImpactCompute(furnace.ID, machines, p))
		if len(got) != 1 || got[0] != "2:iron-plate:1" {
			t.Fatalf("affected = %v", got)
		}
	})
	t.Run("root machine never lists itself", func(t *testing.T) {
		machines := []removalImpactMachine{riMachine(gear, []string{"iron-plate"}, riRoute{furnace, []string{"ag"}})}
		if got := removalImpactCompute(gear.ID, machines, produces); len(got) != 0 {
			t.Fatalf("affected = %v", affectedKeys(got))
		}
	})
}

func TestRemovalImpactSummary(t *testing.T) {
	root := removalImpactNode(t, "b:400", "Belt", 400, "transport-belt", "")
	asm := removalImpactNode(t, "m:370", "Machine", 370, "assembling-machine-1", "inserter")
	if got := removalImpactSummary(root, 0, nil, false); !strings.Contains(got, "safe") || !strings.Contains(got, "no machine") {
		t.Fatalf("summary = %q", got)
	}
	if got := removalImpactSummary(root, 3, nil, false); !strings.Contains(got, "safe") || !strings.Contains(got, "3 downstream") {
		t.Fatalf("summary = %q", got)
	}
	got := removalImpactSummary(root, 3, []*MemgraphAffectedMachine{{Machine: asm, Ingredient: "iron-gear-wheel", RoutesBefore: 1}}, true)
	for _, want := range []string{"belt 400", "1 of 3", "370 assembling-machine-1 (inserter) loses iron-gear-wheel", "incomplete"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}

func TestValidateRemovalImpactInput(t *testing.T) {
	ok := MemgraphRemovalImpactInput{UnitNumber: "5", Surface: "nauvis", MaxHops: 10}
	if err := validateRemovalImpactInput(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*MemgraphRemovalImpactInput){
		"unitNumber": func(in *MemgraphRemovalImpactInput) { in.UnitNumber = "abc" },
		"surface":    func(in *MemgraphRemovalImpactInput) { in.Surface = `x"} DELETE n //` },
		"maxHops":    func(in *MemgraphRemovalImpactInput) { in.MaxHops = supplyChainMaxHopsLimit + 1 },
	}
	for name, mutate := range bad {
		in := ok
		mutate(&in)
		if err := validateRemovalImpactInput(in); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: want an error naming the field, got %v", name, err)
		}
	}
}

// removalImpactFake answers each statement the function issues from canned
// rows, keyed by what the statement is.
func removalImpactFake(t *testing.T, nodes map[int64]map[string]any, down, supply map[int64][][]any, truncatedDown bool) memgraphReader {
	t.Helper()
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		unit, _ := in.Params.(map[string]any)["unit"].(int64)
		switch {
		case strings.HasPrefix(in.Cypher, "MATCH (r {unit: $unit, surface: $surface}) RETURN"):
			n, ok := nodes[unit]
			if !ok {
				return &MemgraphQueryResult{}, nil
			}
			return &MemgraphQueryResult{Rows: [][]any{{n}}}, nil
		case strings.Contains(in.Cypher, "BELT_OUT*0..400]->(:Belt)<-[:PICKS_FROM]"):
			return &MemgraphQueryResult{Rows: down[unit], Truncated: truncatedDown}, nil
		case strings.Contains(in.Cypher, "BELT_OUT*0..400]-(:Belt)<-[:DROPS_TO]"):
			return &MemgraphQueryResult{Rows: supply[unit]}, nil
		case in.Cypher == removalImpactIngredientsCypher:
			return &MemgraphQueryResult{Rows: [][]any{
				{int64(370), []any{"iron-gear-wheel", "iron-plate"}},
			}}, nil
		case in.Cypher == removalImpactProducesCypher:
			return &MemgraphQueryResult{Rows: [][]any{
				{int64(346), []any{"iron-gear-wheel"}}, {int64(4), []any{"iron-plate"}}, {int64(359), []any{"iron-plate"}},
			}}, nil
		}
		t.Fatalf("unexpected statement: %s", in.Cypher)
		return nil, nil
	}
}

func TestRemovalImpactEndToEnd(t *testing.T) {
	bn := func(id, kind string, unit int64, props map[string]any) map[string]any {
		return removalImpactBoltNode(id, kind, unit, props)
	}
	asm := bn("n:370", "Machine", 370, map[string]any{"name": "assembling-machine-1", "recipe": "inserter"})
	gear := bn("n:346", "Machine", 346, map[string]any{"name": "assembling-machine-1", "recipe": "iron-gear-wheel"})
	f1 := bn("n:4", "Machine", 4, map[string]any{"name": "stone-furnace", "recipe": "iron-plate"})
	f2 := bn("n:359", "Machine", 359, map[string]any{"name": "stone-furnace", "recipe": "iron-plate"})
	armG := bn("n:390", "Inserter", 390, nil)
	armA := bn("n:392", "Inserter", 392, nil)
	armB := bn("n:393", "Inserter", 393, nil)
	b400 := bn("n:400", "Belt", 400, map[string]any{"name": "transport-belt"})
	b402 := bn("n:402", "Belt", 402, map[string]any{"name": "transport-belt"})
	b403 := bn("n:403", "Belt", 403, map[string]any{"name": "transport-belt"})
	orphan := bn("n:391", "Belt", 391, map[string]any{"name": "transport-belt"})
	armO := bn("n:395", "Inserter", 395, nil)

	nodes := map[int64]map[string]any{370: asm, 346: gear, 4: f1, 359: f2, 400: b400, 402: b402}
	// Downstream of belt 400: the gear line reaches machine 370.
	down := map[int64][][]any{
		400: {{int64(1), []any{b400, armG, asm}}},
		4:   {{int64(1), []any{f1, armA, b402, armG, asm}}},
		402: {{int64(1), []any{b402, armG, asm}}},
		346: {{int64(1), []any{gear, armG, asm}}},
	}
	supply := map[int64][][]any{
		370: {
			{int64(2), []any{asm, armG, b400, armG, gear}},
			{int64(2), []any{asm, armA, b402, armA, f1}},
			{int64(2), []any{asm, armB, b403, armB, f2}},
			{int64(1), []any{asm, armO, orphan}},
			{int64(3), []any{asm, armG, b400, armG, gear, armA, f1}},
		},
	}
	read := removalImpactFake(t, nodes, down, supply, false)
	run := func(unit string) *MemgraphRemovalImpact {
		t.Helper()
		out, err := removalImpact(context.Background(), read, MemgraphRemovalImpactInput{UnitNumber: unit, Surface: "nauvis", MaxHops: 10})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := run("400")
	if got.Safe || len(got.Affected) != 1 || got.Affected[0].Ingredient != "iron-gear-wheel" || got.Affected[0].Machine.ID != "n:370" || got.Affected[0].RoutesBefore != 1 {
		t.Fatalf("belt 400: %+v", affectedKeys(got.Affected))
	}
	if got.Root.ID != "n:400" || !strings.Contains(got.Summary, "370") {
		t.Fatalf("root/summary = %v / %q", got.Root, got.Summary)
	}

	// Removing one of two redundant iron furnaces: nothing lost.
	if got := run("4"); !got.Safe || len(got.Affected) != 0 || !strings.Contains(got.Summary, "safe") {
		t.Fatalf("furnace 4: %+v %q", affectedKeys(got.Affected), got.Summary)
	}
}

func TestRemovalImpactErrors(t *testing.T) {
	inserter := removalImpactBoltNode("n:390", "Inserter", 390, nil)
	read := removalImpactFake(t, map[int64]map[string]any{390: inserter}, nil, nil, false)
	if _, err := removalImpact(context.Background(), read, MemgraphRemovalImpactInput{UnitNumber: "390", Surface: "nauvis", MaxHops: 10}); err == nil || !strings.Contains(err.Error(), "Inserter") {
		t.Fatalf("inserter root: err = %v, want a root-kind rejection", err)
	}
	if _, err := removalImpact(context.Background(), read, MemgraphRemovalImpactInput{UnitNumber: "999", Surface: "nauvis", MaxHops: 10}); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("missing unit: err = %v", err)
	}
	if _, err := removalImpact(context.Background(), read, MemgraphRemovalImpactInput{UnitNumber: "0", Surface: "nauvis", MaxHops: 10}); err == nil {
		t.Fatal("unit 0 must be rejected")
	}
}

func TestRemovalImpactTruncatedAndEmpty(t *testing.T) {
	chest := removalImpactBoltNode("n:2", "Chest", 2, map[string]any{"name": "infinity-chest"})
	read := removalImpactFake(t, map[int64]map[string]any{2: chest}, map[int64][][]any{}, nil, true)
	out, err := removalImpact(context.Background(), read, MemgraphRemovalImpactInput{UnitNumber: "2", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Safe || out.Affected == nil || len(out.Affected) != 0 || !out.Truncated || !strings.Contains(out.Summary, "incomplete") {
		t.Fatalf("result = %+v", out)
	}
}
