package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// removeMany mines explicit reachable entities, continuing past failures.
func removeMany(r *Resolver, input RemoveManyInput) (*RemoveManyResult, error) {
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	if len(input.Targets) < 1 || len(input.Targets) > 200 {
		return nil, errors.New("targets must contain between 1 and 200 entries")
	}
	var removals strings.Builder
	for i, removal := range input.Targets {
		if removal == nil {
			return nil, fmt.Errorf("removal %d is missing", i+1)
		}
		unitNumber := stringValueOr(removal.UnitNumber, "")
		name := stringValueOr(removal.Entity, "")
		x, y := entityRefCoordinates(removal)
		unit, err := entityTarget(name, unitNumber, x, y)
		if err != nil {
			return nil, fmt.Errorf("removal %d: %w", i+1, err)
		}
		if i > 0 {
			removals.WriteString(",")
		}
		fmt.Fprintf(&removals, "{unit=%d,name=%s,x=%s,y=%s,has_position=%t,surface=%s}", unit, luaString(name), luaCoordinate(x), luaCoordinate(y), !math.IsNaN(x) && !math.IsNaN(y), luaString(stringValueOr(removal.Surface, "nauvis")))
	}
	lua := luaFail + luaEntityResolve + fmt.Sprintf(`local unit=%d; local p; if unit~=0 then p=game.get_entity_by_unit_number(unit); if not p or p.type~="character" then fail("character not found"); return end else p=game.get_player(%s); if not p then fail("player not found"); return end; if p.controller_type~=defines.controllers.character or not p.character then fail("player must control a character"); return end end
local removals={%s}; local surface=p.surface; local removed,failed=0,0; local errors={}
local function fail_at(i,code,message,target)
  failed=failed+1
  if #errors<20 then errors[#errors+1]={index=i,code=code,message=message,target={surface=target.surface,entity=target.name~="" and target.name or nil,unit_number=target.unit~=0 and target.unit or nil,position=(target.has_position and {x=target.x,y=target.y} or nil)}} end
end
for i,target in ipairs(removals) do
  local e,err=locate_entity(target.unit,target.name,{x=target.x,y=target.y},target.surface,p)
  if not e then fail_at(i,"LOOKUP",err,target)
  elseif e.type=="character" then fail_at(i,"VALIDATION","cannot remove a character",target)
  elseif %s then fail_at(i,"REACH","entity is out of reach",target)
  else
    local mined=p.mine_entity(e,false)
    if not mined and e.valid then fail_at(i,"ENGINE","entity could not be mined",target)
    else removed=removed+1 end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=p.force.name,removed=removed,failed=failed,errors=errors}))`,
		character, luaString(player), removals.String(), reachViolation("p", "e"))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[RemoveManyResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("removed %d entities, %d failed", result.Removed, result.Failed))
	return result, nil
}
