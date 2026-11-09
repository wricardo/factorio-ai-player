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
-- JSON has no representation for Lua's inf/nan, and helpers.table_to_json emits
-- them as bare tokens that break the response decoder. Some prototypes (for
-- example electric-energy-interface, whose usage is infinite) report math.huge,
-- so every numeric field is clamped to nil when it is not finite.
local function finite(value)
  if type(value)~="number" then return value end
  if value~=value or value==math.huge or value==-math.huge then return nil end
  return value
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
  local resource_categories={}
  local raw_resource_categories=get(entity,"resource_categories")
  if raw_resource_categories then for category,_ in pairs(raw_resource_categories) do resource_categories[#resource_categories+1]=category end end
  table.sort(resource_categories)
  -- Engine energies are joules per tick; watts are 60 times that.
  local max_energy_usage=call(entity,"get_max_energy_usage")
  local boxes=get(entity,"fluidbox_prototypes")
  local mineable=get(entity,"mineable_properties")
  entity_info={
    name=entity.name,
    type=entity.type,
    tileWidth=get(entity,"tile_width") or 1,
    tileHeight=get(entity,"tile_height") or 1,
    collisionBox=get(entity,"collision_box"),
    selectionBox=get(entity,"selection_box"),
    beltSpeed=finite(get(entity,"belt_speed")),
    maxUndergroundDistance=finite(get(entity,"max_underground_distance")),
    inserterPickup=get(entity,"inserter_pickup_position"),
    inserterDrop=get(entity,"inserter_drop_position"),
    drillOutput=get(entity,"vector_to_place_result"),
    craftingCategories=categories,
    resourceCategories=resource_categories,
    fluidBoxCount=(boxes and #boxes) or 0,
    mineable=(mineable and mineable.minable) or false,
    maxHealth=finite(call(entity,"get_max_health")),
    wireReach=finite(call(entity,"get_max_wire_distance")),
    supplyArea=finite(call(entity,"get_supply_area_distance")),
    craftingSpeed=finite(call(entity,"get_crafting_speed")),
    miningRadius=finite(call(entity,"get_mining_drill_radius") or get(entity,"mining_drill_radius")),
    miningSpeed=finite(get(entity,"mining_speed")),
    energyUsageWatts=finite(max_energy_usage and max_energy_usage*60),
    moduleSlots=finite(get(entity,"module_inventory_size"))
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
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
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
	lua := luaFail + luaTileOccupants + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local item=%s; local quality=%s; local side=%s; local gap=%d; local direction=%d
`, luaString(player), character, luaString(input.Item), luaString(quality), luaString(string(input.Side)), input.Gap, input.Direction) +
		entityRefPrelude("target", unit, name, x, y, input.Surface) + fmt.Sprintf(`
if c.surface.name~=target_surface.name then fail("actor is on another surface"); return end
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
local out_of_reach=false
local refused=false
local layers=(prototypes.entity[new_proto.name] and prototypes.entity[new_proto.name].collision_mask or {}).layers
for _,offset in ipairs(offsets) do
  local position={x=base.x+offset[1],y=base.y+offset[2]}
  if keeps_gap(position) then
    local dx=c.position.x-position.x; local dy=c.position.y-position.y
    if %s then
      out_of_reach=true
    elseif #tile_occupants(target_surface,position)>0 then
      -- create_entity ignores collision boxes, so an occupied candidate tile
      -- would stack a second entity instead of failing; try the next offset.
      refused=true
    elseif #footprint_occupants(target_surface,new_proto.name,position,layers)>0 then
      -- The centre tile can be free while a multi-tile footprint covers a
      -- neighbour; try the next offset instead of creating and undoing it.
      refused=true
    elseif inv.get_item_count{name=item,quality=quality}<1 then
      fail("actor lacks item"); return
    elseif inv.remove{name=item,quality=quality,count=1}~=1 then
      fail("could not remove item"); return
    else
      -- Entity creation is the success test; can_place_entity can reject a
      -- box the engine would snap onto a free tile, so it must not decide.
      local before=tile_unit_set(target_surface,position)
      local created,err=pcall(function() return target_surface.create_entity{name=new_proto.name,position=position,direction=direction,quality=quality,force=c.force,raise_built=true} end)
      local e=nil; if created then e=err end
      -- The engine can raise or return nil after committing the entity;
      -- refunding then would duplicate the item and report a false failure.
      if not e then e=new_tile_entity(target_surface,position,before) end
      if e then
        -- tile_occupants tests one tile, so a multi-tile prototype can still land
        -- on a neighbour; undo it and try the next offset instead.
        local overlap=undo_overlap(e,inv,item,quality,(prototypes.entity[new_proto.name] and prototypes.entity[new_proto.name].collision_mask or {}).layers)
        if not overlap then
          rcon.print(helpers.table_to_json{tick=game.tick,surface=target_surface.name,force=c.force.name,item=item,name=e.name,unitNumber=e.unit_number,position={x=e.position.x,y=e.position.y},relativeTo=target.unit_number,side=side,gap=gap})
          return
        end
        refused=true
      else
        inv.insert{name=item,quality=quality,count=1}
        if not created then fail("entity creation failed: "..tostring(err)); return end
        refused=true
      end
    end
  end
end
if refused then fail("entity creation failed"); return end
if out_of_reach then fail("out of build reach"); return end
fail("cannot place "..new_proto.name.." beside the target; no legal position")
return`,
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
