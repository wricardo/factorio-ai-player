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
		{"large limit", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: maxPageSize + 1, TotalsLimit: 10}},
		{"zero totals limit", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 0}},
		{"large totals limit", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: maxPageSize + 1}},
		{"negative offset", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Offset: -1}},
		{"negative totals offset", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, TotalsOffset: -5}},
		{"empty fluid", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Fluid: &empty}},
		{"blank fluid", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Fluid: &blank}},
		{"oversized area", &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 10, TotalsLimit: 10, Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: maxAreaTiles + 1, RightBottomY: 10}}},
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
	page, err := factoryDecode[FluidStockPage](`{"tick":10.5,"surface":"nauvis","force":"player","locationPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"totalPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"locations":[{"category":"stored","entityName":"storage-tank","entityType":"storage-tank","position":{"x":1,"y":2},"inventoryKind":"fluid","unitNumber":"42","boxIndex":1,"productionType":"output","filter":"crude-oil","segmentId":"1:segment:9","fluid":"crude-oil","amount":250,"temperature":25,"capacity":500}],"totals":[{"fluid":"crude-oil","amount":250,"category":"stored"}]}`)
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
	if location.BoxIndex != 1 || location.Capacity == nil || *location.Capacity != 500 {
		t.Fatalf("unexpected box identity %+v", location)
	}

	write, err := factoryDecode[FluidWriteResult](`{"tick":11,"surface":"nauvis","name":"storage-tank","unitNumber":"42","position":{"x":3.5,"y":4.5},"fluid":"water","before":0,"after":25000,"capacity":25000,"scope":"ENTITY","mode":"SET","boxesAffected":1,"inserted":25000}`)
	if err != nil {
		t.Fatalf("decode fluid write: %v", err)
	}
	if write.UnitNumber == nil || *write.UnitNumber != "42" || write.Name != "storage-tank" {
		t.Fatalf("unexpected write result %+v", write)
	}
	if write.After != 25000 || write.Capacity != 25000 || write.BoxesAffected != 1 || write.Mode != "SET" {
		t.Fatalf("unexpected write amounts %+v", write)
	}
	if write.Scope != FluidWriteScopeEntity || write.Inserted != 25000 || write.Before != 0 {
		t.Fatalf("unexpected write scope or accepted amount %+v", write)
	}
	if write.Fluid == nil || *write.Fluid != "water" {
		t.Fatalf("unexpected fluid %v", write.Fluid)
	}
}

// TestSetFluidUsesSegmentScope proves a box that belongs to an engine fluid
// segment reports the whole segment amount next to the segment capacity, so an
// accepted write cannot look clamped.
func TestSetFluidUsesSegmentScope(t *testing.T) {
	stubs := `
prototypes={fluid={water={name="water"}}}
local segment_amount, box_amount = 450, 50
local fb={}
function refresh() fb[1]={name="water",amount=box_amount,temperature=15} end
refresh()
fb.get_capacity=function(index) return 500 end
fb.get_fluid_segment_id=function(index) return 7 end
fb.get_fluid_segment_contents=function(index) return {water=segment_amount} end
local e={name="pipe",type="pipe",unit_number=99,position={x=1.5,y=2.5},
  surface={name="nauvis"},force={name="player"},fluids_count=1,fluidbox=fb,
  clear_fluid_inside=function() segment_amount=0; box_amount=0; refresh() end,
  insert_fluid=function(params) segment_amount=segment_amount+params.amount; box_amount=box_amount+params.amount; refresh(); return params.amount end}
`
	assertions := `
assert(captured.scope=="SEGMENT","a segmented box must report segment scope, got "..tostring(captured.scope))
assert(captured.before==450,"before must be the segment amount, got "..tostring(captured.before))
assert(captured.after==100,"after must be the segment amount, got "..tostring(captured.after))
assert(captured.capacity==500,"capacity must be the segment capacity")
assert(captured.inserted==100,"the accepted amount must be reported")
assert(captured.boxesAffected==1,"one box changed")
`
	lua := setFluidLua("local e=stub_entity\n", "SET", "water", 100, math.NaN())
	runLuaFixture(t, lua, stubs+"\nlocal stub_entity=e\n", assertions)

	// A box outside any segment keeps entity scope and its own share.
	entityStubs := `
prototypes={fluid={water={name="water"}}}
local fb={}
fb[1]={name="water",amount=40,temperature=20}
fb.get_capacity=function(index) return 100 end
fb.get_fluid_segment_id=function(index) return nil end
fb.get_fluid_segment_contents=function(index) error("non-segment boxes must not request segment contents") end
local e={name="storage-tank",type="storage-tank",unit_number=7,position={x=0,y=0},
  surface={name="nauvis"},force={name="player"},fluids_count=1,fluidbox=fb,
  clear_fluid_inside=function() fb[1]={name="water",amount=0,temperature=20} end,
  insert_fluid=function(params) fb[1].amount=fb[1].amount+params.amount; return params.amount end}
`
	entityAssertions := `
assert(captured.scope=="ENTITY","an unsegmented box must report entity scope")
assert(captured.before==40 and captured.after==60 and captured.capacity==100 and captured.inserted==20)
`
	runLuaFixture(t, setFluidLua("local e=stub_entity\n", "ADD", "water", 20, math.NaN()), entityStubs+"\nlocal stub_entity=e\n", entityAssertions)
}

