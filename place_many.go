package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// placeMany places one normal-quality item at each requested position. Per
// position failures are counted and reported instead of aborting the batch.
func placeMany(r *Resolver, input PlaceManyInput) (*PlaceManyResult, error) {
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	surface := "nauvis"
	if input.Surface != nil {
		if *input.Surface == "" {
			return nil, errors.New("surface must not be empty")
		}
		surface = *input.Surface
	}
	if input.Item == "" {
		return nil, errors.New("item must not be empty")
	}
	quality, err := resolveQuality(input.Quality)
	if err != nil {
		return nil, err
	}
	if len(input.Placements) < 1 || len(input.Placements) > 1000 {
		return nil, errors.New("placements must contain between 1 and 1000 entries")
	}
	var placements strings.Builder
	for i, placement := range input.Placements {
		if placement == nil {
			return nil, fmt.Errorf("placement %d is missing", i+1)
		}
		if placement.Position == nil {
			return nil, fmt.Errorf("placement %d position is required", i+1)
		}
		if placement.Direction < 0 || placement.Direction > 15 {
			return nil, fmt.Errorf("placement %d direction must be between 0 and 15", i+1)
		}
		direction := placement.Direction
		fastReplace := placement.FastReplace != nil && *placement.FastReplace
		if i > 0 {
			placements.WriteString(",")
		}
		fmt.Fprintf(&placements, "{x=%s,y=%s,direction=%d,fast_replace=%t}",
			strconv.FormatFloat(placement.Position.X, 'g', -1, 64),
			strconv.FormatFloat(placement.Position.Y, 'g', -1, 64), direction, fastReplace)
	}
	lua := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
if c.surface~=s then fail("actor is on another surface"); return end
local item=%s; local quality=%s; local proto=prototypes.item[item]; if not proto or not proto.place_result then fail("item does not place an entity"); return end; local name=proto.place_result.name
if not prototypes.quality[quality] then fail("quality not found"); return end
local placements={%s}
local placed,failed=0,0; local errors={}; local entities={}
local function fail_at(i,code,reason,p)
  failed=failed+1
  if #errors<20 then errors[#errors+1]={index=i,code=code,message=reason,target={surface=s.name,entity=name,position={x=p.x,y=p.y}}} end
end
for i,p in ipairs(placements) do
  local ok=true
  local dx=c.position.x-p.x; local dy=c.position.y-p.y
  if %s then ok=false; fail_at(i,"REACH","out of build reach",p) end
  if ok and inv.get_item_count({name=item,quality=quality})<1 then ok=false; fail_at(i,"INVENTORY","actor lacks item",p) end
  if ok and not p.fast_replace and not s.can_place_entity{name=name,position={x=p.x,y=p.y},direction=p.direction,force=c.force} then ok=false; fail_at(i,"COLLISION","cannot place entity",p) end
  if ok and inv.remove{name=item,quality=quality,count=1}~=1 then ok=false; fail_at(i,"INVENTORY","could not remove item",p) end
  if ok then
    local params={name=name,position={x=p.x,y=p.y},direction=p.direction,quality=quality,force=c.force,raise_built=true}
    if p.fast_replace then params.fast_replace=true; params.spill=true end
    local created,e=pcall(function() return s.create_entity(params) end)
    if created and e then
      placed=placed+1
      entities[#entities+1]={name=e.name,type=e.type,position=e.position,unit_number=e.unit_number}
    else
      inv.insert{name=item,quality=quality,count=1}
      fail_at(i,"ENGINE","entity creation failed"..(created and "" or ": "..tostring(e)),p)
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=c.force.name,item=item,placed=placed,failed=failed,errors=errors,entities=entities}))`,
		luaString(player), character, luaString(surface), luaString(input.Item), luaString(quality), placements.String(),
		distanceViolation("dx", "dy", "c.build_distance"))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[PlaceManyResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, placeManySummary(result))
	return result, nil
}

func placeManySummary(r *PlaceManyResult) string {
	return fmt.Sprintf("placed %d of %d %s on %s", r.Placed, r.Placed+r.Failed, r.Item, r.Surface)
}
