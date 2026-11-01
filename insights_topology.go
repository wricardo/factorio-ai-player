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

// transportLineLua names function-local transport lines. It must be
// concatenated before any chunk that reads lanes, because Lua locals are only
// visible to text that follows them in the same chunk.
const transportLineLua = `local transport_line_names={}
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
`

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
local target_types={["assembling-machine"]=true,furnace=true,["mining-drill"]=true,lab=true,["rocket-silo"]=true,container=true,["logistic-container"]=true,["linked-container"]=true,["infinity-container"]=true}
for _,t in ipairs(belt_types) do target_types[t]=true end
local function position_target(e,pos)
  if not valid(e) or type(pos)~="table" then return nil end
  local ok,found=pcall(function() return e.surface.find_entities_filtered{position=pos,force=e.force,limit=16} end)
  if not ok or not found then return nil end
  for _,c in pairs(found) do
    if valid(c) and c~=e and target_types[c.type] then return c end
  end
  return nil
end
local engine_targets={}
local function resolved_target(e,kind,pos_name)
  local ok,target=pcall(function() return e[kind] end)
  if ok and valid(target) then return target end
  local ok_pos,pos=pcall(function() return e[pos_name] end)
  if not ok_pos then return nil end
  return position_target(e,pos)
end
local function inserter_targets(e)
  local k=key(e)
  local cached=engine_targets[k]
  if not cached then
    cached={resolved_target(e,"pickup_target","pickup_position"),resolved_target(e,"drop_target","drop_position")}
    engine_targets[k]=cached
  end
  return cached[1],cached[2]
end
local function connected_inserter(e)
  local p,d=inserter_targets(e)
  return {inserter=endpoint(e),pickup=endpoint(p),drop=endpoint(d)}
end
-- Machines that drop onto an entity (mining drills) have the same staleness.
local function machine_drop_target(e)
  return resolved_target(e,"drop_target","drop_position")
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
      local p,d=inserter_targets(e)
      if (valid(p) and members[key(p)]) or (valid(d) and members[key(d)]) then inserter_entities[#inserter_entities+1]=e end
    end
  end
  table.sort(inserter_entities,less)
  local inserters={}; for _,e in ipairs(inserter_entities) do inserters[#inserters+1]=connected_inserter(e) end
  local edges,boundary=collect_edges(members,raw)
  return {tick=game.tick,surface=seed.surface.name,seed=endpoint(seed),maxEntities=limit,truncated=truncated,nodes=nodes,edges=edges,boundaryEdges=boundary,sources=sources,sinks=sinks,inserters=inserters},members
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
        if belts[1].direction==(e.direction+8)%16 then entry.reason="HEAD_ON"; entry.gap=true; entry.blocker=blocker; return end
      elseif #others>0 then blocker=others[1].name end
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
    local max_index=1
    local ok,value=pcall(function() return e.get_max_transport_line_index() end)
    if ok and type(value)=="number" and value>=1 then max_index=value end
    -- get_contents reports only the items carried by this entity's tile line,
    -- and line_equals spans a whole internal line across tiles, so deduplication
    -- must stay inside one entity. Summing all tiles is the stretch total.
    local entity_lines={}
    for index=1,max_index do
      local line=e.get_transport_line(index)
      local name=transport_line_name(index)
      local items=line.get_contents()
      table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "normal")<(b.quality or "normal") end)
      local side=transport_line_side(name)
      node.lanes[#node.lanes+1]={index=index,lineName=name,side=side,items=items}
      if #items>1 then mixed[side]=true end
      local duplicate=false
      for _,prior in ipairs(entity_lines) do
        if line.line_equals(prior.line) then duplicate=true; break end
      end
      if not duplicate then entity_lines[#entity_lines+1]={line=line,side=transport_line_side(name)} end
    end
    for _,entry in ipairs(entity_lines) do lines[#lines+1]=entry end
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
  return {tick=game.tick,surface=seed.surface.name,seed=endpoint(seed),maxEntities=limit,truncated=truncated,nodes=nodes,ends=ends,left=total("left"),right=total("right"),unknown=total("unknown")}
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
	lua := luaFail + transportLineLua + topologyCommonLua + fmt.Sprintf(`
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

// beltStretchQuery walks one linear stretch and totals items per side, using
// engine transport-line identity so underground tunnel contents count once.
func beltStretchQuery(r *Resolver, surface string, inputX, inputY *float64, unitNumber *string, maxEntities int) (*BeltStretch, error) {
	surface, x, y, err := resolveBeltAddress(r, surface, unitNumber, inputX, inputY)
	if err != nil {
		return nil, err
	}
	if err := topologyBeltParams(surface, x, y, maxEntities); err != nil {
		return nil, err
	}
	lua := luaFail + transportLineLua + topologyCommonLua + fmt.Sprintf(`
local seed,err=belt_at(%s,%s,%s)
if not seed then fail(err); return end
local result=belt_stretch(seed,%d)
rcon.print(helpers.table_to_json(result))`, luaString(surface), strconv.FormatFloat(x, 'g', -1, 64), strconv.FormatFloat(y, 'g', -1, 64), maxEntities)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltStretch](response)
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
	lua := luaFail + transportLineLua + topologyCommonLua + fmt.Sprintf(`
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
	lua := luaFail + transportLineLua + topologyCommonLua + fmt.Sprintf(`
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
    local target=machine_drop_target(e)
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
-- Inserters stand at most one tile from every node they touch, so the scan
-- area covers one expanding tile instead of the whole surface.
local inserter_scan={{area[1][1]-1.5,area[1][2]-1.5},{area[2][1]+1.5,area[2][2]+1.5}}
local inserter_entities={}
for _,e in pairs(surface.find_entities_filtered{area=inserter_scan,type="inserter"}) do
  local p,d=inserter_targets(e)
  if members[key(e)] or (valid(p) and members[key(p)]) or (valid(d) and members[key(d)]) then inserter_entities[#inserter_entities+1]=e end
end
table.sort(inserter_entities,less)
local inserters={}
for _,e in ipairs(inserter_entities) do
  inserters[#inserters+1]=connected_inserter(e)
  local p,d=inserter_targets(e)
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
