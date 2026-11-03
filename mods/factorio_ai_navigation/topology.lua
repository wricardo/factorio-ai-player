-- Belt topology library, compiled once at mod load.
--
-- The console (RCON) side sends only a remote.call with its arguments instead of
-- re-shipping the whole helper prelude, which cost about 0.87 s of Lua compile per
-- topology query. Every console operation keeps an inline fallback with the same
-- text (insights_topology.go copies the marked regions below, and a Go test fails
-- when they drift), so an old save without this mod interface still works.
--
-- Remote interface: factorio_ai_topology, protocol version 3.
--   version() -> 3
--   belt_run(surface, x, y, limit, include_line_links) -> BeltRun payload
--   belt_stretch(surface, x, y, limit) -> BeltStretch payload
--   belt_runs(requests, limit, include_line_links) -> {beltRuns = {BeltRun}}
--   factory_graph(surface, force, area, compact) -> FactoryGraph payload
--   adjacency(surface, force, area, tolerance, limit[, kinds, only_unconnected]) -> AdjacencyGraph payload
--   adjacency_filtered(same) -> the same payload; the named op exists so an older
--   mod that ignores the filters cannot answer filtered calls silently
--   belt_components(surface, force, area, max_entities, limit, offset, dead_only, include_line_links) -> BeltComponentPage payload
--   inspection_logistics(surface, force, name, x, y) -> InspectionLogistics payload
-- A refused operation returns {error = message} instead of raising.
local M = {}

-- The console prints the JSON envelope; the mod raises, and the interface wrapper
-- turns that into {error = message}.
local function fail(message)
  error(message, 0)
end

-- BEGIN factorio_ai_topology.prelude
local transport_line_names={}
if defines.transport_line then
  for name,index in pairs(defines.transport_line) do transport_line_names[index]=name end
end
local function transport_line_name(index)
  return transport_line_names[index] or ("line_"..tostring(index))
end
local function transport_line_side(name)
  if string.find(name,"left",1,true) then return "left" end
  if string.find(name,"right",1,true) then return "right" end
  return "unknown"
end

local belt_types={"transport-belt","underground-belt","splitter","loader","loader-1x1","linked-belt"}
local belt_set={}; for _,t in ipairs(belt_types) do belt_set[t]=true end
local statuses={}
if defines.entity_status then
  for k,v in pairs(defines.entity_status) do statuses[v]=k end
