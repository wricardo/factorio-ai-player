package main

import (
	"strings"
	"testing"
)

// ffTestSnapshot builds a small catalogue: belts (2 per craft), cables
// (2 per craft), electronic circuits (1x iron plate + 3x cable), gears
// (2x plate), iron plates themselves smelted from iron ore. Mined raw =
// iron ore / stone; gathered = wood.
func ffTestSnapshot() *ffRawSnapshot {
	q := func(item string, amt float64) planningQuantity {
		return planningQuantity{planningRef: planningRef{Item: item, MaterialType: "item"}, Amount: amt}
	}
	return &ffRawSnapshot{
		Recipes: []planningRecipe{
			{Name: "transport-belt", Ingredients: []planningQuantity{q("iron-plate", 1), q("iron-gear-wheel", 1)}, Products: []planningQuantity{q("transport-belt", 2)}},
			{Name: "copper-cable", Ingredients: []planningQuantity{q("copper-plate", 1)}, Products: []planningQuantity{q("copper-cable", 2)}},
			{Name: "iron-gear-wheel", Ingredients: []planningQuantity{q("iron-plate", 2)}, Products: []planningQuantity{q("iron-gear-wheel", 1)}},
			{Name: "electronic-circuit", Ingredients: []planningQuantity{q("iron-plate", 1), q("copper-cable", 3)}, Products: []planningQuantity{q("electronic-circuit", 1)}},
			{Name: "iron-plate", Ingredients: []planningQuantity{q("iron-ore", 1)}, Products: []planningQuantity{q("iron-plate", 1)}},
		},
		Mined:    []planningRef{{Item: "iron-ore", MaterialType: "item"}, {Item: "stone", MaterialType: "item"}},
		Gathered: []planningRef{{Item: "wood", MaterialType: "item"}},
	}
}

func TestFFExpandRawBasic(t *testing.T) {
	rows := ffExpandRaw(ffTestSnapshot(), []ffRawDemand{{Name: "transport-belt", Quality: "normal", Count: 4}})
	// 4 belts = 2 crafts => 2 plates + 2 gears (4 plates) => 6 plates => 6 ore.
	got := map[string]int{}
	for _, r := range rows {
		got[r.Name+"@"+r.Quality] += r.Count
	}
	if got["iron-ore@normal"] != 6 {
		t.Fatalf("raw %v want iron-ore=6", got)
	}
	if len(rows) != 1 {
		t.Fatalf("raw rows %v want single leaf", rows)
	}
}

func TestFFExpandRawCeilAndRounding(t *testing.T) {
	rows := ffExpandRaw(ffTestSnapshot(), []ffRawDemand{{Name: "transport-belt", Quality: "normal", Count: 1}})
	// 1 belt = 1 craft => 1 plate + 1 gear (2 plates) = 3 plates => 3 ore.
	if len(rows) != 1 || rows[0].Name != "iron-ore" || rows[0].Count != 3 {
		t.Fatalf("raw %v want 3x iron-ore", rows)
	}
}

func TestFFExpandRawAggregateAndQuality(t *testing.T) {
	rows := ffExpandRaw(ffTestSnapshot(), []ffRawDemand{
		{Name: "transport-belt", Quality: "normal", Count: 2},
		{Name: "copper-cable", Quality: "rare", Count: 2},
	})
	// belts: 1 craft => 3 plates => 3 ore (normal).
	// cables 2x: 1 craft => 1 copper plate; copper plate is recipe-less
	// non-raw, so omitted. Only ore remains.
	if len(rows) != 1 || rows[0].Name != "iron-ore" || rows[0].Quality != "normal" || rows[0].Count != 3 {
		t.Fatalf("raw %v", rows)
	}
}

func TestFFExpandRawUnresearchedOmitted(t *testing.T) {
	snap := ffTestSnapshot()
	snap.LockedRecipes = []planningLockedRecipe{{Name: "advanced", Products: []planningQuantity{{planningRef: planningRef{Item: "advanced", MaterialType: "item"}, Amount: 1}}}}
	// Asking directly for a locked item (no enabled recipe) yields nothing.
	rows := ffExpandRaw(snap, []ffRawDemand{{Name: "advanced", Quality: "normal", Count: 5}})
	if len(rows) != 0 {
		t.Fatalf("raw %v want empty", rows)
	}
}

func TestFFExpandRawCycleGuard(t *testing.T) {
	snap := ffTestSnapshot()
	q := func(item string, amt float64) planningQuantity {
		return planningQuantity{planningRef: planningRef{Item: item, MaterialType: "item"}, Amount: amt}
	}
	snap.Recipes = append(snap.Recipes, planningRecipe{
		Name: "a-to-b", Ingredients: []planningQuantity{q("b", 1)}, Products: []planningQuantity{q("a", 1)},
	}, planningRecipe{
		Name: "b-to-a", Ingredients: []planningQuantity{q("a", 1)}, Products: []planningQuantity{q("b", 1)},
	})
	rows := ffExpandRaw(snap, []ffRawDemand{{Name: "a", Quality: "normal", Count: 1}})
	if len(rows) != 0 {
		t.Fatalf("raw %v want empty on cycle", rows)
	}
}

func TestFFExpandRawEmptyAndNil(t *testing.T) {
	if rows := ffExpandRaw(nil, []ffRawDemand{{Name: "transport-belt", Quality: "normal", Count: 1}}); len(rows) != 0 {
		t.Fatalf("nil snapshot: %v", rows)
	}
	if rows := ffExpandRaw(ffTestSnapshot(), nil); len(rows) != 0 {
		t.Fatalf("nil demands: %v", rows)
	}
}

func TestFFPlanTextIncludesRaw(t *testing.T) {
	text, err := ffPlanText(&FactorioformPlan{
		Manifest: "m", Status: "CHANGES", Tick: 1,
		RawMaterials: []*FactorioformPlanRawMaterial{{Name: "iron-ore", Quality: "normal", Count: 6}, {Name: "stone", Quality: "rare", Count: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(text)
	if !strings.Contains(s, "RAW MATERIALS NEEDED:") || !strings.Contains(s, "6x iron-ore") || !strings.Contains(s, "2x stone@rare") {
		t.Fatalf("text missing raw section:\n%s", s)
	}
}
