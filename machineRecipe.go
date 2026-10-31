package main

import (
	"errors"
	"fmt"
	"strings"
)

// This file holds the resolver side of the machine recipe mutations: the drain
// mode validation for setRecipe and the clearRecipe mutation, which drops the
// machine's current recipe and returns the items the engine hands back.

// recipeDrainModeNames maps the GraphQL enum to the backend drain flag values.
var recipeDrainModeNames = map[RecipeDrainMode]string{
	RecipeDrainModeAbort:   "abort",
	RecipeDrainModeToActor: "to-actor",
	RecipeDrainModeSpill:   "spill",
}

// normalizeRecipeDrain validates a drain mode and returns its backend spelling.
// The empty string means the schema default, abort.
func normalizeRecipeDrain(value string) (string, error) {
	if value == "" {
		return recipeDrainModeNames[RecipeDrainModeAbort], nil
	}
	mode := RecipeDrainMode(strings.ToUpper(strings.ReplaceAll(value, "-", "_")))
	name, ok := recipeDrainModeNames[mode]
	if !ok {
		return "", errors.New("drain must be ABORT, TO_ACTOR, or SPILL")
	}
	return name, nil
}

// setRecipeCommandInput is the backend flag payload for the set-recipe command.
// The actor is already resolved to a character unit number by setRecipe.
type setRecipeCommandInput struct {
	Character     *string  `json:"character,omitempty"`
	Entity        *string  `json:"entity,omitempty"`
	X             *float64 `json:"x,omitempty"`
	Y             *float64 `json:"y,omitempty"`
	UnitNumber    *string  `json:"unitNumber,omitempty"`
	Recipe        string   `json:"recipe"`
	RecipeQuality *string  `json:"recipeQuality,omitempty"`
	Drain         *string  `json:"drain,omitempty"`
	Message       *string  `json:"message,omitempty"`
}

// setRecipe assigns a recipe and drains the machine contents back to the actor.
func setRecipe(r *Resolver, input SetRecipeInput) (*ActionResult, error) {
	if strings.TrimSpace(input.Recipe) == "" {
		return nil, errors.New("recipe must not be empty")
	}
	drain := ""
	if input.Drain != nil {
		if !input.Drain.IsValid() {
			return nil, errors.New("drain must be ABORT, TO_ACTOR, or SPILL")
		}
		drain = string(*input.Drain)
	}
	mode, err := normalizeRecipeDrain(drain)
	if err != nil {
		return nil, err
	}
	quality, err := resolveQuality(input.RecipeQuality)
	if err != nil {
		return nil, err
	}
	character, err := resolveActorCharacter(r, input.Player, input.Character)
	if err != nil {
		return nil, err
	}
	return command[ActionResult](r, "set-recipe", setRecipeCommandInput{
		Character:     &character,
		Entity:        input.Entity,
		X:             input.X,
		Y:             input.Y,
		UnitNumber:    input.UnitNumber,
		Recipe:        input.Recipe,
		RecipeQuality: &quality,
		Drain:         &mode,
		Message:       input.Message,
	})
}

// clearRecipe resolves the target machine, drains its input and output into the
// acting character's main inventory, then drops the recipe. Anything the engine
// still returns is added to the drained list, and whatever does not fit in the
// inventory is spilled at the machine.
func clearRecipe(r *Resolver, input ClearRecipeInput) (*ActionResult, error) {
	character, err := resolveActorCharacter(r, input.Player, input.Character)
	if err != nil {
		return nil, err
	}
	target, err := settingsTargetFrom(nil, &character, input.Surface, input.Entity, input.UnitNumber, input.X, input.Y)
	if err != nil {
		return nil, err
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	lua := luaFail + prefix + machineDrainLua + `if e.is_crafting() then fail("machine has an active craft"); return end
local drained={}
machine_drain(e,drained,c,c.position,true)
local ok,engine_returned=pcall(function() return e.set_recipe(nil) end)
if not ok then fail("recipe could not be cleared: "..tostring(engine_returned)); return end
machine_merge_engine_items(e,drained,c,c.position,true,engine_returned)
rcon.print(helpers.table_to_json({tick=game.tick,entity=e.name,position=e.position,surface=e.surface.name,character=c.unit_number,returned=machine_aggregate(drained),unit_number=e.unit_number}))
`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ActionResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, nil, fmt.Sprintf("cleared recipe on %s%s", stringValue(result.Entity), positionText(result.Position)))
	return result, nil
}
