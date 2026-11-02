package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// placementProbeQuery samples engine placement legality for one prototype on
// every tile center of a bounded area and returns it in the requested shape:
// blocked tiles with their blockers, a compact placeability map, or the largest
// empty tile rectangles. One RCON round trip answers a belt-routing or
// build-site question that would otherwise cost one call per tile.
func placementProbeQuery(r *Resolver, input *PlacementProbeInput) (*PlacementProbeResult, error) {
	if input == nil || input.Surface == "" || input.Force == "" || input.Prototype == "" {
		return nil, errors.New("surface, force and prototype are required")
	}
	if input.Direction < 0 || input.Direction > 15 {
		return nil, errors.New("direction must be between 0 and 15")
	}
	area, areaResult, err := worldAreaBoundsLua(input.Area, maxAreaTiles)
	if err != nil {
		return nil, err
	}
	columns := math.Ceil(input.Area.RightBottomX) - math.Floor(input.Area.LeftTopX)
	rows := math.Ceil(input.Area.RightBottomY) - math.Floor(input.Area.LeftTopY)
	if samples := columns * rows; samples > maxProbeSamples {
		return nil, fmt.Errorf("probe area samples %g tiles, above the %d tile limit; shrink the area", samples, maxProbeSamples)
	}
	mode := strings.ToUpper(string(input.Mode))
	if mode == "" {
		mode = string(PlacementProbeModeBlockedTiles)
	}
	siteWidth, siteHeight, maxSites := 1.0, 1.0, 0
	switch mode {
	case string(PlacementProbeModeBlockedTiles), string(PlacementProbeModeGrid):
	case string(PlacementProbeModeSites):
		siteWidth, siteHeight = input.SiteWidth, input.SiteHeight
		for _, n := range []float64{siteWidth, siteHeight} {
			if math.IsNaN(n) || math.IsInf(n, 0) || n < 1 {
				return nil, errors.New("site width and height must be finite and at least 1")
			}
		}
		if siteWidth > columns || siteHeight > rows {
			return nil, errors.New("site width and height must not exceed the probe area")
		}
		maxSites = input.MaxSites
		if maxSites < 1 || maxSites > maxProbeSites {
			return nil, fmt.Errorf("maxSites must be between 1 and %d", maxProbeSites)
		}
	default:
		return nil, fmt.Errorf("unsupported placement probe mode %q", mode)
	}
	lua := luaFail + luaTileOccupants + fmt.Sprintf(`local surface_name,force_name=%s,%s; local prototype=%s; local direction=%d; local area,area_result=%s,%s
`, luaString(input.Surface), luaString(input.Force), luaString(input.Prototype), input.Direction, area, areaResult) + worldCommonLua + placementProbeLua(mode, siteWidth, siteHeight, maxSites)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[PlacementProbeResult](response)
}

