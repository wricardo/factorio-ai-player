package main

import (
	"strings"
	"testing"
)

// production must expose fluid flows next to item flows, using one window and
// one precision index for both.
func TestFactoryProductionReadsFluidStatistics(t *testing.T) {
	response := `{"tick":12,"surface":"nauvis","force":"player","window":"TEN_MINUTES",
	  "items":[{"item":"coal","produced":610.6,"consumed":190.5,"producedPerMinute":61.06,"consumedPerMinute":19.05}],
	  "fluids":[{"item":"petroleum-gas","produced":900,"consumed":880,"producedPerMinute":90,"consumedPerMinute":88}]}`
	r, commands := serveLuaOnce(t, response)
	production, err := factoryProductionQuery(r, &FactoryOverviewInput{Surface: "nauvis", Force: "player", Window: ProductionWindowTenMinutes})
	if err != nil {
		t.Fatal(err)
	}
	if len(production.Items) != 1 || len(production.Fluids) != 1 || production.Fluids[0].Item != "petroleum-gas" ||
		production.Fluids[0].ProducedPerMinute != 90 {
		t.Fatalf("production: %+v", production)
	}
	lua := <-commands
	for _, want := range []string{
		`get_item_production_statistics`,
		`get_fluid_production_statistics`,
		`local precision=defines.flow_precision_index.ten_minutes`,
		`fluids=fluids`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("production Lua missing %q\n%s", want, lua)
		}
	}
}

// The optional name filter trims rows without touching flow values.
func TestFactoryProductionNameFilter(t *testing.T) {
	production := &FactoryProduction{
		Items: []*ProductionItem{
			{Item: "coal", ProducedPerMinute: 61.06, ConsumedPerMinute: 19.05},
			{Item: "iron-ore", ProducedPerMinute: 200},
		},
		Fluids: []*ProductionItem{{Item: "water", ProducedPerMinute: 1200}, {Item: "steam", ProducedPerMinute: 600}},
	}
	filtered := filterProductionNames(production, []string{"coal", "steam"})
	if len(filtered.Items) != 1 || filtered.Items[0].Item != "coal" || filtered.Items[0].ConsumedPerMinute != 19.05 {
		t.Fatalf("item filter: %+v", filtered.Items)
	}
	if len(filtered.Fluids) != 1 || filtered.Fluids[0].Item != "steam" {
		t.Fatalf("fluid filter: %+v", filtered.Fluids)
	}
	if filtered.Items == nil || filtered.Fluids == nil {
		t.Fatalf("filtered lists must stay non-nil: %+v", filtered)
	}
	fresh := &FactoryProduction{
		Items:  []*ProductionItem{{Item: "coal"}, {Item: "iron-ore"}},
		Fluids: []*ProductionItem{{Item: "water"}, {Item: "steam"}},
	}
	if untouched := filterProductionNames(fresh, nil); len(untouched.Items) != 2 || len(untouched.Fluids) != 2 {
		t.Fatalf("nil filter changed rows: %+v", untouched)
	}
	if unmatched := filterProductionNames(production, []string{"nothing"}); len(unmatched.Items) != 0 || len(unmatched.Fluids) != 0 {
		t.Fatalf("unmatched filter: %+v", unmatched)
	}
	if filterProductionNames(nil, []string{"coal"}) != nil {
		t.Fatal("nil production must stay nil")
	}
}