// TestFluidStockLuaReportsBoxIdentity runs the stock read against a stub pipe
// and checks every per-box field, including the temperature that
// get_fluid_contents cannot report.
func TestFluidStockLuaReportsBoxIdentity(t *testing.T) {
	stubs := `
local surface={name="nauvis",index=1}
local force={name="player"}
game={tick=12,surfaces={nauvis=surface},forces={player=force}}
local fb={}
fb[1]={name="steam",amount=99.87,temperature=165}
fb.get_capacity=function(index) return 500 end
fb.get_prototype=function(index) return {production_type="input",volume=100} end
fb.get_filter=function(index) return {name="steam",minimum_temperature=0,maximum_temperature=1000} end
fb.get_fluid_segment_id=function(index) return 9 end
local entity={valid=true,name="pipe",type="pipe",unit_number=42,position={x=42.5,y=-6.5},
  fluids_count=1,fluidbox=fb,surface=surface,force=force}
surface.find_entities_filtered=function(filter)
  assert(filter.type~=nil,"the stock scan must prefilter fluid-capable types")
  return {entity}
end
`
	assertions := `
local row=captured.locations[1]
assert(row~=nil,"one location expected")
assert(row.entityName=="pipe" and row.unitNumber=="42" and row.boxIndex==1)
assert(row.productionType=="input","production type must be reported")
assert(row.filter=="steam","box filter must be reported")
assert(row.segmentId=="1:segment:9","segment id must be surface-qualified")
assert(row.temperature==165,"temperature must come from the fluidbox")
assert(row.capacity==500,"engine segment capacity must be reported")
assert(row.amount==99.87 and row.fluid=="steam")
assert(captured.totals[1].amount==99.87)
`
	lua := fluidStockLua("nauvis", "player", "", "nil", 10, 0, 10, 0)
	runLuaFixture(t, lua, stubs, assertions)
}

// TestFluidSegmentsClassifiesProblems proves one area call flags a full output
// box and a starved input box on a shared segment.
func TestFluidSegmentsClassifiesProblems(t *testing.T) {
	stubs := `
local surface={name="nauvis",index=1}
local force={name="player"}
game={tick=33,surfaces={nauvis=surface},forces={player=force}}
local fb={}
fb[1]={name="heavy-oil",amount=500,temperature=60}
fb[2]=nil
fb.get_capacity=function(index) return 1000 end
fb.get_prototype=function(index)
  if index==1 then return {production_type="output",volume=500} end
  return {production_type="input",volume=500}
end
fb.get_filter=function(index) return nil end
fb.get_fluid_segment_id=function(index) return 11 end
fb.get_fluid_segment_contents=function(index) return {["heavy-oil"]=500} end
local refinery={valid=true,name="oil-refinery",type="assembling-machine",unit_number=5,
  position={x=5.5,y=5.5},fluids_count=2,fluidbox=fb,surface=surface,force=force}
surface.find_entities_filtered=function(filter) return {refinery} end
`
	assertions := `
assert(captured.visitedBoxes==2,"both boxes must be visited")
assert(#captured.segments==1,"both boxes share one segment")
local segment=captured.segments[1]
assert(segment.id=="1:segment:11" and segment.capacity==1000)
assert(segment.temperature==60 and segment.amount==500)
assert(#segment.members==2 and segment.members[1].unitNumber=="5")
local kinds={}
for _,problem in ipairs(captured.problems) do kinds[problem.kind]=true end
assert(kinds.OUTPUT_FULL,"a full output box must be classified")
assert(kinds.INPUT_EMPTY,"an empty input box must be classified")
assert(not kinds.MIXED,"one fluid is not mixed")
`
	lua := fluidSegmentsLua("nauvis", "player", "{{0,0},{20,20}}", "{leftTopX=0,leftTopY=0,rightBottomX=20,rightBottomY=20}", 500)
	runLuaFixture(t, lua, stubs, assertions)
}

// TestFluidSegmentsValidation rejects inputs before RCON.
func TestFluidSegmentsValidation(t *testing.T) {
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}
	cases := []struct {
		name  string
		input *FluidSegmentsInput
		want  string
	}{
		{"nil input", nil, "input is required"},
		{"empty surface", &FluidSegmentsInput{Surface: "", Force: "player", Area: area}, "surface"},
		{"empty force", &FluidSegmentsInput{Surface: "nauvis", Force: " ", Area: area}, "force"},
		{"missing area", &FluidSegmentsInput{Surface: "nauvis", Force: "player"}, "area is required"},
		{"too wide area", &FluidSegmentsInput{Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: maxAreaTiles + 1, RightBottomY: 10}}, "area"},
		{"max boxes too large", &FluidSegmentsInput{Surface: "nauvis", Force: "player", Area: area, MaxBoxes: maxFluidBoxes + 1}, "maxBoxes"},
		{"max boxes negative", &FluidSegmentsInput{Surface: "nauvis", Force: "player", Area: area, MaxBoxes: -1}, "maxBoxes"},
	}
	for _, tc := range cases {
		if _, err := fluidSegmentsQuery(nil, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected %q error, got %v", tc.name, tc.want, err)
		}
	}
}
