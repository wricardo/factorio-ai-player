package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func topologyBeltParams(surface string, x, y float64, maxEntities int) error {
	if surface == "" {
		return errors.New("surface is required")
	}
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return errors.New("belt coordinates must be finite")
	}
	if maxEntities < 1 || maxEntities > 1000 {
		return errors.New("maxEntities must be between 1 and 1000")
	}
	return nil
}

// topologyCommonLua only follows engine connection objects. Entity-level edges
// deliberately do not claim lane routing, measured flow, or stalled machinery.
const topologyCommonLua = `
local belt_types={"transport-belt","underground-belt","splitter","loader","loader-1x1","linked-belt"}
local belt_set={}; for _,t in ipairs(belt_types) do belt_set[t]=true end
local statuses={}; for k,v in pairs(defines.entity_status) do statuses[v]=k end
local function valid(e) return e and e.valid end
local function is_belt(e) return valid(e) and belt_set[e.type] end
local function key(e)
  if e.unit_number then return tostring(e.surface.index)..":"..tostring(e.unit_number) end
  return tostring(e.surface.index)..":"..e.type..":"..e.name..":"..tostring(e.position.x)..":"..tostring(e.position.y)
end
local function less(a,b)
  if a.surface.index~=b.surface.index then return a.surface.index<b.surface.index end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  if a.name~=b.name then return a.name<b.name end
  return (a.unit_number or 0)<(b.unit_number or 0)
end
local endpoint_cache={}
local function endpoint(e)
  if not valid(e) then return nil end
  local k=key(e); if endpoint_cache[k] then return endpoint_cache[k] end
  local obj={name=e.name,type=e.type,surface=e.surface.name,position=e.position,unitNumber=e.unit_number,lanes={}}
  local ok,status=pcall(function() return e.status end); if ok then obj.status=statuses[status] end
  if is_belt(e) then
    for i=1,e.get_max_transport_line_index() do
      local items=e.get_transport_line(i).get_contents()
      table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "")<(b.quality or "") end)
      obj.lanes[#obj.lanes+1]={index=i,items=items}
    end
  elseif e.type=="inserter" then
    obj.pickupPosition=e.pickup_position; obj.dropPosition=e.drop_position
    local stack=e.held_stack
    if stack and stack.valid_for_read then obj.heldStack={name=stack.name,count=stack.count,quality=stack.quality.name} end
  end
  endpoint_cache[k]=obj
  return obj
end
local function connected_inserter(e)
  return {inserter=endpoint(e),pickup=endpoint(e.pickup_target),drop=endpoint(e.drop_target)}
end
local fluid_types={"offshore-pump","pipe","pipe-to-ground","pump","boiler","generator","fusion-generator","heat-exchanger","storage-tank"}
local fluid_set={}; for _,t in ipairs(fluid_types) do fluid_set[t]=true end
local function is_fluid(e) return valid(e) and fluid_set[e.type] end
-- Directed engine pipe connections. Each port reports its own connection, so a
-- pair of connected boxes can appear from both sides; flowDirection keeps the
-- raw engine value instead of guessing a physical direction.
local function fluid_links(e)
  local result={}
  if not is_fluid(e) then return result end
  local ok,fb=pcall(function() return e.fluidbox end)
  if not ok or not fb then return result end
  for i=1,#fb do
    for _,connection in pairs(fb.get_pipe_connections(i)) do
      local target=connection.target
      if target and target.valid and target.owner and target.owner.valid then
        local other=target.owner
        if other~=e then
          result[#result+1]={a=e,b=other,kind="fluid",flowDirection=connection.flow_direction}
        end
      end
    end
  end
  return result
end
local function edge_kind(a,b)
  if a.type=="underground-belt" and a.belt_to_ground_type=="input" and a.neighbours==b then return "underground" end
  if a.type=="linked-belt" and a.linked_belt_type=="input" and a.linked_belt_neighbour==b then return "linked" end
  return "belt"
end
local function belt_connections(e)
  local result,seen={},{}
  local function add(a,b)
    if not is_belt(a) or not is_belt(b) or key(a)==key(b) then return end
    local k=key(a)..">"..key(b)
    if not seen[k] then seen[k]=true; result[#result+1]={a=a,b=b,kind=edge_kind(a,b)} end
  end
  local neighbours=e.belt_neighbours
  for _,n in pairs(neighbours.inputs) do add(n,e) end
  for _,n in pairs(neighbours.outputs) do add(e,n) end
  if e.type=="underground-belt" then
    local n=e.neighbours
    if valid(n) then if e.belt_to_ground_type=="input" then add(e,n) else add(n,e) end end
  elseif e.type=="linked-belt" then
    local n=e.linked_belt_neighbour
    if valid(n) then if e.linked_belt_type=="input" then add(e,n) else add(n,e) end end
  end
  for i=1,e.get_max_transport_line_index() do
    local line=e.get_transport_line(i)
    for _,n in pairs(line.input_lines) do add(n.owner,e) end
    for _,n in pairs(line.output_lines) do add(e,n.owner) end
  end
  table.sort(result,function(a,b)
    local ka,kb=key(a.a)..">"..key(a.b),key(b.a)..">"..key(b.b)
    return ka<kb
  end)
  return result
end
local function collect_edges(members,raw)
  local edges,boundary,seen={},{},{}
  for _,link in ipairs(raw) do
    local k=key(link.a)..">"..key(link.b)..":"..link.kind..":"..tostring(link.flowDirection or "")
    if not seen[k] then
      seen[k]=true
      local outside=not members[key(link.a)] or not members[key(link.b)]
      local edge={from=endpoint(link.a),to=endpoint(link.b),kind=link.kind,boundary=outside,flowDirection=link.flowDirection}
      edges[#edges+1]=edge
      if outside then boundary[#boundary+1]=edge end
    end
  end
  return edges,boundary
end
local function belt_at(surface_name,x,y)
  local surface=game.surfaces[surface_name]
  if not surface then return nil,"surface not found" end
  local matches=surface.find_entities_filtered{position={x=x,y=y},type=belt_types}
  if #matches==0 then return nil,"belt not found at position" end
  if #matches>1 then return nil,"multiple belts at position" end
  return matches[1]
end
local function belt_run(seed,limit)
  local queue,members={seed},{[key(seed)]=true}
  local raw,inputs,outputs,surfaces={},{},{},{}
  local cursor,truncated=1,false
  while cursor<=#queue do
    local e=queue[cursor]; cursor=cursor+1
    surfaces[e.surface.index]=e.surface
    for _,link in ipairs(belt_connections(e)) do
      raw[#raw+1]=link
      inputs[key(link.b)]=true; outputs[key(link.a)]=true
      local n=link.a
      if key(n)==key(e) then n=link.b end
      local k=key(n)
      if not members[k] then
        if #queue<limit then members[k]=true; queue[#queue+1]=n else truncated=true end
      end
    end
  end
  table.sort(queue,less)
  local nodes,sources,sinks={},{},{}
  for _,e in ipairs(queue) do
    local obj=endpoint(e); nodes[#nodes+1]=obj
    if not inputs[key(e)] then sources[#sources+1]=obj end
    if not outputs[key(e)] then sinks[#sinks+1]=obj end
  end
  local inserter_entities={}
  for _,surface in pairs(surfaces) do
    for _,e in pairs(surface.find_entities_filtered{type="inserter"}) do
      local p,d=e.pickup_target,e.drop_target
      if (valid(p) and members[key(p)]) or (valid(d) and members[key(d)]) then inserter_entities[#inserter_entities+1]=e end
    end
  end
  table.sort(inserter_entities,less)
  local inserters={}; for _,e in ipairs(inserter_entities) do inserters[#inserters+1]=connected_inserter(e) end
  local edges,boundary=collect_edges(members,raw)
  return {tick=game.tick,surface=seed.surface.name,seed=endpoint(seed),maxEntities=limit,truncated=truncated,nodes=nodes,edges=edges,boundaryEdges=boundary,sources=sources,sinks=sinks,inserters=inserters},members
end
`

