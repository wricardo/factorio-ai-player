package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// removeMany mines explicit reachable entities, continuing past failures.
func removeMany(r *Resolver, input RemoveManyInput) (*RemoveManyResult, error) {
	player, character := "", uint64(0)
	if input.Player != nil {
		player = *input.Player
	}
	if input.Character != nil {
		parsed, err := strconv.ParseUint(*input.Character, 10, 32)
		if err != nil || parsed == 0 {
			return nil, errors.New("character must be a unit number from 1 to 4294967295")
		}
		character = parsed
	}
	if (player == "") == (character == 0) {
		return nil, errors.New("exactly one of player or character is required")
	}
	if len(input.Removals) < 1 || len(input.Removals) > 200 {
		return nil, errors.New("removals must contain between 1 and 200 entries")
	}
	var removals strings.Builder
	for i, removal := range input.Removals {
		if removal == nil {
			return nil, fmt.Errorf("removal %d is missing", i+1)
		}
		unitNumber := ""
		if removal.UnitNumber != nil {
			unitNumber = *removal.UnitNumber
		}
		name := ""
		if removal.Entity != nil {
			name = *removal.Entity
		}
		x, y := math.NaN(), math.NaN()
		if removal.X != nil {
			if removal.Y == nil {
				return nil, fmt.Errorf("removal %d needs both x and y, or a unitNumber", i+1)
			}
			x, y = *removal.X, *removal.Y
		}
		unit, err := entityTarget(name, unitNumber, x, y)
		if err != nil {
			return nil, fmt.Errorf("removal %d: %w", i+1, err)
		}
		if i > 0 {
			removals.WriteString(",")
		}
		fmt.Fprintf(&removals, "{unit=%d,name=%s,x=%s,y=%s}", unit, luaString(name), luaCoordinate(x), luaCoordinate(y))
	}
	lua := luaFail + luaEntityResolve + fmt.Sprintf(`local unit=%d; local p; if unit~=0 then p=game.get_entity_by_unit_number(unit); if not p or p.type~="character" then fail("character not found"); return end else p=game.get_player(%s); if not p then fail("player not found"); return end; if p.controller_type~=defines.controllers.character or not p.character then fail("player must control a character"); return end end
local removals={%s}; local surface=p.surface; local removed,failed=0,0; local errors={}
for i,target in ipairs(removals) do
  local e,err=locate_entity(target.unit,target.name,{x=target.x,y=target.y},surface.name,p)
  if not e then failed=failed+1; if #errors<20 then errors[#errors+1]="removal "..i..": "..err end
  elseif e.type=="character" then failed=failed+1; if #errors<20 then errors[#errors+1]="removal "..i..": cannot remove a character" end
  elseif not p.can_reach_entity(e) then failed=failed+1; if #errors<20 then errors[#errors+1]="removal "..i..": entity is out of reach" end
  else
    local mined=p.mine_entity(e,false)
    if not mined and e.valid then failed=failed+1; if #errors<20 then errors[#errors+1]="removal "..i..": entity could not be mined" end
    else removed=removed+1 end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,removed=removed,failed=failed,errors=errors}))`,
		character, luaString(player), removals.String())
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
