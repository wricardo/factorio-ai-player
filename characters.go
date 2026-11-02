package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
)

type charactersArgs struct {
	Surface      string `json:"surface"`
	DetachedOnly bool   `json:"detached-only"`
	Limit        int    `json:"limit"`
	Offset       int    `json:"offset"`
}

// charactersQuery pages every character on a surface; the scan is not bounded
// by the area bound of the entities query.
func charactersQuery(r *Resolver, input *CharactersInput) (*CharacterPage, error) {
	args := charactersArgs{Surface: "nauvis", DetachedOnly: true, Limit: 100}
	if input != nil {
		args.Surface = input.Surface
		args.Limit = input.Limit
		args.Offset = input.Offset
		if input.DetachedOnly != nil {
			args.DetachedOnly = *input.DetachedOnly
		}
	}
	if args.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	if args.Limit < 1 || args.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	if args.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	return command[CharacterPage](r, "characters", args)
}

func characterSpawnLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("character-spawn", flag.ContinueOnError)
	fs.SetOutput(out)
	surface := fs.String("surface", "nauvis", "Surface name")
	x := fs.Float64("x", 0, "X coordinate")
	y := fs.Float64("y", 0, "Y coordinate")
	force := fs.String("force", "player", "Force name")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *surface == "" || *force == "" || math.IsNaN(*x) || math.IsInf(*x, 0) || math.IsNaN(*y) || math.IsInf(*y, 0) {
		return "", errors.New("surface, force, and finite x and y are required")
	}
	return luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local f=game.forces[%s]; if not f then fail("force not found"); return end
local pos=s.find_non_colliding_position("character",{x=%s,y=%s},10,0.5); if not pos then fail("no space for character near that position"); return end
local c=s.create_entity{name="character",position=pos,force=f}; if not c then fail("could not create character"); return end
rcon.print(helpers.table_to_json({tick=game.tick,character=c.unit_number,surface=s.name,force=f.name,position=c.position}))`,
		luaString(*surface), luaString(*force),
		strconv.FormatFloat(*x, 'g', -1, 64), strconv.FormatFloat(*y, 'g', -1, 64)), nil
}

// characterDeleteLua destroys one detached character. It refuses to destroy
// anything that could lose items: the character must belong to no player
// (including a disconnected player's stored character), must match the
// explicitly requested force, and must have empty crafting queue, main
// inventory, and equipment/trash inventories. Items are never voided because a
// nonempty character is rejected instead of being emptied or spilled.
func characterDeleteLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("character-delete", flag.ContinueOnError)
	fs.SetOutput(out)
	force := fs.String("force", "", "Force the character must belong to (required)")
	unit := fs.Uint64("character", 0, "Detached character unit number (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *force == "" || *unit == 0 || *unit > math.MaxUint32 {
		return "", errors.New("force and character are required; no positional arguments allowed")
	}
	return luaFail + fmt.Sprintf(`local f=game.forces[%s]; if not f then fail("force not found"); return end
local c=game.get_entity_by_unit_number(%d); if not c or not c.valid or c.type~="character" then fail("character not found"); return end
if c.player then fail("character is controlled by a player"); return end
for _,q in pairs(game.players) do if q.character==c then fail("character belongs to player "..q.name); return end end
if c.force~=f then fail("character force does not match requested force"); return end
if c.crafting_queue_size>0 then fail("character crafting queue must be empty"); return end
for _,key in ipairs({"character_guns","character_ammo","character_armor","character_trash"}) do local other=c.get_inventory(defines.inventory[key]); if other and not other.is_empty() then fail("character has items outside main inventory"); return end end
local inv=c.get_main_inventory(); if inv and not inv.is_empty() then fail("character main inventory is not empty; empty it before deleting"); return end
local id, surface, force, position=c.unit_number, c.surface.name, c.force.name, c.position
c.destroy()
rcon.print(helpers.table_to_json({tick=game.tick, character=id, surface=surface, force=force, position=position}))`, luaString(*force), *unit), nil
}

func charactersLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("characters", flag.ContinueOnError)
	fs.SetOutput(out)
	surface := fs.String("surface", "nauvis", "Surface name")
	detachedOnly := fs.Bool("detached-only", true, "Only characters without a controlling player")
	limit := fs.Int("limit", 100, "Page size")
	offset := fs.Int("offset", 0, "Page offset")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *surface == "" {
		return "", errors.New("surface is required and no positional arguments are allowed")
	}
	if *limit < 1 || *limit > maxPageSize {
		return "", pageLimitError("limit")
	}
	if *offset < 0 {
		return "", errors.New("offset must be non-negative")
	}
	keep := "true"
	if *detachedOnly {
		keep = "e.player==nil"
	}
	return luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local found={}
for _,e in pairs(s.find_entities_filtered{type="character"}) do if %s then found[#found+1]=e end end
table.sort(found,function(a,b) return a.unit_number<b.unit_number end)
local page={}
for i=%d+1, math.min(#found, %d+%d) do
  local e=found[i]
  page[#page+1]={unit_number=e.unit_number,position=e.position,surface=s.name,force=e.force.name,health=e.health,player=e.player and e.player.name or nil}
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,page_info={total=#found,limit=%d,offset=%d,has_more=#found>%d},characters=page}))`,
		luaString(*surface), keep, *offset, *offset, *limit,
		*limit, *offset, *offset+*limit), nil
}
