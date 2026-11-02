package main

import (
	"strings"
	"testing"
)

func TestTrainsQueryLuaValidation(t *testing.T) {
	station := ""
	blank := ""
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: maxAreaTiles + 1, RightBottomY: 10}
	cases := []struct {
		name  string
		input *TrainsInput
		want  string
	}{
		{"empty surface", &TrainsInput{Surface: "", Force: "player", Limit: 10}, "surface must not be empty"},
		{"empty force", &TrainsInput{Surface: "nauvis", Force: "", Limit: 10}, "force must not be empty"},
		{"limit zero", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 0}, "limit must be between 1 and 10000"},
		{"limit too large", &TrainsInput{Surface: "nauvis", Force: "player", Limit: maxPageSize + 1}, "limit must be between 1 and 10000"},
		{"negative offset", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 10, Offset: -1}, "offset must be non-negative"},
		{"empty station", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 10, Station: &station}, "station must not be empty"},
		{"area too wide", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 10, Area: area}, "area must have positive dimensions no greater than 1024 tiles"},
		{"blank surface from schema default", &TrainsInput{Surface: blank, Force: "player", Limit: 10}, "surface must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := trainsQueryLua(tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestSetTrainScheduleLuaValidation(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	record := func(station string, ticks float64) *TrainScheduleRecordInput {
		return &TrainScheduleRecordInput{Station: station, WaitConditions: []*TrainWaitConditionInput{{Type: TrainWaitConditionTypeTime, Ticks: &ticks}}}
	}
	base := func() SetTrainScheduleInput {
		return SetTrainScheduleInput{Actor: &ActorInput{Player: &player}, Target: &EntityRefInput{Entity: &locomotive, UnitNumber: &unit}}
	}
	tooMany := make([]*TrainScheduleRecordInput, maxTrainScheduleRecords+1)
	for i := range tooMany {
		tooMany[i] = record("station", 1)
	}
	cases := []struct {
		name  string
		input SetTrainScheduleInput
		want  string
	}{
		{"neither records nor clear", base(), "records are required unless clear is true"},
		{"records and clear", func() SetTrainScheduleInput {
			in := base()
			in.Records = []*TrainScheduleRecordInput{record("A", 1)}
			in.Clear = true
			return in
		}(), "give either records or clear, not both"},
		{"too many records", func() SetTrainScheduleInput {
			in := base()
			in.Records = tooMany
			return in
		}(), "at most 1000 schedule records are allowed"},
		{"empty station", func() SetTrainScheduleInput {
			in := base()
			in.Records = []*TrainScheduleRecordInput{record("", 1)}
			return in
		}(), "schedule record station must not be empty"},
		{"negative wait", func() SetTrainScheduleInput {
			in := base()
			in.Records = []*TrainScheduleRecordInput{record("A", -1)}
			return in
		}(), "wait condition ticks must be finite and no less than 0"},
		{"no actor", func() SetTrainScheduleInput {
			in := base()
			in.Actor = nil
			in.Records = []*TrainScheduleRecordInput{record("A", 1)}
			return in
		}(), "exactly one of player or character is required"},
		{"actor and character", func() SetTrainScheduleInput {
			in := base()
			character := "4"
			in.Actor.Character = &character
			in.Records = []*TrainScheduleRecordInput{record("A", 1)}
			return in
		}(), "exactly one of player or character is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := setTrainScheduleLua(tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestTrainControlValidation(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	target := &EntityRefInput{Entity: &locomotive, UnitNumber: &unit}
	if _, err := trainControlLua(TrainControlInput{Actor: &ActorInput{Player: &player}, Target: target}); err == nil || !strings.Contains(err.Error(), "manualMode or goToStation is required") {
		t.Fatalf("expected missing option error, got %v", err)
	}
	blank := ""
	if _, err := trainControlLua(TrainControlInput{Actor: &ActorInput{Player: &player}, Target: target, GoToStation: &blank}); err == nil || !strings.Contains(err.Error(), "goToStation must not be empty") {
		t.Fatalf("expected empty station error, got %v", err)
	}
}

func TestSetDirectionValidation(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	target := &EntityRefInput{Entity: &locomotive, UnitNumber: &unit}
	for _, direction := range []int{-1, 16, 100} {
		if _, err := setDirectionLua(SetDirectionInput{Actor: &ActorInput{Player: &player}, Target: target, Direction: direction}); err == nil || !strings.Contains(err.Error(), "direction must be between 0 and 15") {
			t.Fatalf("direction %d: expected range error, got %v", direction, err)
		}
	}
	if _, err := setDirectionLua(SetDirectionInput{Target: target, Direction: 4}); err == nil || !strings.Contains(err.Error(), "exactly one of player or character is required") {
		t.Fatalf("expected actor error, got %v", err)
	}
}

func TestTrainsDecode(t *testing.T) {
	page, err := factoryDecode[TrainPage](`{"tick":900,"surface":"nauvis","force":"player","pageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"trains":{}}`)
	if err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.Trains == nil || len(page.Trains) != 0 {
		t.Fatalf("expected an empty train list, got %#v", page.Trains)
	}
	info, err := factoryDecode[TrainInfo](`{"trainId":"7","status":{"category":"RUNNING","rawName":"on_the_path","rawCode":2,"terminal":false},"manual_mode":true,"speed":0.5,"locomotives":1,"wagons":3,"fluidWagons":1,"carriages":[{"unitNumber":"20","name":"locomotive","type":"locomotive","position":{"x":1.5,"y":2.5},"direction":0},{"unitNumber":"21","name":"cargo-wagon","type":"cargo-wagon","position":{"x":8.5,"y":2.5},"direction":0}],"station":"Iron Load","pathEndStop":"Iron Drop","scheduleIndex":2,"destination":"Iron Drop","front_position":{"x":1.5,"y":2.5},"schedule":{}, "cargo":{}, "fluids":{}}`)
	if err != nil {
		t.Fatalf("decode train: %v", err)
	}
	if info.TrainID != "7" || info.Status == nil || info.Status.RawName == nil || *info.Status.RawName != "on_the_path" {
		t.Fatalf("unexpected train identity: %#v", info)
	}
	if !info.ManualMode || info.Locomotives != 1 || info.Wagons != 3 || info.FluidWagons != 1 {
		t.Fatalf("unexpected train mode or carriage counts: %#v", info)
	}
	if len(info.Carriages) != 2 || info.Carriages[0].UnitNumber == nil || *info.Carriages[0].UnitNumber != "20" || info.Carriages[1].Name != "cargo-wagon" {
		t.Fatalf("unexpected carriages: %#v", info.Carriages)
	}
	if info.Station == nil || *info.Station != "Iron Load" || info.PathEndStop == nil || *info.PathEndStop != "Iron Drop" {
		t.Fatalf("unexpected stops: %#v %#v", info.Station, info.PathEndStop)
	}
	if info.ScheduleIndex == nil || *info.ScheduleIndex != 2 || info.Destination == nil || *info.Destination != "Iron Drop" {
		t.Fatalf("unexpected schedule position: %#v %#v", info.ScheduleIndex, info.Destination)
	}
	if info.FrontPosition == nil || info.FrontPosition.X != 1.5 || info.FrontPosition.Y != 2.5 {
		t.Fatalf("unexpected front position: %#v", info.FrontPosition)
	}
	if len(info.Schedule) != 0 || len(info.Cargo) != 0 || len(info.Fluids) != 0 {
		t.Fatalf("expected empty lists, got %#v %#v %#v", info.Schedule, info.Cargo, info.Fluids)
	}
}

// TestTrainTargetRequiresExactlyOneAddress proves a train mutation cannot be
// ambiguous and that a train id needs no entity scan.
func TestTrainTargetRequiresExactlyOneAddress(t *testing.T) {
	player := "alice"
	entity := "locomotive"
	unit := "12"
	target := &EntityRefInput{Entity: &entity, UnitNumber: &unit}
	ticks := 5.0
	records := []*TrainScheduleRecordInput{{Station: "A", WaitConditions: []*TrainWaitConditionInput{{Type: TrainWaitConditionTypeTime, Ticks: &ticks}}}}
	both := "42"
	caseInput := func(target *EntityRefInput, trainID *string) SetTrainScheduleInput {
		return SetTrainScheduleInput{Actor: &ActorInput{Player: &player}, Target: target, TrainID: trainID, Records: records}
	}
	if _, err := setTrainScheduleLua(caseInput(nil, nil)); err == nil || !strings.Contains(err.Error(), "exactly one of target or trainId") {
		t.Fatalf("expected address error, got %v", err)
	}
	if _, err := setTrainScheduleLua(caseInput(target, &both)); err == nil || !strings.Contains(err.Error(), "exactly one of target or trainId") {
		t.Fatalf("expected address error, got %v", err)
	}
	bad := "train-7"
	if _, err := setTrainScheduleLua(caseInput(nil, &bad)); err == nil || !strings.Contains(err.Error(), "numeric LuaTrain id") {
		t.Fatalf("expected numeric id error, got %v", err)
	}
	lua, err := setTrainScheduleLua(caseInput(nil, &both))
	if err != nil {
		t.Fatalf("trainId target must be accepted: %v", err)
	}
	for _, want := range []string{"game.train_manager.get_train_by_id(42)", "if not train then fail", "trains_info(train)"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("trainId Lua must contain %q", want)
		}
	}
	if strings.Contains(lua, "local e=") {
		t.Fatal("a train id must not resolve an entity target")
	}
}

