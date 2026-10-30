package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxPlacementProbeSamples bounds one probe so a single RCON call cannot run an
// unbounded number of engine placement checks.
const maxPlacementProbeSamples = 16384

// maxProbeSites bounds the number of rectangles one SITES probe returns.
const maxProbeSites = 200

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
	area, areaResult, err := worldAreaBoundsLua(input.Area, 256)
	if err != nil {
		return nil, err
	}
	columns := math.Ceil(input.Area.RightBottomX) - math.Floor(input.Area.LeftTopX)
	rows := math.Ceil(input.Area.RightBottomY) - math.Floor(input.Area.LeftTopY)
	if samples := columns * rows; samples > maxPlacementProbeSamples {
		return nil, fmt.Errorf("probe area samples %g tiles, above the %d tile limit; shrink the area", samples, maxPlacementProbeSamples)
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
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local prototype=%s; local direction=%d; local area,area_result=%s,%s
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
local layers=proto.collision_mask.layers
local left,top,right,bottom=math.floor(area[1][1]),math.floor(area[1][2]),math.ceil(area[2][1]),math.ceil(area[2][2])
local cols,rows=right-left,bottom-top
local terrain={}
for _,t in pairs(surface.find_tiles_filtered{area=area,collision_mask=layers}) do
  if t.valid then terrain[t.position.x..":"..t.position.y]=true end
end
local sampled,placeable_count=0,0
local grid={}
local blocked={}
for tx=left,right-1 do
  for ty=top,bottom-1 do
    local position={x=tx+0.5,y=ty+0.5}
    sampled=sampled+1
    local key=(ty-top)*cols+(tx-left+1)
    if surface.can_place_entity{name=prototype,position=position,direction=direction,force=force} then
      placeable_count=placeable_count+1
      grid[key]=true
    else
      grid[key]=false
      if collect_blocked then
        local names,seen={},{}
        local found=surface.find_entities_filtered{area={{tx,ty},{tx+1,ty+1}},collision_mask=layers}
        for _,e in pairs(found) do if not seen[e.name] then seen[e.name]=true; names[#names+1]=e.name end end
        table.sort(names)
        local tile=surface.get_tile(tx,ty)
        local tile_name=tile and tile.valid and tile.name or ""
        blocked[#blocked+1]={position=position,blockers=names,terrain=terrain[tx..":"..ty]==true,tile=tile_name}
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
