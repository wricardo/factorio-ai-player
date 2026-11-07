package main

import (
	"errors"
	"fmt"
	"math"
)

// occupancyTerrainMaxTiles bounds the tile scan of the occupancy query: one tile
// read per covered tile, so the area cap keeps the backend call small.
const occupancyTerrainMaxTiles = 65536

// occupancyMaxTerrainRows bounds the terrain rows a single response carries.
const occupancyMaxTerrainRows = 65536

// occupancyQuery reports the entities and blocking terrain tiles of one area in
// a machine-readable shape for local layout planning.
//
// The report is deliberately limited to what the engine states cheaply: entity
// footprints (prototype tile size with the direction swap), direction and unit
// number, plus blocking tile prototypes when terrain is requested. It does not
// claim placement legality for a specific prototype, because collision masks
// differ per prototype; that stays a worldPlacement or probePlacement question.
func occupancyQuery(r *Resolver, input OccupancyInput) (*OccupancyPage, error) {
	if input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must not be negative")
	}
	area, areaResult, err := worldAreaBoundsLua(input.Area, maxAreaTiles)
	if err != nil {
		return nil, err
	}
	terrainLimit := input.TerrainLimit
	if terrainLimit == 0 {
		terrainLimit = 4096
	}
	if terrainLimit < 1 || terrainLimit > occupancyMaxTerrainRows {
		return nil, fmt.Errorf("terrainLimit must be between 1 and %d", occupancyMaxTerrainRows)
	}
	if input.IncludeTerrain {
		width := math.Ceil(input.Area.RightBottomX) - math.Floor(input.Area.LeftTopX)
		height := math.Ceil(input.Area.RightBottomY) - math.Floor(input.Area.LeftTopY)
		if width*height > occupancyTerrainMaxTiles {
			return nil, fmt.Errorf("includeTerrain needs an area of at most %d tiles; this one covers %g",
				occupancyTerrainMaxTiles, width*height)
		}
	}
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area,area_result=%s,%s
local offset,limit=%d,%d; local include_terrain=%t; local terrain_limit=%d
`, luaString(input.Surface), luaString(input.Force), area, areaResult, input.Offset, input.Limit,
		input.IncludeTerrain, terrainLimit) + worldCommonLua + `
-- Incidental content never blocks a build, so it is not occupancy.
local ignored={["item-entity"]=true,corpse=true,["character-corpse"]=true,fish=true,["particle-source"]=true,["smoke-with-trigger"]=true,sticker=true,["highlight-box"]=true,["deconstructible-tile-proxy"]=true,explosion=true,fire=true,projectile=true,beam=true,["spider-leg"]=true,["artillery-projectile"]=true,["combat-robot"]=true,["rocket-silo-rocket"]=true}
local left,top,right,bottom=math.floor(area[1][1]),math.floor(area[1][2]),math.ceil(area[2][1]),math.ceil(area[2][2])
local entries={}
for _,e in ipairs(surface.find_entities_filtered{area={{left,top},{right,bottom}}}) do
  local kind=e.type
  if not ignored[kind] then
    local name=e.name
    local ghost_name=nil
    if kind=="entity-ghost" then ghost_name=e.ghost_name end
    local size_proto=prototypes.entity[ghost_name or name]
    if kind~="tile-ghost" then
      local w,h=1,1
      if size_proto then w,h=size_proto.tile_width or 1,size_proto.tile_height or 1 end
      if e.direction==4 or e.direction==12 then w,h=h,w end
      entries[#entries+1]={unitNumber=e.unit_number and tostring(e.unit_number) or nil,name=name,type=kind,
        force=e.force and e.force.name or nil,position={x=e.position.x,y=e.position.y},direction=e.direction,
        footprint={leftTopX=e.position.x-w/2,leftTopY=e.position.y-h/2,rightBottomX=e.position.x+w/2,rightBottomY=e.position.y+h/2},
        ghostName=ghost_name}
    end
  end
end
-- The scan order is not stable across engines, so sort before paging.
table.sort(entries,function(a,b)
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  return a.name<b.name
end)
local total=#entries
local page={}
for i=offset+1,math.min(offset+limit,total) do page[#page+1]=entries[i] end
local terrain={}
local terrain_truncated=false
if include_terrain then
  for x=left,right-1 do
    for y=top,bottom-1 do
      local tile=surface.get_tile(x,y)
      local proto=tile and tile.prototype
      local mask=proto and proto.collision_mask and proto.collision_mask.layers
      local blocking=false
      -- Every tile carries ground_tile; any other layer (water_tile, empty_space) is a blocker.
      if mask then for layer in pairs(mask) do if layer~="ground_tile" then blocking=true break end end end
      if blocking then
        if #terrain<terrain_limit then terrain[#terrain+1]={position={x=x+0.5,y=y+0.5},tile=tile.name} else terrain_truncated=true end
      end
    end
  end
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,
  area={leftTopX=left,leftTopY=top,rightBottomX=right,rightBottomY=bottom},
  entries=page,terrain=terrain,terrainTruncated=terrain_truncated,
  pageInfo={total=total,limit=limit,offset=offset,hasMore=offset+limit<total}})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[OccupancyPage](response)
}
