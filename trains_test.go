package main

import (
	"strings"
	"testing"
)

func TestTrainsQueryLuaValidation(t *testing.T) {
	station := ""
	blank := ""
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 300, RightBottomY: 10}
	cases := []struct {
		name  string
		input *TrainsInput
		want  string
	}{
		{"empty surface", &TrainsInput{Surface: "", Force: "player", Limit: 10}, "surface must not be empty"},
		{"empty force", &TrainsInput{Surface: "nauvis", Force: "", Limit: 10}, "force must not be empty"},
		{"limit zero", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 0}, "limit must be between 1 and 1000"},
		{"limit too large", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 1001}, "limit must be between 1 and 1000"},
		{"negative offset", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 10, Offset: -1}, "offset must be non-negative"},
		{"empty station", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 10, Station: &station}, "station must not be empty"},
		{"area too wide", &TrainsInput{Surface: "nauvis", Force: "player", Limit: 10, Area: area}, "area must have positive dimensions no greater than 256 tiles"},
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

func TestTrainsQueryLuaContents(t *testing.T) {
	station := "Iron Load"
	lua, err := trainsQueryLua(&TrainsInput{Surface: "nauvis", Force: "player", Limit: 25, Offset: 5, Station: &station})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"game.train_manager.get_trains({surface=surface,force=force.name})",
		"trains_schedule_records",
		"get_contents()",
		"get_fluid_contents()",
		"defines.train_state",
		`wait_seconds=(tonumber(trains_field(condition,"ticks")) or 0)/60`,
		luaString("Iron Load"),
		"trains_page(s,f,",
		",25,5)",
		"trains_in_area",
		"contents[1]~=nil",
		"amount=value.amount",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("query Lua is missing %q", want)
		}
	}
	if strings.Contains(lua, "attempts={") {
		t.Fatal("query Lua must call get_trains once, not try multiple filter shapes")
	}
}

