package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

func craftCancelLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("cancel-craft", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player controlling a character (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	index := fs.Int("index", 1, "One-based crafting queue index (required)")
	count := fs.Int("count", 1, "Number of crafts to cancel")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || *index < 1 || *count < 1 {
		return "", errors.New("exactly one of player or character, a positive queue index, and a positive count are required; no positional arguments allowed")
	}
	return fmt.Sprintf(luaFail+`local unit=%d; local p; if unit~=0 then p=game.get_entity_by_unit_number(unit); if not p or p.type~="character" then fail("character not found"); return end else p=game.get_player(%s); if not p then fail("player not found"); return end; if p.controller_type~=defines.controllers.character or not p.character then fail("player must control a character"); return end end; local index,count=%d,%d; local size=p.crafting_queue_size; if size==0 then fail("crafting queue is empty"); return end; if index>size then fail("crafting queue index out of range"); return end; p.cancel_crafting{index=index,count=count}; local queue=p.crafting_queue or {}; rcon.print(helpers.table_to_json({tick=game.tick,queue=queue,queue_entries=#queue,queue_size=p.crafting_queue_size,progress=p.crafting_queue_progress}))`, *character, luaString(*player), *index, *count), nil
}
