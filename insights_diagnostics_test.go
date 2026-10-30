package main

import (
	"math"
	"testing"
)

func TestMachineDiagnosticItemDemandUsesExactRecipeQuality(t *testing.T) {
	s := &machineDiagnosticSnapshot{Recipe: new("iron-gear-wheel"), RecipeQuality: new("rare"),
		RecipeDuration: 0.5, CraftingSpeed: 0.75,
		Ingredients: []diagnosticIngredient{{Name: "iron-plate", Type: "item", Amount: 2}},
		Input:       []diagnosticInput{{Name: "iron-plate", Quality: "normal", Count: 200}, {Name: "iron-plate", Quality: "rare", Count: 1}}}
	result := calculateMachineDiagnostics(s)
	demand := result.Shortfall[0]
	if demand.Available != 1 || demand.Missing != 1 || demand.RequiredPerCraft != 2 || demand.Quality == nil || *demand.Quality != "rare" {
		t.Fatalf("normal-quality inventory incorrectly satisfies rare demand: %+v", demand)
	}
	s.Input = append(s.Input, diagnosticInput{Name: "iron-plate", Quality: "rare", Count: 5})
	demand = calculateMachineDiagnostics(s).Shortfall[0]
	if demand.Available != 6 || demand.Missing != 0 {
		t.Fatalf("surplus must not produce negative demand: %+v", demand)
	}
}

func TestMachineDiagnosticFluidDemandRespectsTemperature(t *testing.T) {
	s := &machineDiagnosticSnapshot{Recipe: new("steam-process"), RecipeQuality: new("legendary"),
		RecipeDuration: 2, CraftingSpeed: 1,
		Ingredients: []diagnosticIngredient{{Name: "steam", Type: "fluid", Amount: 100, MinimumTemperature: new(165.0), MaximumTemperature: new(500.0)}},
		Fluids:      []diagnosticFluid{{Name: "steam", Amount: 300, Temperature: 100}, {Name: "steam", Amount: 30, Temperature: 165}, {Name: "steam", Amount: 40, Temperature: 500}, {Name: "steam", Amount: 300, Temperature: 501}, {Name: "water", Amount: 300, Temperature: 200}}}
	demand := calculateMachineDiagnostics(s).Shortfall[0]
	if demand.Available != 70 || demand.Missing != 30 || demand.Quality != nil {
		t.Fatalf("only compatible input fluid should satisfy demand: %+v", demand)
	}
	s.Ingredients[0].Temperature = new(500.0)
	demand = calculateMachineDiagnostics(s).Shortfall[0]
	if demand.Available != 40 || demand.Missing != 60 || *demand.MinimumTemperatureCelsius != 500 || *demand.MaximumTemperatureCelsius != 500 {
		t.Fatalf("exact temperature must override range: %+v", demand)
	}
}

func TestMachineDiagnosticTheoreticalRatesDoNotClaimActualOutput(t *testing.T) {
	s := &machineDiagnosticSnapshot{Recipe: new("test-process"), RecipeDuration: 2, CraftingSpeed: 3,
		ProductivityBonus: new(0.5), Products: []diagnosticProduct{
			{Name: "fixed", Type: "item", Amount: new(4.0), Probability: 1},
			{Name: "random", Type: "item", AmountMin: new(1.0), AmountMax: new(5.0), Probability: 0.5, ExtraCountFraction: 0.25},
			{Name: "fluid", Type: "fluid", Amount: new(10.0), Probability: 0.2}}}
	result := calculateMachineDiagnostics(s).Throughput
	if result.TheoreticalCraftsPerSecond != 1.5 || result.ActualCraftsPerSecond != nil {
		t.Fatalf("snapshot must expose theoretical rate and unknown actual rate: %+v", result)
	}
	for i, want := range []float64{6, 2.625, 3} {
		if got := result.TheoreticalBaseProducts[i].TheoreticalBaseUnitsPerSecond; math.Abs(got-want) > 1e-12 {
			t.Errorf("product %d base rate: got %g, want %g; productivity must not enter base yield", i, got, want)
		}
	}
}

func TestMachineDiagnosticMissingRecipeDiffersFromEmptyRecipe(t *testing.T) {
	result := calculateMachineDiagnostics(&machineDiagnosticSnapshot{})
	if result.Shortfall != nil || result.Throughput != nil || result.ThroughputUnavailableReason == nil {
		t.Fatalf("absent recipe must not report empty demand or zero throughput: %+v", result)
	}
	result = calculateMachineDiagnostics(&machineDiagnosticSnapshot{Recipe: new("free-recipe"), RecipeDuration: 1, CraftingSpeed: 1})
	if result.Shortfall == nil || len(result.Shortfall) != 0 || result.Throughput == nil || result.Throughput.TheoreticalCraftsPerSecond != 1 {
		t.Fatalf("selected zero-ingredient recipe should have known empty demand: %+v", result)
	}
}

func TestMachineDiagnosticNonfiniteOrZeroDurationNeverProducesFakeRate(t *testing.T) {
	for _, duration := range []float64{0, -1, math.Inf(1), math.NaN()} {
		result := calculateMachineDiagnostics(&machineDiagnosticSnapshot{Recipe: new("invalid-duration"), RecipeDuration: duration, CraftingSpeed: 1})
		if result.Throughput != nil || result.ThroughputUnavailableReason == nil {
			t.Fatalf("duration %g must leave throughput unknown: %+v", duration, result)
		}
	}
}

func TestMachineDiagnosticBlockingPreservesUnknownStatus(t *testing.T) {
	for _, test := range []struct {
		status   string
		category MachineBlockCategory
		action   bool
	}{
		{"working", MachineBlockCategoryNone, false},
		{"no_recipe", MachineBlockCategoryMissingRecipe, true},
		{"fluid_ingredient_shortage", MachineBlockCategoryMissingIngredients, true},
		{"no_fuel", MachineBlockCategoryMissingFuel, true},
		{"low_power", MachineBlockCategoryMissingPower, true},
		{"full_output", MachineBlockCategoryOutputBlocked, true},
		{"disabled_by_control_behavior", MachineBlockCategoryDisabled, true},
		{"future_engine_status", MachineBlockCategoryOther, false},
	} {
		t.Run(test.status, func(t *testing.T) {
			code := 123
			result := diagnosticBlocked(&test.status, &code)
			if result.Category != test.category || result.RawStatusName == nil || *result.RawStatusName != test.status || result.RawStatusCode == nil || *result.RawStatusCode != code || (result.SuggestedAction != nil) != test.action {
				t.Fatalf("status classification lost raw condition: %+v", result)
			}
		})
	}
}

func TestDiagnosticsEntityRejectsNonfinitePositionAndInvalidUnit(t *testing.T) {
	for _, position := range []*Position{nil, {X: math.NaN()}, {Y: math.Inf(1)}} {
		if _, err := diagnosticsEntityLua("nauvis", "inserter", nil, position, ""); err == nil {
			t.Fatal("invalid entity position accepted")
		}
	}
	for _, id := range []string{"0", "-1", "1.5", "18446744073709551616", "oops"} {
		if _, err := diagnosticsEntityLua("nauvis", "inserter", &id, &Position{}, ""); err == nil {
			t.Fatalf("invalid unit %q accepted", id)
		}
	}
}