// TestSetTrainScheduleWritesEveryWaitCondition proves multi-condition records
// (full OR inactivity) round-trip instead of dropping every condition after the
// first.
func TestSetTrainScheduleWritesEveryWaitCondition(t *testing.T) {
	player := "alice"
	trainID := "42"
	ticks := 300.0
	or := TrainWaitCompareTypeOr
	record := &TrainScheduleRecordInput{Station: "Iron Drop", WaitConditions: []*TrainWaitConditionInput{
		{Type: TrainWaitConditionTypeFull},
		{Type: TrainWaitConditionTypeInactivity, Ticks: &ticks, CompareType: &or},
		{Type: TrainWaitConditionTypeNotEmpty, CompareType: &or},
	}}
	lua, err := setTrainScheduleLua(SetTrainScheduleInput{Actor: &ActorInput{Player: &player}, TrainID: &trainID, Records: []*TrainScheduleRecordInput{record}})
	if err != nil {
		t.Fatalf("setTrainScheduleLua: %v", err)
	}
	for _, want := range []string{"wait_conditions={{type=" + luaString("full"), "type=" + luaString("inactivity"), "ticks=300", "type=" + luaString("not_empty")} {
		if !strings.Contains(lua, want) {
			t.Fatalf("schedule Lua must contain %q", want)
		}
	}
	if strings.Count(lua, "compare_type="+luaString("or")) != 3 {
		t.Fatalf("every wait condition must be written, got %s", lua)
	}
	if _, err := setTrainScheduleLua(SetTrainScheduleInput{Actor: &ActorInput{Player: &player}, TrainID: &trainID, Records: []*TrainScheduleRecordInput{{Station: "A"}}}); err == nil || !strings.Contains(err.Error(), "at least one wait condition") {
		t.Fatalf("expected condition requirement error, got %v", err)
	}
	emptyStation := ""
	if _, err := trainWaitConditionLua(&TrainWaitConditionInput{Type: TrainWaitConditionTypeSpecificDestinationFull, Station: &emptyStation}); err == nil || !strings.Contains(err.Error(), "station must not be empty") {
		t.Fatalf("expected station validation error, got %v", err)
	}
	negativeDamage := -1
	if _, err := trainWaitConditionLua(&TrainWaitConditionInput{Type: TrainWaitConditionTypeDamageTaken, Damage: &negativeDamage}); err == nil || !strings.Contains(err.Error(), "damage") {
		t.Fatalf("expected damage validation error, got %v", err)
	}
}

