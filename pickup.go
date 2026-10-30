package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
)

func pickupLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("pickup", flag.ContinueOnError)
	fs.SetOutput(out)
	character := fs.Uint64("character", 0, "Character unit number (required)")
	x := fs.Float64("x", math.NaN(), "Dropped item X coordinate (required)")
	y := fs.Float64("y", math.NaN(), "Dropped item Y coordinate (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *character == 0 || *character > math.MaxUint32 || math.IsNaN(*x) || math.IsInf(*x, 0) || math.IsNaN(*y) || math.IsInf(*y, 0) {
		return "", errors.New("character and finite x and y are required")
	}
	return fmt.Sprintf(luaFail+`
local c=game.get_entity_by_unit_number(%d); if not c or c.type~="character" then fail("character not found"); return end
local position={x=%s,y=%s}; local found=c.surface.find_entities_filtered{type="item-entity",position=position,radius=0.01,limit=2}
if #found~=1 then fail("expected exactly one dropped item at position"); return end
local e=found[1]; if not c.can_reach_entity(e) then fail("dropped item out of reach"); return end
local stack=e.stack; if not stack.valid_for_read then fail("dropped stack is empty"); return end
local name=stack.name; local quality=stack.quality.name; local before=stack.count; local inv=c.get_main_inventory()
for i=1,#inv do if not e.valid or not stack.valid_for_read then break end; inv[i].transfer_stack(stack) end
local remaining=0; if e.valid and stack.valid_for_read then remaining=stack.count end
if e.valid and remaining==0 then e.destroy() end
local moved=before-remaining; if moved==0 then fail("inventory could not accept dropped item"); return end
rcon.print(helpers.table_to_json({item=name,quality=quality,moved=moved,remaining=remaining,position=position}))`, *character, strconv.FormatFloat(*x, 'g', -1, 64), strconv.FormatFloat(*y, 'g', -1, 64)), nil
}
