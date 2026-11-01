package main

import (
	"errors"
	"fmt"
)

// beltComponentsAreaLimit bounds the read-only area scan. Grouping collisions
// happen on the game thread, not in a paged result set, so a whole base fits in
// one snapshot instead of one query per 256-tile box.
const beltComponentsAreaLimit = 1024

// beltComponentsQuery groups every belt in an area into weakly connected
// components, classifies each one, and returns per-component contents in one
// snapshot. It reuses the topology Lua helpers (belt_connections, endpoint and
// collect_edges) so component edges match factoryGraph and beltRun.
func beltComponentsQuery(r *Resolver, input BeltComponentsInput) (*BeltComponentPage, error) {
	if input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.MaxEntities < 1 || input.MaxEntities > 5000 {
		return nil, errors.New("maxEntities must be between 1 and 5000")
	}
	if input.Limit < 1 || input.Limit > 100 {
		return nil, errors.New("limit must be between 1 and 100")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	areaLua, areaResult, err := worldAreaBoundsLua(input.Area, beltComponentsAreaLimit)
	if err != nil {
		return nil, err
	}
	lua := luaFail + transportLineLua + topologyCommonLua + fmt.Sprintf(`
local surface=game.surfaces[%s]
if not surface then fail("surface not found"); return end
local force=game.forces[%s]
if not force then fail("force not found"); return end
local area,area_result=%s,%s
local max_entities,page_limit,page_offset,dead_only=%d,%d,%d,%t
`, luaString(input.Surface), luaString(input.Force), areaLua, areaResult, input.MaxEntities, input.Limit, input.Offset, input.DeadOnly) + `
local seeds=surface.find_entities_filtered{area=area,force=force,type=belt_types}
table.sort(seeds,less)
local in_area={}
for _,e in ipairs(seeds) do in_area[key(e)]=e end
local components,component_of={},{}
local truncated_any=false
for _,seed in ipairs(seeds) do
  if not component_of[key(seed)] then
    local queue,members={seed},{[key(seed)]=true}
    local cursor,truncated=1,false
    while cursor<=#queue do
      local e=queue[cursor]; cursor=cursor+1
      for _,link in ipairs(belt_connections(e)) do
        local other
        if key(link.a)==key(e) then other=link.b
        elseif key(link.b)==key(e) then other=link.a
        else other=link.a end
        if valid(other) then
          local k=key(other)
          if in_area[k] and not members[k] then
            if #queue<max_entities then members[k]=true; queue[#queue+1]=other else truncated=true end
          end
        end
      end
    end
    table.sort(queue,less)
    local index=#components+1
    for k,_ in pairs(members) do component_of[k]=index end
    components[index]={members=members,nodes=queue,truncated=truncated,
      inputs={},outputs={},raw={},inserters={},inserter_seen={},picked={},dropped={},machine_links=0}
    if truncated then truncated_any=true end
  end
end
-- Directed belt edges inside each component. Connections to belts outside the
-- area are dropped here and surface again as boundary edges in collect_edges.
for _,c in ipairs(components) do
  for _,e in ipairs(c.nodes) do
    for _,link in ipairs(belt_connections(e)) do
      if c.members[key(link.a)] and c.members[key(link.b)] then
        c.raw[#c.raw+1]=link
        c.inputs[key(link.b)]=true
        c.outputs[key(link.a)]=true
      end
    end
  end
end
-- One inserter scan for the whole area. Targets resolve through the engine
-- target first and the geometric arm position second, so idle or unpowered but
-- wired inserters still count as feeding or draining a component. An arm can
-- touch two different components (pickup on one, drop on another), so both
-- sides are recorded and the inserter is listed once per component.
for _,e in pairs(surface.find_entities_filtered{area=area,force=force,type="inserter"}) do
  local pickup,drop=inserter_targets(e)
  for _,side in ipairs({{pickup,"pickup"},{drop,"drop"}}) do
    local target,kind=side[1],side[2]
    if valid(target) and is_belt(target) then
      local index=component_of[key(target)]
      if index then
        local c=components[index]
        if kind=="pickup" then c.picked[key(target)]=true else c.dropped[key(target)]=true end
        local inserter_key=key(e)
        if not c.inserter_seen[inserter_key] then
          c.inserter_seen[inserter_key]=true
          c.inserters[#c.inserters+1]=connected_inserter(e)
        end
      end
    end
  end
end
-- Machines that drop onto belts (mining drills) drain nothing but do feed, so
-- they count as a link and clear the starvation flag on that belt.
local machine_types={"assembling-machine","furnace","mining-drill","lab","rocket-silo"}
for _,e in pairs(surface.find_entities_filtered{area=area,force=force,type=machine_types}) do
  local target=machine_drop_target(e)
  if valid(target) and is_belt(target) then
    local index=component_of[key(target)]
    if index then
      components[index].dropped[key(target)]=true
      components[index].machine_links=components[index].machine_links+1
    end
  end
end
-- A loader moves items into or out of a container, which is a real link even
-- though no inserter is involved.
for _,c in ipairs(components) do
  for _,e in ipairs(c.nodes) do
    if e.type=="loader" or e.type=="loader-1x1" then
      local ok_container,container=pcall(function() return e.loader_container end)
      if ok_container and valid(container) then c.machine_links=c.machine_links+1 end
    end
  end
end
local out={}
for index,c in ipairs(components) do
  local nodes,sources,sinks,stall,starve={},{},{},{},{}
  local items,item_index,item_total={},{},0
  for _,e in ipairs(c.nodes) do
    local obj=endpoint(e)
    nodes[#nodes+1]=obj
    for _,lane in ipairs(obj.lanes or {}) do
      for _,stack in ipairs(lane.items or {}) do
        local item_key=tostring(stack.name).."|"..tostring(stack.quality or "normal")
        local entry=item_index[item_key]
        if not entry then
          entry={name=stack.name,count=0,quality=stack.quality or "normal"}
          item_index[item_key]=entry
          items[#items+1]=entry
        end
        entry.count=entry.count+(stack.count or 0)
        item_total=item_total+(stack.count or 0)
      end
    end
    local fed=c.inputs[key(e)]
    local drained=c.outputs[key(e)]
    if not fed then
      sources[#sources+1]=obj
      if not c.dropped[key(e)] then starve[#starve+1]=obj end
    end
    if not drained then
      sinks[#sinks+1]=obj
      if not c.picked[key(e)] then stall[#stall+1]=obj end
    end
  end
  table.sort(items,function(a,b)
    if a.name~=b.name then return a.name<b.name end
    return tostring(a.quality)<tostring(b.quality)
  end)
  local edges=collect_edges(c.members,c.raw)
  out[#out+1]={id=tostring(index),dead=(#c.inserters==0 and c.machine_links==0),belts=#c.nodes,
    truncated=c.truncated,items=items,itemCount=item_total,nodes=nodes,edges=edges,
    sources=sources,sinks=sinks,inserters=c.inserters,stallEnds=stall,starveHeads=starve,
    first=key(c.nodes[1])}
end
table.sort(out,function(a,b)
  if a.dead~=b.dead then return a.dead end
  if a.belts~=b.belts then return a.belts>b.belts end
  return a.first<b.first
end)
local filtered={}
for _,c in ipairs(out) do if not dead_only or c.dead then filtered[#filtered+1]=c end end
local total=#filtered
local page={}
for i=page_offset+1,math.min(page_offset+page_limit,total) do
  local c=filtered[i]; c.first=nil; page[#page+1]=c
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=area_result,
  beltTotal=#seeds,truncated=truncated_any,
  pageInfo={total=total,limit=page_limit,offset=page_offset,hasMore=(page_offset+#page<total)},
  components=page})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltComponentPage](response)
}