func beltRunQuery(r *Resolver, surface string, inputX, inputY *float64, unitNumber *string, maxEntities int) (*BeltRun, error) {
	surface, x, y, err := resolveBeltAddress(r, surface, unitNumber, inputX, inputY)
	if err != nil {
		return nil, err
	}
	if err := topologyBeltParams(surface, x, y, maxEntities); err != nil {
		return nil, err
	}
	lua := luaFail + topologyCommonLua + fmt.Sprintf(`
local seed,err=belt_at(%s,%s,%s)
if not seed then fail(err); return end
local result=belt_run(seed,%d)
rcon.print(helpers.table_to_json(result))`, luaString(surface), strconv.FormatFloat(x, 'g', -1, 64), strconv.FormatFloat(y, 'g', -1, 64), maxEntities)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltRun](response)
}

func inspectionBeltRuns(r *Resolver, obj *InspectionLogistics, maxEntities int) ([]*BeltRun, error) {
	if obj == nil {
		return nil, errors.New("inspection logistics is required")
	}
	if err := topologyBeltParams(obj.Surface, 0, 0, maxEntities); err != nil {
		return nil, err
	}
	var seeds strings.Builder
	for _, inserter := range obj.Inserters {
		if inserter == nil {
			continue
		}
		for _, e := range []*LogisticsEndpoint{inserter.Pickup, inserter.Drop} {
			if e == nil || !topologyBeltType(e.Type) {
				continue
			}
			if e.Position == nil {
				return nil, errors.New("belt endpoint position is required")
			}
			if err := topologyBeltParams(e.Surface, e.Position.X, e.Position.Y, maxEntities); err != nil {
				return nil, err
			}
			unit := "nil"
			if e.UnitNumber != nil {
				unit = luaString(*e.UnitNumber)
			}
			fmt.Fprintf(&seeds, "{surface=%s,x=%s,y=%s,name=%s,unit=%s},", luaString(e.Surface), strconv.FormatFloat(e.Position.X, 'g', -1, 64), strconv.FormatFloat(e.Position.Y, 'g', -1, 64), luaString(e.Name), unit)
		}
	}
	if seeds.Len() == 0 {
		return []*BeltRun{}, nil
	}
	lua := luaFail + topologyCommonLua + fmt.Sprintf(`
local seeds={%s}
local runs,covered={},{}
for _,request in ipairs(seeds) do
  local seed,err=belt_at(request.surface,request.x,request.y)
  if not seed then fail("inspection belt target changed: "..err); return end
  if seed.name~=request.name or (request.unit and tostring(seed.unit_number)~=request.unit) then fail("inspection belt target changed"); return end
  if not covered[key(seed)] then
    local run,members=belt_run(seed,%d)
    runs[#runs+1]=run
    for k,_ in pairs(members) do covered[k]=true end
  end
end
rcon.print(helpers.table_to_json{beltRuns=runs})`, seeds.String(), maxEntities)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[struct {
		BeltRuns []*BeltRun `json:"beltRuns"`
	}](response)
	if err != nil {
		return nil, err
	}
	return result.BeltRuns, nil
}

