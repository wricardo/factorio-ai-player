package main

import (
	"math"
	"strings"
	"testing"
)

func TestSetFluidValidation(t *testing.T) {
	fluid := "water"
	amountZero := 0.0
	amountNegative := -1.0
	amountNaN := math.NaN()
	amountInf := math.Inf(1)
	temperatureInf := math.Inf(1)
	empty := ""
	blank := "   "

	cases := []struct {
		name  string
		input SetFluidInput
	}{
		{"unknown mode", SetFluidInput{Fluid: &fluid, Amount: &amountZero}},
		{"set without fluid", SetFluidInput{Mode: FluidWriteModeSet, Amount: &amountZero}},
		{"add without fluid", SetFluidInput{Mode: FluidWriteModeAdd, Amount: &amountZero}},
		{"set with blank fluid", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &blank, Amount: &amountZero}},
		{"negative amount", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &fluid, Amount: &amountNegative}},
		{"nan amount", SetFluidInput{Mode: FluidWriteModeAdd, Fluid: &fluid, Amount: &amountNaN}},
		{"infinite amount", SetFluidInput{Mode: FluidWriteModeAdd, Fluid: &fluid, Amount: &amountInf}},
		{"infinite temperature", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &fluid, Amount: &amountZero, Temperature: &temperatureInf}},
		{"no actor", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &fluid, Amount: &amountZero}},
		{"both actors", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &fluid, Amount: &amountZero, Player: stringPointer("wricardo"), Character: stringPointer("4")}},
		{"actor without entity", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &fluid, Amount: &amountZero, Player: stringPointer("wricardo")}},
		{"character zero", SetFluidInput{Mode: FluidWriteModeClear, Character: stringPointer("0"), Entity: stringPointer("storage-tank"), X: floatPointer(1), Y: floatPointer(1)}},
		{"empty entity name without coordinates", SetFluidInput{Mode: FluidWriteModeClear, Character: stringPointer("4"), Entity: &empty}},
		{"nan coordinates", SetFluidInput{Mode: FluidWriteModeClear, Character: stringPointer("4"), Entity: stringPointer("storage-tank"), X: floatPointer(math.NaN()), Y: floatPointer(2)}},
	}
	for _, tc := range cases {
		if _, err := setFluid(nil, tc.input); err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
	}

}

func TestSetFluidLuaInvariants(t *testing.T) {
	lua := setFluidLua("-- prefix\n", "SET", "crude-oil", 250, 45)
	for _, want := range []string{
		"e.clear_fluid_inside()",
		"e.insert_fluid(params)",
		"e.get_fluid_contents()",
		"e.fluidbox.get_capacity(index)",
		"e.fluids_count",
		"prototypes.fluid[fluid_name]",
		luaString("SET"),
		luaString("crude-oil"),
		"if mode == \"CLEAR\" then",
		"local amount, temperature = 250, 45",
		"if temperature ~= nil then params.temperature = temperature end",
		"boxesAffected=boxes",
		"inserted=inserted",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("setFluid lua missing %q", want)
		}
	}
	for _, banned := range []string{"add_fluid", "get_fluid_capacity", "e.get_fluid("} {
		if strings.Contains(lua, banned) {
			t.Fatalf("setFluid lua must not use the 2.1-only %q", banned)
		}
	}
	if !strings.HasPrefix(lua, luaFail) {
		t.Fatal("setFluid lua must start with the fail helper")
	}

	added := setFluidLua("", "ADD", "water", 10, math.NaN())
	if !strings.Contains(added, "local amount, temperature = 10, nil") {
		t.Fatal("ADD without temperature must pass nil")
	}
	if !strings.Contains(added, `if mode == "CLEAR" then`) || !strings.Contains(added, "else") {
		t.Fatal("mode dispatch must remain explicit")
	}
}

func TestFluidStockValidation(t *testing.T) {
	empty := ""
	blank := "  "
	cases := []struct {
		name  string
		input *FluidStockInput
	}{
		{"empty surface", &FluidStockInput{Surface: "", Force: "player", Limit: 10, TotalsLimit: 10}},
		{"empty force", &FluidStockInput{Surface: "nauvis", Force: "", Limit: 10, TotalsLimit: 10}},
		{"zero limit", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 0, TotalsLimit: 10}},
		{"large limit", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 1001, TotalsLimit: 10}},
		{"zero totals limit", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 0}},
		{"large totals limit", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 1001}},
		{"negative offset", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Offset: -1}},
		{"negative totals offset", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, TotalsOffset: -5}},
		{"empty fluid", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Fluid: &empty}},
		{"blank fluid", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Fluid: &blank}},
		{"oversized area", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 300, RightBottomY: 10}}},
		{"inverted area", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Area: &FactoryAreaInput{LeftTopX: 10, LeftTopY: 0, RightBottomX: 0, RightBottomY: 10}}},
		{"non finite area", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Area: &FactoryAreaInput{LeftTopX: math.NaN(), LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}}},
	}
	for _, tc := range cases {
		if _, err := fluidStockQuery(nil, tc.input); err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
	}
}

