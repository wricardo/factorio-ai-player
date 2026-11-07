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

// graphCatalogLua reads the game catalogs the production graph needs: the
// recipes, mineable resources and machine speeds the snapshot references, plus
// every infinity container of the force (filters and contents) and the force's
// mining productivity bonus. It runs after the entity pass, so the request
// lists only prototypes the snapshot actually uses.
func graphCatalogLua(force string, surfaces, recipes, resources, machines []string) string {
	return luaFail + fmt.Sprintf(`
local force=game.forces[%s]
if not force then fail("force not found"); return end
local recipe_names=%s
local resource_names=%s
local machine_names=%s
local surface_names=%s
local function products(list)
  local out={}
  if type(list)~="table" then return out end
  for _,p in ipairs(list) do
    out[#out+1]={type=p.type,name=p.name,amount=p.amount,amountMin=p.amount_min,amountMax=p.amount_max,
      probability=p.probability,extraCountFraction=p.extra_count_fraction}
  end
  return out
end
local recs={}
for _,name in ipairs(recipe_names) do
  local ok,p=pcall(function() return prototypes.recipe[name] end)
  if ok and p then
    recs[name]={energy=p.energy,category=p.category,ingredients=products(p.ingredients),products=products(p.products)}
  end
end
local res={}
for _,name in ipairs(resource_names) do
  local ok,e=pcall(function() return prototypes.entity[name] end)
  if ok and e and e.mineable_properties then
    local mp=e.mineable_properties
    res[name]={miningTime=mp.mining_time,products=products(mp.products)}
  end
end
local mach={}
local accepted={}
for _,name in ipairs(machine_names) do
  local ok,e=pcall(function() return prototypes.entity[name] end)
  if ok and e then
    local row={}
    local okc,cs=pcall(function() return e.get_crafting_speed() end)
    if okc and type(cs)=="number" then row.craftingSpeed=cs end
    local okm,ms=pcall(function() return e.mining_speed end)
    if okm and type(ms)=="number" then row.miningSpeed=ms end
    -- lab_inputs is a prototype attribute valid only on labs: the pcall turns
    -- the engine error for every other prototype into a skip.
    local okl,li=pcall(function() return e.lab_inputs end)
    if okl and type(li)=="table" and #li>0 then
      row.labInputs=li
      for _,pack in ipairs(li) do accepted[pack]=true end
    end
    mach[name]=row
  end
end
local inf={}
for _,sname in ipairs(surface_names) do
  local s=game.surfaces[sname]
  if s then
    for _,e in pairs(s.find_entities_filtered{force=force,type="infinity-container"}) do
      local row={unit=e.unit_number,surface=s.name,filters={},contents={}}
      local okf,filters=pcall(function() return e.infinity_container_filters end)
      if okf and filters then row.filters=filters end
      local okr,remove=pcall(function() return e.remove_unfiltered_items end)
      if okr and type(remove)=="boolean" then row.removeUnfiltered=remove end
      local okc,contents=pcall(function() return e.get_inventory(defines.inventory.chest).get_contents() end)
      if okc and contents then
        for _,c in pairs(contents) do row.contents[#row.contents+1]={name=c.name,quality=c.quality,count=c.count} end
      end
      inf[#inf+1]=row
    end
  end
end
-- Technologies are force-level prototype data. Only technologies whose every
-- research-unit pack some lab in the snapshot accepts are exported, so the
-- list stays small and every row is researchable with the labs on the map.
local techs={}
if next(accepted) then
  for tname,t in pairs(prototypes.technology) do
    local okh,hidden=pcall(function() return t.hidden end)
    if not (okh and hidden) then
      local ings,fits={},true
      local oki,list=pcall(function() return t.research_unit_ingredients end)
      if oki and type(list)=="table" then
        for _,ing in ipairs(list) do
          if not accepted[ing.name] then fits=false; break end
          ings[#ings+1]={name=ing.name,amount=ing.amount}
        end
      end
      if fits then
        local row={name=tname,ingredients=ings,prerequisites={},unlocks={}}
        local okc,count=pcall(function() return t.research_unit_count end)
        if okc and type(count)=="number" then row.count=count end
        local oke,energy=pcall(function() return t.research_unit_energy end)
        if oke and type(energy)=="number" then row.energy=energy end
        local okp,prereq=pcall(function() return t.prerequisites end)
        if okp and type(prereq)=="table" then
          for pname in pairs(prereq) do row.prerequisites[#row.prerequisites+1]=pname end
          table.sort(row.prerequisites)
        end
        local okf,effects=pcall(function() return t.effects end)
        if okf and type(effects)=="table" then
          for _,eff in ipairs(effects) do
            if eff.type=="unlock-recipe" and eff.recipe then row.unlocks[#row.unlocks+1]=eff.recipe end
          end
        end
        techs[#techs+1]=row
      end
    end
  end
  table.sort(techs,function(a,b) return a.name<b.name end)
end
rcon.print(helpers.table_to_json({recipes=recs,resources=res,machines=mach,technologies=techs,
  miningProductivity=force.mining_drill_productivity_bonus,infinity=inf}))`,
		luaString(force), luaStringListOrEmpty(recipes), luaStringListOrEmpty(resources),
		luaStringListOrEmpty(machines), luaStringListOrEmpty(surfaces))
}

