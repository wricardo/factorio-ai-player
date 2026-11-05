package main

import (
	"context"
	"strings"
	"testing"
)

func TestPlaceResolversRejectConveyorsBeforeRCON(t *testing.T) {
	mr := &mutationResolver{Resolver: &Resolver{addr: "127.0.0.1:1"}}
	actor := &ActorInput{Player: strPtr("wricardo")}
	if _, err := mr.Place(context.Background(), PlaceInput{
		Actor: actor, Item: "transport-belt", Position: &PositionInput{X: 0.5, Y: 0.5},
	}); err == nil || !strings.Contains(err.Error(), "placeBelt") {
		t.Fatalf("place belt error = %v, want placeBelt hint", err)
	}
	if _, err := mr.PlaceMany(context.Background(), PlaceManyInput{
		Actor: actor, Item: "pipe", Placements: []*PlacementInput{{Position: &PositionInput{X: 0.5, Y: 0.5}}},
	}); err == nil || !strings.Contains(err.Error(), "placePipe") {
		t.Fatalf("placeMany pipe error = %v, want placePipe hint", err)
	}
}

func TestConveyorPlacementError(t *testing.T) {
	for _, item := range []string{"transport-belt", "fast-transport-belt", "express-transport-belt"} {
		err := conveyorPlacementError(item)
		if err == nil || !strings.Contains(err.Error(), "placeBelt") {
			t.Fatalf("%s must be rejected with a placeBelt hint, got %v", item, err)
		}
	}
	if err := conveyorPlacementError("pipe"); err == nil || !strings.Contains(err.Error(), "placePipe") {
		t.Fatalf("pipe must be rejected with a placePipe hint, got %v", err)
	}
	for _, item := range []string{"underground-belt", "pipe-to-ground", "splitter", "loader", "inserter"} {
		if err := conveyorPlacementError(item); err != nil {
			t.Fatalf("%s must stay directly placeable, got %v", item, err)
		}
	}
}

func TestBeltPipeRunInputKeepsFields(t *testing.T) {
	item := "transport-belt"
	msg := "copper feed"
	from := &PositionInput{X: 1.5, Y: 2.5}
	to := &PositionInput{X: 9.5, Y: 2.5}
	in := BeltPipeRunInput{
		Actor: &ActorInput{Player: strPtr("wricardo")}, Surface: "nauvis", Force: "player",
		From: from, To: to, Item: &item, Routing: ConnectionRoutingAstar,
		AllowUnderground: true, AllowBeltAdjacency: false, Message: &msg,
	}
	belt := beltPipeRunInput(ConnectionKindBelt, in)
	if belt.Kind != ConnectionKindBelt || belt.Item == nil || *belt.Item != item || belt.Routing != ConnectionRoutingAstar || !belt.AllowUnderground || belt.AllowBeltAdjacency {
		t.Fatalf("belt conversion lost fields: %+v", belt)
	}
	if belt.From != from || belt.To != to || belt.Message == nil || *belt.Message != msg {
		t.Fatalf("endpoint/message fields lost: %+v", belt)
	}
	pipe := beltPipeRunInput(ConnectionKindPipe, in)
	if pipe.Kind != ConnectionKindPipe {
		t.Fatalf("pipe conversion kind = %s", pipe.Kind)
	}
}

func TestValidateRunDirection(t *testing.T) {
	dir := func(d int) *int { return &d }
	a := &PositionInput{X: 10.5, Y: 5.5}
	same := &PositionInput{X: 10.5, Y: 5.5}
	sameTile := &PositionInput{X: 10.2, Y: 5.7}
	other := &PositionInput{X: 11.5, Y: 5.5}

	cases := []struct {
		name    string
		from    *PositionInput
		to      *PositionInput
		dir     *int
		wantErr string
	}{
		{name: "single tile without direction", from: a, to: same, wantErr: "single-tile run needs direction"},
		{name: "single tile by floor without direction", from: a, to: sameTile, wantErr: "single-tile run needs direction"},
		{name: "single tile with direction", from: a, to: same, dir: dir(4)},
		{name: "run without direction", from: a, to: other},
		{name: "run with direction", from: a, to: other, dir: dir(4), wantErr: "only applies to a single-tile run"},
		{name: "invalid direction", from: a, to: same, dir: dir(2), wantErr: "unsupported direction"},
		{name: "nil endpoints", from: nil, to: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRunDirection(tc.from, tc.to, tc.dir)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestBeltPipeRunInputCarriesDirection(t *testing.T) {
	d := 12
	in := BeltPipeRunInput{
		Actor: &ActorInput{Player: strPtr("wricardo")}, Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 4.5, Y: 4.5}, To: &PositionInput{X: 4.5, Y: 4.5},
		Item: strPtr("transport-belt"), Direction: &d,
	}
	belt := beltPipeRunInput(ConnectionKindBelt, in)
	if belt.Direction == nil || *belt.Direction != 12 {
		t.Fatalf("direction lost in conversion: %+v", belt.Direction)
	}
}

func TestConnectionLuaPreambleCarriesRunDirection(t *testing.T) {
	from := &PositionInput{X: 4.5, Y: 4.5}
	to := &PositionInput{X: 4.5, Y: 4.5}
	lua := connectionLuaPreamble("nauvis", "player", "transport-belt", from, to, ConnectionKindBelt, ConnectionBendAuto, 4, false, false)
	if !strings.Contains(lua, "local run_direction=4") {
		t.Fatalf("preamble lost the explicit direction:\n%s", lua)
	}
	derived := connectionLuaPreamble("nauvis", "player", "transport-belt", from, to, ConnectionKindBelt, ConnectionBendAuto, runDirectionValue(nil), false, false)
	if !strings.Contains(derived, "local run_direction=-1") {
		t.Fatalf("preamble must mark a route-derived direction as -1:\n%s", derived)
	}
	if !strings.Contains(connectionLuaHelpers, "local d=run_direction") {
		t.Fatal("route builder must start from run_direction instead of a silent north default")
	}
}

func TestConnectionPlanRejectsSingleTile(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1"}
	from := &PositionInput{X: 4.5, Y: 4.5}
	to := &PositionInput{X: 4.5, Y: 4.5}
	_, err := connectionPlanQuery(r, &ConnectionPlanInput{
		Surface: "nauvis", Force: "player", Kind: ConnectionKindBelt, Item: strPtr("transport-belt"),
		From: from, To: to,
	})
	if err == nil || !strings.Contains(err.Error(), "same tile") {
		t.Fatalf("plan of a single tile error = %v, want same-tile refusal", err)
	}
}
