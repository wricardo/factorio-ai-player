package main

import (
	"errors"
	"fmt"
)

// prototypeQuery reads static engine prototype data for one name. Item and
// entity prototypes share names, so both are reported when present; an item
// with no entity of that name falls back to its place result.
func prototypeQuery(r *Resolver, name string) (*Prototype, error) {
	if name == "" {
		return nil, errors.New("name must not be empty")
	}
	lua := luaFail + fmt.Sprintf(`local name=%s
local item=prototypes.item[name]
local entity=prototypes.entity[name]
if not item and not entity then fail("prototype not found: "..name); return end
local function get(proto,key)
  local ok,value=pcall(function() return proto[key] end)
  if ok then return value end
  return nil
end
local function call(proto,method)
  local fn=get(proto,method)
  if type(fn)~="function" then return nil end
  local ok,value=pcall(function() return fn("normal") end)
  if ok then return value end
  return nil
end
local item_info=nil
if item then
  item_info={name=item.name,stackSize=get(item,"stack_size"),stackable=get(item,"stackable"),fuelCategory=get(item,"fuel_category"),fuelValue=get(item,"fuel_value")}
  local placed=get(item,"place_result")
  if placed then item_info.placeResult=placed.name end
  local burnt=get(item,"burnt_result")
  if burnt then item_info.burntResult=burnt.name end
end
local entity_info=nil
if entity then
  local categories={}
  local raw_categories=get(entity,"crafting_categories")
  if raw_categories then for category,_ in pairs(raw_categories) do categories[#categories+1]=category end end
  table.sort(categories)
  local boxes=get(entity,"fluidbox_prototypes")
  local mineable=get(entity,"mineable_properties")
  entity_info={
    name=entity.name,
    type=entity.type,
    tileWidth=get(entity,"tile_width") or 1,
    tileHeight=get(entity,"tile_height") or 1,
    collisionBox=get(entity,"collision_box"),
    selectionBox=get(entity,"selection_box"),
    beltSpeed=get(entity,"belt_speed"),
    maxUndergroundDistance=get(entity,"max_underground_distance"),
    inserterPickup=get(entity,"inserter_pickup_position"),
    inserterDrop=get(entity,"inserter_drop_position"),
    craftingCategories=categories,
    fluidBoxCount=(boxes and #boxes) or 0,
    mineable=(mineable and mineable.minable) or false,
    maxHealth=call(entity,"get_max_health"),
    wireReach=call(entity,"get_max_wire_distance"),
    supplyArea=call(entity,"get_supply_area_distance"),
    craftingSpeed=call(entity,"get_crafting_speed")
  }
end
rcon.print(helpers.table_to_json{name=name,item=item_info,entity=entity_info})`, luaString(name))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[Prototype](response)
}

