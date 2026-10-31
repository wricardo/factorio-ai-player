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

func TestSetTrainScheduleLuaValidation(t *testing.T) {
	player := "alice"
	locomotive := "locomotive"
	unit := "12"
	record := func(station string, ticks float64) *TrainScheduleRecordInput {
		return &TrainScheduleRecordInput{Station: station, WaitCondition: &TrainWaitConditionInput{Type: TrainWaitConditionTypeTime, Ticks: &ticks}}
	}
	base := func() SetTrainScheduleInput {
		return SetTrainScheduleInput{Actor: &ActorInput{Player: &player}, Target: &EntityRefInput{Entity: &locomotive, UnitNumber: &unit}}
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
	info, err := factoryDecode[TrainInfo](`{"id":"7","status":{"category":"RUNNING","rawName":"on_the_path","rawCode":2,"terminal":false},"manual_mode":true,"speed":0.5,"locomotives":1,"wagons":3,"front_position":{"x":1.5,"y":2.5},"schedule":{}, "cargo":{}, "fluids":{}}`)
	if err != nil {
		t.Fatalf("decode train: %v", err)
	}
	if info.ID != "7" || info.Status == nil || info.Status.RawName == nil || *info.Status.RawName != "on_the_path" {
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
