package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

func entityByRef(r *Resolver, input EntityRefInput) (*Entity, error) {
	return command[Entity](r, "entity", input)
}

func entityByUnitNumber(r *Resolver, unitNumber, surface string) (*Entity, error) {
	return entityByRef(r, EntityRefInput{UnitNumber: &unitNumber, Surface: &surface})
}

// entityLua reads one entity by unit number or by name and center position.
// The unit-number fallback excludes entity types that never carry a unit
// number, so the scan does not build a table of every tree, rock, resource and
// item-on-ground on the surface.
func entityLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("entity", flag.ContinueOnError)
	fs.SetOutput(out)
	unitNumber := fs.String("unitNumber", "", "Entity unit number, alternative to entity and x/y")
	entity := fs.String("entity", "", "Entity prototype name")
	surface := fs.String("surface", "nauvis", "Surface to scan")
	x := fs.Float64("x", math.NaN(), "Entity center x")
	y := fs.Float64("y", math.NaN(), "Entity center y")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *surface == "" {
		return "", errors.New("surface and no positional arguments are required")
	}
	unit, err := entityTarget(*entity, *unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(luaFail+luaUnitNumberEntityTypes+`local unit,name,surface_name=%d,%s,%s
local position={x=%s,y=%s}
local e
if unit~=0 then
  e=game.get_entity_by_unit_number(unit)
  if not e then
    local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
    e=find_entity_by_unit_number(s,unit,name)
  end
  if e and name~="" and e.name~=name then fail("entity name does not match unit number"); return end
else
  local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
  for _,candidate in pairs(s.find_entities_filtered{name=name,position=position,radius=0.01}) do
    if math.abs(candidate.position.x-position.x)<0.001 and math.abs(candidate.position.y-position.y)<0.001 then
      if e then fail("multiple matching entities at center"); return end
      e=candidate
    end
  end
end
if not e then fail("entity not found"); return end
local result={tick=game.tick,name=e.name,type=e.type,position=e.position,surface=e.surface.name,force=e.force and e.force.name or nil,direction=e.direction,unit_number=e.unit_number}; if e.type=="resource" then result.amount=e.amount end; rcon.print(helpers.table_to_json(result))`,
		unit, luaString(*entity), luaString(*surface), luaCoordinate(*x), luaCoordinate(*y)), nil
}
