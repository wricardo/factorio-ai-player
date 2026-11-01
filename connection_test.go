package main

import (
	"strings"
	"testing"
)

// A plan must be one read-only RCON call that carries the snapped route, the
// requested item and the pipe adjacency policy, and must decode steps and typed
// conflicts instead of a bare boolean.
func TestConnectionPlanLuaAndDecode(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":100,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":0.5,"y":0.5},"to":{"x":3.5,"y":0.5},"bend":"HORIZONTAL_FIRST","possible":false,"needed":3,"steps":[{"position":{"x":0.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":1.5,"y":0.5},"direction":4,"existing":true},{"position":{"x":2.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":3.5,"y":0.5},"direction":4,"existing":false}],"conflicts":[{"position":{"x":2.5,"y":0.5},"kind":"COLLISION","blockers":["pipe","small-lamp"],"terrain":false,"tile":"grass-1","message":"cannot place transport-belt at this tile"}]}`)
	result, err := connectionPlanQuery(r, &ConnectionPlanInput{
		Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 3, Y: 0},
		Kind: ConnectionKindBelt, Bend: ConnectionBendAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Possible || result.Needed != 3 || len(result.Steps) != 4 {
		t.Fatalf("plan facts: %+v", result)
	}
	if result.Bend != ConnectionBendHorizontalFirst {
		t.Fatalf("bend: %q", result.Bend)
	}
	if result.Steps[1] == nil || !result.Steps[1].Existing || result.Steps[1].Position == nil || result.Steps[1].Position.X != 1.5 {
		t.Fatalf("existing step: %+v", result.Steps[1])
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Kind != ConnectionConflictKindCollision {
		t.Fatalf("conflicts: %+v", result.Conflicts)
	}
	if got := result.Conflicts[0].Blockers; len(got) != 2 || got[0] != "pipe" || got[1] != "small-lamp" {
		t.Fatalf("blockers: %v", got)
	}
	if result.From == nil || result.From.X != 0.5 || result.To == nil || result.To.X != 3.5 {
		t.Fatalf("snapped endpoints: from=%+v to=%+v", result.From, result.To)
	}
	lua := <-commands
	for _, want := range []string{
		`local item=` + luaString("transport-belt"),
		`local kind=` + luaString("BELT"),
		`local bend_request=` + luaString("AUTO"),
		`local allow_adjacency=false`,
		`local expected_type={BELT="transport-belt",PIPE="pipe",POWER_POLE_CHAIN="electric-pole"}`,
		`if kind=="PIPE" and not allow_adjacency then`,
		`for _,d in ipairs({{1,0},{-1,0},{0,1},{0,-1}}) do`,
		`"DIRECTION_MISMATCH"`,
		`surface.can_place_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=force}`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("plan Lua missing %q\n%s", want, lua)
		}
	}
	if strings.Contains(lua, "create_entity") || strings.Contains(lua, "inv.remove") {
		t.Fatalf("plan must not mutate: %s", lua)
	}
}

// Invalid routes and kinds must fail before any RCON dial.
func TestConnectionPlanValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	cases := []struct {
		name  string
		input *ConnectionPlanInput
		want  string
	}{
		{"nil", nil, "surface and force are required"},
		{"empty surface", &ConnectionPlanInput{Force: "player", From: &PositionInput{}, To: &PositionInput{}, Kind: ConnectionKindBelt}, "surface and force are required"},
		{"empty force", &ConnectionPlanInput{Surface: "nauvis", From: &PositionInput{}, To: &PositionInput{}, Kind: ConnectionKindBelt}, "surface and force are required"},
		{"missing from", &ConnectionPlanInput{Surface: "nauvis", Force: "player", To: &PositionInput{}, Kind: ConnectionKindBelt}, "from and to are required"},
		{"missing to", &ConnectionPlanInput{Surface: "nauvis", Force: "player", From: &PositionInput{}, Kind: ConnectionKindBelt}, "from and to are required"},
		{"long route", &ConnectionPlanInput{Surface: "nauvis", Force: "player", From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 600, Y: 0}, Kind: ConnectionKindBelt}, "above the 512 tile limit"},
		{"unknown kind", &ConnectionPlanInput{Surface: "nauvis", Force: "player", From: &PositionInput{}, To: &PositionInput{}, Kind: ConnectionKind("BENDY")}, `unsupported connection kind "BENDY"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := connectionPlanQuery(r, tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// An empty item must fall back to the kind default before the Lua is built.
func TestConnectionPlanDefaultItem(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","kind":"POWER_POLE_CHAIN","item":"medium-electric-pole","from":{"x":0.5,"y":0.5},"to":{"x":7.5,"y":0.5},"bend":"HORIZONTAL_FIRST","possible":true,"needed":2,"steps":[],"conflicts":{}}`)
	empty := ""
	if _, err := connectionPlanQuery(r, &ConnectionPlanInput{
		Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 7, Y: 0},
		Kind: ConnectionKindPowerPoleChain, Item: &empty,
	}); err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, `local item=`+luaString("medium-electric-pole")) {
		t.Fatalf("default pole item missing:\n%s", lua)
	}
	if !strings.Contains(lua, `spacing=math.max(1,math.floor(reach))`) {
		t.Fatalf("pole spacing missing:\n%s", lua)
	}
}

