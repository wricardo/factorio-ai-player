package main

import "testing"

func TestGatherItemsDecodesTieredResult(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":5,"surface":"nauvis","item":"iron-plate","quality":"normal","requested":30,"gathered":25,"stop_reason":"AREA_EXHAUSTED","tiers":[{"tier":"CONTAINER","sources":1,"moved":20},{"tier":"MACHINE_OUTPUT","sources":0,"moved":0},{"tier":"BELT","sources":1,"moved":5},{"tier":"LOOSE","sources":0,"moved":0}],"sources":[{"tier":"CONTAINER","entity":"wooden-chest","unitNumber":42,"position":{"x":1.5,"y":2.5},"moved":20},{"tier":"BELT","entity":"transport-belt","unitNumber":43,"position":{"x":3.5,"y":2.5},"moved":5}],"sources_truncated":false,"failed":0,"errors":{}}`)
	result, err := gatherItems(r, GatherItemsInput{
		Actor: &ActorInput{Character: new("7")},
		Area:  &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8},
		Item:  "iron-plate",
		Count: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-commands
	if result.StopReason != GatherStopReasonAreaExhausted || result.Gathered != 25 || len(result.Tiers) != 4 || len(result.Sources) != 2 || result.Errors == nil {
		t.Fatalf("decoded result: %+v", result)
	}
	if result.Sources[1].Tier != GatherTierBelt || result.Sources[0].UnitNumber == nil || *result.Sources[0].UnitNumber != "42" {
		t.Fatalf("decoded sources: %+v", result.Sources)
	}
}

func TestMineAreaAndGatherItemsRejectBadInput(t *testing.T) {
	r := &Resolver{}
	actor := &ActorInput{Character: new("7")}
	area := &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8}
	huge := &FactoryAreaInput{RightBottomX: 1025, RightBottomY: 1}
	for name, run := range map[string]func() error{
		"mine zero count": func() error { _, err := mineArea(r, MineAreaInput{Actor: actor, Area: area, Count: 0}); return err },
		"mine over cap":   func() error { _, err := mineArea(r, MineAreaInput{Actor: actor, Area: area, Count: 10001}); return err },
		"mine huge area":  func() error { _, err := mineArea(r, MineAreaInput{Actor: actor, Area: huge, Count: 1}); return err },
		"mine no area":    func() error { _, err := mineArea(r, MineAreaInput{Actor: actor, Count: 1}); return err },
		"mine empty ore": func() error {
			_, err := mineArea(r, MineAreaInput{Actor: actor, Area: area, Count: 1, Resource: new("")})
			return err
		},
		"gather empty item": func() error {
			_, err := gatherItems(r, GatherItemsInput{Actor: actor, Area: area, Count: 1})
			return err
		},
		"gather zero count": func() error {
			_, err := gatherItems(r, GatherItemsInput{Actor: actor, Area: area, Item: "iron-plate"})
			return err
		},
		"gather no actor": func() error {
			_, err := gatherItems(r, GatherItemsInput{Actor: &ActorInput{}, Area: area, Item: "iron-plate", Count: 1})
			return err
		},
	} {
		if err := run(); err == nil {
			t.Errorf("%s: want validation error", name)
		}
	}
}