func TestSetTrainScheduleLuaValidation(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	record := func(station string, seconds float64) *TrainScheduleRecordInput {
		return &TrainScheduleRecordInput{Station: station, WaitSeconds: seconds}
	}
	base := func() SetTrainScheduleInput {
		return SetTrainScheduleInput{Player: &player, Entity: &locomotive, UnitNumber: &unit}
	}
	tooMany := make([]*TrainScheduleRecordInput, 101)
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
		}(), "at most 100 schedule records are allowed"},
		{"empty station", func() SetTrainScheduleInput {
			in := base()
			in.Records = []*TrainScheduleRecordInput{record("", 1)}
			return in
		}(), "schedule record station must not be empty"},
		{"negative wait", func() SetTrainScheduleInput {
			in := base()
			in.Records = []*TrainScheduleRecordInput{record("A", -1)}
			return in
		}(), "waitSeconds must be a finite number of seconds no less than 0"},
		{"no actor", func() SetTrainScheduleInput {
			in := base()
			in.Player = nil
			in.Records = []*TrainScheduleRecordInput{record("A", 1)}
			return in
		}(), "exactly one of player or character is required"},
		{"actor and character", func() SetTrainScheduleInput {
			in := base()
			character := "4"
			in.Character = &character
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

func TestSetTrainScheduleLuaContents(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	input := SetTrainScheduleInput{
		Player:     &player,
		Entity:     &locomotive,
		UnitNumber: &unit,
		Records: []*TrainScheduleRecordInput{
			{Station: "Iron Load", WaitSeconds: 30},
			{Station: "Copper Drop", WaitSeconds: 0},
		},
	}
	lua, err := setTrainScheduleLua(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"train.schedule={current=1,records=records}",
		luaString("Iron Load"),
		luaString("Copper Drop"),
		`wait_conditions={{type=` + luaString("time") + `,ticks=math.floor(30*60)}}`,
		`ticks=math.floor(0*60)`,
		"trains_info(train)",
		"can_reach_entity(e)",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("schedule Lua is missing %q", want)
		}
	}
	cleared, err := setTrainScheduleLua(SetTrainScheduleInput{
		Player: &player, Entity: &locomotive, UnitNumber: &unit, Clear: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cleared, "train.schedule=nil") {
		t.Fatalf("clear Lua does not clear the schedule: %s", cleared)
	}
}

func TestTrainControlLua(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	if _, err := trainControlLua(TrainControlInput{Player: &player, Entity: &locomotive, UnitNumber: &unit}); err == nil || !strings.Contains(err.Error(), "manualMode or goToStation is required") {
		t.Fatalf("expected missing option error, got %v", err)
	}
	blank := ""
	if _, err := trainControlLua(TrainControlInput{Player: &player, Entity: &locomotive, UnitNumber: &unit, GoToStation: &blank}); err == nil || !strings.Contains(err.Error(), "goToStation must not be empty") {
		t.Fatalf("expected empty station error, got %v", err)
	}
	manual := true
	station := "Copper Drop"
	lua, err := trainControlLua(TrainControlInput{
		Player: &player, Entity: &locomotive, UnitNumber: &unit, ManualMode: &manual, GoToStation: &station,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"train.manual_mode=true", "go_to_station(index)", luaString("Copper Drop"), "station not found in train schedule"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("control Lua is missing %q", want)
		}
	}
}

func TestSetDirectionLua(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	for _, direction := range []int{-1, 16, 100} {
		if _, err := setDirectionLua(SetDirectionInput{Player: &player, Entity: &locomotive, UnitNumber: &unit, Direction: direction}); err == nil || !strings.Contains(err.Error(), "direction must be between 0 and 15") {
			t.Fatalf("direction %d: expected range error, got %v", direction, err)
		}
	}
	lua, err := setDirectionLua(SetDirectionInput{Player: &player, Entity: &locomotive, UnitNumber: &unit, Direction: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"local before=e.direction",
		"local requested=8",
		"e.direction=requested",
		"local applied=(e.direction==requested)",
		"local clockwise=(requested-before)%16",
		"local counter=(before-requested)%16",
		`e.rotate{reverse=(counter==4)}`,
		"does not allow scripted direction changes",
		"directionCode instead",
		"can_reach_entity(e)",
		"tostring(e.direction)",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("direction Lua is missing %q", want)
		}
	}
	if strings.Contains(lua, "if true then") {
		t.Fatal("direction Lua must not claim success unconditionally")
	}
	if _, err := setDirectionLua(SetDirectionInput{Entity: &locomotive, UnitNumber: &unit, Direction: 4}); err == nil || !strings.Contains(err.Error(), "exactly one of player or character is required") {
		t.Fatalf("expected actor error, got %v", err)
	}
}

func TestTrainsDecode(t *testing.T) {
	page, err := factoryDecode[TrainPage](`{"tick":900,"surface":"nauvis","total":0,"limit":100,"offset":0,"trains":{}}`)
	if err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.Trains == nil || len(page.Trains) != 0 {
		t.Fatalf("expected an empty train list, got %#v", page.Trains)
	}
	info, err := factoryDecode[TrainInfo](`{"id":"7","state":2,"status_name":"on_the_path","manual_mode":true,"speed":0.5,"locomotives":1,"wagons":3,"front_position":{"x":1.5,"y":2.5},"schedule":{}, "cargo":{}, "fluids":{}}`)
	if err != nil {
		t.Fatalf("decode train: %v", err)
	}
	if info.ID != "7" || info.State != 2 || info.StatusName == nil || *info.StatusName != "on_the_path" {
		t.Fatalf("unexpected train identity: %#v", info)
	}
	if !info.ManualMode || info.Locomotives != 1 || info.Wagons != 3 {
		t.Fatalf("unexpected train mode or carriage counts: %#v", info)
	}
	if info.FrontPosition == nil || info.FrontPosition.X != 1.5 || info.FrontPosition.Y != 2.5 {
		t.Fatalf("unexpected front position: %#v", info.FrontPosition)
	}
	if len(info.Schedule) != 0 || len(info.Cargo) != 0 || len(info.Fluids) != 0 {
		t.Fatalf("expected empty lists, got %#v %#v %#v", info.Schedule, info.Cargo, info.Fluids)
	}
}
