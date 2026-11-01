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
	entity := fs.String("entity", "", "Resource, tree, or rock entity name (required unless a unit number is given)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Target entity unit number, alternative to entity and x/y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Target entity unit number, alternative to entity and x/y")
	surface := fs.String("surface", "", "Target surface name (defaults to the actor's surface)")
	x := fs.Float64("x", math.NaN(), "Entity center X (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Entity center Y (required unless a unit number is given)")
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
	actor := fmt.Sprintf(luaFail+`local player=%s; local unit=%d; local actor; if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character unit not found"); return end else actor=game.get_player(player); if not actor then fail("player not found"); return end; if actor.controller_type~=defines.controllers.character or not actor.character then fail("player must control a character"); return end end; local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end; local p=actor; local s=actor.surface; local surface_name=%s; if surface_name~="" then s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end end
`, luaString(*player), *character, luaString(*surface))
	resolve := targetLocals(targetUnit, *entity, *x, *y, "") + `local e
if target_unit~=0 then
  e=game.get_entity_by_unit_number(target_unit)
  if not e then
    -- Resources, trees and rocks usually lack the get-by-unit-number flag.
    for _,candidate in pairs(s.find_entities_filtered{name=(target_name~="" and target_name or nil)}) do
      if candidate.unit_number==target_unit then e=candidate; break end
    end
  end
  if not e then fail("entity not found by unit number on that surface"); return end
  if target_name~="" and e.name~=target_name then fail("entity name does not match unit number"); return end
else
  local found=s.find_entities_filtered{name=target_name,position=target_position,radius=0.01,limit=2}
  if #found~=1 then fail("expected exactly one matching entity at position on target surface"); return end
  e=found[1]
end
if e.type~="resource" and e.type~="tree" and e.type~="simple-entity" then fail("entity is not a resource, tree, or rock"); return end
if e.surface~=actor.surface then fail("entity is on another surface than the actor"); return end
if ` + reachViolation("actor", "e") + ` then fail("entity is out of reach"); return end
local name=e.name; local kind=e.type; local pos=e.position; local surface=s.name; local before=kind=="resource" and e.amount or nil; local items_before=inv.get_item_count(); local removed=actor.mine_entity(e, false); local remaining=e.valid and kind=="resource" and e.amount or nil; if not removed and not (before and remaining and remaining<before) and inv.get_item_count()<=items_before then fail("mining failed; entity may not be minable or inventory may be full"); return end; rcon.print(helpers.table_to_json({player=player,character=unit,surface=surface,entity=name,type=kind,position=pos,mined=true,instant=true,remaining=remaining}))`
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
` + luaEntityResolve + resolveTargetLua() + `if e.type=="character" then fail("cannot remove a character"); return end; if e.surface~=actor.surface then fail("entity is on another surface than the actor"); return end; if ` + reachViolation("actor", "e") + ` then fail("entity is out of reach"); return end; local name=e.name; local kind=e.type; local pos=e.position; local surface=s.name`
	return lua + inventoryItemHelpersLua + `
local before=inventory_item_counts(inv)
local removed=actor.mine_entity(e,false)
if not removed and e.valid then fail("entity could not be mined; it may not be minable"); return end
local items=inventory_item_difference(before,inv)
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface,entity=name,type=kind,position=pos,items=items}))`, nil
}
