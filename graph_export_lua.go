package main

import (
	"fmt"
	"strconv"
	"strings"
)

// The graph exporter reads the world in four kinds of Lua chunk:
//
//   - census: per-surface entity totals for one force, split into static
//     entities (exported per leaf), mobile entities (exported in one pass) and
//     skipped entities (robots and transient effects, never exported);
//   - chunks: the generated chunks of one surface holding at least one entity
//     of the force, paged so a huge map never stalls the game for long;
//   - leaf: every static entity whose centre lies inside one half-open tile
//     rectangle, plus every structural connection those entities own;
//   - mobile: every vehicle and character of the force in one tick.
//
// Leaves are disjoint and cover every non-empty chunk, so every static entity
// is owned by exactly one leaf and the census total proves completeness.
//
// Leaf and mobile chunks reuse the topology prelude. The navigation mod
// compiles that prelude plus graphPreludeLua once and exposes graph_leaf and
// graph_mobile on factorio_ai_topology; the console then sends only a
// remote.call. Without those ops the whole text is sent inline, which is
// correct but slow: a multiplayer server replicates every console command to
// its clients, and a ~35 KB command takes over a second to arrive.

// graphMobileTypes move while the export runs, so per-leaf ownership by centre
// position could miss or double-count them. They are read in one call instead.
var graphMobileTypes = []string{
	"character", "car", "spider-vehicle", "locomotive", "cargo-wagon", "fluid-wagon", "artillery-wagon",
}

// graphSkippedTypes are never exported: robots are logistic stock, the rest are
// transient effects. Counting them would make the census move every tick.
var graphSkippedTypes = []string{
	"logistic-robot", "construction-robot", "combat-robot", "capture-robot",
	"spider-leg", "cargo-pod", "projectile", "artillery-projectile", "beam", "stream",
	"fire", "sticker", "explosion", "particle-source", "smoke-with-trigger",
	"speech-bubble", "highlight-box", "arrow", "deconstructible-tile-proxy",
}

