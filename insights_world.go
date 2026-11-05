package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// worldAreaLua validates bounded scans and returns both API and response representations.
func worldAreaLua(area *FactoryAreaInput, limit, offset int) (string, string, error) {
	areaLua, responseArea, err := worldAreaBoundsLua(area, maxAreaTiles)
	if err != nil {
		return "", "", err
	}
	if limit < 1 || limit > maxPageSize || offset < 0 {
		return "", "", fmt.Errorf("limit must be between 1 and %d and offset must be non-negative", maxPageSize)
	}
	return areaLua, responseArea, nil
}

func worldAreaBoundsLua(area *FactoryAreaInput, maxTiles float64) (string, string, error) {
	if area == nil {
		return "", "", errors.New("area is required")
	}
	v := []float64{area.LeftTopX, area.LeftTopY, area.RightBottomX, area.RightBottomY}
	for _, n := range v {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", "", errors.New("area coordinates must be finite")
		}
	}
	if v[0] >= v[2] || v[1] >= v[3] || v[2]-v[0] > maxTiles || v[3]-v[1] > maxTiles {
		return "", "", fmt.Errorf("area must have positive dimensions no greater than %g tiles", maxTiles)
	}
	n := make([]string, 4)
	for i := range v {
		n[i] = strconv.FormatFloat(v[i], 'g', -1, 64)
	}
	return fmt.Sprintf("{{%s,%s},{%s,%s}}", n[0], n[1], n[2], n[3]), fmt.Sprintf("{leftTopX=%s,leftTopY=%s,rightBottomX=%s,rightBottomY=%s}", n[0], n[1], n[2], n[3]), nil
}

// beltFamilyTypes is the belt-family census type set used by beltTiles and as
// the default when BeltTilesInput.types is empty.
var beltFamilyTypes = []string{"transport-belt", "underground-belt", "splitter", "loader", "loader-1x1", "linked-belt"}

// markWorldLaneMixed sets WorldLane.mixed from lane contents: true when the lane
// carries more than one distinct item prototype or quality combination. mixed is
// not a wire value, so every resolver that returns WorldLane lanes must call it.
func markWorldLaneMixed(lanes []*WorldLane) {
	for _, lane := range lanes {
		if lane != nil {
			lane.Mixed = worldLaneMixed(lane)
		}
	}
}

func worldLaneMixed(lane *WorldLane) bool {
	seen := make(map[string]bool, len(lane.Items))
	for _, stack := range lane.Items {
		if stack == nil {
			continue
		}
		key := stack.Name + "|" + stack.Quality
		if seen[key] {
			continue
		}
		if len(seen) > 0 {
			return true
		}
		seen[key] = true
	}
	return false
}

const worldCommonLua = `
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local function less(a,b)
  if a.kind ~= b.kind then return a.kind < b.kind end
  if a.name ~= b.name then return a.name < b.name end
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  return (a.unitNumber or 0) < (b.unitNumber or 0)
end
`

func worldLogisticsQuery(r *Resolver, input *WorldLogisticsInput) (*WorldLogisticsPage, error) {
	if input == nil || input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	area, areaResult, err := worldAreaLua(input.Area, input.Limit, input.Offset)
	if err != nil {
		return nil, err
	}
	requested := ""
	if input.Type != nil {
		requested = *input.Type
	}
	lua := luaFail + luaUndergroundInfoLua + transportLineLua + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area,area_result=%s,%s; local limit,offset=%d,%d; local requested=%s
`, luaString(input.Surface), luaString(input.Force), area, areaResult, input.Limit, input.Offset, luaString(requested)) + worldCommonLua + `
local types={"transport-belt","underground-belt","splitter","loader","loader-1x1","linked-belt","inserter","pipe","pipe-to-ground","storage-tank","boiler","pump","offshore-pump"}
local valid_type=false
for _,t in ipairs(types) do if requested==t then valid_type=true end end
if requested~="" and not valid_type then fail("unsupported logistics entity type"); return end
local entities=surface.find_entities_filtered{area=area,force=force,type=types}
local matches={}
for _,e in pairs(entities) do if requested=="" or e.type==requested then matches[#matches+1]={kind=e.type,name=e.name,position=e.position,unitNumber=e.unit_number,entity=e} end end
table.sort(matches,less)
local statuses={}; for k,v in pairs(defines.entity_status) do statuses[v]=k end
local result={}
local function contents(inv)
  local items=inv.get_contents()
  table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "")<(b.quality or "") end)
  return items
