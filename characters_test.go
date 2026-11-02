package main

import (
	"bytes"
	"os/exec"
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

func TestCharacterDeleteLua(t *testing.T) {
	var out bytes.Buffer
	lua, err := characterDeleteLua([]string{"--character=967", "--force=player"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"game.forces[" + luaString("player") + "]",
		`game.get_entity_by_unit_number(967)`,
		`c.type~="character"`,
		`if c.player then fail("character is controlled by a player")`,
		`for _,q in pairs(game.players) do if q.character==c then fail("character belongs to player "..q.name)`,
		`if c.force~=f then fail("character force does not match requested force")`,
		`c.crafting_queue_size>0`,
		`character_guns","character_ammo","character_armor","character_trash`,
		`if inv and not inv.is_empty() then fail("character main inventory is not empty; empty it before deleting")`,
		`c.destroy()`,
		`character=id`,
	} {
		if !strings.Contains(lua, want) {
			t.Errorf("delete Lua missing %q:\n%s", want, lua)
		}
	}
	for _, args := range [][]string{
		{},
		{"--character=967"},
		{"--force=player"},
		{"--character=0", "--force=player"},
		{"--character=4294967296", "--force=player"},
		{"--character=967", "--force="},
		{"--character=967", "--force=player", "extra"},
	} {
		if _, err := characterDeleteLua(args, &out); err == nil {
			t.Errorf("delete args %v should be rejected", args)
		}
	}
	if luac, err := exec.LookPath("luac"); err == nil {
		cmd := exec.Command(luac, "-p", "-")
		cmd.Stdin = strings.NewReader(lua)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("delete Lua does not parse: %v\n%s\n%s", err, output, lua)
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
		{"--limit=10001"},
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
		{&CharactersInput{Surface: "nauvis", Limit: maxPageSize + 1}, "limit"},
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
	position := &PositionInput{X: 1, Y: 2}
	zero := "0"
	target := "2"
	tests := []struct {
		name  string
		input NavigateStartInput
		want  string
	}{
		{"character required", NavigateStartInput{Position: position, Tolerance: 0.5, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "exactly one"},
		{"destination required", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, Tolerance: 0.5, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "position"},
		{"coordinates or target", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, Position: position, TargetCharacter: &target, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "exactly one"},
		{"target unit", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetCharacter: &zero, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "targetCharacter"},
		{"entity unit", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetEntity: &EntityRefInput{UnitNumber: &zero}, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "targetEntity"},
		{"entity differs", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetEntity: &EntityRefInput{UnitNumber: new("1")}, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "differ"},
		{"entity plus coordinates", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, Position: position, TargetEntity: &EntityRefInput{Entity: new("wooden-chest"), Position: position}, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "exactly one"},
		{"entity plus character", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetCharacter: &target, TargetEntity: &EntityRefInput{Entity: new("wooden-chest"), Position: position}, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "exactly one"},
		{"entity name without position", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetEntity: &EntityRefInput{Entity: new("wooden-chest")}, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "both"},
		{"entity position without name", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetEntity: &EntityRefInput{Position: position}, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "both"},
		{"entity empty reference", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetEntity: &EntityRefInput{}, Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "targetEntity"},
		{"target differs", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetCharacter: new("1"), Tolerance: 1, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "differ"},
		{"chase tolerance", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, TargetCharacter: &target, Tolerance: 0.5, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "at least 1"},
		{"tolerance bound", NavigateStartInput{Actor: &ActorInput{Character: new("1")}, Position: position, Tolerance: 11, TimeoutTicks: 10, WaitTimeoutSeconds: 1}, "tolerance"},
	}
	for _, tt := range tests {
		if _, err := navigationStart(t.Context(), &Resolver{}, tt.input); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: expected %s error, got %v", tt.name, tt.want, err)
		}
	}
}

func TestNotificationGateFollowsSchemaMutations(t *testing.T) {
	_ = newGraphQLSchema("", "")
	for _, name := range []string{"character-spawn", "character-delete", "place-many", "set-recipe", "belt-throughput-start"} {
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
