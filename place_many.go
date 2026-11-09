package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// placeMany places one normal-quality item at each requested position. Per
// position failures are counted and reported instead of aborting the batch.
// Entity creation is the success test: can_place_entity can reject a position
// the engine would snap onto a free tile, so it only classifies refusals.
func placeMany(r *Resolver, input PlaceManyInput) (*PlaceManyResult, error) {
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
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
	if len(input.Placements) < 1 || len(input.Placements) > maxPlacements {
		return nil, fmt.Errorf("placements must contain between 1 and %d entries", maxPlacements)
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
		undergroundType := ""
		if placement.UndergroundType != nil {
			switch *placement.UndergroundType {
			case UndergroundBeltTypeInput:
				undergroundType = "input"
			case UndergroundBeltTypeOutput:
				undergroundType = "output"
			default:
				return nil, fmt.Errorf("placement %d undergroundType must be input or output", i+1)
			}
		}
		if i > 0 {
			placements.WriteString(",")
		}
		fmt.Fprintf(&placements, "{x=%s,y=%s,direction=%d,fast_replace=%t,underground_type=%q}",
			strconv.FormatFloat(placement.Position.X, 'g', -1, 64),
			strconv.FormatFloat(placement.Position.Y, 'g', -1, 64), direction, fastReplace, undergroundType)
	}
	lua := luaFail + luaTileOccupants + luaUndergroundInfoLua + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
if c.surface~=s then fail("actor is on another surface"); return end
local item=%s; local quality=%s; local proto=prototypes.item[item]; if not proto or not proto.place_result then fail("item does not place an entity"); return end; local name=proto.place_result.name
if not prototypes.quality[quality] then fail("quality not found"); return end
local placements={%s}
local placed,failed=0,0; local errors={}; local entities={}
-- The placed prototype's collision mask is the occupancy rule: only an entity
-- whose mask intersects it can obstruct this build, so corpses and other
-- doodads are never occupants (factorio-ai-player-sth).
local layers=(prototypes.entity[name] and prototypes.entity[name].collision_mask or {}).layers
local function fail_at(i,code,reason,p)
  failed=failed+1
  if #errors<20 then errors[#errors+1]={index=i,code=code,message=reason,target={surface=s.name,entity=name,position={x=p.x,y=p.y}}} end
end
for i,p in ipairs(placements) do
  local ok=true
  local dx=c.position.x-p.x; local dy=c.position.y-p.y
  if %s then ok=false; fail_at(i,"REACH","out of build reach",p) end
  if ok and inv.get_item_count({name=item,quality=quality})<1 then ok=false; fail_at(i,"INVENTORY","actor lacks item",p) end
  -- create_entity ignores collision boxes, so a fast replace must first be
  -- proven replaceable; otherwise the new entity stacks on whatever is there.
  -- A plain placement must refuse an occupied tile for the same reason.
  if ok and not p.fast_replace then
    local blockers=tile_occupants(s,{x=p.x,y=p.y},layers)
    if #blockers>0 then ok=false; fail_at(i,"COLLISION","tile is occupied by "..table.concat(blockers,", "),p) end
    -- A multi-tile prototype's footprint can cover a neighbour while its centre
    -- tile is free; refuse it here so nothing is written for that entry.
    if ok then
      local footprint=footprint_occupants(s,name,{x=p.x,y=p.y},layers)
      if #footprint>0 then ok=false; fail_at(i,"COLLISION","footprint overlaps "..table.concat(footprint,", "),p) end
    end
  end
  if ok and p.fast_replace and not (s.can_fast_replace{name=name,position={x=p.x,y=p.y},direction=p.direction,force=c.force} or s.can_place_entity{name=name,position={x=p.x,y=p.y},direction=p.direction,force=c.force,build_check_type=defines.build_check_type.manual}) then
    ok=false; fail_at(i,"COLLISION","cannot fast-replace: nothing fast-replaceable there and the tile is blocked",p)
  end
  if ok and inv.remove{name=item,quality=quality,count=1}~=1 then ok=false; fail_at(i,"INVENTORY","could not remove item",p) end
  if ok then
    local params={name=name,position={x=p.x,y=p.y},direction=p.direction,quality=quality,force=c.force,raise_built=true}
    if p.underground_type~="" then params.type=p.underground_type end
    if p.fast_replace then
      -- Passing the acting character (and the player when one is selected) makes
      -- the engine return the replaced entity and its contents to that actor
      -- instead of spilling them on the ground.
      params.fast_replace=true; params.spill=true; params.character=c
      if unit==0 then params.player=actor end
    end
    local before=tile_unit_set(s,{x=p.x,y=p.y})
    local created,err=pcall(function() return s.create_entity(params) end)
    local entity=nil; if created then entity=err end
    -- The engine can raise or return nil after committing the entity; refunding
    -- then would duplicate the item and report a failure that did not happen.
    if not entity then entity=new_tile_entity(s,{x=p.x,y=p.y},before) end
    -- tile_occupants tests one tile per placement, so a multi-tile prototype can
    -- still land on a neighbour; undo that creation and report it as a collision.
    local overlap=nil
    if entity then overlap=undo_overlap(entity,inv,item,quality,layers) end
    if entity and overlap then
      entity=nil
    end
    if entity then
      placed=placed+1
      -- Only an underground half has a ground type, and the probe tolerates
      -- LuaCustomTable/userdata neighbours.
      local underground_type=underground_info(entity)
      entities[#entities+1]={index=i-1,name=entity.name,type=entity.type,position=entity.position,unit_number=entity.unit_number,direction=entity.direction,underground_type=underground_type}
    else
      if overlap then
        fail_at(i,"COLLISION",overlap,p)
      else
        inv.insert{name=item,quality=quality,count=1}
        -- The engine decides whether a position is usable: can_place_entity can
        -- reject a box it would snap onto a free tile, so it must not gate the
        -- attempt. It only classifies a creation the engine refused.
        if not created then
          fail_at(i,"ENGINE","entity creation failed: "..tostring(err),p)
        elseif s.can_place_entity{name=name,position={x=p.x,y=p.y},direction=p.direction,force=c.force,build_check_type=defines.build_check_type.manual} then
          fail_at(i,"ENGINE","entity creation failed",p)
        else
          fail_at(i,"COLLISION","cannot place entity",p)
        end
      end
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
	if result.Placed > 0 {
		announceMutation(r, input.Message, placeManySummary(result))
	}
	return result, nil
}

func placeManySummary(r *PlaceManyResult) string {
	return fmt.Sprintf("placed %d of %d %s on %s", r.Placed, r.Placed+r.Failed, r.Item, r.Surface)
}