func luaStringListOrEmpty(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = luaString(v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// graphKnownTypesLua keeps only engine types this game defines, because the
// engine rejects an unknown type in an entity filter. Census and chunk calls
// are small and always run inline.
const graphKnownTypesLua = `
local engine_types={}
for _,p in pairs(prototypes.entity) do engine_types[p.type]=true end
local function known(list)
  local out={}
  for _,t in ipairs(list) do if engine_types[t] then out[#out+1]=t end end
  return out
end
local function count_types(surface,force,list)
  if #list==0 then return 0 end
  return surface.count_entities_filtered{force=force,type=list}
end
`

// graphCensusLua totals each requested surface (all surfaces when names is
// empty) for one force in a single tick.
func graphCensusLua(force string, surfaces []string) string {
	return luaFail + graphKnownTypesLua + fmt.Sprintf(`
local force=game.forces[%s]
if not force then fail("force not found"); return end
local wanted=%s
local mobile=known(%s)
local skipped=known(%s)
local list={}
if #wanted==0 then
  for _,s in pairs(game.surfaces) do list[#list+1]=s end
else
  for _,name in ipairs(wanted) do
    local s=game.surfaces[name]
    if not s then fail("surface not found: "..name); return end
    list[#list+1]=s
  end
end
table.sort(list,function(a,b) return a.index<b.index end)
local rows={}
for _,s in ipairs(list) do
  local total=s.count_entities_filtered{force=force}
  local m=count_types(s,force,mobile)
  local k=count_types(s,force,skipped)
  rows[#rows+1]={surface=s.name,index=s.index,total=total,mobile=m,skipped=k,static=total-m-k}
end
rcon.print(helpers.table_to_json({tick=game.tick,force=force.name,surfaces=rows}))`,
		luaString(force), luaStringListOrEmpty(surfaces),
		luaStringListOrEmpty(graphMobileTypes), luaStringListOrEmpty(graphSkippedTypes))
}

// graphChunksLua lists the generated chunks of one surface that intersect at
// least one entity of the force, visiting at most limit chunks from offset.
// Counts include every entity type; they only size leaves.
func graphChunksLua(surface, force string, offset, limit int) string {
	return luaFail + fmt.Sprintf(`
local s=game.surfaces[%s]
if not s then fail("surface not found"); return end
local force=game.forces[%s]
if not force then fail("force not found"); return end
local offset,limit=%d,%d
local i,visited,rows=0,0,{}
local done=true
for c in s.get_chunks() do
  if i>=offset then
    if visited>=limit then done=false; break end
    visited=visited+1
    local n=s.count_entities_filtered{area=c.area,force=force}
    if n>0 then rows[#rows+1]={x=c.x,y=c.y,n=n} end
  end
  i=i+1
end
local payload={tick=game.tick,next=offset+visited,done=done}
if #rows>0 then payload.chunks=rows end
rcon.print(helpers.table_to_json(payload))`, luaString(surface), luaString(force), offset, limit)
}

// graphPreludeLua builds per-entity node records and their owned links on top of
// the topology prelude (valid, is_belt, statuses, belt_connections,
// inserter_targets, machine_drop_target). It is the factorio_ai_topology
// graph_prelude region of the navigation mod, byte for byte.
const graphPreludeLua = `
-- defines are read when a wire is seen, not at load, so harnesses that stub
-- only the defines they need can still load the module.
local function wire_name(wire_type)
  for k,v in pairs(defines.wire_type) do if v==wire_type then return k end end
  return tostring(wire_type)
end
-- Side of one belt relative to another entity's travel direction, measured
-- between bounding-box centres: a splitter spans two tiles, so its position is
-- not the middle of its footprint.
local travel={[0]={0,-1},[4]={1,0},[8]={0,1},[12]={-1,0}}
local function graph_side(owner,other)
  local dir=travel[owner.direction]
  if not dir then return nil end
  local ok_a,ba=pcall(function() return owner.bounding_box end)
  local ok_b,bb=pcall(function() return other.bounding_box end)
  if not ok_a or not ok_b or not ba or not bb then return nil end
  local dx=(bb.left_top.x+bb.right_bottom.x)/2-(ba.left_top.x+ba.right_bottom.x)/2
  local dy=(bb.left_top.y+bb.right_bottom.y)/2-(ba.left_top.y+ba.right_bottom.y)/2
  -- Left of travel: a north-facing pair keeps its west neighbour on the left,
  -- an east-facing pair keeps its north neighbour on the left.
  local dot=dx*dir[2]-dy*dir[1]
  if dot>0.001 then return "left" end
  if dot<-0.001 then return "right" end
  return nil
end
local combinator_types={["arithmetic-combinator"]=true,["decider-combinator"]=true,["selector-combinator"]=true}
-- Connector ids are shared between entity kinds (pole_copper and
-- power_switch_left_copper are both 5), so the name depends on the owner.
local function connector_name(e,id)
  local candidates
  if combinator_types[e.type] then
    candidates={"combinator_input_red","combinator_input_green","combinator_output_red","combinator_output_green"}
  elseif e.type=="power-switch" then
    candidates={"power_switch_left_copper","power_switch_right_copper","circuit_red","circuit_green"}
  else
    candidates={"circuit_red","circuit_green","pole_copper"}
  end
  for _,name in ipairs(candidates) do
    if defines.wire_connector_id[name]==id then return name end
  end
  return tostring(id)
end
local crafting={["assembling-machine"]=true,furnace=true,["rocket-silo"]=true}
local dropping={["mining-drill"]=true,["assembling-machine"]=true,furnace=true}
local rail_types={["straight-rail"]=true,["curved-rail-a"]=true,["curved-rail-b"]=true,["half-diagonal-rail"]=true,["rail-ramp"]=true,["elevated-straight-rail"]=true,["elevated-curved-rail-a"]=true,["elevated-curved-rail-b"]=true,["elevated-half-diagonal-rail"]=true,["legacy-straight-rail"]=true,["legacy-curved-rail"]=true}
local graph_types=nil
local function graph_known(list)
  if not graph_types then
    graph_types={}
    for _,p in pairs(prototypes.entity) do graph_types[p.type]=true end
  end
  local out={}
  for _,t in ipairs(list) do if graph_types[t] then out[#out+1]=t end end
  return out
end
local graph_edges,graph_refs,graph_refseen={},{},{}
local function graph_reset()
  graph_edges,graph_refs,graph_refseen={},{},{}
end
local function graph_id(e)
  if e.unit_number then return e.surface.name..":"..tostring(e.unit_number) end
  return e.surface.name..":"..e.type..":"..e.name..":"..tostring(e.position.x)..":"..tostring(e.position.y)
end
local function graph_ref(e)
  local id=graph_id(e)
  if not graph_refseen[id] then
    graph_refseen[id]=true
    graph_refs[#graph_refs+1]={id=id,unit=e.unit_number,name=e.name,type=e.type,surface=e.surface.name,force=e.force.name,x=e.position.x,y=e.position.y}
  end
  return id
end
local function graph_link(kind,a,b,extra)
  if not valid(a) or not valid(b) or a==b then return end
  local edge={kind=kind,from=graph_ref(a),to=graph_ref(b)}
  if extra then for k,v in pairs(extra) do edge[k]=v end end
  graph_edges[#graph_edges+1]=edge
end
local function graph_get(e,field)
  local ok,v=pcall(function() return e[field] end)
  if ok then return v end
  return nil
end
local function graph_recipe(e,n)
  if not crafting[e.type] then return end
  local ok,r,q=pcall(function() return e.get_recipe() end)
  if ok and r then
    n.recipe=r.name
    if q then n.recipeQuality=q.name end
    return
  end
  if e.type=="furnace" then
    local prev=graph_get(e,"previous_recipe")
    if prev and prev.name then
      n.recipe=type(prev.name)=="string" and prev.name or prev.name.name
      n.recipeSource="previous"
    end
  end
end
local function graph_modules(e,n)
  local ok,inv=pcall(function() return e.get_module_inventory() end)
  if not ok or not inv then return end
  local list={}
  for _,c in pairs(inv.get_contents()) do list[#list+1]=c.name..":"..(c.quality or "normal")..":"..tostring(c.count) end
  table.sort(list)
  if #list>0 then n.modules=list end
end
local function graph_wires(e,n)
  local ok,connectors=pcall(function() return e.get_wire_connectors(false) end)
  if not ok or not connectors then return end
  local circuit={}
  for id,c in pairs(connectors) do
    local color=wire_name(c.wire_type)
    local cname=connector_name(e,id)
    if color~="copper" then
      local ok_net,net=pcall(function() return c.network_id end)
      if ok_net and net and net>0 then circuit[#circuit+1]={connector=cname,color=color,network=net} end
    end
    for _,w in pairs(c.connections) do
      local target=w.target
      if target and target.valid and valid(target.owner) then
        graph_link(color,e,target.owner,{fromConnector=cname,toConnector=connector_name(target.owner,target.wire_connector_id)})
      end
    end
  end
  if #circuit>0 then n.circuit=circuit end
end
-- Every entity with a fluidbox reports its own pipe connections, machines
-- included, so a refinery feeding a chemical plant directly is not lost.
local function graph_fluids(e)
  local ok,fb=pcall(function() return e.fluidbox end)
  if not ok or not fb then return end
  for i=1,#fb do
    local ok_c,conns=pcall(function() return fb.get_pipe_connections(i) end)
    if ok_c and conns then
      for _,c in pairs(conns) do
        local t=c.target
        if t and t.valid and t.owner and t.owner.valid and t.owner~=e then
          graph_link("fluid",e,t.owner,{flowDirection=c.flow_direction})
        end
      end
    end
  end
end
local function graph_rails(e)
  if not rail_types[e.type] then return end
  for _,rd in pairs(defines.rail_direction) do
    for _,cd in pairs(defines.rail_connection_direction) do
      if cd~=defines.rail_connection_direction.none then
        local ok,other=pcall(function() return e.get_connected_rail{rail_direction=rd,rail_connection_direction=cd} end)
        if ok and valid(other) then graph_link("rail",e,other) end
      end
    end
  end
end
-- Items of one inventory as sorted "name:count" strings (quality other than
-- normal is "name@quality:count") and the total item count.
local function graph_items(inv)
  local ok,contents=pcall(function() return inv.get_contents() end)
  if not ok or not contents then return nil end
  local list,total={},0
  for _,c in pairs(contents) do
    local q=c.quality
    if q=="normal" then q=nil end
    list[#list+1]=c.name..(q and ("@"..q) or "")..":"..tostring(c.count)
    total=total+c.count
  end
  table.sort(list)
  return list,total
end
-- key is always paired with keyCount so an empty inventory (count 0) stays
-- distinguishable from an entity that has no such inventory (both absent).
local function graph_set_items(n,key,inv)
  if not inv then return end
  local list,total=graph_items(inv)
  if not list then return end
  n[key.."Count"]=total
  if #list>0 then n[key]=list end
end
local function graph_inv(e,names)
  for _,name in ipairs(names) do
    local ok,inv=pcall(function() return e.get_inventory(defines.inventory[name]) end)
    if ok and inv then return inv end
  end
  return nil
end
local fuel_types={furnace=true,boiler=true,["mining-drill"]=true,inserter=true,["assembling-machine"]=true}
-- Contents are a point-in-time sample at the leaf tick: machine fuel, input and
-- output, chest items, and per-lane belt items (engine transport lines 1 and 2,
-- exported as left and right; the side mapping is the engine's, not re-derived).
local function graph_contents(e,n)
  if fuel_types[e.type] then
    local ok,inv=pcall(function() return e.get_fuel_inventory() end)
    if ok then graph_set_items(n,"fuel",inv) end
  end
  if e.type=="furnace" or e.type=="assembling-machine" or e.type=="rocket-silo" then
    graph_set_items(n,"input",graph_inv(e,{"furnace_source","assembling_machine_input"}))
    graph_set_items(n,"output",graph_inv(e,{"furnace_result","assembling_machine_output"}))
  elseif e.type=="container" or e.type=="logistic-container" then
    graph_set_items(n,"items",graph_inv(e,{"chest"}))
  elseif e.type=="transport-belt" then
    for key,index in pairs({left=1,right=2}) do
      local ok,line=pcall(function() return e.get_transport_line(index) end)
      if ok and line then graph_set_items(n,key,line) end
    end
  end
end
-- Lane of a belt a drop point lands on, relative to the belt's direction of
-- travel.
local function graph_lane(belt,pos)
  if not belt or not pos then return nil end
  if belt.type~="transport-belt" and belt.type~="underground-belt" then return nil end
  local dir=travel[belt.direction]
  if not dir then return nil end
  local dot=(pos.x-belt.position.x)*dir[2]-(pos.y-belt.position.y)*dir[1]
  if dot>0.001 then return "left" end
  if dot<-0.001 then return "right" end
  return nil
end
-- Side of a belt an inserter stands on, relative to the belt's direction of
-- travel. The engine pickup point is always the tile centre, so the standing
-- side is the only per-inserter geometry; it is not a claim about which lane
-- the arm can reach.
local function graph_stand(belt,inserter)
  if not belt then return nil end
  if belt.type~="transport-belt" and belt.type~="underground-belt" then return nil end
  return graph_side(belt,inserter)
end
local function graph_node(e,belt_index,line_links)
  local n={id=graph_id(e),unit=e.unit_number,name=e.name,type=e.type,surface=e.surface.name,force=e.force.name,x=e.position.x,y=e.position.y}
  local dir=graph_get(e,"direction"); if dir then n.direction=dir end
  local q=graph_get(e,"quality"); if q then n.quality=q.name end
  local st=graph_get(e,"status"); if st then n.status=statuses[st] end
  if e.type=="entity-ghost" or e.type=="tile-ghost" then n.ghostName=graph_get(e,"ghost_name"); n.ghostType=graph_get(e,"ghost_type") end
  graph_recipe(e,n)
  local en=graph_get(e,"electric_network_id"); if type(en)=="number" then n.electricNetwork=en end
  local ln=graph_get(e,"logistic_network"); if ln and ln.valid then n.logisticNetwork=ln.network_id end
  local train=graph_get(e,"train"); if train and train.valid then n.train=train.id end
  graph_modules(e,n)
  graph_contents(e,n)
  graph_wires(e,n)
  if is_belt(e) then
    if e.type=="splitter" then
      -- A splitter's two outputs are structural ports: which side of the
      -- splitter each outgoing belt leaves on is the routing fact a graph can
      -- hold, unlike lane balancing between them.
      local neighbours=e.belt_neighbours
      if neighbours then
        for key,n in pairs(neighbours.inputs or {}) do
          graph_link("belt",n,e,{toPort=graph_side(e,n),toPortIndex=tostring(key)})
        end
        for key,n in pairs(neighbours.outputs or {}) do
          graph_link("belt",e,n,{fromPort=graph_side(e,n),fromPortIndex=tostring(key)})
        end
      end
    else
      for _,l in ipairs(belt_connections(e)) do graph_link(l.kind,l.a,l.b) end
    end
    if line_links then for _,l in ipairs(belt_line_connections(e)) do graph_link("line",l.a,l.b) end end
    if e.type=="loader" or e.type=="loader-1x1" then
      local container=graph_get(e,"loader_container")
      if valid(container) then
        if e.loader_type=="input" then graph_link("loader",e,container) else graph_link("loader",container,e) end
      end
    end
  end
  if e.type=="splitter" then
    n.inputPriority=graph_get(e,"splitter_input_priority")
    n.outputPriority=graph_get(e,"splitter_output_priority")
    -- Splitter filters are not available in every 2.0 build: get_filter returns
    -- nil there even after set_filter, so report only what the engine returns.
    local filters={}
    for i=1,2 do
      local ok,f=pcall(function() return e.get_filter(i) end)
      if ok and f then filters[#filters+1]={index=i,name=f.name,quality=f.quality and f.quality.name or nil} end
    end
    if #filters>0 then n.filters=filters end
  end
  if e.type=="inserter" then
    local p,d=inserter_targets(e,belt_index)
    graph_link("pickup",p,e,{side=graph_stand(p,e)})
    graph_link("drop",e,d,{side=graph_lane(d,graph_get(e,"drop_position"))})
  elseif dropping[e.type] then
    local t=machine_drop_target(e,belt_index)
    if valid(t) then graph_link("drop",e,t) end
  end
  if e.type=="mining-drill" then
    local t=graph_get(e,"mining_target")
    if valid(t) then graph_link("mines",e,t) end
  end
  if e.type=="beacon" then
    local ok,receivers=pcall(function() return e.get_beacon_effect_receivers() end)
    if ok and receivers then for _,r in pairs(receivers) do graph_link("beacon",e,r) end end
  end
  graph_fluids(e)
  graph_rails(e)
  return n
end
local function graph_payload(nodes,extra)
  local payload={tick=game.tick}
  if extra then for k,v in pairs(extra) do payload[k]=v end end
  if #nodes>0 then payload.nodes=nodes end
  if #graph_edges>0 then payload.edges=graph_edges end
  if #graph_refs>0 then payload.refs=graph_refs end
  return payload
end`

// graphLeafCallLua is the factorio_ai_topology graph_leaf region. It expects
// surface_name, force_name, area ({{lx,ly},{rx,ry}}), pad, include_line_links
// and excluded_types as locals and leaves the result in payload.
const graphLeafCallLua = `
graph_reset()
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local lx,ly,rx,ry=area[1][1],area[1][2],area[2][1],area[2][2]
local excluded={}
for _,t in ipairs(excluded_types) do excluded[t]=true end
-- The scan is padded so arms and drills at the border still resolve the belts
-- they reach through the tile index; ownership stays by centre position.
local found=surface.find_entities_filtered{area={{lx-pad,ly-pad},{rx+pad,ry+pad}},force=force}
table.sort(found,less)
local belt_index=belt_tile_index(found)
local nodes={}
for _,e in ipairs(found) do
  if valid(e) and not excluded[e.type] then
    local p=e.position
    if p.x>=lx and p.x<rx and p.y>=ly and p.y<ry then nodes[#nodes+1]=graph_node(e,belt_index,include_line_links) end
  end
end
local payload=graph_payload(nodes,{scanned=#found})`

// graphMobileCallLua is the factorio_ai_topology graph_mobile region. It
// expects surface_name, force_name and mobile_types as locals.
const graphMobileCallLua = `
graph_reset()
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local mobile=graph_known(mobile_types)
local nodes={}
if #mobile>0 then
  local found=surface.find_entities_filtered{force=force,type=mobile}
  table.sort(found,less)
  for _,e in ipairs(found) do if valid(e) then nodes[#nodes+1]=graph_node(e,nil,false) end end
end
local payload=graph_payload(nodes,nil)`

func graphExcludedTypes() []string {
	return append(append([]string{}, graphMobileTypes...), graphSkippedTypes...)
}

func graphNumber(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// graphLeafLua exports every static entity of the force whose centre lies in
// the half-open rectangle [lx,rx)x[ly,ry).
func graphLeafLua(r *Resolver, surface, force string, lx, ly, rx, ry, pad float64, includeLineLinks bool) string {
	area := fmt.Sprintf("{{%s,%s},{%s,%s}}", graphNumber(lx), graphNumber(ly), graphNumber(rx), graphNumber(ry))
	excluded := luaStringListOrEmpty(graphExcludedTypes())
	args := []string{luaString(surface), luaString(force), area, graphNumber(pad), luaBool(includeLineLinks), excluded}
	locals := fmt.Sprintf("local surface_name,force_name,area,pad,include_line_links,excluded_types=%s\n", strings.Join(args, ","))
	return topologyChunkWithOp(r, "graph_leaf", args, locals, graphPreludeLua+"\n"+graphLeafCallLua, true)
}

// graphMobileLua exports every mobile entity of the force on one surface in a
// single tick, so moving vehicles and characters are neither missed nor
// counted twice.
func graphMobileLua(r *Resolver, surface, force string) string {
	args := []string{luaString(surface), luaString(force), luaStringListOrEmpty(graphMobileTypes)}
	locals := fmt.Sprintf("local surface_name,force_name,mobile_types=%s\n", strings.Join(args, ","))
	return topologyChunkWithOp(r, "graph_mobile", args, locals, graphPreludeLua+"\n"+graphMobileCallLua, true)
}
