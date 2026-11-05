package main

import (
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
	if len(input.Targets) < 1 || len(input.Targets) > maxBatchTargets {
		return nil, fmt.Errorf("targets must contain between 1 and %d entries", maxBatchTargets)
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
	lua := luaFail + luaHarvestStumpCleanup + luaEntityResolve + luaBatchEntityIndexLua + inventoryItemHelpersLua + fmt.Sprintf(`local unit=%d; local p; if unit~=0 then p=game.get_entity_by_unit_number(unit); if not p or p.type~="character" then fail("character not found"); return end else p=game.get_player(%s); if not p then fail("player not found"); return end; if p.controller_type~=defines.controllers.character or not p.character then fail("player must control a character"); return end end
local inv=p.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local void_mode=%t
local removals={%s}; local surface=p.surface; local removed,failed=0,0; local errors={}; local results={}
local function fail_at(i,code,message,target)
  failed=failed+1
  if #errors<20 then errors[#errors+1]={index=i,code=code,message=message,target={surface=target.surface,entity=target.name~="" and target.name or nil,unit_number=target.unit~=0 and target.unit or nil,position=(target.has_position and {x=target.x,y=target.y} or nil)}} end
end
-- The inventory diff per target is what the single remove mutation reports as
-- items, and the aggregate diff across the batch is the net gain.
local before_all=inventory_item_counts(inv)
for i,target in ipairs(removals) do
  local e,err
  if target.unit==0 then
    -- A target without a unit number is addressed by name plus position; rocks,
    -- trees and resource tiles are neutral force, so neutral is allowed too.
    e,err=locate_entity(0,target.name,{x=target.x,y=target.y},target.surface,p,true,target.has_position,true)
  else
    e,err=find_batch_entity(target)
    if not e then err=err or "entity not found by unit number on that surface" end
  end
  if not e then fail_at(i,"LOOKUP",err or "entity not found on that surface",target)
  elseif e.force~=p.force and e.force.name~="neutral" then fail_at(i,"LOOKUP","entity is not on the actor's force",target)
  elseif target.unit~=0 and target.name~="" and e.name~=target.name then fail_at(i,"LOOKUP","entity name does not match unit number",target)
  elseif e.type=="character" then fail_at(i,"VALIDATION","cannot remove a character",target)
  elseif (not void_mode) and (%s) then fail_at(i,"REACH","entity is out of reach",target)
  else
    local name=e.name; local kind=e.type; local pos={x=e.position.x,y=e.position.y}; local entity_unit=e.unit_number
    local tree_box=(kind=="tree") and e.bounding_box or nil
    local before=inventory_item_counts(inv)
    local amount_before=(kind=="resource" and e.amount) or nil
    local mined
    if void_mode then
      -- DESTROY mode: delete the entity and everything inside it. No item
      -- transfer, so the actor's inventory never constrains demolition.
      mined=e.destroy{raise_destroy=true}
    elseif kind=="corpse" then
      -- Corpses are not mineable; destroy removes them without items.
      mined=e.destroy{raise_destroy=true}
    else
      mined=p.mine_entity(e,false)
    end
    -- A true return is not proof of a state change: require the entity to leave
    -- or a resource to shrink, otherwise report the engine refusal.
    local state_changed=(not e.valid) or (amount_before~=nil and (e.amount or 0)<amount_before)
    if not state_changed then fail_at(i,"ENGINE",mined and "engine reported success but the entity is still present" or "entity could not be mined",target)
    else
      removed=removed+1
      if kind=="tree" then clear_tree_stump(surface,tree_box) end
      results[#results+1]={index=i,name=name,type=kind,position=pos,unitNumber=entity_unit,items=inventory_item_difference(before,inv)}
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=p.force.name,removed=removed,failed=failed,errors=errors,results=results,items=inventory_item_difference(before_all,inv)}))`,
		character, luaString(player), voidMode(input.Mode), removals.String(), reachViolation("p", "e"))
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

// voidMode reports whether the removal deletes the entity outright instead of
// mining it, which skips the item transfer and the reach requirement.
func voidMode(mode *RemovalMode) bool {
	return mode != nil && *mode == RemovalModeDestroy
}
