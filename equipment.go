package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

const luaEquipmentActor = `local unit=%d; local actor; if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end else actor=game.get_player(%s); if not actor then fail("player not found"); return end; if not actor.character then fail("player has no character"); return end end; `

func equipLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("equip", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player name (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	item := fs.String("item", "", "Normal-quality item name (required)")
	count := fs.Uint64("count", 1, "Item count")
	inventory := fs.String("inventory", "", "Equipment inventory: guns, ammo, armor, or trash (required)")
	direction := fs.String("direction", "", "Transfer direction: to-equipment or to-main (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || *item == "" || *count < 1 || *count > math.MaxUint32 {
		return "", errors.New("exactly one of player or character, item, and count between 1 and 4294967295 are required")
	}
	if *inventory != "guns" && *inventory != "ammo" && *inventory != "armor" && *inventory != "trash" {
		return "", errors.New("inventory must be guns, ammo, armor, or trash")
	}
	if *direction != "to-equipment" && *direction != "to-main" {
		return "", errors.New("direction must be to-equipment or to-main")
	}
	return fmt.Sprintf(luaFail+luaEquipmentActor+`local key=%s; local names={guns="character_guns",ammo="character_ammo",armor="character_armor",trash="character_trash"}; local inventory_name=names[key]; local equipment=actor.get_inventory(defines.inventory[inventory_name]); if not equipment then fail("equipment inventory not available"); return end; local main=actor.get_main_inventory(); if not main then fail("actor has no main inventory"); return end; local source,destination=main,equipment; if %s=="to-main" then source,destination=equipment,main end; local item=%s; local requested=%d; local available=source.get_item_count({name=item,quality="normal"}); local capacity=destination.get_insertable_count({name=item,quality="normal"}); local take=math.min(requested,available,capacity); local moved=0; if take>0 then local removed=source.remove{name=item,quality="normal",count=take}; if removed>0 then local inserted=destination.insert{name=item,quality="normal",count=removed}; moved=inserted; if inserted<removed then source.insert{name=item,quality="normal",count=removed-inserted} end end end; rcon.print(helpers.table_to_json({inventory=key,direction=%s,item=item,quality="normal",count=requested,available=available,capacity=capacity,moved=moved}))`,
		*character, luaString(*player), luaString(*inventory), luaString(*direction), luaString(*item), *count, luaString(*direction)), nil
}