// TestTrainInfoLuaReportsEngineStateAndWaitConditions runs the snapshot Lua
// against a stub train: the engine state list, carriage references, stops and
// every wait condition must surface.
func TestTrainInfoLuaReportsEngineStateAndWaitConditions(t *testing.T) {
	stubs := `
local front={name="locomotive",type="locomotive",unit_number=20,position={x=1.5,y=2.5},direction=0,surface={name="nauvis"},force={name="player"}}
local wagon={name="fluid-wagon",type="fluid-wagon",unit_number=21,position={x=8.5,y=2.5},direction=0,surface={name="nauvis"},force={name="player"}}
local schedule={current=2,records={
  {station={name="Iron Load"},wait_conditions={{type="full"},{type="inactivity",ticks=300,compare_type="or"},
    {type="circuit",compare_type="and",condition={comparator=">",constant=5,fulfilled=true,
      first_signal={name="signal-A",type="virtual"},second_signal={name="steam",type="fluid"}}}}},
  {station={name="Iron Drop"},wait_conditions={{type="not_empty"},
    {type="item_count",condition={comparator=">",first_signal={name="iron-plate"}}}}},
}}
local station={backer_name="Iron Load"}
local path_end_stop={backer_name="Iron Drop"}
local stub_train={id=12,state=9,manual_mode=false,speed=0.25,
  carriages={front,wagon},
  locomotives={front_movers={front},back_movers={}},
  fluid_wagons={wagon},
  station=station,path_end_stop=path_end_stop,schedule=schedule,
  get_contents=function() return {{name="iron-ore",count=50,quality="normal"}} end,
  get_fluid_contents=function() return {{name="crude-oil",amount=1000}} end}
defines={train_state={wait_signal=9,on_the_path=7,arrive_signal=3,no_path=2}}
game={tick=900}
`
	assertions := `
local info=captured
assert(info.train_id=="12","trainId must be the LuaTrain id")
assert(info.status.category=="BLOCKED","wait_signal must be BLOCKED, got "..tostring(info.status.category))
assert(info.status.raw_name=="wait_signal")
assert(#info.carriages==2 and info.carriages[1].unit_number=="20" and info.carriages[2].name=="fluid-wagon")
assert(info.locomotives==1,"locomotives must count front and back movers")
assert(info.wagons==1,"wagons must count non-locomotive rolling stock")
assert(info.fluid_wagons==1,"fluid wagons must be counted")
assert(info.station=="Iron Load" and info.path_end_stop=="Iron Drop")
assert(info.schedule_index==2 and info.destination=="Iron Drop")
assert(#info.schedule[1].wait_conditions==3,"every wait condition must be decoded")
assert(info.schedule[1].wait_conditions[2].ticks==300 and info.schedule[1].wait_conditions[2].compare_type=="OR")
assert(info.schedule[2].wait_conditions[1].type=="NOT_EMPTY")
assert(info.schedule[1].wait_conditions[1].type=="FULL")
local circuit=info.schedule[1].wait_conditions[3]
assert(circuit.type=="CIRCUIT" and circuit.condition.comparator==">" and circuit.condition.constant==5)
assert(circuit.condition.firstSignal.type=="VIRTUAL","virtual signals must normalize to the SignalType enum")
assert(circuit.condition.secondSignal.type=="FLUID")
assert(circuit.condition.fulfilled==true)
local item=info.schedule[2].wait_conditions[2]
assert(item.type=="ITEM_COUNT" and item.condition.firstSignal.type=="ITEM","a nil engine signal type means item")
assert(item.condition.secondSignal==nil)
`
	source := trainsLua + "\nlocal train=stub_train\nrcon.print(helpers.table_to_json(trains_info(train)))\n"
	runLuaFixture(t, source, stubs, assertions)
}

