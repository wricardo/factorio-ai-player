package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

const (
	nearestPlacementMaxRadius        = 512.0
	nearestPlacementMaxGap           = 64.0
	nearestPlacementMaxResourceTiles = 100000
)

// nearestPlacementQuery finds the nearest legal footprint for one prototype.
// Candidates are generated and ordered in Lua so one RCON call answers the
// question that would otherwise cost one placement probe per tile.
func nearestPlacementQuery(r *Resolver, input *NearestPlacementInput) (*PlacementCandidate, error) {
	if input == nil || input.Surface == "" || input.Force == "" || input.Prototype == "" {
		return nil, errors.New("surface, force and prototype are required")
	}
	if input.Origin == nil {
		return nil, errors.New("origin is required")
	}
	if math.IsNaN(input.Origin.X) || math.IsInf(input.Origin.X, 0) || math.IsNaN(input.Origin.Y) || math.IsInf(input.Origin.Y, 0) {
		return nil, errors.New("origin must be finite")
	}
	if input.Direction < 0 || input.Direction > 15 {
		return nil, errors.New("direction must be between 0 and 15")
	}
	if math.IsNaN(input.MaxRadius) || math.IsInf(input.MaxRadius, 0) || input.MaxRadius <= 0 || input.MaxRadius > nearestPlacementMaxRadius {
		return nil, fmt.Errorf("maxRadius must be greater than 0 and at most %g", nearestPlacementMaxRadius)
	}
	gap := -1.0
	if input.MinDistanceFromEntities != nil {
		gap = *input.MinDistanceFromEntities
		if math.IsNaN(gap) || math.IsInf(gap, 0) || gap < 0 || gap > nearestPlacementMaxGap {
			return nil, fmt.Errorf("minDistanceFromEntities must be between 0 and %g", nearestPlacementMaxGap)
		}
	}
	requireResource := ""
	if input.RequireResource != nil {
		requireResource = *input.RequireResource
	}
	minResourceTiles := 0
	if input.MinResourceTiles != nil {
		minResourceTiles = *input.MinResourceTiles
	}
	if requireResource == "" && minResourceTiles != 0 {
		return nil, errors.New("minResourceTiles requires requireResource")
	}
	if minResourceTiles != 0 && (minResourceTiles < 1 || minResourceTiles > nearestPlacementMaxResourceTiles) {
		return nil, fmt.Errorf("minResourceTiles must be between 1 and %d", nearestPlacementMaxResourceTiles)
	}
	if requireResource != "" && minResourceTiles == 0 {
		minResourceTiles = 1
	}
	resourceLua := "nil"
	if requireResource != "" {
		resourceLua = luaString(requireResource)
	}
	n := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local prototype=%s; local direction=%d
local ox,oy,radius=%s,%s,%s
local avoid_resources,avoid_water,min_gap,need_power,need_water_adjacent=%t,%t,%s,%t,%t
local require_resource,min_resource_tiles=%s,%d
`, luaString(input.Surface), luaString(input.Force), luaString(input.Prototype), input.Direction,
		n(input.Origin.X), n(input.Origin.Y), n(input.MaxRadius),
		input.AvoidResources, input.AvoidWater, n(gap), input.RequirePowerCoverage, input.RequireWaterAdjacency,
		resourceLua, minResourceTiles) +
		worldCommonLua + nearestPlacementLua
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[PlacementCandidate](response)
}

const nearestPlacementLua = `
local proto=prototypes.entity[prototype]
if not proto then fail("entity prototype not found"); return end
-- manual is the check a player placing by hand gets: collisions plus
-- prototype rules such as offshore pumps needing water and pumpjacks needing
-- resources. The engine default (script) only tests collisions.
local build_check=defines.build_check_type.manual
local w=proto.tile_width or 1
local h=proto.tile_height or 1
if direction==4 or direction==12 then w,h=h,w end
local layers=proto.collision_mask.layers
-- mining_drill_radius only exists on mining drill prototypes, so it is read
-- behind pcall and only when a resource requirement is requested.
local mining_radius=nil
if require_resource then
  local ok_read,value=pcall(function()
    local radius=proto.mining_drill_radius
    if type(radius)=="number" then return radius end
    return proto.get_mining_drill_radius("normal")
  end)
  if ok_read and type(value)=="number" then mining_radius=value end
  if mining_radius==nil then
    fail("requireResource needs a mining drill prototype that exposes mining_drill_radius"); return
  end
end
local function snap(value,size)
  if size % 2 == 1 then return math.floor(value)+0.5 end
  return math.floor(value+0.5)
end
local function box(c,extra)
  extra=extra or 0
  return {{c.x-w/2-extra,c.y-h/2-extra},{c.x+w/2+extra,c.y+h/2+extra}}
end
local function mining_box(c)
  return {{c.x-mining_radius,c.y-mining_radius},{c.x+mining_radius,c.y+mining_radius}}
end
-- Water tiles carry a pumpable fluid; out-of-map tiles share the water_tile
-- collision layer but are not water.
local function is_water(tile)
  if not tile or not tile.valid then return false end
  local ok,fluid=pcall(function() return tile.prototype.fluid end)
  return ok and fluid~=nil
end
local function footprint_tiles(c)
  local x0=math.floor(c.x-w/2+0.001); local x1=math.ceil(c.x+w/2-0.001)-1
  local y0=math.floor(c.y-h/2+0.001); local y1=math.ceil(c.y+h/2-0.001)-1
  return x0,x1,y0,y1
end
local function over_water(c)
  local x0,x1,y0,y1=footprint_tiles(c)
  for ty=y0,y1 do for tx=x0,x1 do if is_water(surface.get_tile(tx,ty)) then return true end end end
  return false
end
local ignored={resource=true,tree=true,["simple-entity"]=true,cliff=true,character=true,corpse=true,["item-entity"]=true,fish=true,["character-corpse"]=true,["particle-source"]=true,["smoke-with-trigger"]=true}
local supply_cache={}
-- The supply distance only depends on the prototype, so a pole entity and an
-- electric-pole prototype share one cache entry. LuaEntityPrototype objects
-- raise on unknown keys, so never index .prototype on a prototype.
local function supply_area_of(prototype)
  local cached=supply_cache[prototype.name]
  if cached~=nil then return cached end
  local value=0
  local fn=prototype.get_supply_area_distance
  if type(fn)=="function" then
    local ok,result=pcall(function() return fn("normal") end)
    if ok and type(result)=="number" then value=result end
  end
  supply_cache[prototype.name]=value
  return value
end
local function supply_distance(pole)
  return supply_area_of(pole.prototype)
end
-- A pole whose center is just outside the footprint still powers it, so the
-- search has to reach the largest supply_area_distance any pole prototype has.
local supply_scan_margin=8
for _,pole_proto in pairs(prototypes.entity) do
  if pole_proto.type=="electric-pole" then
    supply_scan_margin=math.max(supply_scan_margin,supply_area_of(pole_proto))
  end
end
local function powered(c)
  local b=box(c)
  for _,pole in pairs(surface.find_entities_filtered{area=box(c,supply_scan_margin),type="electric-pole",force=force}) do
    local d=supply_distance(pole)
    local px,py=pole.position.x,pole.position.y
    if px+d>b[1][1] and px-d<b[2][1] and py+d>b[1][2] and py-d<b[2][2] then return true end
  end
  return false
end
local function accepted(c)
  if not surface.can_place_entity{name=prototype,position=c,direction=direction,force=force,build_check_type=build_check} then return false end
  if avoid_resources and surface.count_entities_filtered{area=box(c),type="resource"}>0 then return false end
  if avoid_water and over_water(c) then return false end
  if min_gap>=0 then
    for _,e in pairs(surface.find_entities_filtered{area=box(c,min_gap)}) do
      if e.valid and not ignored[e.type] then return false end
    end
  end
  if need_power and not powered(c) then return false end
  if need_water_adjacent then
    local wet=false
    for _,tile in pairs(surface.find_tiles_filtered{area=box(c,1),collision_mask={water_tile=true}}) do
      if is_water(tile) then wet=true; break end
    end
    if not wet then return false end
  end
  local covered,amount=0,0
  if require_resource then
    for _,resource in pairs(surface.find_entities_filtered{area=mining_box(c),type="resource",name=require_resource}) do
      covered=covered+1
      amount=amount+(resource.amount or 0)
    end
    if covered<min_resource_tiles then return false end
  end
  return true,covered,amount
end

local candidates={}
local x_from,x_to=math.floor(ox-radius)-1,math.ceil(ox+radius)+1
local y_from,y_to=math.floor(oy-radius)-1,math.ceil(oy+radius)+1
for ix=x_from,x_to do
  local cx=(w % 2 == 1) and ix+0.5 or ix
  for iy=y_from,y_to do
    local cy=(h % 2 == 1) and iy+0.5 or iy
    local dx,dy=cx-ox,cy-oy
    local d2=dx*dx+dy*dy
    if d2<=radius*radius then candidates[#candidates+1]={x=cx,y=cy,d2=d2} end
  end
end
table.sort(candidates,function(a,b)
  if a.d2~=b.d2 then return a.d2<b.d2 end
  if a.x~=b.x then return a.x<b.x end
  return a.y<b.y
end)

local origin={x=snap(ox,w),y=snap(oy,h)}
local blockers,terrain={},false
local origin_tile=surface.get_tile(math.floor(ox),math.floor(oy))
local tile_name=(origin_tile and origin_tile.valid) and origin_tile.name or ""
if not surface.can_place_entity{name=prototype,position=origin,direction=direction,force=force,build_check_type=build_check} then
  local seen={}
  for _,e in pairs(surface.find_entities_filtered{area=box(origin),collision_mask=layers}) do
    if e.valid and not seen[e.name] then seen[e.name]=true; blockers[#blockers+1]=e.name end
  end
  table.sort(blockers)
  local x0,x1,y0,y1=footprint_tiles(origin)
  for ty=y0,y1 do for tx=x0,x1 do
    local tile=surface.get_tile(tx,ty)
    local mask=tile and tile.valid and tile.prototype.collision_mask or nil
    local tile_layers=mask and mask.layers or {}
    for layer,_ in pairs(layers) do if tile_layers[layer] then terrain=true end end
  end end
end

local expanded=0
local found,found_tiles,found_amount=nil,0,0
for _,c in ipairs(candidates) do
  expanded=expanded+1
  local ok,covered,amount=accepted(c)
  if ok then found=c; found_tiles=covered or 0; found_amount=amount or 0; break end
end
local result={tick=game.tick,surface=surface.name,force=force.name,prototype=prototype,direction=direction,found=found~=nil,blockers=blockers,terrain=terrain,tile=tile_name,expanded=expanded,resourceTiles=found_tiles,resourceAmount=found_amount}
if found then result.position={x=found.x,y=found.y}; result.distance=math.sqrt(found.d2) end
rcon.print(helpers.table_to_json(result))`
