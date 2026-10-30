package main

import (
	"math"
	"testing"
)

func planningItem(name string) planningRef {
	return planningRef{Item: name, MaterialType: "item"}
}

func planningItemQuantity(name string, amount float64) planningQuantity {
	return planningQuantity{planningRef: planningItem(name), Amount: amount}
}

func planningTestOptions(item string, amount float64) planningOptions {
	return planningOptions{Item: item, Amount: amount, Quality: "normal", StockSource: "FACTORY", MaxDepth: 16, MaxExpansions: 1000}
}

func planningMaterialByItem(t *testing.T, result *planningResult, item string) *planningMaterial {
	t.Helper()
	for _, material := range result.Materials {
		if material.Item == item {
			return material
		}
	}
	t.Fatalf("material %q missing from %#v", item, result.Materials)
	return nil
}

func planningRequirementsByReason(list []*planningRequirement, reason string) []*planningRequirement {
	var matches []*planningRequirement
	for _, entry := range list {
		if entry.Reason == reason {
			matches = append(matches, entry)
		}
	}
	return matches
}

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b))
}

// Stock must be spent once across the whole recursive plan: 3 stored gears cover
// 3 of the 10 requested gears, so only 7 crafts consume plates, and the 7 stored
// plates are allocated to those crafts rather than counted again per level.
func TestPlanningAllocatesStockOnce(t *testing.T) {
	snapshot := planningSnapshot{
		Tick:    120,
		Force:   "player",
		Surface: "nauvis",
		Recipes: []planningRecipe{{
			Name:        "gear",
			Ingredients: []planningQuantity{planningItemQuantity("plate", 2)},
			Products:    []planningQuantity{planningItemQuantity("gear", 1)},
		}},
		Stock: []planningStock{
			{planningRef: planningItem("gear"), Quality: "normal", Amount: 3},
			{planningRef: planningItem("plate"), Quality: "normal", Amount: 7},
		},
		Mined: []planningRef{planningItem("plate")},
	}
	result, err := planBillOfMaterials(snapshot, planningTestOptions("gear", 10))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !result.Complete {
		t.Fatalf("plan incomplete, unresolved %#v", result.Unresolved)
	}
	// One expansion for the gear crafts, one for the mined plate leaf.
	if result.Expansions != 2 {
		t.Fatalf("expansions = %d, want 2", result.Expansions)
	}
	gear := planningMaterialByItem(t, result, "gear")
	if !nearlyEqual(gear.Required, 10) || !nearlyEqual(gear.AllocatedStock, 3) || !nearlyEqual(gear.MissingStock, 7) {
		t.Fatalf("gear material = %#v", gear)
	}
	plate := planningMaterialByItem(t, result, "plate")
	if !nearlyEqual(plate.Required, 14) || !nearlyEqual(plate.AvailableStock, 7) || !nearlyEqual(plate.AllocatedStock, 7) {
		t.Fatalf("plate material = %#v", plate)
	}
	if !nearlyEqual(plate.MissingStock, 7) {
		t.Fatalf("plate missing = %v, want 7", plate.MissingStock)
	}
	if len(result.Steps) != 1 || result.Steps[0].Recipe != "gear" || !nearlyEqual(result.Steps[0].Crafts, 7) {
		t.Fatalf("steps = %#v", result.Steps)
	}
	if len(result.RawRequirements) != 1 || result.RawRequirements[0].Reason != "MINED_RAW" || !nearlyEqual(result.RawRequirements[0].Amount, 7) {
		t.Fatalf("raw requirements = %#v", result.RawRequirements)
	}
}

// Two recipes that consume each other must terminate with an explicit cycle
// report instead of recursing or silently claiming the plan is complete.
func TestPlanningReportsRecipeCycle(t *testing.T) {
	snapshot := planningSnapshot{
		Tick: 30,
		Recipes: []planningRecipe{
			{Name: "a", Ingredients: []planningQuantity{planningItemQuantity("b", 1)}, Products: []planningQuantity{planningItemQuantity("a", 1)}},
			{Name: "b", Ingredients: []planningQuantity{planningItemQuantity("a", 1)}, Products: []planningQuantity{planningItemQuantity("b", 1)}},
		},
	}
	result, err := planBillOfMaterials(snapshot, planningTestOptions("a", 1))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Complete {
		t.Fatal("cyclic plan reported complete")
	}
	cycles := planningRequirementsByReason(result.Unresolved, "RECIPE_CYCLE")
	if len(cycles) != 1 {
		t.Fatalf("cycle entries = %#v", result.Unresolved)
	}
	if cycles[0].Item != "a" && cycles[0].Item != "b" {
		t.Fatalf("cycle reported for unexpected item %q", cycles[0].Item)
	}
}

// Probabilistic yields must surface as expected values with an explicit warning,
// never as guaranteed output.
func TestPlanningFlagsProbabilisticYield(t *testing.T) {
	snapshot := planningSnapshot{
		Tick: 7,
		Recipes: []planningRecipe{{
			Name:        "risky",
			Ingredients: []planningQuantity{planningItemQuantity("plate", 1)},
			Products:    []planningQuantity{{planningRef: planningItem("shard"), Amount: 1, Probabilistic: true}},
		}},
		Mined: []planningRef{planningItem("plate")},
	}
	result, err := planBillOfMaterials(snapshot, planningTestOptions("shard", 1))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !result.Probabilistic {
		t.Fatal("probabilistic yield not flagged")
	}
	if len(result.Warnings) == 0 {
		t.Fatal("probabilistic plan has no warning")
	}
}

// An override that does not actually produce the requested item is a caller
// error, not an empty plan.
func TestPlanningRejectsOverrideWithoutYield(t *testing.T) {
	snapshot := planningSnapshot{
		Tick: 1,
		Recipes: []planningRecipe{{
			Name:        "other",
			Ingredients: []planningQuantity{planningItemQuantity("plate", 1)},
			Products:    []planningQuantity{planningItemQuantity("other", 1)},
		}},
	}
	options := planningTestOptions("gear", 1)
	options.Overrides = map[string]string{"gear": "other"}
	if _, err := planBillOfMaterials(snapshot, options); err == nil {
		t.Fatal("override without matching yield accepted")
	}
}

// Expansion bounds must fail the plan explicitly rather than reporting a
// complete, silently truncated dependency tree.
func TestPlanningExpansionLimitIsExplicit(t *testing.T) {
	snapshot := planningSnapshot{
		Tick: 5,
		Recipes: []planningRecipe{{
			Name:        "widget",
			Ingredients: []planningQuantity{planningItemQuantity("plate", 1)},
			Products:    []planningQuantity{planningItemQuantity("widget", 1)},
		}},
		Mined: []planningRef{planningItem("plate")},
	}
	options := planningTestOptions("widget", 1)
	options.MaxExpansions = 1
	snapshot.Recipes = append(snapshot.Recipes, planningRecipe{
		Name:        "plate",
		Ingredients: []planningQuantity{planningItemQuantity("ore", 1)},
		Products:    []planningQuantity{planningItemQuantity("plate", 1)},
	})
	result, err := planBillOfMaterials(snapshot, options)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Complete {
		t.Fatal("bounded plan reported complete")
	}
	if len(planningRequirementsByReason(result.Unresolved, "EXPANSION_LIMIT")) == 0 {
		t.Fatalf("no expansion limit reported: %#v", result.Unresolved)
	}
}