// graphPreludeLua builds per-entity node records and their owned links on top of
// the topology prelude (valid, is_belt, statuses, belt_connections,
// inserter_targets, machine_drop_target). It is the factorio_ai_topology
// graph_prelude region of the navigation mod, byte for byte.
const graphPreludeLua = `-- defines are read when a wire is seen, not at load, so harnesses that stub
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
local function recipe_name(value)
  if type(value)=="string" then return value end
  return value and value.name or nil
end
local function graph_recipe(e,n)
  if crafting[e.type] then
    local ok,r,q=pcall(function() return e.get_recipe() end)
    if ok and r then
      n.recipe=r.name
      if q then n.recipeQuality=q.name end
    else
      local prev=recipe_name(graph_get(e,"previous_recipe"))
      if prev then
        n.recipe=prev
        n.recipeSource="previous"
      end
    end
    -- products_finished and crafting_progress are cumulative/in-flight counters on
    -- every crafting machine in 2.0; a furnace that switched recipe reports the
    -- old one separately, which is how a recipe-flipping furnace becomes visible.
    local done=graph_get(e,"products_finished")
    if type(done)=="number" then n.productsFinished=done end
    local progress=graph_get(e,"crafting_progress")
    if type(progress)=="number" then n.craftingProgress=progress end
    local speed=graph_get(e,"crafting_speed")
    if type(speed)=="number" then n.craftingSpeed=speed end
  end
  if e.type=="furnace" then
    local prev=recipe_name(graph_get(e,"previous_recipe"))
    if prev then n.previousRecipe=prev end
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
local fuel_types={furnace=true,boiler=true,["mining-drill"]=true,inserter=true,["assembling-machine"]=true,locomotive=true}
local graph_belt_family={["transport-belt"]=true,["underground-belt"]=true,splitter=true,loader=true,["loader-1x1"]=true,["linked-belt"]=true,["lane-splitter"]=true}
-- Every transport line of a belt-family entity, named and sided the way
-- beltTiles/beltStretch name lanes, so a graph sample joins live lane reads.
-- left/right stay lines 1 and 2 for existing queries; lanes carries every
-- non-empty line (undergrounds expose surface and tunnel lines, splitters four);
-- mixedLeft/mixedRight mark a side holding more than one item or quality.
local function graph_belt_lanes(e,n)
  local ok_max,max=pcall(function() return e.get_max_transport_line_index() end)
  if not ok_max or type(max)~="number" or max<1 then return end
  local lanes,side_names={},{}
  local side_counts={left=0,right=0,unknown=0}
  for index=1,max do
    local ok,line=pcall(function() return e.get_transport_line(index) end)
    if ok and line then
      local items,total=graph_items(line)
      if items then
        local name=transport_line_name(index)
        local side=transport_line_side(name)
        side_counts[side]=(side_counts[side] or 0)+total
        local names=side_names[side]
        if not names then names={}; side_names[side]=names end
        for _,entry in ipairs(items) do
          local sep=string.find(entry,":",1,true)
          names[sep and string.sub(entry,1,sep-1) or entry]=true
        end
        if index<=2 then
          local field=index==1 and "left" or "right"
          if #items>0 then n[field]=items end
        end
        if #items>0 then lanes[#lanes+1]=tostring(index).."|"..name.."|"..side.."|"..table.concat(items,"+") end
      end
    end
  end
  if #lanes>0 then n.lanes=lanes end
  -- Counts cover every line of that side: a splitter keeps items on lines 5-8
  -- and an underground on 3-4, so lines 1-2 alone would report zero.
  n.leftCount=side_counts.left or 0
  n.rightCount=side_counts.right or 0
  if side_counts.unknown>0 then n.unknownCount=side_counts.unknown end
  for _,side in ipairs({"left","right"}) do
    local distinct=0
    for _ in pairs(side_names[side] or {}) do distinct=distinct+1 end
    if distinct>1 then
      if side=="left" then n.mixedLeft=true else n.mixedRight=true end
    end
  end
end
-- Contents are a point-in-time sample at the leaf tick: machine fuel, input and
-- output, chest items, and the engine transport lines of every belt-family
-- entity (named, sided and per-side mixed, see graph_belt_lanes).
-- Fluidbox contents are a point-in-time sample: pipe adjacency is FLUID_LINK, but
-- a tank's stock, a refinery's input and a machine's output only exist as amounts
-- inside the boxes. Temperature is what makes steam distinct from water, so each
-- box records it. fluidTotal and fluidCapacity keep an empty box (0) distinct
-- from an entity with no fluidbox (absent).
local function graph_fluid_contents(e,n)
  local ok,fb=pcall(function() return e.fluidbox end)
  if not ok or not fb or #fb==0 then return end
  local list={}
  local total,capacity=0,0
  for i=1,#fb do
    local ok_cap,cap=pcall(function() return fb.get_capacity(i) end)
    if ok_cap and type(cap)=="number" then capacity=capacity+cap end
    local ok_f,fluid=pcall(function() return fb[i] end)
    if ok_f and fluid and fluid.name and type(fluid.amount)=="number" then
      total=total+fluid.amount
      if fluid.amount>0 then
        list[#list+1]=tostring(i).."|"..fluid.name.."|"..tostring(fluid.amount).."|"..tostring(fluid.temperature or 0)
      end
    end
  end
  if #list>0 then n.fluids=list end
  n.fluidTotal=total
  n.fluidCapacity=capacity
end
-- Logistic requests live on the entity's logistic point, not on any inventory:
-- a requester chest can be empty and still demand items, so an empty chest would
-- otherwise be indistinguishable from an unconfigured one.
local function graph_logistic_requests(e,n)
  if e.type~="logistic-container" then return end
  local ok,point=pcall(function() return e.get_logistic_point(0) end)
  if not ok or not point or not point.valid then return end
  local ok_filters,filters=pcall(function() return point.filters end)
  if not ok_filters or not filters then return end
  local list={}
  for _,f in ipairs(filters) do
    if f.name then
      local count=f.max_count or f.count or 0
      list[#list+1]=f.name..((f.quality and f.quality~="normal") and ("@"..f.quality) or "")..":"..tostring(count)
    end
  end
  table.sort(list)
  if #list>0 then n.requests=list end
end
-- A logistic or infinity container's role is the engine's logistic mode (the
-- prototype value, so a chest outside any network still has one). "none" means
-- the container is not a logistic chest at all.
local logistic_chest_types={["logistic-container"]=true,["infinity-container"]=true}
local function graph_chest_mode(e)
  if not logistic_chest_types[e.type] then return nil end
  local ok,mode=pcall(function() return e.prototype.logistic_mode end)
  if ok and type(mode)=="string" and mode~="none" then return mode end
  return nil
end
-- A roboport covers every logistic chest inside its logistic area; construction
-- is true when the chest is also inside its construction area. This is the
-- structural roboport -> chest relation: both also share the network node, but
-- only the cell geometry says which roboport actually reaches a chest.
local function graph_logistic_coverage(e)
  if e.type~="roboport" then return end
  local ok,cell=pcall(function() return e.logistic_cell end)
  if not ok or not cell or not cell.valid then return end
  local ok_r,radius=pcall(function() return cell.logistic_radius end)
  if not ok_r or type(radius)~="number" or radius<=0 then return end
  local p=e.position
  local pad=radius+1
  local ok_f,found=pcall(function()
    return e.surface.find_entities_filtered{area={{p.x-pad,p.y-pad},{p.x+pad,p.y+pad}},force=e.force,type={"logistic-container","infinity-container"}}
  end)
  if not ok_f or not found then return end
  for _,c in ipairs(found) do
    if valid(c) and graph_chest_mode(c) then
      local ok_in,inside=pcall(function() return cell.is_in_logistic_range(c.position) end)
      if ok_in and inside then
        local ok_c,construction=pcall(function() return cell.is_in_construction_range(c.position) end)
        graph_link("logistic_cover",e,c,{construction=ok_c and construction==true})
      end
    end
  end
end
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
  elseif e.type=="cargo-wagon" or e.type=="car" or e.type=="spider-vehicle" then
    graph_set_items(n,"items",graph_inv(e,{"cargo_wagon","car_trunk","spider_trunk"}))
  elseif e.type=="turret" or e.type=="ammo-turret" or e.type=="electric-turret" or e.type=="fluid-turret" or e.type=="artillery-turret" then
    graph_set_items(n,"ammo",graph_inv(e,{"turret_ammo","artillery_turret_ammo"}))
  elseif e.type=="lab" then
    graph_set_items(n,"input",graph_inv(e,{"lab_input"}))
  elseif e.type=="roboport" then
    graph_set_items(n,"robots",graph_inv(e,{"roboport_robot"}))
  elseif graph_belt_family[e.type] then
    graph_belt_lanes(e,n)
  end
end
-- Lane of a belt a drop point lands on, relative to the belt's direction of
-- travel. A drop in line with travel has no perpendicular offset; the engine
-- puts those items on the right transport line, for short and long-handed arms
-- and both toward and away from the belt's motion (engine-verified in
-- integration_test.go TestIntegration/InserterDropLaneRule).
local function graph_lane(belt,pos)
  if not belt or not pos then return nil end
  if belt.type~="transport-belt" and belt.type~="underground-belt" then return nil end
  local dir=travel[belt.direction]
  if not dir then return nil end
  local dot=(pos.x-belt.position.x)*dir[2]-(pos.y-belt.position.y)*dir[1]
  if dot>0.001 then return "left" end
  if dot<-0.001 then return "right" end
  return "right"
end
-- Side of a belt an inserter stands on, relative to the belt's direction of
-- travel. The engine pickup point is always the tile centre, so the standing
-- side is the only per-inserter geometry; it is not a claim about which lane
-- the arm can reach. An arm standing in line with the belt reports ahead (past
-- the belt along its travel) or behind instead of a null side.
local function graph_stand(belt,inserter)
  if not belt then return nil end
  if belt.type~="transport-belt" and belt.type~="underground-belt" then return nil end
  local side=graph_side(belt,inserter)
  if side then return side end
  local dir=travel[belt.direction]
  if not dir then return nil end
  -- The perpendicular dot was already zero to get here, so project along travel.
  local along=(inserter.position.x-belt.position.x)*dir[1]+(inserter.position.y-belt.position.y)*dir[2]
  if along>0.001 then return "ahead" end
  if along<-0.001 then return "behind" end
  return nil
end
-- Inserter settings a mixed belt or a stalled arm is blamed on: which lanes it
-- may pick from, its filter, stack override, the item it currently holds, and
-- whether its drop target resolved at all.
local function graph_inserter_settings(e,n,drop)
  local pos=graph_get(e,"pickup_position")
  if pos then n.pickupX=pos.x; n.pickupY=pos.y end
  pos=graph_get(e,"drop_position")
  if pos then n.dropX=pos.x; n.dropY=pos.y end
  local left=graph_get(e,"pickup_from_left_lane")
  local right=graph_get(e,"pickup_from_right_lane")
  if type(left)=="boolean" then n.pickupLeftLane=left end
  if type(right)=="boolean" then n.pickupRightLane=right end
  local override=graph_get(e,"inserter_stack_size_override")
  if type(override)=="number" then n.stackSizeOverride=override end
  local use=graph_get(e,"use_filters")
  if type(use)=="boolean" then
    n.useFilters=use
    local mode=graph_get(e,"inserter_filter_mode")
    if type(mode)=="string" then n.filterMode=mode end
    local filters={}
    local ok_slots,slots=pcall(function() return e.filter_slot_count end)
    if ok_slots and type(slots)=="number" then
      for i=1,slots do
        local ok,f=pcall(function() return e.get_filter(i) end)
        if ok and f then filters[#filters+1]={index=i,name=f.name,quality=f.quality and f.quality.name or nil} end
      end
    end
    if #filters>0 then n.filters=filters end
  end
  local ok_stack,stack=pcall(function() return e.held_stack end)
  if ok_stack and stack and stack.valid_for_read then n.held=stack.name..":"..tostring(stack.count) end
  if not valid(drop) then n.dropResolved=false end
end
-- A drill's mining_target is one tile and moves as tiles deplete, so the graph
-- also carries the ore composition of the whole mining_area (2.0.77 LuaEntity
-- member) and its bounding box: a drill over two ores is what puts a foreign
-- plate into a furnace that feeds a single-product bus.
local function graph_mining_area(e,n)
  local ok,area=pcall(function() return e.mining_area end)
  if not ok or type(area)~="table" or not area.left_top then return end
  n.miningLeft=area.left_top.x; n.miningTop=area.left_top.y
  n.miningRight=area.right_bottom.x; n.miningBottom=area.right_bottom.y
  local ok_f,found=pcall(function() return e.surface.find_entities_filtered{area=area,type="resource"} end)
  if not ok_f or not found then return end
  local tiles,amounts={},{}
  local total=0
  for _,r in ipairs(found) do
    tiles[r.name]=(tiles[r.name] or 0)+1
    amounts[r.name]=(amounts[r.name] or 0)+(r.amount or 0)
    total=total+1
  end
  n.areaTiles=total
  local list,sums={},{}
  for name,count in pairs(tiles) do list[#list+1]=name..":"..tostring(count) end
  for name,count in pairs(amounts) do sums[#sums+1]=name..":"..tostring(count) end
  table.sort(list)
  table.sort(sums)
  if #list>0 then n.areaResources=list end
  if #sums>0 then n.areaAmounts=sums end
end
local function graph_node(e,belt_index,line_links)
  local n={id=graph_id(e),unit=e.unit_number,name=e.name,type=e.type,surface=e.surface.name,force=e.force.name,x=e.position.x,y=e.position.y}
  local dir=graph_get(e,"direction"); if dir then n.direction=dir end
  local q=graph_get(e,"quality"); if q then n.quality=q.name end
  local st=graph_get(e,"status"); if st then n.status=statuses[st] end
  if e.type=="entity-ghost" or e.type=="tile-ghost" then
    n.ghostName=graph_get(e,"ghost_name"); n.ghostType=graph_get(e,"ghost_type")
    if e.type=="entity-ghost" then
      local ok_recipe,recipe=pcall(function() return e.get_recipe() end)
      if ok_recipe and recipe then
        local ok_name,name=pcall(function() return recipe.name end)
        if ok_name and type(name)=="string" then n.ghostRecipe=name end
      end
    end
  end
  graph_recipe(e,n)
  local en=graph_get(e,"electric_network_id"); if type(en)=="number" then n.electricNetwork=en end
  local ln=graph_get(e,"logistic_network"); if ln and ln.valid then n.logisticNetwork=ln.network_id end
  local train=graph_get(e,"train"); if train and train.valid then n.train=train.id end
  graph_modules(e,n)
  graph_contents(e,n)
  graph_fluid_contents(e,n)
  graph_logistic_requests(e,n)
  local chest_mode=graph_chest_mode(e)
  if chest_mode then n.logisticMode=chest_mode end
  graph_logistic_coverage(e)
  graph_wires(e,n)
  if is_belt(e) then
    if e.type=="splitter" or e.type=="lane-splitter" then
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
    -- BeltStretch building needs the engine's own straight/left/right shape: a
    -- curved belt's input is a continuation, not a side-load.
    local shape=graph_get(e,"belt_shape")
    if type(shape)=="string" then n.beltShape=shape end
    if e.type=="loader" or e.type=="loader-1x1" then
      local container=graph_get(e,"loader_container")
      if valid(container) then
        if e.loader_type=="input" then graph_link("loader",e,container) else graph_link("loader",container,e) end
      end
    end
  end
  if e.type=="splitter" or e.type=="lane-splitter" then
    n.inputPriority=graph_get(e,"splitter_input_priority")
    n.outputPriority=graph_get(e,"splitter_output_priority")
    -- 2.0.77 exposes the splitter filter as one ItemFilter property
    -- (LuaEntity.splitter_filter, shared with LaneSplitter), not through
    -- get_filter(slot): that path is inserters, loaders, drills, asteroid
    -- collectors and logistic containers only, so it silently returned nil here.
    local ok_f,f=pcall(function() return e.splitter_filter end)
    if ok_f and f and f.name then
      n.filters={{index=1,name=f.name,quality=f.quality and f.quality.name or nil}}
    end
  end
  if e.type=="inserter" then
    local p,d=inserter_targets(e,belt_index)
    graph_link("pickup",p,e,{side=graph_stand(p,e)})
    graph_link("drop",e,d,{side=graph_lane(d,graph_get(e,"drop_position"))})
    graph_inserter_settings(e,n,d)
  elseif dropping[e.type] then
    local t=machine_drop_target(e,belt_index)
    if valid(t) then graph_link("drop",e,t) end
  end
  if e.type=="mining-drill" then
    local t=graph_get(e,"mining_target")
    if valid(t) then graph_link("mines",e,t) end
    graph_mining_area(e,n)
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
