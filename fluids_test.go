package main

import (
	"math"
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
		{"both actors", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &fluid, Amount: &amountZero, Actor: &ActorInput{Player: stringPointer("wricardo"), Character: stringPointer("4")}, Target: &EntityRefInput{Entity: stringPointer("storage-tank"), Position: &PositionInput{X: 1, Y: 1}}}},
		{"actor without entity", SetFluidInput{Mode: FluidWriteModeSet, Fluid: &fluid, Amount: &amountZero, Actor: &ActorInput{Player: stringPointer("wricardo")}}},
		{"character zero", SetFluidInput{Mode: FluidWriteModeClear, Actor: &ActorInput{Character: stringPointer("0")}, Target: &EntityRefInput{Entity: stringPointer("storage-tank"), Position: &PositionInput{X: 1, Y: 1}}}},
		{"empty entity name without coordinates", SetFluidInput{Mode: FluidWriteModeClear, Actor: &ActorInput{Character: stringPointer("4")}, Target: &EntityRefInput{Entity: &empty}}},
		{"nan coordinates", SetFluidInput{Mode: FluidWriteModeClear, Actor: &ActorInput{Character: stringPointer("4")}, Target: &EntityRefInput{Entity: stringPointer("storage-tank"), Position: &PositionInput{X: math.NaN(), Y: 2}}}},
	}
	for _, tc := range cases {
		if _, err := setFluid(nil, tc.input); err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
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

func TestFluidDecode(t *testing.T) {
	page, err := factoryDecode[FluidStockPage](`{"tick":10.5,"surface":"nauvis","force":"player","locationPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"totalPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"locations":[{"category":"stored","entityName":"storage-tank","entityType":"storage-tank","position":{"x":1,"y":2},"inventoryKind":"fluid","fluid":"crude-oil","amount":250,"temperature":25}],"totals":[{"fluid":"crude-oil","amount":250,"category":"stored"}]}`)
	if err != nil {
		t.Fatalf("decode fluid stock: %v", err)
	}
	if page.LocationPageInfo == nil || page.LocationPageInfo.Total != 1 || page.TotalPageInfo == nil || page.TotalPageInfo.Total != 1 || len(page.Locations) != 1 || len(page.Totals) != 1 {
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
