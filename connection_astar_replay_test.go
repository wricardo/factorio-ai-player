package main

import (
	"strings"
	"testing"
)

// connectEntitiesAStar computes the plan in Go and then applies it with the
// placement Lua, so this test drives only that apply phase.
func serveAStarPlacementReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "connection_astar_engine.lua", []string{"CONNECTION_ASTAR_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "create_entity")
	})
}

func connectBeltRun(t *testing.T, scenario string) (*ConnectEntitiesResult, error) {
	t.Helper()
	plan := &ConnectionPlan{
		Tick: 6000, Surface: "nauvis", Force: "player", Kind: ConnectionKindBelt, Item: "transport-belt",
		From: &Position{X: 0.5, Y: 0.5}, To: &Position{X: 2.5, Y: 0.5},
		Routing: ConnectionRoutingAstar, Possible: true, Needed: 3, Steps: []*ConnectionStep{},
	}
	for x := 0.5; x <= 2.5; x++ {
		plan.Steps = append(plan.Steps, &ConnectionStep{Position: &Position{X: x, Y: 0.5}, Direction: 4})
	}
	return applyRoutePlan(serveAStarPlacementReplay(t, scenario), ConnectEntitiesInput{
		Actor:   &ActorInput{Character: new("4")},
		Surface: "nauvis",
		Force:   "player",
		From:    &PositionInput{X: 0, Y: 0},
		To:      &PositionInput{X: 2, Y: 0},
		Kind:    ConnectionKindBelt,
	}, plan)
}

func TestConnectEntitiesIgnoresDisagreeingPlacementCheck(t *testing.T) {
	result, err := connectBeltRun(t, "snap")
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 3 || result.Failed != 0 || len(result.Errors) != 0 {
		t.Fatalf("engine accepted every tile but the run reported failures: %+v", result)
	}
	if len(result.Entities) != 3 {
		t.Fatalf("expected three placed entities, got %+v", result.Entities)
	}
}

func TestConnectEntitiesReportsGenuineCollision(t *testing.T) {
	result, err := connectBeltRun(t, "blocked")
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 3 {
		t.Fatalf("expected three blocked tiles, got %+v", result)
	}
	for _, entry := range result.Errors {
		if entry.Code != BatchErrorCodeCollision {
			t.Fatalf("expected COLLISION errors, got %+v", entry)
		}
	}
}

func TestConnectEntitiesReportsEngineError(t *testing.T) {
	result, err := connectBeltRun(t, "engine")
	if err != nil {
		t.Fatal(err)
	}
	if result.Placed != 0 || result.Failed != 3 {
		t.Fatalf("expected three engine failures, got %+v", result)
	}
	for _, entry := range result.Errors {
		if entry.Code != BatchErrorCodeEngine {
			t.Fatalf("expected ENGINE errors, got %+v", entry)
		}
	}
}