end
for i=offset+1,math.min(#matches,offset+limit) do
  local e=matches[i].entity
  local obj={name=e.name,type=e.type,position=e.position,direction=e.direction,unitNumber=e.unit_number,lanes={},fluids={},worldInventories={}}
  local ok,status=pcall(function() return e.status end); if ok then obj.status=statuses[status] end
  if e.type=="transport-belt" or e.type=="underground-belt" or e.type=="splitter" or e.type=="loader" or e.type=="loader-1x1" or e.type=="linked-belt" then
    for j=1,e.get_max_transport_line_index() do
      local name=transport_line_name(j)
      obj.lanes[#obj.lanes+1]={index=j,lineName=name,side=transport_line_side(name),items=contents(e.get_transport_line(j))}
    end
  elseif e.type=="inserter" then
    obj.pickupPosition=e.pickup_position; obj.dropPosition=e.drop_position
    local stack=e.held_stack; if stack and stack.valid_for_read then obj.heldStack={name=stack.name,count=stack.count,quality=stack.quality.name} end
  end
  if e.type=="underground-belt" then
    local underground_type,paired=underground_info(e)
    obj.undergroundType=underground_type
    -- UnitNumber is a string scalar on the wire, so the numeric engine id is
    -- converted here instead of relying on a normalizer key that does not cover
    -- every UnitNumber field.
    obj.pairedUnitNumber=(paired~=nil) and tostring(paired) or nil
  end
  local boxes=e.fluidbox
  if boxes then for j=1,#boxes do local f=boxes[j]; local item={index=j,capacity=boxes.get_capacity(j)}; if f then item.name=f.name; item.amount=f.amount; item.temperature=f.temperature end; obj.fluids[#obj.fluids+1]=item end end
  local fuel=e.get_fuel_inventory(); if fuel then obj.worldInventories[#obj.worldInventories+1]={kind="fuel",items=contents(fuel)} end
  local burnt=e.get_burnt_result_inventory(); if burnt then obj.worldInventories[#obj.worldInventories+1]={kind="burntResult",items=contents(burnt)} end
  local output=e.get_output_inventory(); if output then obj.worldInventories[#obj.worldInventories+1]={kind="output",items=contents(output)} end
  result[#result+1]=obj
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=area_result,pageInfo={total=#matches,limit=limit,offset=offset,hasMore=#matches>offset+limit},logisticsEntities=result})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	page, err := factoryDecode[WorldLogisticsPage](response)
	if err != nil {
		return nil, err
	}
	for _, entity := range page.LogisticsEntities {
		markWorldLaneMixed(entity.Lanes)
	}
	return page, nil
}

func beltTilesQuery(r *Resolver, input *BeltTilesInput) (*BeltTilesPage, error) {
	if input == nil || input.Surface == "" {
		return nil, errors.New("surface is required")
	}
	area, areaResult, err := worldAreaLua(input.Area, input.Limit, input.Offset)
	if err != nil {
		return nil, err
	}
	requested := beltFamilyTypes
	if len(input.Types) > 0 {
		requested = input.Types
	}
	validTypes := make(map[string]bool, len(beltFamilyTypes))
	for _, t := range beltFamilyTypes {
		validTypes[t] = true
	}
	literals := make([]string, len(requested))
	for i, t := range requested {
		if !validTypes[t] {
			return nil, fmt.Errorf("unsupported belt type %q", t)
		}
		literals[i] = luaString(t)
	}
	lua := luaFail + transportLineLua + fmt.Sprintf(`local surface_name=%s; local area,area_result=%s,%s; local limit,offset=%d,%d; local type_list={%s}
`, luaString(input.Surface), area, areaResult, input.Limit, input.Offset, strings.Join(literals, ",")) + `
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local type_set={}
for _,t in ipairs(type_list) do type_set[t]=true end
local entities=surface.find_entities_filtered{area=area,type=type_list}
local matches={}
for _,e in pairs(entities) do
  if type_set[e.type] then matches[#matches+1]={entity=e,position=e.position,unitNumber=e.unit_number} end
end
table.sort(matches,function(a,b)
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  return (a.unitNumber or 0)<(b.unitNumber or 0)
end)
local result={}
for i=offset+1,math.min(#matches,offset+limit) do
  local e=matches[i].entity
  local obj={name=e.name,type=e.type,position=e.position,direction=e.direction,unitNumber=e.unit_number,lanes={}}
  for j=1,e.get_max_transport_line_index() do
    local name=transport_line_name(j)
    local items=e.get_transport_line(j).get_contents()
    table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "")<(b.quality or "") end)
    obj.lanes[#obj.lanes+1]={index=j,lineName=name,side=transport_line_side(name),items=items}
  end
  result[#result+1]=obj
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,area=area_result,pageInfo={total=#matches,limit=limit,offset=offset,hasMore=#matches>offset+limit},total=#matches,tiles=result})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	page, err := factoryDecode[BeltTilesPage](response)
	if err != nil {
		return nil, err
	}
	for _, tile := range page.Tiles {
		markWorldLaneMixed(tile.Lanes)
	}
	return page, nil
}

func worldResourcesQuery(r *Resolver, input *WorldResourcesInput) (*WorldResourcesPage, error) {
	if input == nil || input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	area, areaResult, err := worldAreaLua(input.Area, input.Limit, input.Offset)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(input.DrillRadius) || math.IsInf(input.DrillRadius, 0) || input.DrillRadius < 0 || input.DrillRadius > maxDrillRadius {
		return nil, fmt.Errorf("drillRadius must be between 0 and %d", maxDrillRadius)
	}
	supportedKinds := map[string]bool{"resource": true, "tree": true, "cliff": true, "simple-entity": true, "water": true}
	kindLiterals := make([]string, len(input.Kinds))
	for i, kind := range input.Kinds {
		if !supportedKinds[kind] {
			return nil, fmt.Errorf("unsupported resource kind %q", kind)
		}
		kindLiterals[i] = luaString(kind)
	}
	name := ""
	if input.ResourceName != nil {
		name = *input.ResourceName
	}
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area,area_result=%s,%s; local limit,offset=%d,%d; local resource_name=%s; local radius=%s; local group_patches=%t; local kind_list={%s}
`, luaString(input.Surface), luaString(input.Force), area, areaResult, input.Limit, input.Offset, luaString(name), strconv.FormatFloat(input.DrillRadius, 'g', -1, 64), input.GroupPatches, strings.Join(kindLiterals, ",")) + worldCommonLua + `
local kind_filter={}
for _,k in ipairs(kind_list) do kind_filter[k]=true end
local function keep(kind) return #kind_list==0 or kind_filter[kind]==true end
local entries={}; local resources={}; local resource_total,obstacle_total,water_total=0,0,0
local status_names={}; for name,value in pairs(defines.entity_status) do status_names[value]=name end
for _,e in pairs(surface.find_entities_filtered{area=area,type={"resource","tree","cliff","simple-entity"}}) do
  if e.type=="resource" then
    if resource_name=="" or e.name==resource_name then
      local props=e.prototype.mineable_properties
      resource_total=resource_total+1
      resources[#resources+1]={name=e.name,position=e.position,amount=e.amount or 0,infinite=props.infinite==true}
      if keep("resource") then
        entries[#entries+1]={kind="resource",name=e.name,position=e.position,amount=e.amount,requiredFluid=props.required_fluid,fluidAmount=props.fluid_amount,entity=e}
      end
    end
  else
    obstacle_total=obstacle_total+1
    if keep(e.type) then entries[#entries+1]={kind=e.type,name=e.name,position=e.position,unitNumber=e.unit_number} end
  end
end
for _,tile in pairs(surface.find_tiles_filtered{area=area}) do
  -- Out-of-map tiles share the water_tile layer; only tiles with a pumpable fluid are water.
  local ok_fluid,fluid=pcall(function() return tile.prototype.fluid end)
  if ok_fluid and fluid and tile.collides_with("water_tile") then
    water_total=water_total+1
    if keep("water") then entries[#entries+1]={kind="water",name=tile.name,position=tile.position} end
  end
end
table.sort(entries,less)
local result={}
for i=offset+1,math.min(#entries,offset+limit) do
  local entry=entries[i]; local drills={}
  if entry.kind=="resource" then
    for _,d in pairs(surface.find_entities_filtered{position=entry.position,radius=radius,type="mining-drill",force=force}) do
      drills[#drills+1]={kind="drill",name=d.name,position=d.position,unitNumber=d.unit_number,status=status_names[d.status]}
    end
    table.sort(drills,less)
  end
  entry.entity=nil; entry.nearbyDrills=drills; result[#result+1]=entry
end
local patches={}
if group_patches and #resources>0 then
  local parent={}
  local items={}
  for _,res in ipairs(resources) do
    local tx,ty=math.floor(res.position.x),math.floor(res.position.y)
    local key=res.name..":"..tx..":"..ty
    if parent[key]==nil then
      parent[key]=key
      items[#items+1]={key=key,name=res.name,position=res.position,amount=res.amount,tx=tx,ty=ty,infinite=res.infinite}
    end
  end
  local function find(a)
    local root=a
    while parent[root]~=root do root=parent[root] end
    while parent[a]~=root do local next_node=parent[a]; parent[a]=root; a=next_node end
    return root
  end
  local function union(a,b)
    local ra,rb=find(a),find(b)
    if ra~=rb then parent[rb]=ra end
  end
  for _,item in ipairs(items) do
    for dx=-1,1 do
      for dy=-1,1 do
        if dx~=0 or dy~=0 then
          local neighbour=item.name..":"..(item.tx+dx)..":"..(item.ty+dy)
          if parent[neighbour]~=nil then union(item.key,neighbour) end
        end
      end
    end
  end
  local groups={}
  for _,item in ipairs(items) do
    local root=find(item.key)
    local group=groups[root]
    if not group then
      group={name=item.name,tiles=0,totalAmount=0,yieldPercent=0,infinite=false,minx=math.huge,miny=math.huge,maxx=-math.huge,maxy=-math.huge,sumx=0,sumy=0,tileKeys={}}
      groups[root]=group
    end
    group.tiles=group.tiles+1
    group.tileKeys[math.floor(item.position.x)..":"..math.floor(item.position.y)]=true
    -- Infinite resources (crude oil) carry a yield percentage, not a finite
    -- amount, so they are reported separately instead of inflating totalAmount.
    if item.infinite then
      group.infinite=true
      group.yieldPercent=group.yieldPercent+item.amount
    else
      group.totalAmount=group.totalAmount+item.amount
    end
    group.sumx=group.sumx+item.position.x
    group.sumy=group.sumy+item.position.y
    if item.position.x<group.minx then group.minx=item.position.x end
    if item.position.y<group.miny then group.miny=item.position.y end
    if item.position.x>group.maxx then group.maxx=item.position.x end
    if item.position.y>group.maxy then group.maxy=item.position.y end
  end
  local list={}
  for _,group in pairs(groups) do
    group.sortAmount=(group.totalAmount>0) and group.totalAmount or group.yieldPercent
    list[#list+1]=group
  end
  table.sort(list,function(a,b)
    if a.sortAmount~=b.sortAmount then return a.sortAmount>b.sortAmount end
    if a.name~=b.name then return a.name<b.name end
    if a.minx~=b.minx then return a.minx<b.minx end
    return a.miny<b.miny
  end)
  -- Drills are matched by their mining area (mining_drill_radius around the
  -- drill), not by distance, so a drill next to a patch it cannot mine is never
  -- reported for it, and every reported drill carries its engine status.
  local max_mining_radius=radius
  for _,drill_proto in pairs(prototypes.entity) do
    if drill_proto.type=="mining-drill" then
      local value=drill_proto.mining_drill_radius
      if type(value)=="number" and value>max_mining_radius then max_mining_radius=value end
    end
  end
  local function drill_radius(d)
    local value=d.prototype.mining_drill_radius
    if type(value)=="number" then return value end
    return radius
  end
  for _,group in ipairs(list) do
    local drills,seen={},{}
    local covered={}
    local covered_tiles,covered_amount=0,0
    local found=surface.find_entities_filtered{area={{group.minx-max_mining_radius,group.miny-max_mining_radius},{group.maxx+max_mining_radius,group.maxy+max_mining_radius}},type="mining-drill",force=force}
    for _,d in pairs(found) do
      if not seen[d.unit_number] then
        seen[d.unit_number]=true
        local r=drill_radius(d)
        local mine_area={{d.position.x-r,d.position.y-r},{d.position.x+r,d.position.y+r}}
        local drill_tiles,drill_amount=0,0
        for _,resource in pairs(surface.find_entities_filtered{area=mine_area,type="resource",name=group.name}) do
          local key=math.floor(resource.position.x)..":"..math.floor(resource.position.y)
          -- A drill next to several patches must only credit tiles of this
          -- group; otherwise a one-tile patch can report more covered tiles
          -- than it owns.
          if group.tileKeys[key] then
            local amount=resource.amount or 0
            drill_tiles=drill_tiles+1
            drill_amount=drill_amount+amount
            if not covered[key] then covered[key]=true; covered_tiles=covered_tiles+1; covered_amount=covered_amount+amount end
          end
        end
        if drill_tiles>0 then
          drills[#drills+1]={kind="drill",name=d.name,position=d.position,unitNumber=d.unit_number,status=status_names[d.status],coveredTiles=drill_tiles,coveredAmount=drill_amount}
        end
      end
    end
    table.sort(drills,less)
    local clipped=(group.minx<=area[1][1]+0.5 or group.maxx>=area[2][1]-0.5 or group.miny<=area[1][2]+0.5 or group.maxy>=area[2][2]-0.5)
    patches[#patches+1]={name=group.name,tiles=group.tiles,totalAmount=group.totalAmount,infinite=group.infinite,yieldPercent=group.yieldPercent,bounds={leftTopX=group.minx,leftTopY=group.miny,rightBottomX=group.maxx,rightBottomY=group.maxy},centroid={x=group.sumx/group.tiles,y=group.sumy/group.tiles},nearbyDrills=drills,coveredTiles=covered_tiles,coveredAmount=covered_amount,clippedByArea=clipped}
  end
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=area_result,drillRadius=radius,pageInfo={total=#entries,limit=limit,offset=offset,hasMore=#entries>offset+limit},resourceTotal=resource_total,obstacleTotal=obstacle_total,waterTotal=water_total,worldEntries=result,patches=patches})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[WorldResourcesPage](response)
}

func worldPlacementQuery(r *Resolver, input *WorldPlacementInput) (*WorldPlacementResult, error) {
	if input == nil || input.Surface == "" || input.Force == "" || input.Prototype == "" {
		return nil, errors.New("surface, force and prototype are required")
	}
	if input.Position == nil {
		return nil, errors.New("position is required")
	}
	if input.Direction < 0 || input.Direction > 15 {
		return nil, errors.New("direction must be between 0 and 15")
	}
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local prototype=%s; local x,y=%s,%s; local direction=%d
`, luaString(input.Surface), luaString(input.Force), luaString(input.Prototype), strconv.FormatFloat(input.Position.X, 'g', -1, 64), strconv.FormatFloat(input.Position.Y, 'g', -1, 64), input.Direction) + worldCommonLua + `
local proto=prototypes.entity[prototype]
if not proto then fail("entity prototype not found"); return end
-- manual is the check a player placing by hand gets; the engine default only
-- tests collisions, so an offshore pump would pass on dry land.
local build_check=defines.build_check_type.manual
local w=proto.tile_width or 1
local h=proto.tile_height or 1
if direction==4 or direction==12 then w,h=h,w end
local layers=proto.collision_mask.layers
local position={x=x,y=y}
local can_place=surface.can_place_entity{name=prototype,position=position,direction=direction,force=force,build_check_type=build_check}
local blockers={}
local terrain=false
local tile=surface.get_tile(math.floor(x),math.floor(y))
local tile_name=(tile and tile.valid) and tile.name or ""
if not can_place then
  -- The refusal comes from the whole footprint, so blockers and terrain are
  -- collected over every tile the entity would occupy, not just the center tile.
  local seen={}
  for _,e in pairs(surface.find_entities_filtered{area={{x-w/2,y-h/2},{x+w/2,y+h/2}},collision_mask=layers}) do
    if e.valid and not seen[e.name] then seen[e.name]=true; blockers[#blockers+1]=e.name end
  end
  table.sort(blockers)
  local x0,x1=math.floor(x-w/2+0.001),math.ceil(x+w/2-0.001)-1
  local y0,y1=math.floor(y-h/2+0.001),math.ceil(y+h/2-0.001)-1
  for ty=y0,y1 do for tx=x0,x1 do
    local footprint_tile=surface.get_tile(tx,ty)
    local mask=footprint_tile and footprint_tile.valid and footprint_tile.prototype.collision_mask or nil
    local tile_layers=mask and mask.layers or {}
    for layer,_ in pairs(layers) do if tile_layers[layer] then terrain=true end end
  end end
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,prototype=prototype,position=position,direction=direction,canPlace=can_place,blockers=blockers,terrain=terrain,tile=tile_name})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[WorldPlacementResult](response)
}
