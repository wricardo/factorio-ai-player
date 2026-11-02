package main

import (
	"math"
	"strings"
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

// A research-locked recipe must be reported as such instead of being planned or
// replaced by an unrelated recipe that happens to produce the item.
func TestPlanningReportsLockedRecipeInsteadOfFallingBack(t *testing.T) {
	snapshot := planningSnapshot{
		Tick: 10,
		LockedRecipes: []planningLockedRecipe{{
			Name:     "steel-plate",
			Products: []planningQuantity{planningItemQuantity("steel-plate", 1)},
		}},
	}
	result, err := planBillOfMaterials(snapshot, planningTestOptions("steel-plate", 5))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Complete || len(result.Steps) != 0 || result.Expansions != 1 {
		t.Fatalf("locked recipe must not be planned: %+v", result)
	}
	locked := planningRequirementsByReason(result.Unresolved, "RECIPE_NOT_RESEARCHED")
	if len(locked) != 1 || !nearlyEqual(locked[0].Amount, 5) {
		t.Fatalf("unresolved locked demand: %#v", result.Unresolved)
	}
	if len(planningRequirementsByReason(result.RawRequirements, "RECIPE_NOT_RESEARCHED")) != 1 {
		t.Fatalf("raw locked demand: %#v", result.RawRequirements)
	}
}

// Gathered products (wood from trees, fish, stone from rocks) are raw material:
// they must not be reported as an unresolved, incomplete plan.
func TestPlanningTreatsGatheredProductsAsRaw(t *testing.T) {
	snapshot := planningSnapshot{
		Tick: 11,
		Recipes: []planningRecipe{{
			Name:        "wooden-chest",
			Ingredients: []planningQuantity{planningItemQuantity("wood", 2)},
			Products:    []planningQuantity{planningItemQuantity("wooden-chest", 1)},
		}},
		Gathered: []planningRef{planningItem("wood")},
	}
	result, err := planBillOfMaterials(snapshot, planningTestOptions("wooden-chest", 2))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !result.Complete {
		t.Fatalf("gathered raw material made the plan incomplete: %#v", result.Unresolved)
	}
	gathered := planningRequirementsByReason(result.RawRequirements, "GATHERED_RAW")
	if len(gathered) != 1 || !nearlyEqual(gathered[0].Amount, 4) || len(result.Unresolved) != 0 {
		t.Fatalf("gathered requirement: %#v unresolved %#v", result.RawRequirements, result.Unresolved)
	}
}

// The planner never falls back to a recipe listed only in the locked set, and an
// item with neither an enabled nor a locked recipe stays NO_ENABLED_RECIPE.
func TestPlanningDistinguishesMissingRecipeFromLockedRecipe(t *testing.T) {
	snapshot := planningSnapshot{
		Tick: 12,
		LockedRecipes: []planningLockedRecipe{{
			Name:     "locked-widget",
			Products: []planningQuantity{planningItemQuantity("locked-widget", 1)},
		}},
		Mined: []planningRef{planningItem("iron-ore")},
	}
	result, err := planBillOfMaterials(snapshot, planningTestOptions("iron-ore", 3))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !result.Complete || len(planningRequirementsByReason(result.RawRequirements, "MINED_RAW")) != 1 {
		t.Fatalf("mined raw demand: %+v", result)
	}
	missing, err := planBillOfMaterials(snapshot, planningTestOptions("nothing-produces-this", 1))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if missing.Complete || len(planningRequirementsByReason(missing.Unresolved, "NO_ENABLED_RECIPE")) != 1 {
		t.Fatalf("missing recipe demand: %+v", missing)
	}
}

// The recipe snapshot must exclude hidden, recycling and parameter recipes and
// carry locked recipes and gathered products so the plan can reason about them.
func TestBillOfMaterialsCatalogueSkipsHiddenAndGathersNaturalProducts(t *testing.T) {
	response := `{"tick":7,"force":"player","surface":"nauvis","bomRecipes":[],` +
		`"bomLockedRecipes":[{"name":"steel-plate","bomProducts":[{"item":"steel-plate","materialType":"item","amount":1}]}],` +
		`"bomStock":[],"bomMined":[],"bomGathered":[{"item":"wood","materialType":"item"}],` +
		`"stockTruncated":false,"recipeCatalogueTruncated":false}`
	r, commands := serveLuaOnce(t, response)
	plan, err := billOfMaterialsQuery(r, BillOfMaterialsInput{Force: "player", Surface: "nauvis", Item: "steel-plate",
		Amount: 2, Quality: "normal", Stock: &BOMStockInput{Source: BOMStockSourceFactory}, MaxDepth: 8, MaxExpansions: 100})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Complete || len(plan.Unresolved) != 1 || plan.Unresolved[0].Reason != "RECIPE_NOT_RESEARCHED" {
		t.Fatalf("locked recipe plan: %+v", plan)
	}
	lua := <-commands
	for _, want := range []string{
		`if recipe.hidden then return false end`,
		`if string.sub(category,1,9)=="recycling" then return false end`,
		`if category=="parameters" or category=="parameter" then return false end`,
		`result.bomLockedRecipes[#result.bomLockedRecipes+1]=entry`,
		`local is_gathered=t=="tree" or t=="plant" or t=="simple-entity" or t=="fish" or t=="asteroid-chunk"`,
		`result.bomGathered[#result.bomGathered+1]={item=p.name,materialType=p.type}`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("billOfMaterials Lua missing %q\n%s", want, lua)
		}
	}
}