func topologyBeltType(entityType string) bool {
	switch entityType {
	case "transport-belt", "underground-belt", "splitter", "loader", "loader-1x1", "linked-belt":
		return true
	}
	return false
}

func factoryGraphQuery(r *Resolver, area *FactoryAreaInput, force, surface string) (*FactoryGraph, error) {
	if surface == "" || force == "" {
		return nil, errors.New("surface and force are required")
	}
	areaLua, areaResult, err := worldAreaLua(area, 1, 0)
	if err != nil {
		return nil, err
	}
	lua := luaFail + topologyCommonLua + fmt.Sprintf(`
local surface=game.surfaces[%s]
if not surface then fail("surface not found"); return end
local force=game.forces[%s]
if not force then fail("force not found"); return end
local area,area_result=%s,%s
`, luaString(surface), luaString(force), areaLua, areaResult) + `
local machine_set={ ["assembling-machine"]=true,furnace=true,["mining-drill"]=true,lab=true,["rocket-silo"]=true }
local chest_set={container=true,["logistic-container"]=true,["linked-container"]=true,["infinity-container"]=true}
local types={"assembling-machine","furnace","mining-drill","lab","rocket-silo","inserter","container","logistic-container","linked-container","infinity-container"}
for _,t in ipairs(belt_types) do types[#types+1]=t end
for _,t in ipairs(fluid_types) do types[#types+1]=t end
local entities=surface.find_entities_filtered{area=area,force=force,type=types}
table.sort(entities,less)
local members={}; for _,e in ipairs(entities) do members[key(e)]=true end
local nodes,machines,belts,chests,fluids,raw={},{},{},{},{},{}
for _,e in ipairs(entities) do
  local obj=endpoint(e); nodes[#nodes+1]=obj
  if machine_set[e.type] then
    machines[#machines+1]=obj
    local target=e.drop_target
    if valid(target) then raw[#raw+1]={a=e,b=target,kind="drop"} end
  elseif chest_set[e.type] then chests[#chests+1]=obj
  elseif is_fluid(e) then
    fluids[#fluids+1]=obj
    for _,link in ipairs(fluid_links(e)) do raw[#raw+1]=link end
  elseif is_belt(e) then
    belts[#belts+1]=obj
    for _,link in ipairs(belt_connections(e)) do raw[#raw+1]=link end
    if e.type=="loader" or e.type=="loader-1x1" then
      local container=e.loader_container
      if valid(container) then
        if e.loader_type=="input" then raw[#raw+1]={a=e,b=container,kind="loader"}
        else raw[#raw+1]={a=container,b=e,kind="loader"} end
      end
    end
  end
end
local inserter_entities={}
for _,e in pairs(surface.find_entities_filtered{type="inserter"}) do
  local p,d=e.pickup_target,e.drop_target
  if members[key(e)] or (valid(p) and members[key(p)]) or (valid(d) and members[key(d)]) then inserter_entities[#inserter_entities+1]=e end
end
table.sort(inserter_entities,less)
local inserters={}
for _,e in ipairs(inserter_entities) do
  inserters[#inserters+1]=connected_inserter(e)
  local p,d=e.pickup_target,e.drop_target
  if valid(p) then raw[#raw+1]={a=p,b=e,kind="pickup"} end
  if valid(d) then raw[#raw+1]={a=e,b=d,kind="drop"} end
end
local edges,boundary=collect_edges(members,raw)
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=area_result,nodes=nodes,machines=machines,belts=belts,chests=chests,inserters=inserters,fluidEntities=fluids,edges=edges,boundaryEdges=boundary})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[FactoryGraph](response)
}
