package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// luaEntityResolve finds the entity an operation addresses. Callers pass a unit
// number (0 to address by coordinates), the prototype name (optional with a
// unit number, required without one), the coordinates used only for coordinate
// lookups, the surface to search, and the acting actor `p` for its force.
//
// Only prototypes flagged get-by-unit-number (characters, vehicles, some hubs)
// resolve directly; everything else is found by scanning the surface for that
// prototype name and comparing unit numbers.
const luaEntityResolve = `local function locate_entity(unit,name,pos,surface_name,p)
  local e,s
  if unit~=0 then
    e=game.get_entity_by_unit_number(unit)
    if not e then
      if name=="" then return nil,"prototype is not resolvable by unit number alone; pass entity too" end
      s=game.surfaces[surface_name]; if not s then return nil,"surface not found" end
      for _,candidate in pairs(s.find_entities_filtered{name=name}) do
        if candidate.unit_number==unit then e=candidate; break end
      end
      if not e then return nil,"entity not found by unit number on that surface" end
    end
    if e.force~=p.force then return nil,"entity is not on the actor's force" end
    if name~="" and e.name~=name then return nil,"entity name does not match unit number" end
  else
    if name=="" then return nil,"entity name or unit number is required" end
    s=game.surfaces[surface_name]; if not s then return nil,"surface not found" end
    local found=s.find_entities_filtered{name=name,position=pos,radius=0.01,force=p.force}
    for _,candidate in pairs(found) do
      if math.abs(candidate.position.x-pos.x)<0.001 and math.abs(candidate.position.y-pos.y)<0.001 then
        if e then return nil,"multiple matching entities at center" end
        e=candidate
      end
    end
    if not e then return nil,"entity not found at center on actor's force" end
  end
  return e
end
local function resolve_entity(unit,name,pos,surface_name,p)
  local e,err=locate_entity(unit,name,pos,surface_name,p)
  if not e then fail(err); return nil end
  return e
end
`

// entityTarget validates the two ways to address an entity and returns the unit
// number to emit; zero means the caller addresses by name and coordinates.
func entityTarget(name, unitNumber string, x, y float64) (uint64, error) {
	hasCoordinates := !math.IsNaN(x) || !math.IsNaN(y)
	if unitNumber != "" {
		parsed, err := strconv.ParseUint(unitNumber, 10, 32)
		if err != nil || parsed == 0 {
			return 0, errors.New("unitNumber must be an entity unit number from 1 to 4294967295")
		}
		if hasCoordinates {
			return 0, errors.New("address the entity by unitNumber or by entity with x and y, not both")
		}
		return parsed, nil
	}
	if name == "" {
		return 0, errors.New("entity name or unitNumber is required")
	}
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return 0, errors.New("entity x and y must be finite when addressing by name")
	}
	return 0, nil
}

// targetLocals emits the Lua locals consumed by luaEntityResolve helpers.
func targetLocals(unit uint64, name string, x, y float64, surface string) string {
	return fmt.Sprintf(`local target_unit=%d
local target_name=%s
local target_position={x=%s,y=%s}
local surface_name=%s
`, unit, luaString(name), luaCoordinate(x), luaCoordinate(y), luaString(surface))
}

// resolveTargetLua resolves the target into `e` (and returns early on failure).
func resolveTargetLua() string {
	return `local e=resolve_entity(target_unit,target_name,target_position,surface_name,p); if not e then return end
local s=e.surface
`
}

func luaCoordinate(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "0"
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// resolveBeltAddress returns the surface and position of an entity addressed
// either by unit number or by coordinates. It is the Go-side counterpart of
// luaEntityResolve for queries that call the navigation mod, which only accepts
// tile coordinates.
func resolveBeltAddress(r *Resolver, surface string, unitNumber *string, x, y *float64) (string, float64, float64, error) {
	if unitNumber != nil && *unitNumber != "" {
		if x != nil || y != nil {
			return "", 0, 0, errors.New("address the entity by unitNumber or by x and y, not both")
		}
		if _, err := strconv.ParseUint(*unitNumber, 10, 32); err != nil {
			return "", 0, 0, errors.New("unitNumber must be an entity unit number from 1 to 4294967295")
		}
		entity, err := entityByUnitNumber(r, *unitNumber, surface)
		if err != nil {
			return "", 0, 0, err
		}
		if entity.Surface == nil || entity.Position == nil {
			return "", 0, 0, errors.New("entity has no surface or position")
		}
		return *entity.Surface, entity.Position.X, entity.Position.Y, nil
	}
	if x == nil || y == nil {
		return "", 0, 0, errors.New("belt x and y, or unitNumber, are required")
	}
	return surface, *x, *y, nil
}
