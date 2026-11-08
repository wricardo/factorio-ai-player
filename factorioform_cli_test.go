package main

import (
	"strings"
	"testing"
)

// TestFFPlanTextUnitNumbers asserts that ffPlanText prints the live unit
// number for every detailed diff row that carries one, omits it for rows
// without one (CREATE), and resolves per-step Units from step addresses.
func TestFFPlanTextUnitNumbers(t *testing.T) {
	plan := &FactorioformPlan{
		Manifest: "test",
		Status:   "CHANGES",
		Summary:  &FactorioformPlanSummary{Create: 1, Update: 1, Replace: 1, Delete: 1, Noop: 1},
		Diffs: []*FactorioformPlanDiff{
			{Action: "CREATE", Address: "chest.new", Entity: new("wooden-chest"), Position: &Position{X: 1.5, Y: 2.5}},
			{Action: "UPDATE", Address: "chest.old", Entity: new("wooden-chest"), Position: &Position{X: 3.5, Y: 2.5}, UnitNumber: new("42")},
			{Action: "REPLACE", Address: "inserter.swap", Entity: new("inserter"), Position: &Position{X: 5.5, Y: 2.5}, UnitNumber: new("43")},
			{Action: "DELETE", Address: "belt.gone", Entity: new("transport-belt"), Position: &Position{X: 7.5, Y: 2.5}, UnitNumber: new("44")},
			{Action: "NOOP", Address: "chest.same", Entity: new("wooden-chest"), UnitNumber: new("45")},
		},
		Steps: []*FactorioformPlanStep{
			{Order: 1, Kind: "delete", Operation: "removeMany", Addresses: []string{"belt.gone"}, RequiresActor: true},
			{Order: 2, Kind: "create", Operation: "placeMany", Addresses: []string{"chest.new"}, RequiresActor: true},
			{Order: 3, Kind: "config", Operation: "setInserterSettings", Addresses: []string{"inserter.swap", "chest.old", "inserter.swap"}},
		},
	}
	out, err := ffPlanText(plan)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)

	for _, want := range []string{"    Unit: 42\n", "    Unit: 43\n", "    Unit: 44\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// NOOP rows are skipped entirely, even with a unit number.
	if strings.Contains(text, "chest.same") || strings.Contains(text, "Unit: 45") {
		t.Errorf("NOOP row leaked into text output:\n%s", text)
	}
	// CREATE row has no unit line.
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if l == "  CREATE chest.new (wooden-chest)" {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "    Unit:") {
				t.Errorf("CREATE row unexpectedly has a unit line: %s", lines[i+1])
			}
		}
	}
	// Step units resolve from addresses, deduped; create step has none.
	if !strings.Contains(text, "    Units: 44\n") {
		t.Errorf("delete step missing Units line:\n%s", text)
	}
	if !strings.Contains(text, "    Units: 43, 42\n") {
		t.Errorf("config step missing deduped Units line:\n%s", text)
	}
	createStep := "  Step 2 [create] placeMany\n    Addresses: [chest.new]\n"
	if !strings.Contains(text, createStep+"    Requires: actor with inventory\n") {
		t.Errorf("create step should have no Units line:\n%s", text)
	}
}
