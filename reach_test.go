package main

import (
	"io"
	"strings"
	"testing"
)

func TestReachDisabledByDefault(t *testing.T) {
	t.Setenv("FACTORIO_ENFORCE_REACH", "")
	if !reachDisabled() {
		t.Fatal("reach checks must be bypassed without the env flag")
	}
	if got := reachViolation("c", "e"); got != "false" {
		t.Fatalf("reachViolation = %q", got)
	}
	if got := reachable("c", "e"); got != "true" {
		t.Fatalf("reachable = %q", got)
	}
	if got := distanceViolation("dx", "dy", "c.build_distance"); got != "false" {
		t.Fatalf("distanceViolation = %q", got)
	}
}

func TestReachEnabledByFlag(t *testing.T) {
	t.Setenv("FACTORIO_ENFORCE_REACH", "yes")
	if reachDisabled() {
		t.Fatal("truthy env must enable reach checks")
	}
	if got := reachViolation("c", "e"); got != "not c.can_reach_entity(e)" {
		t.Fatalf("reachViolation = %q", got)
	}
	if got := reachable("c", "e"); got != "c.can_reach_entity(e)" {
		t.Fatalf("reachable = %q", got)
	}
	if got := distanceViolation("dx", "dy", "c.build_distance"); got != "dx*dx+dy*dy>(c.build_distance)*(c.build_distance)" {
		t.Fatalf("distanceViolation = %q", got)
	}
}

func TestReachBypassReachesGeneratedLua(t *testing.T) {
	args := []string{"--character", "1", "--entity", "iron-ore", "--x", "1", "--y", "2"}
	t.Setenv("FACTORIO_ENFORCE_REACH", "1")
	enforced, err := resourceLua("mine", args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enforced, "not actor.can_reach_entity(e)") {
		t.Fatalf("mine lua lost reach check: %q", enforced)
	}
	t.Setenv("FACTORIO_ENFORCE_REACH", "")
	bypassed, err := resourceLua("mine", args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bypassed, "can_reach_entity") || !strings.Contains(bypassed, `if false then fail("entity is out of reach")`) {
		t.Fatalf("mine lua still enforces reach: %q", bypassed)
	}
}

func TestReachBypassAppliesToPlacement(t *testing.T) {
	args := []string{"--character", "1", "--item", "transport-belt", "--x", "300", "--y", "300"}
	t.Setenv("FACTORIO_ENFORCE_REACH", "1")
	enforced, err := actionLua("place", args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enforced, "c.build_distance") {
		t.Fatalf("place lua lost build reach check: %q", enforced)
	}
	t.Setenv("FACTORIO_ENFORCE_REACH", "")
	bypassed, err := actionLua("place", args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bypassed, "build_distance") || !strings.Contains(bypassed, `if false then fail("position is out of build reach")`) {
		t.Fatalf("place lua still enforces build reach: %q", bypassed)
	}
}