// placementProbeLua builds the body after the shared surface/force/area
// preamble. The grid is always built because the counts and every mode share
// the same engine checks; only the reporting differs.
func placementProbeLua(mode string, siteWidth, siteHeight float64, maxSites int) string {
	n := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
	body := `
local proto=prototypes.entity[prototype]
if not proto then fail("entity prototype not found"); return end
-- manual is the check a player placing by hand gets; the engine default only
-- tests collisions.
local build_check=defines.build_check_type.manual
local layers=proto.collision_mask.layers
local w=proto.tile_width or 1
local h=proto.tile_height or 1
if direction==4 or direction==12 then w,h=h,w end
local left,top,right,bottom=math.floor(area[1][1]),math.floor(area[1][2]),math.ceil(area[2][1]),math.ceil(area[2][2])
local cols,rows=right-left,bottom-top
-- The footprint of an entity centered inside the area can overhang it, so the
-- terrain prefilter is grown by half the footprint.
local margin=math.max(math.ceil(w/2),math.ceil(h/2))
local terrain={}
for _,t in pairs(surface.find_tiles_filtered{area={{left-margin,top-margin},{right+margin,bottom+margin}},collision_mask=layers}) do
  if t.valid then terrain[t.position.x..":"..t.position.y]=true end
end
local function footprint(px,py)
  return math.floor(px-w/2+0.001),math.ceil(px+w/2-0.001)-1,math.floor(py-h/2+0.001),math.ceil(py+h/2-0.001)-1
end
local function footprint_blocks(px,py)
  local x0,x1,y0,y1=footprint(px,py)
  for fy=y0,y1 do for fx=x0,x1 do if terrain[fx..":"..fy] then return true end end end
  return false
end
local sampled,placeable_count=0,0
local grid={}
local blocked={}
for tx=left,right-1 do
  for ty=top,bottom-1 do
    local position={x=tx+0.5,y=ty+0.5}
    sampled=sampled+1
    local key=(ty-top)*cols+(tx-left+1)
    -- can_place_entity is the engine's manual check, but it accepts a position it
    -- would fast-replace: a belt on a belt of another direction passes, so the
    -- answer changes with the probe direction. A tile holding a real entity is
    -- occupied for placement planning, so it is blocked whatever the direction.
    -- tile_occupants keeps resources, ghosts, item stacks and corpses out of the
    -- way: it keeps only entities whose collision mask intersects the probe
    -- prototype's mask, so a tree-stump corpse (doodad layer, tiles only) never
    -- blocks the tile while a belt or inserter does.
    local occupants=tile_occupants(surface,position,layers)
    if #occupants==0 and surface.can_place_entity{name=prototype,position=position,direction=direction,force=force,build_check_type=build_check} then
      placeable_count=placeable_count+1
      grid[key]=true
    else
      grid[key]=false
      if collect_blocked then
        -- A refusal can come from any tile of the footprint, so the whole
        -- footprint is searched instead of the tile under the center. Occupants
        -- of the sampled tile are merged in because the engine may refuse a
        -- fast-replaceable tile without reporting a collision; the footprint
        -- scan filters by the probe prototype's collision mask, so decorations
        -- and corpses are never named.
        local names,seen={},{}
        for _,name in ipairs(occupants) do if not seen[name] then seen[name]=true; names[#names+1]=name end end
        local found=surface.find_entities_filtered{area={{position.x-w/2,position.y-h/2},{position.x+w/2,position.y+h/2}},collision_mask=layers}
        for _,e in pairs(found) do if not seen[e.name] then seen[e.name]=true; names[#names+1]=e.name end end
        table.sort(names)
        local tile=surface.get_tile(tx,ty)
        local tile_name=tile and tile.valid and tile.name or ""
        blocked[#blocked+1]={position=position,blockers=names,terrain=footprint_blocks(position.x,position.y),tile=tile_name}
      end
    end
  end
end
local row_strings={}
local sites={}
`
	var tail string
	switch mode {
	case string(PlacementProbeModeGrid):
		tail = `
for ry=0,rows-1 do
  local parts={}
  for rx=1,cols do parts[rx]=grid[ry*cols+rx] and "1" or "0" end
  row_strings[#row_strings+1]=table.concat(parts)
end
`
	case string(PlacementProbeModeSites):
		tail = fmt.Sprintf(`
local minw,minh,maxsites=%s,%s,%d
local function largest_site()
  local best=nil
  local heights={}
  for i=1,cols do heights[i]=0 end
  for ry=0,rows-1 do
    for i=1,cols do
      if grid[ry*cols+i] then heights[i]=heights[i]+1 else heights[i]=0 end
    end
    local stack={}
    for i=1,cols+1 do
      local h=(i<=cols) and heights[i] or 0
      local start=i
      while #stack>0 and stack[#stack].h>=h do
        local top_seg=stack[#stack]
        table.remove(stack)
        local w,hgt=i-top_seg.start,top_seg.h
        if hgt>=minh and w>=minw then
          local area_now=w*hgt
          if best==nil or area_now>best.area then
            best={area=area_now,x=top_seg.start-1,y=ry-hgt+1,w=w,h=hgt}
          end
        end
        start=top_seg.start
      end
      stack[#stack+1]={h=h,start=start}
    end
  end
  return best
end
for _=1,maxsites do
  local site=largest_site()
  if not site then break end
  sites[#sites+1]={origin={x=left+site.x,y=top+site.y},width=site.w,height=site.h}
  for j=site.y,site.y+site.h-1 do
    for i=site.x+1,site.x+site.w do grid[j*cols+i]=false end
  end
end
`, n(siteWidth), n(siteHeight), maxSites)
	}
	collectBlocked := "false"
	if mode == string(PlacementProbeModeBlockedTiles) {
		collectBlocked = "true"
	}
	return fmt.Sprintf("local collect_blocked=%s\n", collectBlocked) + body + tail + `
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,prototype=prototype,direction=direction,area=area_result,sampled=sampled,placeable=placeable_count,blocked=sampled-placeable_count,blockedTiles=blocked,rows=row_strings,sites=sites})`
}
