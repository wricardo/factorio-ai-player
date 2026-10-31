package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

// playerInspectLua generates read-only player and character inspections.
func playerInspectLua(command string, args []string, out io.Writer) (string, error) {
	if command != "player" && command != "inventory" && command != "queue" && command != "recipe" {
		return "", fmt.Errorf("unknown player inspection command %q", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player name (alternative to --character)")
	character := fs.Uint64("character", 0, "Character entity unit number (alternative to --player)")
	var recipe *string
	if command == "recipe" {
		recipe = fs.String("recipe", "", "Recipe name (required)")
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || (recipe != nil && *recipe == "") {
		return "", errors.New("exactly one of player or positive character unit number is required, plus any required recipe; no positional arguments allowed")
	}
	prefix := luaFail
	if *player != "" {
		prefix += fmt.Sprintf(`local p=game.get_player(%s); if not p then fail("player not found"); return end; local c=p.character; local subject={player=p.name}; `, luaString(*player))
	} else {
		prefix += fmt.Sprintf(`local p=game.get_entity_by_unit_number(%d); if not p or not p.valid then fail("character not found"); return end; if p.type~="character" then fail("entity is not a character"); return end; local c=p; local subject={character=p.unit_number}; `, *character)
	}
	switch command {
	case "player":
		return prefix + `subject.tick=game.tick; subject.force=p.force.name; subject.surface=p.surface.name; subject.position=p.position; subject.has_character=c~=nil; if c then subject.character=c.unit_number; subject.health=c.health end; if subject.player then subject.connected=p.connected; subject.controller_type=p.controller_type end; ` + luaActorGear + `; rcon.print(helpers.table_to_json(subject))`, nil
	case "inventory":
		return prefix + `local inv=p.get_main_inventory(); if not inv then fail("subject has no main inventory"); return end; local items=inv.get_contents(); subject.tick=game.tick; subject.inventory="main"; subject.items=items; subject.item_entries=#items; rcon.print(helpers.table_to_json(subject))`, nil
	case "queue":
		return prefix + `local queue=p.crafting_queue or {}; subject.tick=game.tick; subject.queue=queue; subject.queue_entries=#queue; subject.queue_size=p.crafting_queue_size; subject.progress=p.crafting_queue_progress; rcon.print(helpers.table_to_json(subject))`, nil
	case "recipe":
		return prefix + fmt.Sprintf(`local recipe=p.force.recipes[%s]; if not recipe then fail("recipe not found"); return end; subject.tick=game.tick; subject.force=p.force.name; subject.name=recipe.name; subject.enabled=recipe.enabled; subject.category=recipe.category; subject.energy=recipe.energy; subject.ingredients=recipe.ingredients; subject.products=recipe.products; subject.hidden_from_player_crafting=recipe.prototype.hidden_from_player_crafting; subject.craftable_count=p.get_craftable_count(recipe.name); rcon.print(helpers.table_to_json(subject))`, luaString(*recipe)), nil
	}
	panic("unreachable")
}
