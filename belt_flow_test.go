package main

import (
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
