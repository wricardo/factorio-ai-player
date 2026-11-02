package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// luaUnitNumberEntityTypes lists the entity types that never carry a unit
// number plus the unit-number lookup used when the caller does not know the
// prototype name. Scanning inverts this filter instead of walking the whole
// surface, which would otherwise build a table of every tree, rock, resource
// tile, item-on-ground and cliff just to match one number. The per-surface
// index is chunk-local, so a batch that addresses many unit numbers pays for
// one scan per surface instead of one per target.
const luaUnitNumberEntityTypes = `local lua_unit_number_excluded_types={"tree","simple-entity","resource","item-entity","fish","cliff"}
local lua_unit_number_cache={}
local function find_entity_by_unit_number(surface,unit,name)
  if name~="" then
    for _,candidate in pairs(surface.find_entities_filtered{name=name}) do
      if candidate.unit_number==unit then return candidate end
    end
    return nil
  end
  local cache=lua_unit_number_cache[surface.name]
  if not cache then
    cache={}
    for _,candidate in pairs(surface.find_entities_filtered{type=lua_unit_number_excluded_types,invert=true}) do
      if candidate.unit_number then cache[candidate.unit_number]=candidate end
    end
    lua_unit_number_cache[surface.name]=cache
  end
  return cache[unit]
end
`

// luaEntityResolve finds the entity an operation addresses. Callers pass a unit
// number (0 to address by coordinates), the prototype name (optional with a
// unit number, required without one), the coordinates used only for coordinate
// lookups, the surface to search, and the acting actor `p` for its force.
//
// Only prototypes flagged get-by-unit-number (characters, vehicles, some hubs)
// resolve directly; everything else is found by scanning the surface for that
// prototype name and comparing unit numbers, or, when no name is known, by
// scanning the entities that can carry a unit number at all.
//
// require_force defaults to true and is what every mutation needs: it restricts
// coordinate lookups to the actor's force and rejects a unit-number hit on
// another force. Read-only callers pass false so resources, trees, item stacks
// and enemy entities can be inspected too.
//
// has_position defaults to false. When true, a unit number is first matched
// against entities of that name at the given center, which turns a full-surface
// scan into a one-tile lookup for callers that already know the position.
//
// allow_neutral defaults to false. When true, a coordinate lookup also accepts
// a neutral-force entity, which is what rocks, trees and resource tiles are.
// Everything else still requires the actor's force.
//
// Coordinate lookups do not require the entity's centre to equal the named
// point: a candidate is kept when the point falls inside its bounding box, so a
// rock or resource tile that does not sit on the tile grid is still addressable
// by the tile the caller names. An exact-centre hit wins over a box-only hit.
const luaEntityResolve = luaUnitNumberEntityTypes + `local function locate_entity(unit,name,pos,surface_name,p,require_force,has_position,allow_neutral)
  if require_force==nil then require_force=true end
  local e,s
  if unit~=0 then
    e=game.get_entity_by_unit_number(unit)
    if not e and name~="" and has_position then
      s=game.surfaces[surface_name]; if not s then return nil,"surface not found" end
      for _,candidate in pairs(s.find_entities_filtered{name=name,position=pos,radius=0.01}) do
        if candidate.unit_number==unit and math.abs(candidate.position.x-pos.x)<0.001 and math.abs(candidate.position.y-pos.y)<0.001 then e=candidate; break end
      end
    end
    if not e then
      s=game.surfaces[surface_name]; if not s then return nil,"surface not found" end
      e=find_entity_by_unit_number(s,unit,name)
      if not e then return nil,"entity not found by unit number on that surface" end
    end
    if require_force and e.force~=p.force then return nil,"entity is not on the actor's force" end
    if name~="" and e.name~=name then return nil,"entity name does not match unit number" end
  else
    if name=="" then return nil,"entity name or unit number is required" end
    s=game.surfaces[surface_name]; if not s then return nil,"surface not found" end
    local matches={}
    local foreign=false
    for _,candidate in pairs(s.find_entities_filtered{name=name,position=pos,radius=0.5}) do
      local centred=math.abs(candidate.position.x-pos.x)<0.001 and math.abs(candidate.position.y-pos.y)<0.001
      local covers=false
      local ok,box=pcall(function() return candidate.bounding_box end)
      if ok and box then
        covers=pos.x>=box.left_top.x-0.001 and pos.y>=box.left_top.y-0.001 and pos.x<=box.right_bottom.x+0.001 and pos.y<=box.right_bottom.y+0.001
      end
      if centred or covers then
        local same_force=true
        if require_force then
          same_force=(candidate.force==p.force) or (allow_neutral and candidate.force.name=="neutral")
        end
        if not same_force then
          foreign=true
        elseif centred then
          if e then return nil,"multiple matching entities at position" end
          e=candidate
        else
          matches[#matches+1]=candidate
        end
      end
    end
    if not e then
      if #matches>1 then return nil,"multiple matching entities at position" end
      e=matches[1]
    end
    if not e then
      if foreign then return nil,"entity not found at center on actor's force" end
      return nil,"no addressable "..name.." at the requested position on that surface"
    end
  end
  return e
end
local function resolve_entity(unit,name,pos,surface_name,p,require_force,has_position,allow_neutral)
  local e,err=locate_entity(unit,name,pos,surface_name,p,require_force,has_position,allow_neutral)
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
// target_position_valid tells locate_entity whether the coordinates are a real
// hint or the 0,0 placeholder used for unit-number-only addressing.
func targetLocals(unit uint64, name string, x, y float64, surface string) string {
	valid := "false"
	if !math.IsNaN(x) && !math.IsNaN(y) {
		valid = "true"
	}
	return fmt.Sprintf(`local target_unit=%d
local target_name=%s
local target_position={x=%s,y=%s}
local target_position_valid=%s
local surface_name=%s
`, unit, luaString(name), luaCoordinate(x), luaCoordinate(y), valid, luaString(surface))
}

// resolveTargetLua resolves the target into `e` (and returns early on failure).
// It keeps the actor-force check that every mutation relies on.
func resolveTargetLua() string {
	return `local e=resolve_entity(target_unit,target_name,target_position,surface_name,p,true,target_position_valid); if not e then return end
local s=e.surface
`
}

// resolveTargetNeutralLua resolves the target into `e` for mutations that may
// mine neutral-force obstacles such as rocks, trees and resource tiles. The
// actor's force is still required; any other force is rejected.
func resolveTargetNeutralLua() string {
	return `local e=resolve_entity(target_unit,target_name,target_position,surface_name,p,true,target_position_valid,true); if not e then return end
local s=e.surface
`
}

// resolveTargetAnyForceLua resolves the target into `e` without restricting it
func resolveTargetAnyForceLua() string {
	return `local e=resolve_entity(target_unit,target_name,target_position,surface_name,p,false,target_position_valid); if not e then return end
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
