package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

// A detached character is an actual Factorio character entity. Its unit number
// is the explicit handle used by the other commands and survives save/reload.
func characterLua(command string, args []string, out io.Writer) (string, error) {
	if command == "player-restore-character" {
		return playerRestoreCharacterLua(args, out)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Disconnected player whose main inventory is transferred (required)")
	var unit *uint64
	if command == "character-return" {
		unit = fs.Uint64("character", 0, "Detached character unit number (required)")
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *player == "" || fs.NArg() != 0 || (unit != nil && (*unit == 0 || *unit > math.MaxUint32)) {
		return "", errors.New("player and any required character are required; no positional arguments allowed")
	}
	prefix := fmt.Sprintf(luaFail+`local p=game.get_player(%s); if not p then fail("player not found"); return end; if p.connected then fail("player must be disconnected"); return end; if p.crafting_queue_size>0 then fail("player crafting queue must be empty"); return end; local pinv=p.get_main_inventory(); if not pinv then fail("player has no main inventory"); return end; `, luaString(*player))
	if command == "character-create" {
		return prefix + `local s=p.surface; local pos=s.find_non_colliding_position("character",p.position,5,0.5); if not pos then fail("no space for character near player"); return end; local c=s.create_entity{name="character",position=pos,force=p.force}; if not c then fail("could not create character"); return end; local inv=c.get_main_inventory(); if #inv<#pinv then c.destroy(); fail("character inventory has fewer slots than player inventory"); return end; local moved={}; for i=1,#pinv do if pinv[i].valid_for_read then if not inv[i].swap_stack(pinv[i]) then for _,j in ipairs(moved) do inv[j].swap_stack(pinv[j]) end; c.destroy(); fail("inventory transfer failed; items returned"); return end; moved[#moved+1]=i end end; rcon.print(helpers.table_to_json({character=c.unit_number,player=p.name,surface=s.name,position=c.position,transferred_stacks=#moved}))`, nil
	}
	return prefix + fmt.Sprintf(`local c=game.get_entity_by_unit_number(%d); if not c or c.type~="character" or c.player then fail("detached character not found"); return end; if c.force~=p.force then fail("character and player forces differ"); return end; if c.crafting_queue_size>0 then fail("character crafting queue must be empty"); return end; for _,key in ipairs({"character_guns","character_ammo","character_armor","character_trash"}) do local other=c.get_inventory(defines.inventory[key]); if other and not other.is_empty() then fail("character has items outside main inventory"); return end end; local inv=c.get_main_inventory(); local slots={}; for i=1,#pinv do if not pinv[i].valid_for_read then slots[#slots+1]=i end end; local needed=0; for i=1,#inv do if inv[i].valid_for_read then needed=needed+1 end end; if #slots<needed then fail("player inventory has insufficient empty slots"); return end; local moved={}; for i=1,#inv do if inv[i].valid_for_read then local j=slots[#moved+1]; if not pinv[j].swap_stack(inv[i]) then for _,pair in ipairs(moved) do pinv[pair[2]].swap_stack(inv[pair[1]]) end; fail("inventory transfer failed; items returned to character"); return end; moved[#moved+1]={i,j} end end; local id=c.unit_number; c.destroy(); rcon.print(helpers.table_to_json({character=id,player=p.name,returned_stacks=#moved}))`, *unit), nil
}

// playerRestoreCharacterLua creates a character for a connected player that
// controls none, for example a sandbox god controller. The player's main
// inventory is left untouched, so player-addressed builds keep using it.
func playerRestoreCharacterLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("player-restore-character", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Connected player without a character (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *player == "" {
		return "", errors.New("player is required; no positional arguments allowed")
	}
	return fmt.Sprintf(luaFail+`local p=game.get_player(%s); if not p then fail("player not found"); return end; if not p.connected then fail("player must be connected"); return end; if p.character then fail("player already controls a character"); return end; local created=p.create_character(); local c=p.character; if not created or not c then fail("could not create character for player"); return end; rcon.print(helpers.table_to_json({tick=game.tick,player=p.name,character=c.unit_number,surface=c.surface.name,position=c.position}))`, luaString(*player)), nil
}
