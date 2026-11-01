package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// resolveQuality returns the requested item quality prototype name, defaulting
// to normal when the option is omitted.
func resolveQuality(value *string) (string, error) {
	if value == nil {
		return "normal", nil
	}
	if *value == "" {
		return "", errors.New("quality must not be empty")
	}
	return *value, nil
}

// dropGroundLua places a stack on the ground. allow_belts must stay false: with
// the engine default, a ground drop on a belt tile is offered to the belt and
// silently discarded when the belt rejects it, which used to report success
// while destroying the items. spill_item_stack returns the created item
// entities, so an empty result is a hard failure.
const dropGroundLua = `
local function ground_drop_blocked(s, position)
  -- A belt tile absorbs an item entity created under it, so a ground drop there
  -- must be refused instead of reporting success.
  local belts={"transport-belt","underground-belt","splitter","loader","loader-1x1","linked-belt"}
  return #s.find_entities_filtered{position=position,type=belts}>0
end
local function spill_on_ground(s, position, stack, actor)
  local ok,spilled=pcall(function() return s.spill_item_stack{position=position,stack=stack,enable_looted=true,force=actor.force,allow_belts=false} end)
  if not ok or type(spilled)~="table" or #spilled==0 then return false end
  return true
end
`

// dropItem moves items from the acting character's main inventory onto the
// ground stack at a position or into the transport line of the belt there.
func dropItem(r *Resolver, input DropItemInput) (*DropItemResult, error) {
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	if input.Item == "" {
		return nil, errors.New("item must not be empty")
	}
	quality, err := resolveQuality(input.Quality)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(input.Count) || math.IsInf(input.Count, 0) || input.Count != math.Trunc(input.Count) || input.Count < 1 || input.Count > math.MaxUint32 {
		return nil, errors.New("count must be an integer from 1 to 4294967295")
	}
	if input.Position == nil {
		return nil, errors.New("position is required")
	}
	if input.Target != DropTargetGround && input.Target != DropTargetBelt {
		return nil, errors.New("target must be GROUND or BELT")
	}
	surface := "nauvis"
	if input.Surface != nil {
		if *input.Surface == "" {
			return nil, errors.New("surface must not be empty")
		}
		surface = *input.Surface
	}
	count := strconv.FormatFloat(input.Count, 'f', -1, 64)
	lua := luaFail + dropGroundLua + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
if c.surface~=s then fail("actor is on another surface"); return end
local requested_position={x=%s,y=%s}; -- ground items and belt insertion both address tile centers
local position={x=math.floor(requested_position.x)+0.5,y=math.floor(requested_position.y)+0.5}; local item=%s; local quality=%s; local requested=%s; local target=%s
if not prototypes.item[item] then fail("item prototype not found"); return end
if not prototypes.quality[quality] then fail("quality not found"); return end
local dx=c.position.x-position.x; local dy=c.position.y-position.y
if %s then fail("position is out of build reach"); return end
local available=inv.get_item_count{name=item,quality=quality}
if available<requested then fail("actor lacks requested items"); return end
if target=="GROUND" and ground_drop_blocked(s,position) then fail("position is occupied by a belt; use target BELT or a clear position"); return end
local stack={name=item,quality=quality,count=requested}
local line=0
if target=="BELT" then
  local belt
  for _,e in pairs(s.find_entities_filtered{area={{x=position.x-0.01,y=position.y-0.01},{x=position.x+0.01,y=position.y+0.01}},limit=20}) do
    if e.type=="transport-belt" or e.type=="linked-belt" then belt=e; break end
  end
  if not belt then fail("no transport belt at position"); return end
  if %s then fail("belt is out of reach"); return end
  local ok_spec,spec_line,spec_position=pcall(function() return belt.get_item_insert_specification(position) end)
  if not ok_spec or not spec_line then fail("belt has no insert position here"); return end
  local transport=belt.get_transport_line(spec_line)
  if not transport or not transport.valid then fail("belt transport line not found"); return end
  if not transport.can_insert_at(spec_position) then fail("belt position cannot accept items"); return end
  line=spec_line
  if inv.remove{name=item,quality=quality,count=requested}~=requested then fail("could not remove requested items"); return end
  local ok_insert,inserted=pcall(function() return transport.insert_at(spec_position,stack) end)
  if not ok_insert or not inserted then inv.insert{name=item,quality=quality,count=requested}; fail("belt rejected items; items returned"); return end
else
  if inv.remove{name=item,quality=quality,count=requested}~=requested then fail("could not remove requested items"); return end
  if not spill_on_ground(s,position,stack,c) then inv.insert{name=item,quality=quality,count=requested}; fail("could not place items on the ground; items returned"); return end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=c.force.name,item=item,quality=quality,requested=requested,moved=requested,position=position,line=line}))`,
		luaString(player), character, luaString(surface),
		luaCoordinate(input.Position.X), luaCoordinate(input.Position.Y),
		luaString(input.Item), luaString(quality), count, luaString(string(input.Target)),
		distanceViolation("dx", "dy", "c.build_distance"), reachViolation("c", "belt"))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[DropItemResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fmt.Sprintf("dropped %s %s %s%s",
		strconv.FormatFloat(result.Moved, 'g', -1, 64), result.Quality, result.Item, positionText(result.Position)))
	return result, nil
}