// TestTrainStatusCategoriesCoverEngineStates checks every 2.0.77 train_state
// value maps to a category instead of falling into OTHER.
func TestTrainStatusCategoriesCoverEngineStates(t *testing.T) {
	cases := []string{
		`assert(trains_status(1,"no_schedule").category=="BLOCKED")`,
		`assert(trains_status(2,"no_path").category=="BLOCKED")`,
		`assert(trains_status(3,"arrive_signal").category=="RUNNING")`,
		`assert(trains_status(4,"wait_signal").category=="BLOCKED")`,
		`assert(trains_status(5,"arrive_station").category=="IDLE")`,
		`assert(trains_status(6,"manual_control_stop").category=="IDLE")`,
		`assert(trains_status(7,"on_the_path").category=="RUNNING")`,
		`assert(trains_status(8,"wait_station").category=="IDLE")`,
		`assert(trains_status(9,"destination_full").category=="BLOCKED")`,
		`assert(trains_status(10,"manual_control").category=="IDLE")`,
	}
	source := trainsLua + "\n" + strings.Join(cases, "\n") + `
assert(trains_status(11,"not_a_train_state").category=="OTHER")
`
	runLuaFixture(t, source, "game={tick=1}\n", "")
}

// TestTrainStopsQueryLua reports the engine limit semantics: the uint32 maximum
// means unlimited and must surface as null.
func TestTrainStopsQueryLua(t *testing.T) {
	if _, err := trainStopsQuery(nil, "", "player"); err == nil || !strings.Contains(err.Error(), "surface") {
		t.Fatalf("expected surface error, got %v", err)
	}
	if _, err := trainStopsQuery(nil, "nauvis", " "); err == nil || !strings.Contains(err.Error(), "force") {
		t.Fatalf("expected force error, got %v", err)
	}
	stubs := `
local surface={name="nauvis",index=1}
local force={name="player"}
game={tick=10,surfaces={nauvis=surface},forces={player=force}}
local function stop(unit,limit,count,priority,assigned)
  return {valid=true,type="train-stop",name="train-stop",unit_number=unit,backer_name="Station "..unit,
    position={x=unit,y=0},trains_limit=limit,trains_count=count,priority=priority,
    get_train_stop_trains=function() local list={} for i=1,assigned do list[i]={id=i} end return list end}
end
surface.find_entities_filtered=function(filter)
  assert(filter.type=="train-stop","train stops only")
  return {stop(3,4294967295,1,50,2),stop(1,3,2,10,1)}
end
`
	assertions := `
assert(#captured.trainStops==2,"two stops expected")
assert(captured.trainStops[1].unitNumber=="1","stops must sort by unit number")
assert(captured.trainStops[1].trainsLimit==3,"a real limit must be reported")
assert(captured.trainStops[2].trainsLimit==nil,"the uint32 maximum means unlimited")
assert(captured.trainStops[2].trainsCount==1 and captured.trainStops[2].priority==50 and captured.trainStops[2].assignedTrains==2)
assert(captured.trainStops[1].backerName=="Station 1")
`
	lua := trainStopsLua("nauvis", "player")
	runLuaFixture(t, lua, stubs, assertions)
}

