package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCharacterSpawnLua(t *testing.T) {
	var out bytes.Buffer
	lua, err := characterSpawnLua([]string{"--surface=nauvis", "--x=1.5", "--y=-2", "--force=player"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"game.surfaces[" + luaString("nauvis") + "]", "game.forces[" + luaString("player") + "]", `find_non_colliding_position("character"`, `create_entity{name="character"`, `x=1.5`, `y=-2`} {
		if !strings.Contains(lua, want) {
			t.Errorf("spawn Lua missing %q:\n%s", want, lua)
		}
	}
	for _, args := range [][]string{
		{"--x=NaN"},
		{"--y=+Inf"},
		{"--surface="},
		{"--force="},
		{"--x=0", "extra"},
	} {
		if _, err := characterSpawnLua(args, &out); err == nil {
			t.Errorf("spawn args %v should be rejected", args)
		}
	}
}

func TestCharactersLua(t *testing.T) {
	var out bytes.Buffer
	lua, err := charactersLua([]string{"--surface=nauvis"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`type="character"`, `e.player==nil`, `total=#found`, `limit=100`, `offset=0`} {
		if !strings.Contains(lua, want) {
			t.Errorf("characters Lua missing %q:\n%s", want, lua)
		}
	}
	lua, err = charactersLua([]string{"--detached-only=false", "--limit=5", "--offset=10"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lua, "e.player==nil") || !strings.Contains(lua, "limit=5") || !strings.Contains(lua, "offset=10") {
		t.Errorf("characters Lua must list every character with the requested page:\n%s", lua)
	}
	for _, args := range [][]string{
		{"--surface="},
		{"--limit=0"},
		{"--limit=1001"},
		{"--offset=-1"},
		{"--limit=1", "extra"},
	} {
		if _, err := charactersLua(args, &out); err == nil {
			t.Errorf("characters args %v should be rejected", args)
		}
	}
}

func TestCharactersQueryRejectsBadPages(t *testing.T) {
	tests := []struct {
		input *CharactersInput
		want  string
	}{
		{&CharactersInput{Surface: ""}, "surface"},
		{&CharactersInput{Surface: "nauvis", Limit: 0}, "limit"},
		{&CharactersInput{Surface: "nauvis", Limit: 1001}, "limit"},
		{&CharactersInput{Surface: "nauvis", Limit: 10, Offset: -1}, "offset"},
	}
	for _, tt := range tests {
		tt.input.DetachedOnly = new(true)
		if _, err := charactersQuery(&Resolver{}, tt.input); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("input %#v: expected %s error, got %v", tt.input, tt.want, err)
		}
	}
}

func TestNavigationStartChaseValidation(t *testing.T) {
	x, y := 1.0, 2.0
	zero := "0"
	target := "2"
	tests := []struct {
		name  string
		input NavigateStartInput
		want  string
	}{
		{"character required", NavigateStartInput{Character: "", X: &x, Y: &y, Tolerance: 0.5, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "character"},
		{"destination required", NavigateStartInput{Character: "1", Tolerance: 0.5, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "x and y"},
		{"coordinates or target", NavigateStartInput{Character: "1", X: &x, Y: &y, TargetCharacter: &target, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "exactly one"},
		{"target unit", NavigateStartInput{Character: "1", TargetCharacter: &zero, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "targetCharacter"},
		{"entity unit", NavigateStartInput{Character: "1", TargetEntity: &zero, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "targetEntity"},
		{"entity differs", NavigateStartInput{Character: "1", TargetEntity: new("1"), Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "differ"},
		{"entity plus coordinates", NavigateStartInput{Character: "1", X: &x, Y: &y, TargetEntity: new("3"), Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "exactly one"},
		{"entity plus character", NavigateStartInput{Character: "1", TargetCharacter: &target, TargetEntity: new("3"), Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "exactly one"},
		{"unpaired coordinate", NavigateStartInput{Character: "1", X: &x, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "together"},
		{"target differs", NavigateStartInput{Character: "1", TargetCharacter: new("1"), Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "differ"},
		{"chase tolerance", NavigateStartInput{Character: "1", TargetCharacter: &target, Tolerance: 0.5, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "at least 1"},
		{"tolerance bound", NavigateStartInput{Character: "1", X: &x, Y: &y, Tolerance: 11, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "tolerance"},
	}
	for _, tt := range tests {
		if _, err := navigationStart(t.Context(), &Resolver{}, tt.input); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: expected %s error, got %v", tt.name, tt.want, err)
		}
	}
}

func TestNotificationGateFollowsSchemaMutations(t *testing.T) {
	_ = newGraphQLSchema("", "")
	for _, name := range []string{"character-spawn", "place-many", "set-recipe", "belt-throughput-start"} {
		if !isMutationCommand(name) {
			t.Errorf("%s must be announced as a mutation", name)
		}
	}
	for _, name := range []string{"status", "characters", "entity", "player", "factory-overview"} {
		if isMutationCommand(name) {
			t.Errorf("%s must not be announced as a mutation", name)
		}
	}
}
