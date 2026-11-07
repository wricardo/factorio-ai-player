package main

import (
	"context"
	"strings"
	"testing"
)

// buildConnection returns the nested connectEntities result, whose actor field is
// non-null. Before this the resolver copied the result through untouched, so a
// client selecting buildConnection(...){result{actor{...}}} failed the null check
// on every real placement.
func TestBuildConnectionResultCarriesActor(t *testing.T) {
	plan := `{"tick":9,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":0.5,"y":0.5},"to":{"x":2.5,"y":0.5},"bend":"AUTO","possible":true,"needed":3,"steps":[{"position":{"x":0.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":1.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":2.5,"y":0.5},"direction":4,"existing":false}],"conflicts":[]}`
	applied := `{"tick":9,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":0.5,"y":0.5},"to":{"x":2.5,"y":0.5},"bend":"AUTO","possible":true,"placed":3,"skipped":0,"failed":0,"entities":[{"name":"transport-belt","type":"transport-belt","position":{"x":0.5,"y":0.5},"unitNumber":1}],"conflicts":{},"errors":{}}`
	resolver, _ := serveScriptedRCON(t, []scriptRule{
		{"local placed,skipped,failed", applied},
		{"local out_steps=", plan},
	})
	mutations := &mutationResolver{Resolver: resolver}
	input := ConnectEntitiesInput{
		Actor: &ActorInput{Character: new("5")}, Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 2, Y: 0}, Kind: ConnectionKindBelt,
	}
	built, err := mutations.BuildConnection(context.Background(), input, false)
	if err != nil {
		t.Fatal(err)
	}
	if built == nil || built.Result == nil {
		t.Fatalf("buildConnection returned no nested result: %+v", built)
	}
	if built.Result.Placed != 3 {
		t.Fatalf("placed = %d, want 3", built.Result.Placed)
	}
	if built.Result.Actor == nil {
		t.Fatal("result.actor is nil; selecting it would violate the non-null ActorIdentity")
	}
	if got := built.Result.Actor.Character; got == nil || *got != "5" {
		t.Fatalf("result.actor.character = %v, want 5", got)
	}
}

// A dry run plans without placing: it reads the route through RCON (so the
// scripted harness is required) and must never send the placement command or
// carry a nested result.
func TestBuildConnectionDryRunPlansWithoutPlacing(t *testing.T) {
	plan := `{"tick":9,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":0.5,"y":0.5},"to":{"x":2.5,"y":0.5},"bend":"AUTO","possible":true,"needed":3,"steps":[{"position":{"x":0.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":1.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":2.5,"y":0.5},"direction":4,"existing":false}],"conflicts":[]}`
	resolver, recorder := serveScriptedRCON(t, []scriptRule{
		{"local placed,skipped,failed", `{"error":"a dry run must not place"}`},
		{"local out_steps=", plan},
	})
	mutations := &mutationResolver{Resolver: resolver}
	input := ConnectEntitiesInput{
		Actor: &ActorInput{Character: new("5")}, Surface: "nauvis", Force: "player",
		From: &PositionInput{X: 0, Y: 0}, To: &PositionInput{X: 2, Y: 0}, Kind: ConnectionKindBelt,
	}
	built, err := mutations.BuildConnection(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	if built == nil || !built.DryRun {
		t.Fatalf("dry run result: %+v", built)
	}
	if built.Result != nil {
		t.Fatalf("dry run carried a nested result: %+v", built.Result)
	}
	if built.Plan == nil || !built.Plan.Possible || built.Plan.Needed != 3 {
		t.Fatalf("dry run plan: %+v", built.Plan)
	}
	if len(recorder.commands) != 1 {
		t.Fatalf("dry run sent %d commands, want only the plan read", len(recorder.commands))
	}
	for _, command := range recorder.commands {
		if strings.Contains(command, "local placed,skipped,failed") {
			t.Fatalf("dry run sent a placement command\n%s", command)
		}
	}
}
