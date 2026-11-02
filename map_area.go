package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// mapAreaMaxDimension bounds each output dimension of a textual map.
const mapAreaMaxDimension = maxMapDimension

// mapAreaQuery renders one bounded area as a compact character grid so headless
// clients get spatial context without screenshots or paged entity lists.
func mapAreaQuery(r *Resolver, input MapAreaInput) (*MapArea, error) {
	if input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.MaxRows < 1 || input.MaxRows > mapAreaMaxDimension {
		return nil, fmt.Errorf("maxRows must be between 1 and %d", mapAreaMaxDimension)
	}
	if input.MaxColumns < 1 || input.MaxColumns > mapAreaMaxDimension {
		return nil, fmt.Errorf("maxColumns must be between 1 and %d", mapAreaMaxDimension)
	}
	area, areaResult, err := worldAreaBoundsLua(input.Area, maxAreaTiles)
	if err != nil {
		return nil, err
	}
	layer := string(input.Layer)
	switch layer {
	case "", "ALL":
		layer = "ALL"
	case "FACTORY", "RESOURCES":
	default:
		return nil, fmt.Errorf("unsupported map layer %q", layer)
	}
	width := math.Ceil(input.Area.RightBottomX) - math.Floor(input.Area.LeftTopX)
	height := math.Ceil(input.Area.RightBottomY) - math.Floor(input.Area.LeftTopY)
	cell := math.Max(math.Ceil(width/float64(input.MaxColumns)), math.Ceil(height/float64(input.MaxRows)))
	if cell < 1 {
		cell = 1
	}
	columns := int(math.Ceil(width / cell))
	if columns < 1 {
		columns = 1
	}
	rows := int(math.Ceil(height / cell))
	if rows < 1 {
		rows = 1
	}
	n := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area,area_result=%s,%s
local layer=%s; local cell_size=%s; local columns,rows=%d,%d
local left,top=math.floor(area[1][1]),math.floor(area[1][2])
`, luaString(input.Surface), luaString(input.Force), area, areaResult, luaString(layer), n(cell), columns, rows) + worldCommonLua + `
-- Incidental entities (dropped items, corpses, effects, projectiles) are not map content.
local ignored_entities={["item-entity"]=true,corpse=true,["character-corpse"]=true,fish=true,["particle-source"]=true,["smoke-with-trigger"]=true,sticker=true,["highlight-box"]=true,["tile-ghost"]=true,["deconstructible-tile-proxy"]=true,explosion=true,fire=true,projectile=true,beam=true,["spider-leg"]=true,["artillery-projectile"]=true,["combat-robot"]=true,["rocket-silo-rocket"]=true}
local function priority(e)
  local t=e.type
  if ignored_entities[t] then return 0 end
  if t=="character" then return 100 end
  if t=="assembling-machine" or t=="furnace" or t=="mining-drill" or t=="lab" or t=="rocket-silo" then return 90 end
  if t=="inserter" then return 80 end
  if t=="transport-belt" or t=="underground-belt" or t=="splitter" or t=="loader" or t=="loader-1x1" or t=="linked-belt" then return 70 end
  -- Enemy units and spawners outrank plain infrastructure so threats stay visible.
  if force.is_enemy(e.force) then return 65 end
  if t=="pipe" or t=="pipe-to-ground" or t=="pump" or t=="storage-tank" or t=="boiler" or t=="offshore-pump" or t=="heat-exchanger" or t=="generator" or t=="steam-engine" or t=="steam-turbine" then return 60 end
  if t=="electric-pole" then return 55 end
  if t=="container" or t=="logistic-container" or t=="infinity-container" or t=="linked-container" then return 50 end
  if t=="entity-ghost" then return 47 end
  if t=="resource" then return 40 end
  if t=="tree" then return 30 end
  if t=="simple-entity" then return 20 end
  if t=="cliff" then return 10 end
  -- Every other type keeps a default rank instead of disappearing (radars, lamps,
  -- walls, gates, turrets, accumulators, solar panels, roboports, beacons,
  -- combinators, rails, train stops, vehicles, and modded entities).
  return 45
end
local natural={resource=true,tree=true,["simple-entity"]=true,cliff=true}
local function allowed(e,p)
  if p<=0 then return false end
  if layer=="FACTORY" then return not natural[e.type] end
  if layer=="RESOURCES" then return e.type=="resource" end
  return true
end
local function included(e)
  if natural[e.type] then return true end
  if e.force==force then return true end
  if e.type=="entity-ghost" then return true end
  -- Enemy-force entities are rendered so a threat picture and a factory picture
  -- can be read in one map.
  return force.is_enemy(e.force)
end
local candidates={}
for _,e in pairs(surface.find_entities_filtered{area=area}) do
  local p=priority(e)
  if allowed(e,p) and included(e) then
    candidates[#candidates+1]={entity=e,priority=p,position=e.position,unitNumber=e.unit_number}
  end
end
table.sort(candidates,function(a,b)
  if a.priority~=b.priority then return a.priority>b.priority end
  if a.entity.name~=b.entity.name then return a.entity.name<b.entity.name end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  return (a.unitNumber or 0)<(b.unitNumber or 0)
end)
local cells={}
-- Paint every cell an entity's footprint covers, so a multi-tile machine does
-- not leave its remaining cells looking empty or like ore underneath. Candidates
-- are already ordered by descending priority, so the first paint of a cell wins.
local function paint(entity)
  local ok_ghost,ghost_proto=pcall(function() return entity.ghost_prototype end)
  local proto=(ok_ghost and ghost_proto) or entity.prototype
  local fw=proto.tile_width or 1
  local fh=proto.tile_height or 1
  if entity.direction==4 or entity.direction==12 then fw,fh=fh,fw end
  local px,py=entity.position.x,entity.position.y
  local x0=math.floor(px-fw/2+0.001); local x1=math.ceil(px+fw/2-0.001)-1
  local y0=math.floor(py-fh/2+0.001); local y1=math.ceil(py+fh/2-0.001)-1
  for ty=y0,y1 do
    local cy=math.floor((ty-top)/cell_size)
    if cy>=0 and cy<rows then
      for tx=x0,x1 do
        local cx=math.floor((tx-left)/cell_size)
        if cx>=0 and cx<columns then
          local index=cy*columns+cx+1
          if not cells[index] then
            cells[index]={name=entity.name,type=entity.type,unitNumber=entity.unit_number}
          end
        end
      end
    end
  end
end
for _,candidate in ipairs(candidates) do paint(candidate.entity) end
-- Water tiles carry a pumpable fluid; out-of-map tiles share the water_tile
-- collision layer but are not water.
local function is_water(tile)
  if not tile or not tile.valid then return false end
  local ok,fluid=pcall(function() return tile.prototype.fluid end)
  return ok and fluid~=nil
end
local ALPHABET="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,-/:;<=>@[]^_{|}"
local char_by_name={}; local next_char=1; local legend={}; local char_counts={}; local other_entry=nil
local function assign(name,type,unitNumber)
  local char=char_by_name[name]
  if not char then
    if next_char<=#ALPHABET then
      char=string.sub(ALPHABET,next_char,next_char); next_char=next_char+1
    else
      char="?"
      if not other_entry then other_entry={char="?",name="other",type="other"} end
    end
    char_by_name[name]=char
    if char~="?" then legend[#legend+1]={char=char,name=name,type=type,unitNumber=unitNumber} end
  end
  char_counts[char]=(char_counts[char] or 0)+1
  return char
end
local rows_out={}
local empty_count,water_count=0,0
for ry=0,rows-1 do
  local parts={}
  for rx=0,columns-1 do
    local index=ry*columns+rx+1
    local cell=cells[index]
    if cell then
      parts[#parts+1]=assign(cell.name,cell.type,cell.unitNumber)
    else
      local tile=surface.get_tile(left+rx*cell_size,top+ry*cell_size)
      if is_water(tile) then parts[#parts+1]="~"; water_count=water_count+1
      else parts[#parts+1]="."; empty_count=empty_count+1 end
    end
  end
  rows_out[#rows_out+1]=table.concat(parts)
end
local ordered={}
if empty_count>0 then ordered[#ordered+1]={char=".",name="empty",type="empty",count=empty_count} end
if water_count>0 then ordered[#ordered+1]={char="~",name="water",type="tile",count=water_count} end
for _,entry in ipairs(legend) do entry.count=char_counts[entry.char] or 0; ordered[#ordered+1]=entry end
if other_entry then other_entry.count=char_counts["?"] or 0; ordered[#ordered+1]=other_entry end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=area_result,cellSize=cell_size,columns=columns,rows=rows_out,legend=ordered,truncated=cell_size>1})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[MapArea](response)
}