// placeNextTo places one entity beside an anchor entity using the prototype
// bounding boxes, so callers never compute or guess footprints.
func placeNextTo(r *Resolver, input PlaceNextToInput) (*PlaceNextToResult, error) {
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	if input.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	if input.Item == "" {
		return nil, errors.New("item must not be empty")
	}
	if input.Direction != 0 && input.Direction != 4 && input.Direction != 8 && input.Direction != 12 {
		return nil, errors.New("direction must be a cardinal engine direction: 0, 4, 8, or 12")
	}
	if input.Gap < 0 || input.Gap > 16 {
		return nil, errors.New("gap must be between 0 and 16")
	}
	quality, err := resolveQuality(input.Quality)
	if err != nil {
		return nil, err
	}
	if input.RelativeTo == nil {
		return nil, errors.New("relativeTo is required")
	}
	unit, name, x, y, refSurface, err := entityRefTarget(input.RelativeTo)
	if err != nil {
		return nil, err
	}
	if refSurface != input.Surface {
		return nil, fmt.Errorf("relativeTo surface %q must match surface %q", refSurface, input.Surface)
	}
	switch input.Side {
	case RelativeSideEast, RelativeSideWest, RelativeSideNorth, RelativeSideSouth:
	default:
		return nil, fmt.Errorf("unsupported side %q", input.Side)
	}
	lua := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local item=%s; local quality=%s; local side=%s; local gap=%d; local direction=%d
`, luaString(player), character, luaString(input.Item), luaString(quality), luaString(string(input.Side)), input.Gap, input.Direction) +
		entityRefPrelude(unit, name, x, y, input.Surface) + fmt.Sprintf(`
if c.surface.name~=surface.name then fail("actor is on another surface"); return end
if c.force.name~=target.force.name then fail("target is not on the actor's force"); return end
local item_proto=prototypes.item[item]
if not item_proto then fail("item prototype not found"); return end
local new_proto=item_proto.place_result
if not new_proto then fail("item does not place an entity"); return end
if not prototypes.quality[quality] then fail("quality not found"); return end
local box=target.bounding_box
local center=target.position
local w=new_proto.tile_width or 1
local h=new_proto.tile_height or 1
if direction==4 or direction==12 then w,h=h,w end
local half_w,half_h=w/2,h/2
local function snap(value,size)
  if size%%2==1 then return math.floor(value)+0.5 end
  return math.floor(value+0.5)
end
local base
if side=="EAST" then base={x=box.right_bottom.x+gap+half_w,y=center.y}
elseif side=="WEST" then base={x=box.left_top.x-gap-half_w,y=center.y}
elseif side=="SOUTH" then base={x=center.x,y=box.right_bottom.y+gap+half_h}
else base={x=center.x,y=box.left_top.y-gap-half_h} end
base={x=snap(base.x,w),y=snap(base.y,h)}
local offsets={}
for r=0,2 do
  for dx=-r,r do
    for dy=-r,r do
      if math.max(math.abs(dx),math.abs(dy))==r then offsets[#offsets+1]={dx*0.5,dy*0.5} end
    end
  end
end
local function keeps_gap(position)
  if side=="EAST" then return position.x-half_w>=box.right_bottom.x+gap-0.000001 end
  if side=="WEST" then return position.x+half_w<=box.left_top.x-gap+0.000001 end
  if side=="SOUTH" then return position.y-half_h>=box.right_bottom.y+gap-0.000001 end
  return position.y+half_h<=box.left_top.y-gap+0.000001
end
local chosen=nil
for _,offset in ipairs(offsets) do
  local position={x=base.x+offset[1],y=base.y+offset[2]}
  if keeps_gap(position) and surface.can_place_entity{name=new_proto.name,position=position,direction=direction,force=c.force} then
    chosen=position
    break
  end
end
if not chosen then fail("cannot place "..new_proto.name.." beside the target; no legal position"); return end
local dx=c.position.x-chosen.x; local dy=c.position.y-chosen.y
if %s then fail("out of build reach"); return end
if inv.get_item_count{name=item,quality=quality}<1 then fail("actor lacks item"); return end
if inv.remove{name=item,quality=quality,count=1}~=1 then fail("could not remove item"); return end
local created,e=pcall(function() return surface.create_entity{name=new_proto.name,position=chosen,direction=direction,quality=quality,force=c.force,raise_built=true} end)
if not created or not e then
  inv.insert{name=item,quality=quality,count=1}
  fail("entity creation failed"..(created and "" or ": "..tostring(e)))
  return
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=c.force.name,item=item,name=e.name,unitNumber=e.unit_number,position={x=e.position.x,y=e.position.y},relativeTo=target.unit_number,side=side,gap=gap})`,
		distanceViolation("dx", "dy", "c.build_distance"))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[PlaceNextToResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, placeNextToSummary(result))
	return result, nil
}

func placeNextToSummary(r *PlaceNextToResult) string {
	return fmt.Sprintf("placed %s at (%g, %g)", r.Name, r.Position.X, r.Position.Y)
}
