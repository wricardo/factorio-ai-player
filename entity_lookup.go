package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

type entityByUnitArgs struct {
	UnitNumber string `json:"unit-number"`
	Surface    string `json:"surface"`
}

// entityByUnitNumber returns the basic entity record for a unit number.
func entityByUnitNumber(r *Resolver, unitNumber, surface string) (*Entity, error) {
	return command[Entity](r, "entity", entityByUnitArgs{UnitNumber: unitNumber, Surface: surface})
}

func entityLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("entity", flag.ContinueOnError)
	fs.SetOutput(out)
	unit := fs.Uint64("unit-number", 0, "Entity unit number (required)")
	surface := fs.String("surface", "nauvis", "Surface to scan when the prototype does not support direct lookup")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *unit == 0 || *unit > math.MaxUint32 || *surface == "" {
		return "", errors.New("unit number from 1 to 4294967295 and a surface are required; no positional arguments allowed")
	}
	return fmt.Sprintf(luaFail+`local unit=%d; local e=game.get_entity_by_unit_number(unit)
if not e then
  local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
  for _,candidate in pairs(s.find_entities_filtered{}) do if candidate.unit_number==unit then e=candidate; break end end
end
if not e then fail("entity not found; try another surface or nearby"); return end
local result={name=e.name,type=e.type,position=e.position,surface=e.surface.name,direction=e.direction,unit_number=e.unit_number}; if e.type=="resource" then result.amount=e.amount end; rcon.print(helpers.table_to_json(result))`, *unit, luaString(*surface)), nil
}
