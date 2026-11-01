package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

const (
	nearestPlacementMaxRadius = 64.0
	nearestPlacementMaxGap    = 16.0
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
	n := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local prototype=%s; local direction=%d
local ox,oy,radius=%s,%s,%s
local avoid_resources,avoid_water,min_gap,need_power,need_water_adjacent=%t,%t,%s,%t,%t
`, luaString(input.Surface), luaString(input.Force), luaString(input.Prototype), input.Direction,
		n(input.Origin.X), n(input.Origin.Y), n(input.MaxRadius),
		input.AvoidResources, input.AvoidWater, n(gap), input.RequirePowerCoverage, input.RequireWaterAdjacency) +
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
local w=proto.tile_width or 1
local h=proto.tile_height or 1
if direction==4 or direction==12 then w,h=h,w end
local layers=proto.collision_mask.layers
local function snap(value,size)
  if size % 2 == 1 then return math.floor(value)+0.5 end
  return math.floor(value+0.5)
end
local function box(c,extra)
  extra=extra or 0
  return {{c.x-w/2-extra,c.y-h/2-extra},{c.x+w/2+extra,c.y+h/2+extra}}
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
local function supply_distance(pole)
  local cached=supply_cache[pole.name]
  if cached~=nil then return cached end
  local value=0
  local fn=pole.prototype.get_supply_area_distance
  if type(fn)=="function" then
    local ok,result=pcall(function() return fn("normal") end)
    if ok and type(result)=="number" then value=result end
  end
  supply_cache[pole.name]=value
  return value
end
local function powered(c)
  local b=box(c)
  for _,pole in pairs(surface.find_entities_filtered{area=box(c,8),type="electric-pole",force=force}) do
    local d=supply_distance(pole)
    local px,py=pole.position.x,pole.position.y
    if px+d>b[1][1] and px-d<b[2][1] and py+d>b[1][2] and py-d<b[2][2] then return true end
  end
  return false
end
local function accepted(c)
  if not surface.can_place_entity{name=prototype,position=c,direction=direction,force=force} then return false end
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
  return true
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
if not surface.can_place_entity{name=prototype,position=origin,direction=direction,force=force} then
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
local found=nil
for _,c in ipairs(candidates) do
  expanded=expanded+1
  if accepted(c) then found=c; break end
end
local result={tick=game.tick,surface=surface.name,force=force.name,prototype=prototype,direction=direction,found=found~=nil,blockers=blockers,terrain=terrain,tile=tile_name,expanded=expanded}
if found then result.position={x=found.x,y=found.y}; result.distance=math.sqrt(found.d2) end
rcon.print(helpers.table_to_json(result))`
