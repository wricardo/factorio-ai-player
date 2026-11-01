package main

import (
	"strings"
	"testing"
)

func astarPlanInput(kind ConnectionKind, fromX, fromY, toX, toY float64) *ConnectionPlanInput {
	return &ConnectionPlanInput{
		Surface: "nauvis", Force: "player", From: &PositionInput{X: fromX, Y: fromY}, To: &PositionInput{X: toX, Y: toY},
		Kind: kind, Bend: ConnectionBendAuto, Routing: ConnectionRoutingAstar,
	}
}

// ASTAR-only fields must be rejected on DIRECT routing and bad values refused
// before any RCON dial, so a caller never believes a limit was applied.
func TestConnectionPlanRoutingValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	empty := ""
	one, zero, big := 1, 0, routeMaxExpanded+1
	farMargin := routeMaxMargin + 1
	tooLong := routeMaxTunnelOffset + 1
	many := make([]*FactoryAreaInput, routeMaxAvoidAreas+1)
	for i := range many {
		many[i] = &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 1, RightBottomY: 1}
	}
	cases := []struct {
		name   string
		mutate func(*ConnectionPlanInput)
		want   string
	}{
		{"direct allowUnderground", func(in *ConnectionPlanInput) { in.Routing = ConnectionRoutingDirect; in.AllowUnderground = true }, "allowUnderground apply to ASTAR routing only"},
		{"direct avoid areas", func(in *ConnectionPlanInput) {
			in.Routing = ConnectionRoutingDirect
			in.AvoidAreas = []*FactoryAreaInput{{RightBottomX: 1, RightBottomY: 1}}
		}, "avoidAreas apply to ASTAR routing only"},
		{"direct limits", func(in *ConnectionPlanInput) {
			in.Routing = ConnectionRoutingDirect
			in.MaxExpandedTiles = &one
			in.SearchMargin = &zero
		}, "maxExpandedTiles, searchMargin apply to ASTAR routing only"},
		{"unknown routing", func(in *ConnectionPlanInput) { in.Routing = ConnectionRouting("WARP") }, `unsupported connection routing "WARP"`},
		{"pole chain", func(in *ConnectionPlanInput) { in.Kind = ConnectionKindPowerPoleChain }, "supports BELT and PIPE only"},
		{"underground item without allow", func(in *ConnectionPlanInput) { in.UndergroundItem = new("underground-belt") }, "require allowUnderground"},
		{"max underground without allow", func(in *ConnectionPlanInput) { in.MaxUndergroundDistance = &tooLong }, "require allowUnderground"},
		{"empty underground item", func(in *ConnectionPlanInput) { in.AllowUnderground = true; in.UndergroundItem = &empty }, "undergroundItem must not be empty"},
		{"span too short", func(in *ConnectionPlanInput) { in.AllowUnderground = true; in.MaxUndergroundDistance = &one }, "maxUndergroundDistance must be between 2 and 32"},
		{"span too long", func(in *ConnectionPlanInput) { in.AllowUnderground = true; in.MaxUndergroundDistance = &tooLong }, "maxUndergroundDistance must be between 2 and 32"},
		{"too many avoid areas", func(in *ConnectionPlanInput) { in.AvoidAreas = many }, "at most 20 areas"},
		{"bad avoid area", func(in *ConnectionPlanInput) {
			in.AvoidAreas = []*FactoryAreaInput{{LeftTopX: 5, RightBottomX: 1, RightBottomY: 1}}
		}, "avoid area 1"},
		{"expansion zero", func(in *ConnectionPlanInput) { in.MaxExpandedTiles = &zero }, "maxExpandedTiles must be between 1 and 50000"},
		{"expansion huge", func(in *ConnectionPlanInput) { in.MaxExpandedTiles = &big }, "maxExpandedTiles must be between 1 and 50000"},
		{"margin negative", func(in *ConnectionPlanInput) { neg := -1; in.SearchMargin = &neg }, "searchMargin must be between 0 and 64"},
		{"margin huge", func(in *ConnectionPlanInput) { in.SearchMargin = &farMargin }, "searchMargin must be between 0 and 64"},
		{"region too large", func(in *ConnectionPlanInput) { in.To = &PositionInput{X: 200, Y: 200} }, "above the 16384 tile limit"},
		{"missing endpoint", func(in *ConnectionPlanInput) { in.To = nil }, "from and to are required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := astarPlanInput(ConnectionKindBelt, 0, 0, 4, 0)
			tc.mutate(input)
			_, err := connectionPlanQuery(r, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func regionRules(response string) []scriptRule {
	return []scriptRule{{"grid_rows(entity_name)", response}}
}

// A planned route must come from one region read and decode into steps with
// engine directions, costs and the underground counts.
func TestConnectionPlanAStarDecodesRoute(t *testing.T) {
	// Row of five tiles, a blocked tile at x=2 and an underground-capable grid.
	response := `{"tick":50,"surface":"nauvis","force":"player","entity":"transport-belt","tunnelEntity":"underground-belt","maxTunnel":5,"open":["11011"],"openTunnel":["11011"],"fluid":["00000"],"tunnel":["00000"],"existing":{}}`
	r, recorder := serveScriptedRCON(t, regionRules(response))
	input := astarPlanInput(ConnectionKindBelt, 0.4, 0.6, 4.9, 0.1)
	input.AllowUnderground = true
	margin := 0
	input.SearchMargin = &margin
	plan, err := connectionPlanQuery(r, input)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Possible || plan.Routing != ConnectionRoutingAstar || plan.StopReason != ConnectionStopReasonFound {
		t.Fatalf("plan: %+v", plan)
	}
	if plan.From.X != 0.5 || plan.To.X != 4.5 || plan.To.Y != 0.5 {
		t.Fatalf("endpoints must snap to tile centers: %+v %+v", plan.From, plan.To)
	}
	if plan.UndergroundItem == nil || *plan.UndergroundItem != "underground-belt" {
		t.Fatalf("undergroundItem: %v", plan.UndergroundItem)
	}
	if plan.Expanded < 1 {
		t.Fatalf("expanded: %d", plan.Expanded)
	}
	var tunnels, surface int
	for _, step := range plan.Steps {
		if step.UndergroundType != nil {
			tunnels++
			if step.Direction != 4 {
				t.Fatalf("underground belt halves face the travel direction: %+v", step)
			}
		} else {
			surface++
		}
	}
	if tunnels != 2 || plan.UndergroundNeeded != 2 || plan.Needed != surface {
		t.Fatalf("tunnels=%d undergroundNeeded=%d needed=%d surface=%d: %+v", tunnels, plan.UndergroundNeeded, plan.Needed, surface, plan.Steps)
	}
	if plan.Cost <= 0 {
		t.Fatalf("cost: %v", plan.Cost)
	}
	if got := recorder.seen("grid_rows(entity_name)"); got != 1 {
		t.Fatalf("region reads=%d, want exactly one", got)
	}
	lua := recorder.commands[0]
	for _, want := range []string{
		`local tunnel_item=` + luaString("underground-belt"),
		`local kind=` + luaString("BELT"),
		`local left,top,cols,rows=0,0,5,1`,
		`max_underground_distance`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("region Lua missing %q\n%s", want, lua)
		}
	}
}

// A failed search must come back as a typed conflict instead of an error, and
// an empty plan must not claim any items.
func TestConnectionPlanAStarFailuresAreTyped(t *testing.T) {
	wall := `{"tick":1,"surface":"nauvis","force":"player","entity":"transport-belt","tunnelEntity":"","maxTunnel":0,"open":["10001"],"openTunnel":{},"fluid":["00000"],"tunnel":["00000"],"existing":{}}`
	margin := 0
	for name, tc := range map[string]struct {
		response string
		mutate   func(*ConnectionPlanInput)
		stop     ConnectionStopReason
		kind     ConnectionConflictKind
	}{
		"no path": {wall, func(in *ConnectionPlanInput) {}, ConnectionStopReasonNoPath, ConnectionConflictKindNoPath},
		"expansion limit": {
			`{"tick":1,"surface":"nauvis","force":"player","entity":"transport-belt","tunnelEntity":"","maxTunnel":0,"open":["11111"],"openTunnel":{},"fluid":["00000"],"tunnel":["00000"],"existing":{}}`,
			func(in *ConnectionPlanInput) { limit := 1; in.MaxExpandedTiles = &limit },
			ConnectionStopReasonExpansionLimit, ConnectionConflictKindExpansionLimit,
		},
		"endpoint blocked": {
			`{"tick":1,"surface":"nauvis","force":"player","entity":"transport-belt","tunnelEntity":"","maxTunnel":0,"open":["01111"],"openTunnel":{},"fluid":["00000"],"tunnel":["00000"],"existing":{}}`,
			func(in *ConnectionPlanInput) {}, ConnectionStopReasonEndpointBlocked, ConnectionConflictKindCollision,
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := serveScriptedRCON(t, regionRules(tc.response))
			input := astarPlanInput(ConnectionKindBelt, 0, 0, 4, 0)
			input.SearchMargin = &margin
			tc.mutate(input)
			plan, err := connectionPlanQuery(r, input)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Possible || plan.StopReason != tc.stop || plan.Needed != 0 || plan.UndergroundNeeded != 0 || len(plan.Steps) != 0 {
				t.Fatalf("failed plan: %+v", plan)
			}
			if len(plan.Conflicts) != 1 || plan.Conflicts[0].Kind != tc.kind || plan.Conflicts[0].Message == "" || plan.Conflicts[0].Position == nil {
				t.Fatalf("conflict: %+v", plan.Conflicts)
			}
		})
	}
}

// A malformed region must fail loudly rather than route on garbage.
func TestConnectionPlanAStarRejectsMalformedRegion(t *testing.T) {
	r, _ := serveScriptedRCON(t, regionRules(`{"tick":1,"surface":"nauvis","force":"player","entity":"transport-belt","tunnelEntity":"","maxTunnel":0,"open":["111"],"openTunnel":{},"fluid":["00000"],"tunnel":["00000"],"existing":{}}`))
	margin := 0
	input := astarPlanInput(ConnectionKindBelt, 0, 0, 4, 0)
	input.SearchMargin = &margin
	if _, err := connectionPlanQuery(r, input); err == nil || !strings.Contains(err.Error(), "open grid row 1 has 3 columns, want 5") {
		t.Fatalf("expected malformed grid error, got %v", err)
	}
}

// A pipe plan must read pipe-like neighbours and use pipe-to-ground by default.
func TestConnectionPlanAStarPipeDefaults(t *testing.T) {
	// A foreign pipe sits on the middle tile row; the planner must tunnel over it.
	response := `{"tick":1,"surface":"nauvis","force":"player","entity":"pipe","tunnelEntity":"pipe-to-ground","maxTunnel":10,"open":["11011"],"openTunnel":["11011"],"fluid":["00100"],"tunnel":["00000"],"existing":{}}`
	r, recorder := serveScriptedRCON(t, regionRules(response))
	input := astarPlanInput(ConnectionKindPipe, 0, 0, 4, 0)
	input.AllowUnderground = true
	margin := 0
	input.SearchMargin = &margin
	plan, err := connectionPlanQuery(r, input)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Possible || plan.UndergroundItem == nil || *plan.UndergroundItem != "pipe-to-ground" || plan.Item != "pipe" {
		t.Fatalf("pipe plan: %+v", plan)
	}
	var directions []int
	for _, step := range plan.Steps {
		if step.UndergroundType != nil {
			directions = append(directions, step.Direction)
		}
	}
	if len(directions) != 2 || directions[0] != 12 || directions[1] != 4 {
		t.Fatalf("eastbound pipe-to-ground faces west then east: %v", directions)
	}
	if !strings.Contains(recorder.commands[0], `local tunnel_item=`+luaString("pipe-to-ground")) || !strings.Contains(recorder.commands[0], `local kind=`+luaString("PIPE")) {
		t.Fatalf("region Lua: %s", recorder.commands[0])
	}
}

// A refused plan must place nothing and must not run the placement read.
func TestConnectEntitiesAStarRefusesWithoutPlacing(t *testing.T) {
	wall := `{"tick":1,"surface":"nauvis","force":"player","entity":"transport-belt","tunnelEntity":"","maxTunnel":0,"open":["10001"],"openTunnel":{},"fluid":["00000"],"tunnel":["00000"],"existing":{}}`
	r, recorder := serveScriptedRCON(t, regionRules(wall))
	margin := 0
	result, err := connectEntities(r, ConnectEntitiesInput{
		Actor: &ActorInput{Character: new("5")}, Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 4, Y: 0}, Kind: ConnectionKindBelt, Bend: ConnectionBendAuto,
		Routing: ConnectionRoutingAstar, SearchMargin: &margin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Possible || result.Placed != 0 || result.StopReason != ConnectionStopReasonNoPath || result.Routing != ConnectionRoutingAstar {
		t.Fatalf("refused result: %+v", result)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Kind != ConnectionConflictKindNoPath {
		t.Fatalf("conflicts: %+v", result.Conflicts)
	}
	if recorder.seen("need_tunnel") != 0 || recorder.seen("create_entity") != 0 {
		t.Fatalf("a refused plan must never run placement: %v", recorder.commands)
	}
}

// A feasible plan must place exactly the planned steps: tunnel halves with the
// underground item and type, ordinary tiles with the route item, and an
// inventory precheck before any removal.
func TestConnectEntitiesAStarPlacesPlannedSteps(t *testing.T) {
	region := `{"tick":50,"surface":"nauvis","force":"player","entity":"transport-belt","tunnelEntity":"underground-belt","maxTunnel":5,"open":["11011"],"openTunnel":["11011"],"fluid":["00000"],"tunnel":["00000"],"existing":{}}`
	applied := `{"tick":60,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":0.5,"y":0.5},"to":{"x":4.5,"y":0.5},"bend":"AUTO","possible":true,"placed":3,"skipped":0,"failed":0,"entities":[{"name":"underground-belt","type":"underground-belt","position":{"x":0.5,"y":0.5},"unitNumber":1}],"conflicts":{},"errors":{}}`
	r, recorder := serveScriptedRCON(t, []scriptRule{{"need_tunnel", applied}, {"grid_rows(entity_name)", region}})
	margin := 0
	input := ConnectEntitiesInput{
		Actor: &ActorInput{Character: new("5")}, Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 4, Y: 0}, Kind: ConnectionKindBelt, Bend: ConnectionBendAuto,
		Routing: ConnectionRoutingAstar, AllowUnderground: true, SearchMargin: &margin,
	}
	result, err := connectEntities(r, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Possible || result.Placed != 3 || result.Routing != ConnectionRoutingAstar || result.StopReason != ConnectionStopReasonFound {
		t.Fatalf("applied: %+v", result)
	}
	var apply string
	for _, command := range recorder.commands {
		if strings.Contains(command, "need_tunnel") {
			apply = command
		}
	}
	for _, want := range []string{
		`local unit=5`,
		`local item=` + luaString("transport-belt") + `; local tunnel_item=` + luaString("underground-belt"),
		`tunnel="input"`,
		`tunnel="output"`,
		`if have_item<need_item then fail(`,
		`params.type=s.tunnel`,
		`inv.insert{name=use_item,quality="normal",count=1}`,
		`if false then ok=false; fail_at(i,"REACH"`,
	} {
		if !strings.Contains(apply, want) {
			t.Fatalf("placement Lua missing %q\n%s", want, apply)
		}
	}
}
