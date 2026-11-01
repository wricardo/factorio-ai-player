package main

import (
	"io"
	"strings"
	"testing"
)

// Every EntityRefInput field must have a matching backend flag. The GraphQL
// bridge flattens a target struct into --surface/--entity/--unitNumber/--x/--y
// before dispatching, so one missing flag makes the whole mutation unusable
// with that target shape. The backend address is unreachable on purpose: only
// flag parsing and validation are under test here.
func TestCommandFlagsAcceptEntityRef(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"belt-take-position", []string{"belt-take", "--surface=nauvis", "--entity=transport-belt", "--x=0", "--y=0", "--player=wricardo", "--item=iron-plate", "--count=1"}},
		{"belt-take-unit-number", []string{"belt-take", "--surface=nauvis", "--entity=transport-belt", "--unitNumber=42", "--character=7", "--item=iron-plate", "--count=1"}},
		{"mine-position", []string{"mine", "--surface=nauvis", "--entity=stone", "--x=0", "--y=0", "--character=7"}},
		{"mine-unit-number", []string{"mine", "--surface=nauvis", "--entity=stone", "--unitNumber=42", "--character=7"}},
		{"remove-unit-number", []string{"remove", "--surface=nauvis", "--entity=substation", "--unitNumber=42", "--character=7"}},
		{"remove-position", []string{"remove", "--surface=nauvis", "--entity=substation", "--x=0", "--y=0", "--character=7"}},
		{"rotate-position", []string{"rotate", "--surface=nauvis", "--entity=transport-belt", "--x=0", "--y=0", "--character=7", "--turns=1"}},
		{"rotate-unit-number", []string{"rotate", "--surface=nauvis", "--entity=transport-belt", "--unitNumber=42", "--character=7", "--turns=1"}},
		{"set-recipe-position", []string{"set-recipe", "--surface=nauvis", "--entity=assembling-machine-1", "--x=0", "--y=0", "--character=7", "--recipe=iron-gear-wheel"}},
		{"set-recipe-unit-number", []string{"set-recipe", "--surface=nauvis", "--entity=assembling-machine-1", "--unitNumber=42", "--character=7", "--recipe=iron-gear-wheel"}},
		{"fuel-position", []string{"fuel", "--surface=nauvis", "--entity=stone-furnace", "--x=0", "--y=0", "--character=7", "--item=coal", "--count=1"}},
		{"transfer-position", []string{"transfer", "--surface=nauvis", "--entity=wooden-chest", "--x=0", "--y=0", "--character=7", "--inventory=chest", "--direction=to-entity", "--item=coal", "--count=1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := executeGameOperation(tc.args, io.Discard, "127.0.0.1:1", "password")
			if err == nil {
				return
			}
			for _, rejected := range []string{"flag provided but not defined", "flag needs an argument", "invalid value"} {
				if strings.Contains(err.Error(), rejected) {
					t.Fatalf("%s rejects EntityRefInput flags: %v", tc.name, err)
				}
			}
		})
	}
}

func TestPlaceUndergroundTypeValidation(t *testing.T) {
	base := []string{"--player=wricardo", "--surface=nauvis", "--x=0", "--y=0", "--item=underground-belt", "--direction=8"}
	for _, value := range []string{"bogus", "Input", "OUTPUT"} {
		args := append(append([]string{}, base...), "--undergroundType="+value)
		if _, err := actionLua("place", args, io.Discard); err == nil || !strings.Contains(err.Error(), "undergroundType") {
			t.Fatalf("undergroundType %q must be rejected with a clear error, got %v", value, err)
		}
	}
	for _, value := range []string{"input", "output"} {
		args := append(append([]string{}, base...), "--undergroundType="+value)
		if _, err := actionLua("place", args, io.Discard); err != nil {
			t.Fatalf("undergroundType %q must be accepted, got %v", value, err)
		}
	}
}