end
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
  local ok_status,status=pcall(function() return e.status end); if ok_status then obj.status=statuses[status] end
  local ok_direction,direction=pcall(function() return e.direction end); if ok_direction then obj.direction=direction end
  if is_belt(e) then
    for i=1,e.get_max_transport_line_index() do
      local items=e.get_transport_line(i).get_contents()
      table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "")<(b.quality or "") end)
      local name=transport_line_name(i)
      obj.lanes[#obj.lanes+1]={index=i,lineName=name,side=transport_line_side(name),items=items}
    end
  elseif e.type=="inserter" then
    obj.pickupPosition=e.pickup_position; obj.dropPosition=e.drop_position
    local stack=e.held_stack
    if stack and stack.valid_for_read then obj.heldStack={name=stack.name,count=stack.count,quality=stack.quality.name} end
  end
  endpoint_cache[k]=obj
  return obj
end
-- The engine leaves pickup_target/drop_target nil on inserters that have not
-- run since placement or since losing power, so an idle-but-wired inserter
-- reads as dead. Resolve the entity under the arm's geometric pickup/drop
-- position as a fallback: wiring is then reported independently of activity.
-- Any entity with an inventory or a burner is insertable, so boilers, reactors,
-- burner generators, wagons, turrets, roboports and beacons resolve too.
local function insertable(c)
  if not valid(c) then return false end
  -- An inserter picks from and drops onto belts as well as into inventories.
  if belt_set[c.type] then return true end
  local ok_index,index=pcall(function() return c.get_max_inventory_index() end)
  if ok_index and type(index)=="number" and index>0 then return true end
  local ok_burner,burner=pcall(function() return c.burner end)
  return ok_burner and burner~=nil
end
local function position_target(e,pos)
  if not valid(e) or type(pos)~="table" then return nil end
  local ok,found=pcall(function() return e.surface.find_entities_filtered{position=pos,force=e.force,limit=16} end)
  if not ok or not found then return nil end
  table.sort(found,function(a,b)
    if a.type~=b.type then return a.type<b.type end
    if a.name~=b.name then return a.name<b.name end
    return (a.unit_number or 0)<(b.unit_number or 0)
  end)
  for _,c in pairs(found) do
    if valid(c) and c~=e and insertable(c) then return c end
  end
  return nil
end
-- An arm position is not the target's centre: a working inserter that drops onto
-- the belt at (29.5, 30.5) reports a drop position of (29.5, 30.699), because the
-- engine offsets the reached point by about 0.2 tiles along the arm. The entity
-- an arm reaches is the one on the tile containing that position, which is also
-- the tile the item lands on. A surface point search is not that rule: it answers
-- per collision box, and live 2.0 returns nothing at all for the belts an idle
-- arm points at, so every resolver that already holds the belts of its snapshot
-- indexes them by tile once instead of probing the surface per arm.
local function tile_key(x,y)
  return tostring(math.floor(x))..":"..tostring(math.floor(y))
end
local function belt_tile_index(entities)
  local index={}
  for _,e in pairs(entities) do
    if is_belt(e) then
      local ok,bb=pcall(function() return e.bounding_box end)
      if ok and bb then
        -- Belts sit inset inside their tile while a splitter spans two tiles, so
        -- the index follows the box instead of assuming one tile.
        for x=math.floor(bb.left_top.x),math.floor(bb.right_bottom.x-0.001) do
          for y=math.floor(bb.left_top.y),math.floor(bb.right_bottom.y-0.001) do
            local k=x..":"..y
            if not index[k] then index[k]=e end
          end
        end
      end
    end
  end
  return index
end
local function belt_at_tile(index,pos)
  if not index or type(pos)~="table" then return nil end
  return index[tile_key(pos.x,pos.y)]
end
local engine_targets={}
local function resolved_target(e,kind,pos_name,belt_index)
  local ok,target=pcall(function() return e[kind] end)
  if ok and valid(target) then return target end
  local ok_pos,pos=pcall(function() return e[pos_name] end)
  if not ok_pos then return nil end
  local belt=belt_at_tile(belt_index,pos)
  if valid(belt) then return belt end
  return position_target(e,pos)
end
local function inserter_targets(e,belt_index)
  local k=key(e)
  -- The pair is memoized per snapshot: a different belt index answers different
  -- questions, so it takes its own entry instead of reusing this one.
  local cache_key=k
  if belt_index then cache_key=k.."|"..tostring(belt_index) end
  local cached=engine_targets[cache_key]
  if not cached then
    cached={resolved_target(e,"pickup_target","pickup_position",belt_index),resolved_target(e,"drop_target","drop_position",belt_index)}
    engine_targets[cache_key]=cached
  end
  return cached[1],cached[2]
end
local function connected_inserter(e,belt_index)
  local p,d=inserter_targets(e,belt_index)
  return {inserter=endpoint(e),pickup=endpoint(p),drop=endpoint(d)}
end
-- Machines that drop onto an entity (mining drills) have the same staleness.
local function machine_drop_target(e,belt_index)
  return resolved_target(e,"drop_target","drop_position",belt_index)
end
-- This prelude is compiled once at mod load, so the memo tables above outlive
-- the state they describe: an inserter rotated after a read kept its old
-- direction and its old pickup/drop pair forever. Every remote interface call
-- clears them, which keeps the per-call dedupe without cross-call staleness.
local function reset_topology_caches()
  endpoint_cache={}
  engine_targets={}
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
-- Structural connections only: tile-adjacent engine belt neighbours plus
-- underground and linked partners. The owner of an internal transport line can
-- be any belt along that line, so line links are traversal-only and never claim
-- a structural feed.
local function belt_connections(e)
  local result,seen={},{}
  local function add(a,b)
    if not is_belt(a) or not is_belt(b) or key(a)==key(b) then return end
    local k=key(a)..">"..key(b)
    if not seen[k] then seen[k]=true; result[#result+1]={a=a,b=b,kind=edge_kind(a,b)} end
  end
  local neighbours=e.belt_neighbours
  if neighbours then
    for _,n in pairs(neighbours.inputs) do add(n,e) end
    for _,n in pairs(neighbours.outputs) do add(e,n) end
  end
  if e.type=="underground-belt" then
    local n=e.neighbours
    if valid(n) then if e.belt_to_ground_type=="input" then add(e,n) else add(n,e) end end
  elseif e.type=="linked-belt" then
    local n=e.linked_belt_neighbour
    if valid(n) then if e.linked_belt_type=="input" then add(e,n) else add(n,e) end end
  end
  table.sort(result,function(a,b)
    local ka,kb=key(a.a)..">"..key(a.b),key(b.a)..">"..key(b.b)
    if ka~=kb then return ka<kb end
    return a.kind<b.kind
  end)
  return result
end
-- Transport-line links. They join belts that can be tens of tiles apart, so they
-- extend traversal (and are edges only when the caller asks for kind "line") but
-- never define sources or sinks.
local function belt_line_connections(e)
  local result,seen={},{}
  local function add(a,b)
    if not is_belt(a) or not is_belt(b) or key(a)==key(b) then return end
    local k=key(a)..">"..key(b)
    if not seen[k] then seen[k]=true; result[#result+1]={a=a,b=b,kind="line"} end
  end
  local ok,index=pcall(function() return e.get_max_transport_line_index() end)
  if not ok or type(index)~="number" then return result end
  for i=1,index do
    local ok_line,line=pcall(function() return e.get_transport_line(i) end)
    if ok_line and line then
      for _,n in pairs(line.input_lines) do add(n.owner,e) end
      for _,n in pairs(line.output_lines) do add(e,n.owner) end
    end
  end
  table.sort(result,function(a,b)
    local ka,kb=key(a.a)..">"..key(a.b),key(b.a)..">"..key(b.b)
    if ka~=kb then return ka<kb end
    return a.kind<b.kind
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
-- Bounding boxes are kept as tile ranges so one padded entity scan can serve a
-- whole component instead of every seed scanning the surface again.
local function expand_box(box,e)
  local x=math.floor(e.position.x)+0.5
  local y=math.floor(e.position.y)+0.5
  if not box then return {surface=e.surface,left=x,right=x,top=y,bottom=y} end
  if x<box.left then box.left=x end
  if x>box.right then box.right=x end
  if y<box.top then box.top=y end
  if y>box.bottom then box.bottom=y end
  return box
end
local function box_area(box,pad)
  return {{box.left-pad,box.top-pad},{box.right+pad,box.bottom+pad}}
end
local function scan_inserters(surface,area)
  local list={}
  for _,e in pairs(surface.find_entities_filtered{area=area,type="inserter"}) do
    if valid(e) then list[#list+1]=e end
  end
  table.sort(list,less)
  return list
end
local function inserters_for_members(members,candidates)
  local result={}
  local belt_index=belt_tile_index(members)
  for _,e in ipairs(candidates) do
    local p,d=inserter_targets(e,belt_index)
    if (valid(p) and members[key(p)]) or (valid(d) and members[key(d)]) then result[#result+1]=connected_inserter(e,belt_index) end
  end
  return result
end
local function sort_connected_inserters(list)
  table.sort(list,function(a,b)
    local x,y=a.inserter,b.inserter
    if x.surface~=y.surface then return x.surface<y.surface end
    if x.position.x~=y.position.x then return x.position.x<y.position.x end
    if x.position.y~=y.position.y then return x.position.y<y.position.y end
    return (x.unitNumber or "0")<(y.unitNumber or "0")
  end)
  return list
end
-- One inserter scan for the whole component, padded by 2.5 tiles so long-handed
-- inserters just outside the component still count.
local function component_inserters(members,order)
  local list,boxes={},{}
  for _,e in ipairs(order) do boxes[e.surface.index]=expand_box(boxes[e.surface.index],e) end
  for _,box in pairs(boxes) do
    local candidates=scan_inserters(box.surface,box_area(box,2.5))
    for _,inserter in ipairs(inserters_for_members(members,candidates)) do list[#list+1]=inserter end
  end
  return sort_connected_inserters(list)
end
local function belt_run(seed,limit,include_line_links,defer_inserters)
  local queue,members={seed},{[key(seed)]=seed}
  local raw,inputs,outputs={},{},{}
  local cursor,truncated=1,false
  while cursor<=#queue do
    local e=queue[cursor]; cursor=cursor+1
    local structural=belt_connections(e)
    for _,link in ipairs(structural) do
      raw[#raw+1]=link
      inputs[key(link.b)]=true; outputs[key(link.a)]=true
    end
    local line_links=belt_line_connections(e)
    if include_line_links then
      for _,link in ipairs(line_links) do raw[#raw+1]=link end
    end
    local function visit(link)
      local n=link.a
      if key(n)==key(e) then n=link.b end
      local k=key(n)
      if not members[k] then
        if #queue<limit then members[k]=n; queue[#queue+1]=n else truncated=true end
      end
    end
    for _,link in ipairs(structural) do visit(link) end
    for _,link in ipairs(line_links) do visit(link) end
  end
  table.sort(queue,less)
  local nodes,sources,sinks={},{},{}
  for _,e in ipairs(queue) do
    local obj=endpoint(e); nodes[#nodes+1]=obj
    if not inputs[key(e)] then sources[#sources+1]=obj end
    if not outputs[key(e)] then sinks[#sinks+1]=obj end
  end
  local inserters={}
  if not defer_inserters then inserters=component_inserters(members,queue) end
  local edges,boundary=collect_edges(members,raw)
  local payload={tick=game.tick,surface=seed.surface.name,seed=endpoint(seed),maxEntities=limit,truncated=truncated,nodes=nodes,edges=edges,boundaryEdges=boundary,sources=sources,sinks=sinks,inserters=inserters}
  return payload,members
end
-- A linear walk must only follow tile-adjacent engine connections. Line
-- connections describe internal transport lines, whose owner can be a belt far
-- along the same line, so they are valid topology but not sequence steps.
local function stretch_connections(e)
  local result,seen={},{}
  local function add(a,b)
    if not is_belt(a) or not is_belt(b) or key(a)==key(b) then return end
    local k=key(a)..">"..key(b)
    if seen[k] then return end
    seen[k]=true
    result[#result+1]={a=a,b=b}
  end
  local neighbours=e.belt_neighbours
  if neighbours then
    for _,n in pairs(neighbours.inputs) do add(n,e) end
    for _,n in pairs(neighbours.outputs) do add(e,n) end
  end
  if e.type=="underground-belt" then
    local n=e.neighbours
    if valid(n) then if e.belt_to_ground_type=="input" then add(e,n) else add(n,e) end end
  elseif e.type=="linked-belt" then
    local n=e.linked_belt_neighbour
    if valid(n) then if e.linked_belt_type=="input" then add(e,n) else add(n,e) end end
  end
  return result
end
local function stretch_successors(e)
  local result={}
  for _,link in ipairs(stretch_connections(e)) do
    if key(link.a)==key(e) then result[#result+1]=link end
  end
  return result
end
local function stretch_predecessors(e)
  local result={}
  for _,link in ipairs(stretch_connections(e)) do
    if key(link.b)==key(e) then result[#result+1]=link end
  end
  return result
end
-- Classify a forward boundary from the last belt's direction and the tiles
-- ahead. A gap needs evidence that the line continues: an aligned belt within
-- the lookahead. Without it the end is natural, because runs routinely end
-- beside their last inserter or in front of a machine.
local end_lookahead=8
local end_vectors={[0]={0,-1},[4]={1,0},[8]={0,1},[12]={-1,0}}
local end_ignored={resource=true,corpse=true,["item-entity"]=true,character=true,["character-corpse"]=true,["particle-source"]=true,["smoke-with-trigger"]=true}
local function tile_occupants(surface,x,y)
  local belts,others={},{}
  for _,e in pairs(surface.find_entities_filtered{area={{x-0.45,y-0.45},{x+0.45,y+0.45}}}) do
    if e.valid and not end_ignored[e.type] then
      if belt_set[e.type] then belts[#belts+1]=e else others[#others+1]=e end
    end
  end
  table.sort(belts,function(a,b) return a.name<b.name end)
  table.sort(others,function(a,b) return a.name<b.name end)
  return belts,others
end
local function classify_forward_end(e,entry)
  local forward_capable=e.type=="transport-belt" or (e.type=="underground-belt" and e.belt_to_ground_type=="output")
  if e.type=="underground-belt" and e.belt_to_ground_type=="input" and not valid(e.neighbours) then
    entry.reason="UNPAIRED_UNDERGROUND"; entry.gap=true
    local v=end_vectors[e.direction]
    if v then entry.expectedTarget={x=e.position.x+v[1],y=e.position.y+v[2]} end
    return
  end
  if not forward_capable then return end
  local v=end_vectors[e.direction]
  if not v then return end
  local surface=e.surface
  entry.expectedTarget={x=e.position.x+v[1],y=e.position.y+v[2]}
  entry.reason="NATURAL_END"
  local blocker=nil
  for i=1,end_lookahead do
    local x,y=e.position.x+v[1]*i,e.position.y+v[2]*i
    local belts,others=tile_occupants(surface,x,y)
    if i==1 then
      if #belts>0 then
        blocker=belts[1].name
        entry.downstream=endpoint(belts[1])
        if belts[1].direction==(e.direction+8)%16 then entry.reason="HEAD_ON"; entry.gap=true; entry.blocker=blocker; return end
      elseif #others>0 then
        blocker=others[1].name
        entry.downstream=endpoint(others[1])
      end
    elseif blocker==nil and #others>0 then
      blocker=others[1].name
    end
    local aligned=false
    for _,belt in ipairs(belts) do if belt.direction==e.direction then aligned=true end end
    if aligned then
      if i>=2 then
        entry.gap=true
        entry.reason=blocker~=nil and "BLOCKED" or "MISSING_BELT"
        entry.blocker=blocker
      end
      return
    end
  end
  entry.blocker=blocker
end
-- One linear walk, not the whole component: stop wherever the sequence is not
-- unambiguous, so a returned stretch never silently picks a branch.
local function belt_stretch(seed,limit)
  local backward,forward,ends={},{},{}
  local seen={[key(seed)]=true}
  local budget=limit-1
  local truncated=false
  local function record_end(e,kind,links,is_forward)
    local targets={}
    for _,link in ipairs(links) do
      local other=link.b
      if key(other)==key(e) then other=link.a end
      targets[#targets+1]=endpoint(other)
    end
    local entry={position=e.position,unitNumber=e.unit_number,kind=kind,targets=targets,gap=false,reason="NOT_APPLICABLE"}
    if kind=="boundary" and is_forward then classify_forward_end(e,entry) end
    ends[#ends+1]=entry
  end
  local cursor=seed
  while true do
    local preds=stretch_predecessors(cursor)
    if #preds==0 then record_end(cursor,"boundary",preds); break end
    local next=preds[1].a
    if #preds>1 or next.type=="splitter" then record_end(cursor,"merge",preds); break end
    if seen[key(next)] then record_end(cursor,"loop",preds); break end
    if budget<=0 then truncated=true; record_end(cursor,"truncated",preds); break end
    seen[key(next)]=true; budget=budget-1
    backward[#backward+1]=next; cursor=next
  end
  cursor=seed
  while true do
    local succs=stretch_successors(cursor)
    if #succs==0 then record_end(cursor,"boundary",succs,true); break end
    local next=succs[1].b
    if #succs>1 or next.type=="splitter" then record_end(cursor,"split",succs); break end
    if seen[key(next)] then record_end(cursor,"loop",succs); break end
    if budget<=0 then truncated=true; record_end(cursor,"truncated",succs); break end
    seen[key(next)]=true; budget=budget-1
    forward[#forward+1]=next; cursor=next
  end
  local ordered={}
  for i=#backward,1,-1 do ordered[#ordered+1]=backward[i] end
  ordered[#ordered+1]=seed
  for _,e in ipairs(forward) do ordered[#ordered+1]=e end
  local nodes,lines,mixed={},{},{left=false,right=false,unknown=false}
  for _,e in ipairs(ordered) do
    local node={name=e.name,type=e.type,position=e.position,unitNumber=e.unit_number,direction=e.direction,lanes={}}
    local node_side={}
    local max_index=1
    local ok,value=pcall(function() return e.get_max_transport_line_index() end)
    if ok and type(value)=="number" and value>=1 then max_index=value end
    -- get_contents reports only the items carried by this entity's tile line.
    -- An underground entrance reports its surface lanes and its tunnel contents
    -- as additional lines that share one internal line, so every line this
    -- entity exposes is counted; identity would silently drop the tunnel.
    for index=1,max_index do
      local line=e.get_transport_line(index)
      local name=transport_line_name(index)
      local items=line.get_contents()
      table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "normal")<(b.quality or "normal") end)
      local side=transport_line_side(name)
      node.lanes[#node.lanes+1]={index=index,lineName=name,side=side,items=items}
      node_side[side]=node_side[side] or {}
      for _,item in ipairs(items) do
        node_side[side][tostring(item.name).."|"..tostring(item.quality or "normal")]=true
      end
      lines[#lines+1]={line=line,side=side}
    end
    -- mixed is a per-side property of one belt: an underground entrance can
    -- expose surface and tunnel lines that share a side, so count distinct
    -- prototype/quality combinations across every line of that side instead
    -- of requiring two stacks inside one line.
    for side,names in pairs(node_side) do
      local distinct=0
      for _ in pairs(names) do distinct=distinct+1 end
      if distinct>1 then mixed[side]=true end
    end
    nodes[#nodes+1]=node
  end
  local totals={left={},right={},unknown={}}
  local counts={left=0,right=0,unknown=0}
  for _,entry in ipairs(lines) do
    local side=entry.side
    if side~="left" and side~="right" then side="unknown" end
    local bucket=totals[side]
    for _,item in ipairs(entry.line.get_contents()) do
      local quality=item.quality or "normal"
      local found=nil
      for _,existing in ipairs(bucket) do
        if existing.name==item.name and existing.quality==quality then found=existing; break end
      end
      if found then found.count=found.count+item.count
      else bucket[#bucket+1]={name=item.name,quality=quality,count=item.count} end
      counts[side]=counts[side]+item.count
    end
  end
  local function total(side)
    local items=totals[side]
    table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
    return {side=side,items=items,itemCount=counts[side],mixed=mixed[side]}
  end
  local members={}
  for _,e in ipairs(ordered) do members[key(e)]=e end
  return {tick=game.tick,surface=seed.surface.name,seed=endpoint(seed),maxEntities=limit,truncated=truncated,nodes=nodes,ends=ends,inserters=component_inserters(members,ordered),left=total("left"),right=total("right"),unknown=total("unknown")}
end
-- END factorio_ai_topology.prelude

local function belt_run_op(surface_name,seed_x,seed_y,limit,include_line_links)
-- BEGIN factorio_ai_topology.belt_run
local seed,err=belt_at(surface_name,seed_x,seed_y)
if not seed then fail(err); return end
local payload=belt_run(seed,limit,include_line_links)
-- END factorio_ai_topology.belt_run
  return payload
end

local function belt_stretch_op(surface_name,seed_x,seed_y,limit)
-- BEGIN factorio_ai_topology.belt_stretch
local seed,err=belt_at(surface_name,seed_x,seed_y)
if not seed then fail(err); return end
local payload=belt_stretch(seed,limit)
-- END factorio_ai_topology.belt_stretch
  return payload
end

-- Requests carry their surface, tile and identity so a changed or removed belt is
-- reported instead of silently measuring a different component. Components are
-- traversed first, then one inserter scan covers the union of their bounds.
local function belt_runs_op(requests,limit,include_line_links)
-- BEGIN factorio_ai_topology.belt_runs
local runs,covered={},{}
for _,request in ipairs(requests) do
  local seed,err=belt_at(request.surface,request.x,request.y)
  if not seed then fail("inspection belt target changed: "..err); return end
  if seed.name~=request.name or (request.unit and tostring(seed.unit_number)~=request.unit) then fail("inspection belt target changed"); return end
  if not covered[key(seed)] then
    local run,members=belt_run(seed,limit,include_line_links,true)
    for k,_ in pairs(members) do covered[k]=true end
    runs[#runs+1]={run=run,members=members}
  end
end
local bounds={}
for _,entry in ipairs(runs) do
  for _,e in pairs(entry.members) do bounds[e.surface.index]=expand_box(bounds[e.surface.index],e) end
end
for _,box in pairs(bounds) do
  local candidates=scan_inserters(box.surface,box_area(box,2.5))
  for _,entry in ipairs(runs) do
    for _,inserter in ipairs(inserters_for_members(entry.members,candidates)) do
      entry.run.inserters[#entry.run.inserters+1]=inserter
    end
  end
end
local output={}
for _,entry in ipairs(runs) do
  sort_connected_inserters(entry.run.inserters)
  output[#output+1]=entry.run
end
local payload={beltRuns=output}
-- END factorio_ai_topology.belt_runs
  return payload
end

local function factory_graph_op(surface_name,force_name,area,compact,include_line_links)
-- BEGIN factorio_ai_topology.factory_graph
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local area_result={leftTopX=area[1][1],leftTopY=area[1][2],rightBottomX=area[2][1],rightBottomY=area[2][2]}
local machine_set={ ["assembling-machine"]=true,furnace=true,["mining-drill"]=true,lab=true,["rocket-silo"]=true }
local chest_set={container=true,["logistic-container"]=true,["linked-container"]=true,["infinity-container"]=true}
local types={"assembling-machine","furnace","mining-drill","lab","rocket-silo","inserter","container","logistic-container","linked-container","infinity-container"}
for _,t in ipairs(belt_types) do types[#types+1]=t end
for _,t in ipairs(fluid_types) do types[#types+1]=t end
local entities=surface.find_entities_filtered{area=area,force=force,type=types}
table.sort(entities,less)
local members={}; for _,e in ipairs(entities) do members[key(e)]=true end
-- Arms resolve their belt by the tile they reach, so the belt set of this
-- snapshot is indexed once instead of probing the surface per arm.
local belt_index=belt_tile_index(entities)
local nodes,machines,belts,chests,fluids,raw={},{},{},{},{},{}
for _,e in ipairs(entities) do
  local obj=endpoint(e); nodes[#nodes+1]=obj
  if machine_set[e.type] then
    machines[#machines+1]=obj
    local target=machine_drop_target(e,belt_index)
    if valid(target) then raw[#raw+1]={a=e,b=target,kind="drop"} end
  elseif chest_set[e.type] then chests[#chests+1]=obj
  elseif is_fluid(e) then
    fluids[#fluids+1]=obj
    for _,link in ipairs(fluid_links(e)) do raw[#raw+1]=link end
  elseif is_belt(e) then
    belts[#belts+1]=obj
    for _,link in ipairs(belt_connections(e)) do raw[#raw+1]=link end
    if include_line_links then
      for _,link in ipairs(belt_line_connections(e)) do raw[#raw+1]=link end
    end
    if e.type=="loader" or e.type=="loader-1x1" then
      local container=e.loader_container
      if valid(container) then
        if e.loader_type=="input" then raw[#raw+1]={a=e,b=container,kind="loader"}
        else raw[#raw+1]={a=container,b=e,kind="loader"} end
      end
    end
  end
end
-- Inserters stand at most one tile from every node they touch, so the scan
-- area covers one expanding tile instead of the whole surface.
local inserter_scan={{area[1][1]-2.5,area[1][2]-2.5},{area[2][1]+2.5,area[2][2]+2.5}}
local inserter_entities={}
for _,e in pairs(surface.find_entities_filtered{area=inserter_scan,type="inserter"}) do
  local p,d=inserter_targets(e,belt_index)
  if members[key(e)] or (valid(p) and members[key(p)]) or (valid(d) and members[key(d)]) then inserter_entities[#inserter_entities+1]=e end
end
table.sort(inserter_entities,less)
local inserters={}
for _,e in ipairs(inserter_entities) do
  inserters[#inserters+1]=connected_inserter(e,belt_index)
  local p,d=inserter_targets(e,belt_index)
  if valid(p) then raw[#raw+1]={a=p,b=e,kind="pickup"} end
  if valid(d) then raw[#raw+1]={a=e,b=d,kind="drop"} end
end
local edges,boundary=collect_edges(members,raw)
local payload={tick=game.tick,surface=surface.name,force=force.name,area=area_result,nodes=nodes,machines=machines,belts=belts,chests=chests,inserters=inserters,fluidEntities=fluids,edges=edges,boundaryEdges=boundary}
if compact then
  -- Nodes once, edges as endpoint keys, and no repeated entity lists: the node
  -- type already says which nodes are machines, belts, chests or fluid entities.
  local function endpoint_key(obj)
    if not obj then return nil end
    if obj.unitNumber then return obj.surface..":"..tostring(obj.unitNumber) end
    return obj.surface..":"..obj.type..":"..obj.name..":"..tostring(obj.position.x)..":"..tostring(obj.position.y)
  end
  local function compact_edge(edge)
    return {from=endpoint_key(edge.from),to=endpoint_key(edge.to),kind=edge.kind,boundary=edge.boundary,flowDirection=edge.flowDirection}
  end
  local function compact_inserter(entry)
    return {inserter=endpoint_key(entry.inserter),pickup=endpoint_key(entry.pickup),drop=endpoint_key(entry.drop)}
  end
  local compact_edges,compact_boundary,compact_inserters={},{},{}
  for _,edge in ipairs(edges) do compact_edges[#compact_edges+1]=compact_edge(edge) end
  for _,edge in ipairs(boundary) do compact_boundary[#compact_boundary+1]=compact_edge(edge) end
  for _,entry in ipairs(inserters) do compact_inserters[#compact_inserters+1]=compact_inserter(entry) end
  payload={tick=game.tick,surface=surface.name,force=force.name,area=area_result,nodes=nodes,machines={},belts={},chests={},inserters={},fluidEntities={},edges={},boundaryEdges={}}
  -- Empty lists are omitted instead of encoded as {}, so a compact graph with no
  -- edges or inserters decodes without relying on response normalization.
  if #compact_edges>0 then payload.compactEdges=compact_edges end
  if #compact_boundary>0 then payload.compactBoundaryEdges=compact_boundary end
  if #compact_inserters>0 then payload.compactInserters=compact_inserters end
end
-- END factorio_ai_topology.factory_graph
  return payload
end

local function adjacency_op(surface_name,force_name,area,tolerance,max_nodes,max_pairs,kind_list,only_unconnected)
kind_list=kind_list or {}
only_unconnected=only_unconnected and true or false
-- BEGIN factorio_ai_topology.adjacency
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local area_result={leftTopX=area[1][1],leftTopY=area[1][2],rightBottomX=area[2][1],rightBottomY=area[2][2]}
local skip={character=true,unit=true,["spider-unit"]=true,["construction-robot"]=true,["logistic-robot"]=true,["combat-robot"]=true,["spider-leg"]=true,corpse=true,["item-entity"]=true,["entity-ghost"]=true,["tile-ghost"]=true,["item-request-proxy"]=true}
local kind_set={}
for _,kind in ipairs(kind_list) do
  local ok,found=pcall(function() return surface.find_entities_filtered{area=area,type=kind,limit=1} end)
  if not ok then fail("unknown entity type in kinds: "..kind); return end
  kind_set[kind]=true
end
local filter_kinds=#kind_list>0
local candidates={}
for _,e in pairs(surface.find_entities_filtered{area=area,force=force}) do
  if valid(e) and not skip[e.type] and (not filter_kinds or kind_set[e.type]) then candidates[#candidates+1]=e end
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
-- An inserter edge is decided by the tile its arm reaches, because the arm
-- position carries a sub-tile offset and a point search does not answer for
-- belts. Indexing the scanned belts once keeps the pair rule exact.
local belt_index=belt_tile_index(candidates)
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
    local pickup,drop=inserter_targets(a,belt_index)
    if pickup==b then return "pickup" end
    if drop==b then return "drop" end
  end
  if b.type=="inserter" then
    local pickup,drop=inserter_targets(b,belt_index)
    if pickup==a then return "pickup" end
    if drop==a then return "drop" end
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
            local kind=connection_kind(a,b)
            if only_unconnected and kind~="" then
              -- Connected pairs are skipped before the limit so limit bounds
              -- only the suspicious rows.
            else
              if full then truncated=true; break end
              pair_list[#pair_list+1]={a=endpoint(a),b=endpoint(b),connection=kind,gap=math.floor(g*1000+0.5)/1000}
              if #pair_list>=max_pairs then full=true end
            end
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
local payload={tick=game.tick,surface=surface.name,force=force.name,area=area_result,
  tolerance=tolerance,limit=max_nodes,truncated=truncated,nodes=nodes,pairs=pair_list}
-- END factorio_ai_topology.adjacency
  return payload
end

local function belt_components_op(surface_name,force_name,area,max_entities,page_limit,page_offset,dead_only,include_line_links)
-- BEGIN factorio_ai_topology.belt_components
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
  components=page}
-- END factorio_ai_topology.belt_components
  return payload
end

-- Inspecting an inserter must return its own targets, which the shared scan
-- cannot see because it keeps inserters whose target is the inspected entity.
local function inspection_logistics_op(surface_name,force_name,entity_name,position)
-- BEGIN factorio_ai_topology.inspection_logistics
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local target
for _,candidate in pairs(surface.find_entities_filtered{name=entity_name,position=position,radius=0.01,force=force}) do
  if candidate.position.x==position.x and candidate.position.y==position.y then
    if target then fail("multiple matching entities at center"); return end
    target=candidate
  end
end
if not target then fail("entity not found at center on force"); return end
local result={}
local seen={}
local bounds=target.bounding_box
local scan_area={{bounds.left_top.x-1.5,bounds.left_top.y-1.5},{bounds.right_bottom.x+1.5,bounds.right_bottom.y+1.5}}
-- An arm reaches the tile containing its pickup/drop position, which carries a
-- sub-tile offset, so the belts around the inspected entity are indexed by tile
-- instead of relying on a point search that never answers for a belt.
local belt_index=belt_tile_index(surface.find_entities_filtered{area=scan_area,type=belt_types})
local self_entry=nil
if target.type=="inserter" then
  local pickup,drop=inserter_targets(target,belt_index)
  seen[key(target)]=true
  self_entry={inserter=endpoint(target),pickup=endpoint(pickup),drop=endpoint(drop)}
end
local neighbours={}
for _,e in pairs(surface.find_entities_filtered{area=scan_area,type="inserter"}) do
  if not seen[key(e)] then
    local pickup,drop=inserter_targets(e,belt_index)
    if pickup==target or drop==target then
      seen[key(e)]=true
      neighbours[#neighbours+1]={inserter=endpoint(e),pickup=endpoint(pickup),drop=endpoint(drop)}
    end
  end
end
table.sort(neighbours,function(a,b)
  local x,y=a.inserter,b.inserter
  if x.position.x~=y.position.x then return x.position.x<y.position.x end
  if x.position.y~=y.position.y then return x.position.y<y.position.y end
  return (x.unitNumber or 0)<(y.unitNumber or 0)
end)
-- The inspected inserter is always first: it is the entity the caller asked about.
if self_entry then result[#result+1]=self_entry end
for _,entry in ipairs(neighbours) do result[#result+1]=entry end
local payload={tick=game.tick,surface=surface.name,inserters=result}
-- END factorio_ai_topology.inspection_logistics
  return payload
end

local function guarded(fn)
  return function(...)
    reset_topology_caches()
    local ok,result=pcall(fn,...)
    if not ok then return {error=tostring(result)} end
    return result
  end
end

remote.add_interface("factorio_ai_topology", {
  version = function() return 3 end,
  belt_run = guarded(belt_run_op),
  belt_stretch = guarded(belt_stretch_op),
  belt_runs = guarded(belt_runs_op),
  factory_graph = guarded(factory_graph_op),
  adjacency = guarded(adjacency_op),
  adjacency_filtered = guarded(adjacency_op),
  belt_components = guarded(belt_components_op),
  inspection_logistics = guarded(inspection_logistics_op)
})

return M
