package main

import (
	"context"
	"os/exec"
	"testing"
)

// The belt-flow sampler lives in the navigation mod and is driven by game ticks,
// so it is exercised by its Lua harness instead of the Go RCON fakes.
func TestBeltFlowSampler(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	out, err := exec.Command(lua, "mods/factorio_ai_navigation/belt_flow_test.lua").CombinedOutput()
	if err != nil {
		t.Fatalf("belt flow sampler: %v\n%s", err, out)
	}
}

func TestSelectStretchSample(t *testing.T) {
	node := func(x float64, kind string) *BeltStretchNode {
		return &BeltStretchNode{Type: kind, Position: &Position{X: x, Y: 0}}
	}
	nodes := []*BeltStretchNode{
		node(0, "underground-belt"),
		node(1, "transport-belt"),
		node(2, "transport-belt"),
		node(3, "transport-belt"),
		{Type: "transport-belt"},
		node(4, "underground-belt"),
	}
	cases := []struct {
		sample BeltStretchSample
		want   float64
	}{
		{BeltStretchSampleFirst, 1},
		{BeltStretchSampleMiddle, 2},
		{BeltStretchSampleLast, 3},
	}
	for _, tc := range cases {
		selected, err := selectStretchSample(nodes, tc.sample)
		if err != nil {
			t.Fatalf("%s: %v", tc.sample, err)
		}
		if selected.Position.X != tc.want {
			t.Fatalf("%s: selected x=%v, want %v", tc.sample, selected.Position.X, tc.want)
		}
	}
	if _, err := selectStretchSample([]*BeltStretchNode{node(0, "underground-belt"), node(1, "splitter")}, BeltStretchSampleMiddle); err == nil {
		t.Fatal("stretch without a transport belt must be rejected")
	}
	if _, err := selectStretchSample(nil, BeltStretchSampleFirst); err == nil {
		t.Fatal("empty stretch must be rejected")
	}
}

func TestBeltStretchMeasureValidation(t *testing.T) {
	r := &Resolver{}
	target := &EntityRefInput{Position: &PositionInput{X: 1, Y: 1}}
	base := BeltStretchMeasureInput{Target: target, DurationTicks: 600, WaitTimeoutSeconds: 30}
	input := base
	input.DurationTicks = 0
	if _, err := beltStretchMeasure(context.Background(), r, input); err == nil {
		t.Fatal("durationTicks 0 must be rejected before any RCON call")
	}
	input = base
	input.WaitTimeoutSeconds = 0
	if _, err := beltStretchMeasure(context.Background(), r, input); err == nil {
		t.Fatal("waitTimeoutSeconds 0 must be rejected before any RCON call")
	}
}
