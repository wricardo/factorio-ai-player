package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

// resourceLua mines one selected resource, tree, or rock with the engine's
// player mining operation. This operation is instantaneous; it does not
// simulate the time or input stream of manual mining.
func resourceLua(command string, args []string, out io.Writer) (string, error) {
	if command != "mine" {
		return "", fmt.Errorf("unknown resource command %q", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player controlling a character (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	entity := fs.String("entity", "", "Resource, tree, or rock entity name (required)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Rejected: resources, trees and rocks have no unit numbers")
	fs.StringVar(&unitNumber, "unitNumber", "", "Rejected: resources, trees and rocks have no unit numbers")
	surface := fs.String("surface", "", "Target surface name (defaults to the actor's surface)")
	x := fs.Float64("x", math.NaN(), "Entity center X (required)")
	y := fs.Float64("y", math.NaN(), "Entity center Y (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 {
		return "", errors.New("exactly one of player or character (unit number 1 to 4294967295) is required; no positional arguments allowed")
	}
	if unitNumber != "" {
		return "", errors.New("mine targets have no unit numbers; pass entity with x and y")
	}
	if _, err := entityTarget(*entity, "", *x, *y); err != nil {
		return "", err
	}
	actor := fmt.Sprintf(luaFail+`local player=%s; local unit=%d; local actor; if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character unit not found"); return end else actor=game.get_player(player); if not actor then fail("player not found"); return end; if actor.controller_type~=defines.controllers.character or not actor.character then fail("player must control a character"); return end end; local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end; local p=actor;
`, luaString(*player), *character)
	// Resources, trees and rocks carry no unit number, so mine always addresses
	// the entity by prototype name plus position; there is no unit-number scan
	// to fall back to. The entity is resolved by the named point, so a rock that
	// is off the tile grid is still addressable, and it may be neutral force.
	resolve := targetLocals(0, *entity, *x, *y, *surface) + `if surface_name=="" then surface_name=actor.surface.name end
` + luaEntityResolve + resolveTargetNeutralLua() + `if e.surface~=actor.surface then fail("entity is on another surface than the actor"); return end
if e.type~="resource" and e.type~="tree" and e.type~="simple-entity" then fail("entity is not a resource, tree, or rock"); return end
if ` + reachViolation("actor", "e") + ` then fail("entity is out of reach"); return end
` + luaHarvestStumpCleanup + inventoryItemHelpersLua + `local name=e.name; local kind=e.type; local pos=e.position; local surface=s.name
local tree_box=(kind=="tree") and e.bounding_box or nil
local amount_before=(kind=="resource" and e.amount) or nil
local items_before=inventory_item_counts(inv)
local removed=actor.mine_entity(e,false)
-- mine_entity's return value alone is not proof of a state change: report the
-- mine only when the entity actually left the world or the resource shrank.
local state_changed=(not e.valid) or (amount_before~=nil and (e.amount or 0)<amount_before)
if not state_changed then fail("entity is still present after mining"..(removed and " even though the engine reported success" or "").."; it may not be minable by this actor or its inventory may be full"); return end
-- A mined tree leaves a non-mineable stump corpse. It does not block
-- placement (corpses collide with tiles only), but clear it here as well so a
-- harvested route holds no debris.
if kind=="tree" then clear_tree_stump(s,tree_box) end
local remaining=(e.valid and kind=="resource") and e.amount or nil
rcon.print(helpers.table_to_json({player=player,character=unit,surface=surface,entity=name,type=kind,position=pos,mined=true,instant=true,remaining=remaining,items=inventory_item_difference(items_before,inv)}))`
	return actor + resolve, nil
}

// removeLua mines one reachable entity so the actor recovers it and its
// contents. Entities that cannot be mined, including characters, fail.
func removeLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player controlling a character (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	entity := fs.String("entity", "", "Entity prototype name (required unless a unit number is given)")
	surface := fs.String("surface", "", "Target surface name (defaults to the actor's surface)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Target entity unit number, alternative to entity and x/y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Target entity unit number, alternative to entity and x/y")
	x := fs.Float64("x", math.NaN(), "Entity center X on the actor's surface (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Entity center Y on the actor's surface (required unless a unit number is given)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 {
		return "", errors.New("exactly one of player or character (unit number 1 to 4294967295) is required; no positional arguments allowed")
	}
	targetUnit, err := entityTarget(*entity, unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	lua := fmt.Sprintf(luaFail+`local player=%s; local unit=%d; local actor; if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character unit not found"); return end else actor=game.get_player(player); if not actor then fail("player not found"); return end; if actor.controller_type~=defines.controllers.character or not actor.character then fail("player must control a character"); return end end; local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end; local p=actor; `, luaString(*player), *character) +
		targetLocals(targetUnit, *entity, *x, *y, *surface) + `if surface_name=="" then surface_name=actor.surface.name end
` + luaEntityResolve + resolveTargetNeutralLua() + `if e.type=="character" then fail("cannot remove a character"); return end; if e.surface~=actor.surface then fail("entity is on another surface than the actor"); return end; if ` + reachViolation("actor", "e") + ` then fail("entity is out of reach"); return end; local name=e.name; local kind=e.type; local pos=e.position; local surface=s.name
`
	return lua + luaHarvestStumpCleanup + inventoryItemHelpersLua + `
local before=inventory_item_counts(inv)
local tree_box=(kind=="tree") and e.bounding_box or nil
local amount_before=(kind=="resource" and e.amount) or nil
local removed
if kind=="corpse" then
  -- A corpse (a tree stump left by Factorio 2.0 tree mining, or a remnant) is
  -- not mineable, but destroy removes it; it carries no items, so nothing is
  -- lost and the tile becomes placeable again.
  removed=e.destroy{raise_destroy=true}
else
  removed=actor.mine_entity(e,false)
end
-- The engine can report success without changing the world, so the entity
-- leaving (or a resource shrinking) is what proves the removal.
local state_changed=(not e.valid) or (amount_before~=nil and (e.amount or 0)<amount_before)
if not state_changed then fail("entity is still present after removal"..(removed and " even though the engine reported success" or "").."; it may not be minable"); return end
-- A mined tree leaves a stump corpse of its own; clear it so the removal
-- leaves a placeable tile.
if kind=="tree" then clear_tree_stump(s,tree_box) end
local items=inventory_item_difference(before,inv)
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface,entity=name,type=kind,position=pos,items=items}))`, nil
}
