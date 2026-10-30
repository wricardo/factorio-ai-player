package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

// beltTakeLua moves normal-quality items from a reachable belt into the acting
// character's inventory.
func beltTakeLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("belt-take", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player name (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Belt unit number, alternative to x and y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Belt unit number, alternative to x and y")
	x := fs.Float64("x", math.NaN(), "Belt center X (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Belt center Y (required unless a unit number is given)")
	item := fs.String("item", "", "Normal-quality item to pick up")
	count := fs.Uint64("count", 1, "Maximum items to pick up")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || *item == "" || *count == 0 || *count > math.MaxUint32 {
		return "", errors.New("exactly one of player or character, item, and positive count are required")
	}
	// beltTake only accepts transport belts, so unit-number lookups scan for that prototype.
	targetUnit, err := entityTarget("transport-belt", unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	prefix := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local c
if unit~=0 then
  c=game.get_entity_by_unit_number(unit); if not c or c.type~="character" then fail("character not found"); return end
else
  local pl=game.get_player(player); if not pl then fail("player not found"); return end
  c=pl.character; if not c then fail("player has no character"); return end
end
local p=c
`, luaString(*player), *character)
	prefix += targetLocals(targetUnit, "transport-belt", *x, *y, "") + "surface_name=c.surface.name\n" + luaEntityResolve + resolveTargetLua()
	return prefix + fmt.Sprintf(`if e.type~="transport-belt" then fail("entity is not a transport belt"); return end
if not c.can_reach_entity(e) then fail("belt is out of reach"); return end
local inv=c.get_main_inventory(); if not inv then fail("character has no main inventory"); return end
local name=%s; local requested=%d; local capacity=inv.get_insertable_count{name=name,quality="normal"}; local remaining=math.min(requested,capacity); local moved=0
for i=1,2 do if remaining>0 then local removed=e.get_transport_line(i).remove_item{name=name,quality="normal",count=remaining}; if removed>0 then local inserted=inv.insert{name=name,quality="normal",count=removed}; moved=moved+inserted; remaining=remaining-removed; if inserted<removed then c.surface.spill_item_stack{position=c.position,stack={name=name,quality="normal",count=removed-inserted},enable_looted=true,force=c.force}; fail("inventory rejected items; remainder spilled near character"); return end end end end
rcon.print(helpers.table_to_json({position=e.position,item=name,requested=requested,moved=moved,unit_number=e.unit_number}))`, luaString(*item), *count), nil

}