// TestTrainWaitConditionDecodesSharedCircuitCondition proves a train wait
// condition decodes into the shared CircuitCondition/SignalID shape with the
// SignalType enum, matching what SignalConditionInput writes.
func TestTrainWaitConditionDecodesSharedCircuitCondition(t *testing.T) {
	info, err := factoryDecode[TrainInfo](`{"trainId":"3","status":{"category":"IDLE","rawName":"wait_station","rawCode":8},"manualMode":false,"speed":0,"locomotives":1,"wagons":1,"fluidWagons":0,"carriages":[],"schedule":[{"station":"Oil","waitConditions":[{"type":"CIRCUIT","rawType":"circuit","compareType":"AND","condition":{"comparator":">","constant":5,"fulfilled":true,"firstSignal":{"type":"VIRTUAL","name":"signal-A"},"secondSignal":{"type":"FLUID","name":"steam"}}}]}],"cargo":[],"fluids":[]}`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(info.Schedule) != 1 || len(info.Schedule[0].WaitConditions) != 1 {
		t.Fatalf("schedule = %+v", info.Schedule)
	}
	condition := info.Schedule[0].WaitConditions[0].Condition
	if condition == nil || condition.FirstSignal == nil || condition.SecondSignal == nil {
		t.Fatalf("condition = %+v", condition)
	}
	if condition.FirstSignal.Type != SignalTypeVirtual || condition.SecondSignal.Type != SignalTypeFluid {
		t.Fatalf("signal types = %q/%q", condition.FirstSignal.Type, condition.SecondSignal.Type)
	}
	if condition.Comparator == nil || *condition.Comparator != ">" || condition.Constant == nil || *condition.Constant != 5 {
		t.Fatalf("condition values = %+v", condition)
	}
	if condition.Fulfilled == nil || !*condition.Fulfilled {
		t.Fatalf("fulfilled = %v", condition.Fulfilled)
	}
}