func TestFluidStockLuaInvariants(t *testing.T) {
	lua := fluidStockLua("nauvis", "player", "water", "nil", 50, 5, 20, 2)
	for _, want := range []string{
		"surface.find_entities_filtered{force=scan_force, area=area}",
		"prototypes.fluid[fluid_name]",
		"entity.get_fluid_contents()",
		"entity.fluids_count",
		"entity-ghost",
		`scan_forces[#scan_forces+1] = "neutral"`,
		luaString("nauvis"),
		luaString("player"),
		luaString("water"),
		"local area, limit, offset, totals_limit, totals_offset = nil, 50, 5, 20, 2",
		`if type(value) == "table" then amount, temp = value.amount, value.temperature end`,
		"inventoryKind=\"fluid\"",
		"locations=page",
		"totals=totals_page",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("fluidStock lua missing %q", want)
		}
	}

	neutral := fluidStockLua("nauvis", "neutral", "", "{{0,0},{10,10}}", 1, 0, 1, 0)
	if strings.Contains(neutral, `scan_forces[#scan_forces+1] = "neutral"`) && !strings.Contains(neutral, `if force_name ~= "neutral" then scan_forces[#scan_forces+1] = "neutral" end`) {
		t.Fatal("the neutral force must be scanned at most once")
	}
	if !strings.Contains(neutral, `if force_name ~= "neutral" then scan_forces[#scan_forces+1] = "neutral" end`) {
		t.Fatal("the neutral force scan must be guarded")
	}
	if !strings.Contains(neutral, "local area, limit, offset, totals_limit, totals_offset = {{0,0},{10,10}}, 1, 0, 1, 0") {
		t.Fatal("area bounds must be inlined")
	}
}

func TestFluidDecode(t *testing.T) {
	page, err := factoryDecode[FluidStockPage](`{"tick":10.5,"surface":"nauvis","force":"player","total":1,"totalsTotal":1,"limit":100,"offset":0,"totalsLimit":100,"totalsOffset":0,"locations":[{"category":"stored","entityName":"storage-tank","entityType":"storage-tank","position":{"x":1,"y":2},"inventoryKind":"fluid","fluid":"crude-oil","amount":250,"temperature":25}],"totals":[{"fluid":"crude-oil","amount":250,"category":"stored"}]}`)
	if err != nil {
		t.Fatalf("decode fluid stock: %v", err)
	}
	if page.Total != 1 || page.TotalsTotal != 1 || page.Limit != 100 || len(page.Locations) != 1 || len(page.Totals) != 1 {
		t.Fatalf("unexpected page %+v", page)
	}
	location := page.Locations[0]
	if location.EntityName != "storage-tank" || location.Fluid != "crude-oil" || location.Amount != 250 || location.InventoryKind != "fluid" {
		t.Fatalf("unexpected location %+v", location)
	}
	if location.Position == nil || location.Position.X != 1 || location.Position.Y != 2 {
		t.Fatalf("unexpected position %+v", location.Position)
	}
	if location.Temperature == nil || *location.Temperature != 25 {
		t.Fatalf("unexpected temperature %v", location.Temperature)
	}

	write, err := factoryDecode[FluidWriteResult](`{"tick":11,"surface":"nauvis","name":"storage-tank","unitNumber":"42","position":{"x":3.5,"y":4.5},"fluid":"water","before":0,"after":25000,"capacity":25000,"mode":"SET","boxesAffected":1,"inserted":25000}`)
	if err != nil {
		t.Fatalf("decode fluid write: %v", err)
	}
	if write.UnitNumber == nil || *write.UnitNumber != "42" || write.Name != "storage-tank" {
		t.Fatalf("unexpected write result %+v", write)
	}
	if write.After != 25000 || write.Capacity != 25000 || write.BoxesAffected != 1 || write.Mode != "SET" {
		t.Fatalf("unexpected write amounts %+v", write)
	}
	if write.Fluid == nil || *write.Fluid != "water" {
		t.Fatalf("unexpected fluid %v", write.Fluid)
	}
}
