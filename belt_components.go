package main

import (
	"errors"
	"fmt"
	"strconv"
)

// beltComponentsAreaLimit bounds the read-only area scan. Grouping collisions
// happen on the game thread, not in a paged result set, so a whole base fits in
// one snapshot instead of one query per bounded box.
const beltComponentsAreaLimit = 1024

const beltComponentsCallLua = `
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local area_result={leftTopX=area[1][1],leftTopY=area[1][2],rightBottomX=area[2][1],rightBottomY=area[2][2]}
local seeds=surface.find_entities_filtered{area=area,force=force,type=belt_types}
table.sort(seeds,less)
local in_area={}
for _,e in ipairs(seeds) do in_area[key(e)]=e end
local components,component_of={},{}
local truncated_any=false
for _,seed in ipairs(seeds) do
  if not component_of[key(seed)] then
    local queue,members={seed},{[key(seed)]=seed}
    local cursor,truncated=1,false
    while cursor<=#queue do
      local e=queue[cursor]; cursor=cursor+1
      local links=belt_connections(e)
      local line_links=belt_line_connections(e)
      local function visit(link)
        local other
        if key(link.a)==key(e) then other=link.b
        elseif key(link.b)==key(e) then other=link.a
        else other=link.a end
        if valid(other) then
          local k=key(other)
          if in_area[k] and not members[k] then
            if #queue<max_entities then members[k]=other; queue[#queue+1]=other else truncated=true end
          end
        end
      end
      for _,link in ipairs(links) do visit(link) end
      for _,link in ipairs(line_links) do visit(link) end
    end
    table.sort(queue,less)
    local index=#components+1
    for k,_ in pairs(members) do component_of[k]=index end
    components[index]={members=members,nodes=queue,truncated=truncated,
      inputs={},outputs={},raw={},inserters={},inserter_seen={},picked={},dropped={},pickup_counts={},machine_links=0}
    if truncated then truncated_any=true end
  end
end
-- Directed structural belt edges inside each component. Connections to belts
-- outside the area are dropped here and surface again as boundary edges.
for _,c in ipairs(components) do
  for _,e in ipairs(c.nodes) do
    for _,link in ipairs(belt_connections(e)) do
      if c.members[key(link.a)] and c.members[key(link.b)] then
        c.raw[#c.raw+1]=link
        c.inputs[key(link.b)]=true
        c.outputs[key(link.a)]=true
      end
    end
    if include_line_links then
      for _,link in ipairs(belt_line_connections(e)) do
        if c.members[key(link.a)] and c.members[key(link.b)] then c.raw[#c.raw+1]=link end
      end
    end
  end
end
-- One inserter scan for the whole area, padded past the edge and without a
-- force filter: an inserter just outside the area that picks from or drops onto
-- a belt inside it is what keeps that component alive. Targets resolve through
-- the engine target first and the geometric arm position second.
local area_box={surface=surface,left=area[1][1],top=area[1][2],right=area[2][1],bottom=area[2][2]}
-- An arm reports a position about 0.2 tiles past its target's centre (a belt
-- drop reads 30.699 for the tile at 30.5), so an arm is joined through the tile
-- it reaches. The area's belts are indexed once; a point search would miss them.
local area_belt_index=belt_tile_index(seeds)
for _,e in ipairs(scan_inserters(surface,box_area(area_box,2.5))) do
  local pickup,drop=inserter_targets(e,area_belt_index)
  for _,side in ipairs({{pickup,"pickup"},{drop,"drop"}}) do
    local target,kind=side[1],side[2]
    if valid(target) and is_belt(target) then
      local index=component_of[key(target)]
      if index then
        local c=components[index]
        if kind=="pickup" then
          c.picked[key(target)]=true
          local tile_key=key(target)
          c.pickup_counts[tile_key]=(c.pickup_counts[tile_key] or 0)+1
        else
          c.dropped[key(target)]=true
        end
        local inserter_key=key(e)
        if not c.inserter_seen[inserter_key] then
          c.inserter_seen[inserter_key]=true
          c.inserters[#c.inserters+1]=connected_inserter(e,area_belt_index)
        end
      end
    end
  end
end
-- Machines that drop onto belts (mining drills) drain nothing but do feed, so
-- they count as a link and clear the starvation flag on that belt. The machine
-- scan is padded by the largest machine radius, so a drill centred outside the
-- area still counts for a belt it drops onto inside it.
local machine_types={"assembling-machine","furnace","mining-drill","lab","rocket-silo"}
for _,e in pairs(surface.find_entities_filtered{area=box_area(area_box,2.5),force=force,type=machine_types}) do
  local target=machine_drop_target(e,area_belt_index)
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
  local side_mixed=false
  for _,e in ipairs(c.nodes) do
    local obj=endpoint(e)
    local side_names={}
    nodes[#nodes+1]=obj
    for _,lane in ipairs(obj.lanes or {}) do
      local side=lane.side or "unknown"
      side_names[side]=side_names[side] or {}
      for _,stack in ipairs(lane.items or {}) do
        side_names[side][tostring(stack.name).."|"..tostring(stack.quality or "normal")]=true
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
    for _,names in pairs(side_names) do
      local distinct=0
      for _ in pairs(names) do distinct=distinct+1 end
      if distinct>1 then side_mixed=true end
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
  local shared_pickups=0
  for _,count in pairs(c.pickup_counts) do
    if count>1 then shared_pickups=shared_pickups+1 end
  end
  table.sort(items,function(a,b)
    if a.name~=b.name then return a.name<b.name end
    return tostring(a.quality)<tostring(b.quality)
  end)
  local edges=collect_edges(c.members,c.raw)
  out[#out+1]={id=tostring(index),dead=(#c.inserters==0 and c.machine_links==0),belts=#c.nodes,
    truncated=c.truncated,items=items,itemCount=item_total,
    mixed=side_mixed,sharedPickupSources=shared_pickups,nodes=nodes,edges=edges,
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
local payload={tick=game.tick,surface=surface.name,force=force.name,area=area_result,
  beltTotal=#seeds,truncated=truncated_any,
  pageInfo={total=total,limit=page_limit,offset=page_offset,hasMore=(page_offset+#page<total)},
  components=page}`

