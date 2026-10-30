package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

func adjacencyParams(area *FactoryAreaInput, force, surface string, tolerance float64, limit int) error {
	if surface == "" || force == "" {
		return errors.New("surface and force are required")
	}
	if math.IsNaN(tolerance) || math.IsInf(tolerance, 0) || tolerance < 0 || tolerance > 4 {
		return errors.New("tolerance must be finite and between 0 and 4 tiles")
	}
	if limit < 1 || limit > 2000 {
		return errors.New("limit must be between 1 and 2000")
	}
	return nil
}

// adjacencyLua reports geometric neighbours, then verifies each pair against
// engine connection objects so touching yet unconnected entities stay visible.
// Geometry alone never claims item or fluid movement.
const adjacencyLua = `
local skip={character=true,unit=true,["spider-unit"]=true,["construction-robot"]=true,["logistic-robot"]=true,["combat-robot"]=true,["spider-leg"]=true,corpse=true,["item-entity"]=true,["entity-ghost"]=true,["tile-ghost"]=true,["item-request-proxy"]=true}
local candidates={}
for _,e in pairs(surface.find_entities_filtered{area=area,force=force}) do
  if valid(e) and not skip[e.type] then candidates[#candidates+1]=e end
end
table.sort(candidates,less)
local truncated=false
if #candidates>max_nodes then
  truncated=true
  local trimmed={}
  for i=1,max_nodes do trimmed[i]=candidates[i] end
  candidates=trimmed
end
local members={}
for _,e in ipairs(candidates) do members[key(e)]=true end
local nodes={}
for _,e in ipairs(candidates) do nodes[#nodes+1]=endpoint(e) end
local function gap(a,b)
  local ab,bb=a.bounding_box,b.bounding_box
  local dx=math.max(ab.left_top.x-bb.right_bottom.x,bb.left_top.x-ab.right_bottom.x,0)
  local dy=math.max(ab.left_top.y-bb.right_bottom.y,bb.left_top.y-ab.right_bottom.y,0)
  return math.sqrt(dx*dx+dy*dy)
end
local function belt_pair(a,b)
  if not (is_belt(a) and is_belt(b)) then return nil end
  if a.type=="underground-belt" and a.neighbours==b then return "underground" end
  if b.type=="underground-belt" and b.neighbours==a then return "underground" end
  if a.type=="linked-belt" and a.linked_belt_neighbour==b then return "linked" end
  if b.type=="linked-belt" and b.linked_belt_neighbour==a then return "linked" end
  for _,n in pairs(a.belt_neighbours.inputs) do if n==b then return "belt" end end
  for _,n in pairs(a.belt_neighbours.outputs) do if n==b then return "belt" end end
  for _,n in pairs(b.belt_neighbours.inputs) do if n==a then return "belt" end end
  for _,n in pairs(b.belt_neighbours.outputs) do if n==a then return "belt" end end
  return nil
end
local function inserter_pair(a,b)
  if a.type=="inserter" then
    if a.pickup_target==b then return "pickup" end
    if a.drop_target==b then return "drop" end
  end
  if b.type=="inserter" then
    if b.pickup_target==a then return "pickup" end
    if b.drop_target==a then return "drop" end
  end
  return nil
end
local function loader_pair(a,b)
  if (a.type=="loader" or a.type=="loader-1x1") and a.loader_container==b then return "loader" end
  if (b.type=="loader" or b.type=="loader-1x1") and b.loader_container==a then return "loader" end
  return nil
end
local function fluid_pair(a,b)
  for _,link in ipairs(fluid_links(a)) do if link.a==b or link.b==b then return "fluid" end end
  for _,link in ipairs(fluid_links(b)) do if link.a==a or link.b==a then return "fluid" end end
  return nil
end
local function connection_kind(a,b)
  return belt_pair(a,b) or inserter_pair(a,b) or loader_pair(a,b) or fluid_pair(a,b) or ""
end
local pair_list,seen={},{}
local full=false
for i=1,#candidates do
  local a=candidates[i]
  local bb=a.bounding_box
  local query={left_top={x=bb.left_top.x-tolerance-1e-6,y=bb.left_top.y-tolerance-1e-6},
    right_bottom={x=bb.right_bottom.x+tolerance+1e-6,y=bb.right_bottom.y+tolerance+1e-6}}
  for _,b in pairs(surface.find_entities_filtered{area=query,force=force}) do
    if valid(b) and not skip[b.type] then
      local ka,kb=key(a),key(b)
      if ka<kb and members[kb] then
        local dedupe=ka.."\0"..kb
        if not seen[dedupe] then
          local g=gap(a,b)
          if g<=tolerance+1e-6 then
            seen[dedupe]=true
            if full then truncated=true; break end
            pair_list[#pair_list+1]={a=endpoint(a),b=endpoint(b),connection=connection_kind(a,b),gap=math.floor(g*1000+0.5)/1000}
            if #pair_list>=max_pairs then full=true end
          end
        end
      end
    end
  end
  if truncated then break end
end
local function endpoint_less(x,y)
  if x.surface~=y.surface then return x.surface<y.surface end
  if x.position.x~=y.position.x then return x.position.x<y.position.x end
  if x.position.y~=y.position.y then return x.position.y<y.position.y end
  if x.name~=y.name then return x.name<y.name end
  return (x.unitNumber or 0)<(y.unitNumber or 0)
end
table.sort(pair_list,function(p,q)
  if endpoint_less(p.a,q.a) then return true end
  if endpoint_less(q.a,p.a) then return false end
  return endpoint_less(p.b,q.b)
end)
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=area_result,
  tolerance=tolerance,limit=max_nodes,truncated=truncated,nodes=nodes,pairs=pair_list})
`

func adjacencyQuery(r *Resolver, area *FactoryAreaInput, force, surface string, tolerance float64, limit int) (*AdjacencyGraph, error) {
	if err := adjacencyParams(area, force, surface, tolerance, limit); err != nil {
		return nil, err
	}
	areaLua, areaResult, err := worldAreaBoundsLua(area, 64)
	if err != nil {
		return nil, err
	}
	lua := luaFail + topologyCommonLua + fmt.Sprintf(`
local surface=game.surfaces[%s]
if not surface then fail("surface not found"); return end
local force=game.forces[%s]
if not force then fail("force not found"); return end
local area,area_result=%s,%s
local tolerance=%s
local max_nodes=%d
local max_pairs=%d
`, luaString(surface), luaString(force), areaLua, areaResult,
		strconv.FormatFloat(tolerance, 'g', -1, 64), limit, limit) + adjacencyLua
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[AdjacencyGraph](response)
}