// A refused plan must report conflicts and place nothing without a second RCON
// call or a notification.
func TestConnectEntitiesRefusesOnConflicts(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":50,"surface":"nauvis","force":"player","kind":"PIPE","item":"pipe","from":{"x":0.5,"y":0.5},"to":{"x":1.5,"y":0.5},"bend":"HORIZONTAL_FIRST","possible":false,"placed":0,"skipped":0,"failed":0,"entities":{},"conflicts":[{"position":{"x":0.5,"y":0.5},"kind":"FLUID_ADJACENCY","blockers":[],"terrain":false,"tile":"","message":"pipe network with pipe touches this route"}],"errors":{}}`)
	input := ConnectEntitiesInput{
		Actor:   &ActorInput{Character: new("123")},
		Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 1, Y: 0},
		Kind: ConnectionKindPipe, Bend: ConnectionBendAuto,
	}
	result, err := connectEntities(r, input)
	result, err = setActorResult(result, input.Actor, err)
	if err != nil {
		t.Fatal(err)
	}
	if result.Possible || result.Placed != 0 || len(result.Conflicts) != 1 || result.Conflicts[0].Kind != ConnectionConflictKindFluidAdjacency {
		t.Fatalf("refused result: %+v", result)
	}
	if result.Actor == nil || result.Actor.Character == nil || *result.Actor.Character != "123" {
		t.Fatalf("actor identity: %+v", result.Actor)
	}
	lua := <-commands
	for _, want := range []string{
		`local player=""`,
		`local unit=123`,
		`if #chosen.conflicts>0 then`,
		`if have<chosen.needed then fail(`,
		`inv.remove{name=item,quality="normal",count=1}`,
		`surface.create_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=c.force,quality="normal",raise_built=true}`,
		`if false then ok=false; fail_at(i,"REACH"`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("apply Lua missing %q\n%s", want, lua)
		}
	}
}

// Reach enforcement must inject the distance check instead of the disabled
// literal.
func TestConnectEntitiesReachPolicy(t *testing.T) {
	t.Setenv("FACTORIO_ENFORCE_REACH", "1")
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":0.5,"y":0.5},"to":{"x":1.5,"y":0.5},"bend":"HORIZONTAL_FIRST","possible":false,"placed":0,"skipped":0,"failed":0,"entities":{},"conflicts":[{"position":{"x":0.5,"y":0.5},"kind":"COLLISION","blockers":["tree-01"],"terrain":false,"tile":"grass-1","message":"cannot place"}],"errors":{}}`)
	if _, err := connectEntities(r, ConnectEntitiesInput{
		Actor:   &ActorInput{Character: new("9")},
		Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 1, Y: 0},
		Kind: ConnectionKindBelt, Bend: ConnectionBendAuto,
	}); err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, "dx*dx+dy*dy>(c.build_distance)*(c.build_distance)") {
		t.Fatalf("reach check missing:\n%s", lua)
	}
}

// A server-side precondition failure must surface as an error so callers cannot
// mistake a refused run for a successful one.
func TestConnectEntitiesInventoryError(t *testing.T) {
	r, _ := serveLuaOnce(t, `{"error":"actor has 2 transport-belt but the run needs 5"}`)
	_, err := connectEntities(r, ConnectEntitiesInput{
		Actor:   &ActorInput{Character: new("5")},
		Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 4, Y: 0},
		Kind: ConnectionKindBelt, Bend: ConnectionBendAuto,
	})
	if err == nil || !strings.Contains(err.Error(), "but the run needs 5") {
		t.Fatalf("expected inventory error, got %v", err)
	}
}