// beltComponentsQuery groups every belt in an area into weakly connected
// components, classifies each one, and returns per-component contents in one
// snapshot. It reuses the topology Lua helpers (belt_connections, endpoint and
// collect_edges) so component edges match factoryGraph and beltRun.
func beltComponentsQuery(r *Resolver, input BeltComponentsInput) (*BeltComponentPage, error) {
	if input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.MaxEntities < 1 || input.MaxEntities > maxBeltTraversal {
		return nil, limitError("maxEntities", maxBeltTraversal)
	}
	if input.Limit < 1 || input.Limit > maxBeltComponents {
		return nil, limitError("limit", maxBeltComponents)
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	areaLua, _, err := worldAreaBoundsLua(input.Area, beltComponentsAreaLimit)
	if err != nil {
		return nil, err
	}
	lua := topologyChunk(r, "belt_components",
		[]string{luaString(input.Surface), luaString(input.Force), areaLua,
			strconv.Itoa(input.MaxEntities), strconv.Itoa(input.Limit), strconv.Itoa(input.Offset),
			luaBool(input.DeadOnly), luaBool(input.IncludeLineLinks)},
		fmt.Sprintf(`
local surface_name,force_name=%s,%s
local area=%s
local max_entities,page_limit,page_offset,dead_only,include_line_links=%d,%d,%d,%t,%t
`, luaString(input.Surface), luaString(input.Force), areaLua,
			input.MaxEntities, input.Limit, input.Offset, input.DeadOnly, input.IncludeLineLinks),
		beltComponentsCallLua)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltComponentPage](response)
}
