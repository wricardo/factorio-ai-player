package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Inspection reads one entity, so it must not demand an actor: without
// --player or --character the generated chunk must carry no actor binding at
// all, while target resolution and the force-agnostic lookup stay the same.
func TestInspectLuaWithoutActorOmitsActorBinding(t *testing.T) {
	var out bytes.Buffer
	lua, err := productionLua("inspect", []string{"--unit-number=439"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"local p\n",
		"p=game.get_player(",
		"p=game.get_entity_by_unit_number(",
		"game.players",
	} {
		if strings.Contains(lua, forbidden) {
			t.Errorf("actorless inspect Lua must not bind an actor, found %q:\n%s", forbidden, lua)
		}
	}
	for _, want := range []string{
		"local target_unit=439",
		"resolve_entity(target_unit,target_name,target_position,surface_name,p,false,target_position_valid)",
		"inspect_snapshot(e,s)",
	} {
		if !strings.Contains(lua, want) {
			t.Errorf("actorless inspect Lua missing %q:\n%s", want, lua)
		}
	}
}

// An actor is still accepted and still validated exactly as before, so callers
// that pass one keep the actor-era behavior.
func TestInspectLuaWithActorKeepsBinding(t *testing.T) {
	var out bytes.Buffer
	lua, err := productionLua("inspect", []string{"--player=wricardo", "--unit-number=439"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"p=game.get_player(" + luaString("wricardo") + ")",
		"resolve_entity(target_unit,target_name,target_position,surface_name,p,false,target_position_valid)",
	} {
		if !strings.Contains(lua, want) {
			t.Errorf("inspect Lua missing %q:\n%s", want, lua)
		}
	}
}

// transfer acts on the actor's inventory, so it must keep requiring an actor
// even though inspect no longer does.
func TestTransferStillRequiresActor(t *testing.T) {
	var out bytes.Buffer
	_, err := productionLua("transfer", []string{"--unit-number=439", "--inventory=chest", "--direction=to-player", "--item=iron-plate", "--count=5"}, &out)
	if err == nil || !strings.Contains(err.Error(), "exactly one of player or character") {
		t.Fatalf("transfer without an actor must be rejected, got %v", err)
	}
}

// recipe without an actor reads the named force's prototype data and cannot
// compute craftableCount, which needs a character inventory.
func TestRecipeLuaForceOnlyOmitsCraftableCount(t *testing.T) {
	lua, err := playerInspectLua("recipe", []string{"--force=player", "--recipe=iron-gear-wheel"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"local force=game.forces[" + luaString("player") + "]",
		"if not force then fail(\"force not found\")",
		"local recipe=force.recipes[" + luaString("iron-gear-wheel") + "]",
		"local subject={force=force.name}",
	} {
		if !strings.Contains(lua, want) {
			t.Errorf("force-only recipe Lua missing %q:\n%s", want, lua)
		}
	}
	for _, forbidden := range []string{"get_craftable_count", "p.force.recipes", "p=game.get_player(", "p=game.get_entity_by_unit_number("} {
		if strings.Contains(lua, forbidden) {
			t.Errorf("force-only recipe Lua must not use %q:\n%s", forbidden, lua)
		}
	}
}

// A supplied actor still wins over force and keeps the craftable count.
func TestRecipeLuaActorWinsOverForce(t *testing.T) {
	lua, err := playerInspectLua("recipe", []string{"--player=wricardo", "--force=enemy", "--recipe=iron-gear-wheel"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"p=game.get_player(" + luaString("wricardo") + ")",
		"p.force.recipes[" + luaString("iron-gear-wheel") + "]",
		"p.get_craftable_count(recipe.name)",
	} {
		if !strings.Contains(lua, want) {
			t.Errorf("actor recipe Lua missing %q:\n%s", want, lua)
		}
	}
	if strings.Contains(lua, "game.forces[") {
		t.Errorf("actor recipe Lua must not read a named force:\n%s", lua)
	}
}

// The decoded recipe must report a null craftableCount and still carry the
// prototype fields when no actor supplied a character.
func TestRecipeQueryForceOnlyDecodesNullCraftableCount(t *testing.T) {
	r, recorder := serveScriptedRCON(t, []scriptRule{{
		marker: "local force=game.forces[",
		response: `{"tick":7,"force":"player","name":"iron-gear-wheel","enabled":true,` +
			`"category":"crafting","energy":0.5,"ingredients":[{"name":"iron-plate","amount":2}],` +
			`"products":[{"name":"iron-gear-wheel","amount":1}],"hidden_from_player_crafting":false}`,
	}})
	force, recipe := "player", "iron-gear-wheel"
	result, err := command[Recipe](r, "recipe", RecipeInput{Force: force, Recipe: recipe})
	if err != nil {
		t.Fatal(err)
	}
	if result.CraftableCount != nil {
		t.Fatalf("craftableCount = %v, want null without an actor", *result.CraftableCount)
	}
	if result.Force != "player" || result.Name == nil || *result.Name != recipe {
		t.Fatalf("recipe = %+v, want the player force recipe", result)
	}
	if len(result.Ingredients) != 1 || result.Ingredients[0].Name != "iron-plate" {
		t.Fatalf("ingredients = %+v, want the prototype ingredients", result.Ingredients)
	}
	if recorder.seen("get_craftable_count") != 0 {
		t.Fatal("force-only recipe must not ask the engine for a craftable count")
	}
	if recorder.seen("game.get_player(") != 0 {
		t.Fatal("force-only recipe must not bind a player")
	}
}

// An actor-addressed recipe keeps reporting the numeric craftable count.
func TestRecipeQueryActorDecodesCraftableCount(t *testing.T) {
	r, recorder := serveScriptedRCON(t, []scriptRule{{
		marker: "p.get_craftable_count(recipe.name)",
		response: `{"tick":8,"player":"wricardo","force":"player","name":"iron-gear-wheel",` +
			`"enabled":true,"craftable_count":12,"ingredients":[],"products":[]}`,
	}})
	player, recipe := "wricardo", "iron-gear-wheel"
	result, err := command[Recipe](r, "recipe", RecipeInput{Actor: &ActorInput{Player: &player}, Recipe: recipe})
	if err != nil {
		t.Fatal(err)
	}
	if result.CraftableCount == nil || *result.CraftableCount != 12 {
		t.Fatalf("craftableCount = %v, want 12", result.CraftableCount)
	}
	if recorder.seen("game.get_player("+luaString("wricardo")+")") != 1 {
		t.Fatal("actor recipe must bind the named player")
	}
}

// An unknown force is rejected by the chunk before it reads a recipe.
func TestRecipeUnknownForceFails(t *testing.T) {
	r, _ := serveScriptedRCON(t, []scriptRule{{
		marker:   "local force=game.forces[",
		response: `{"error":"force not found"}`,
	}})
	force, recipe := "no-such-force", "iron-gear-wheel"
	_, err := command[Recipe](r, "recipe", RecipeInput{Force: force, Recipe: recipe})
	if err == nil || !strings.Contains(err.Error(), "force not found") {
		t.Fatalf("unknown force must fail, got %v", err)
	}
}

// The nested recipeInfo resolver forwards force and no actor when only a force
// is given, so a queue or inspection can be read without a character.
func TestRecipeInfoForceOnlyPassesForce(t *testing.T) {
	r, recorder := serveScriptedRCON(t, []scriptRule{{
		marker:   "local force=game.forces[",
		response: `{"tick":9,"force":"player","name":"iron-gear-wheel","enabled":true,"ingredients":[],"products":[]}`,
	}})
	name, force := "iron-gear-wheel", "player"
	result, err := recipeInfo(r, &name, nil, force)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name == nil || *result.Name != name {
		t.Fatalf("recipe = %+v, want the requested recipe", result)
	}
	if recorder.seen("local force=game.forces["+luaString("player")+"]") != 1 {
		t.Fatal("recipeInfo must pass the force through to the backend")
	}
	if recorder.seen("game.get_player(") != 0 {
		t.Fatal("recipeInfo must not bind a player when no actor is given")
	}
}

// A supplied actor wins over the force argument, matching RecipeInput.
func TestRecipeInfoActorWinsOverForce(t *testing.T) {
	r, recorder := serveScriptedRCON(t, []scriptRule{{
		marker:   "p.get_craftable_count(recipe.name)",
		response: `{"tick":10,"player":"wricardo","force":"player","name":"iron-gear-wheel","enabled":true,"craftable_count":3,"ingredients":[],"products":[]}`,
	}})
	name, player, force := "iron-gear-wheel", "wricardo", "enemy"
	result, err := recipeInfo(r, &name, &ActorInput{Player: &player}, force)
	if err != nil {
		t.Fatal(err)
	}
	if result.CraftableCount == nil || *result.CraftableCount != 3 {
		t.Fatalf("craftableCount = %v, want the actor's count", result.CraftableCount)
	}
	if recorder.seen("game.get_player("+luaString("wricardo")+")") != 1 {
		t.Fatal("recipeInfo must bind the actor when one is given")
	}
	if recorder.seen("game.forces[") != 0 {
		t.Fatal("recipeInfo must ignore force when an actor is given")
	}
}

// The decoded inspect response must come back from an actorless call, since
// Entity.inspection forwards a nil actor straight through.
func TestInspectWithoutActorDecodesSnapshot(t *testing.T) {
	r, recorder := serveScriptedRCON(t, []scriptRule{{
		marker: "inspect_snapshot(e,s)",
		response: `{"tick":11,"name":"assembling-machine-1","type":"assembling-machine",` +
			`"position":{"x":100.5,"y":-20.5},"surface":"nauvis","force":"player",` +
			`"recipe":"iron-gear-wheel","inventories":{},"fluids":[],"to_be_deconstructed":false}`,
	}})
	entity, unit := "assembling-machine-1", "12888"
	result, err := inspect(r, InspectInput{Target: &EntityRefInput{Entity: &entity, UnitNumber: &unit}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name == nil || *result.Name != entity {
		t.Fatalf("name = %v, want the inspected entity", result.Name)
	}
	if result.Recipe == nil || *result.Recipe != "iron-gear-wheel" {
		t.Fatalf("recipe = %v, want the machine recipe", result.Recipe)
	}
	if recorder.seen("game.get_player(") != 0 || recorder.seen("game.players") != 0 {
		t.Fatal("actorless inspect must not resolve a player")
	}
}
