package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// topologyModInterface is the compiled-once topology library in the
// factorio_ai_navigation mod. Sending a remote.call with its arguments replaces
// reshipping ~17.6 KB of helper Lua, which cost about 0.87 s of compile per
// topology query. The inline fallback below stays byte-identical to the mod file
// (TestTopologyModPreludeParity) so an old save without the interface still works.
const (
	topologyModInterface = "factorio_ai_topology"
	topologyModProtocol  = 9
)

// topologyInterfaceState memoizes whether this save has the compiled topology mod.
// The probe is one tiny RCON command; caching it keeps ~0.87 s of Lua compile off
// every later query instead of shipping the inline prelude and returning early.
type topologyInterfaceState struct {
	once      sync.Once
	available bool
}

var (
	topologyInterface         topologyInterfaceState
	topologyInterfaceOverride *bool
)

// topologyOpCache memoizes whether the loaded mod exposes one operation, so a
// filtered call pays the probe once per process instead of once per query.
var topologyOpCache sync.Map

func topologyOpAvailable(r *Resolver, op string) bool {
	if cached, ok := topologyOpCache.Load(op); ok {
		return cached.(bool)
	}
	lua := luaFail + fmt.Sprintf(`local interface=remote.interfaces[%s]
if interface and interface[%s] then rcon.print(helpers.table_to_json{supported=1})
else rcon.print(helpers.table_to_json{supported=0}) end`,
		luaString(topologyModInterface), luaString(op))
	supported := false
	if response, _, err := executeLua(r.addr, r.password, lua); err == nil {
		if result, derr := factoryDecode[struct {
			Supported int `json:"supported"`
		}](response); derr == nil {
			supported = result.Supported == 1
		}
	}
	topologyOpCache.Store(op, supported)
	return supported
}

// resetTopologyInterfaceCache clears the memoized capability, for tests.
func resetTopologyInterfaceCache() {
	topologyInterface = topologyInterfaceState{}
	topologyOpCache = sync.Map{}
}

func probeTopologyInterface(r *Resolver) bool {
	lua := luaFail + fmt.Sprintf(`local interface=remote.interfaces[%s]
if interface and interface["version"] and remote.call(%s,"version")==%d then
  rcon.print(helpers.table_to_json{topology=1})
else
  rcon.print(helpers.table_to_json{topology=0})
end`, luaString(topologyModInterface), luaString(topologyModInterface), topologyModProtocol)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return false
	}
	result, err := factoryDecode[struct {
		Topology int `json:"topology"`
	}](response)
	if err != nil {
		return false
	}
	return result.Topology == 1
}

// topologyModAvailable reports whether the mod interface can serve this process.
// The probe runs at most once per process; a failed probe falls back to the inline
// implementation, which is always correct.
func topologyModAvailable(r *Resolver) bool {
	if topologyInterfaceOverride != nil {
		return *topologyInterfaceOverride
	}
	topologyInterface.once.Do(func() { topologyInterface.available = probeTopologyInterface(r) })
	return topologyInterface.available
}

// topologyRemoteChunk is the whole command when the mod interface is present: the
// remote call plus shape guards. The inline prelude and body stay out of the
// chunk so Lua compiles a few hundred bytes instead of ~17 KB.
func topologyRemoteChunk(op string, args []string) string {
	return fmt.Sprintf(`local interface=remote.interfaces[%s]
if not interface or not interface[%s] then fail(%s); return end
local ok,result=pcall(function() return remote.call(%s,%s,%s) end)
if not ok then fail(tostring(result)); return end
if type(result)~="table" then fail(%s); return end
if result.error~=nil then fail(tostring(result.error)); return end
rcon.print(helpers.table_to_json(result))`,
		luaString(topologyModInterface), luaString(op),
		luaString("factorio_ai_topology "+op+" is not loaded in this save"),
		luaString(topologyModInterface), luaString(op), strings.Join(args, ","),
		luaString("factorio_ai_topology "+op+" returned no payload"))
}

// topologyChunk assembles one topology RCON command on exactly one path: the
// compiled-mod call when the interface is available, otherwise the inline
// prelude and body. Choosing in Go is what removes the compile cost; an early
// return inside a single chunk would still pay it.
func topologyChunk(r *Resolver, op string, args []string, locals, body string) string {
	return topologyChunkWithOp(r, op, args, locals, body, false)
}

// topologyChunkWithOp picks the compiled-mod call or the inline fallback. When
// requireOp is set, the mod must actually expose op: an older mod that ignores
// extra arguments would otherwise answer silently with unfiltered data, so a
// missing op falls back to the inline implementation instead.
func topologyChunkWithOp(r *Resolver, op string, args []string, locals, body string, requireOp bool) string {
	if topologyModAvailable(r) && (!requireOp || topologyOpAvailable(r, op)) {
		return luaFail + topologyRemoteChunk(op, args)
	}
	return luaFail + transportLineLua + topologyCommonLua + "\n" + locals + body + "\nrcon.print(helpers.table_to_json(payload))"
}

func topologyNumber(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func topologyBeltParams(surface string, x, y float64, maxEntities int) error {
	if surface == "" {
		return errors.New("surface is required")
	}
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return errors.New("belt coordinates must be finite")
	}
	if maxEntities < 1 || maxEntities > maxBeltTraversal {
		return limitError("maxEntities", maxBeltTraversal)
	}
	return nil
}

// transportLineLua names function-local transport lines. It must be
// concatenated before any chunk that reads lanes, because Lua locals are only
// visible to text that follows them in the same chunk.
const transportLineLua = `-- defines.transport_line collides: 3 is both left_underground_line and
-- secondary_left_line (4 right, 7/8 the secondary splitter lines), so building
-- this table by iterating it is map-order dependent and can rename a lane
-- between loads. Each belt family reports a fixed index order; side is always
-- odd=left, even=right.
local transport_line_names={[1]="left_line",[2]="right_line",[3]="left_underground_line",[4]="right_underground_line",[5]="left_split_line",[6]="right_split_line",[7]="secondary_left_split_line",[8]="secondary_right_split_line"}
local function transport_line_name(index)
  return transport_line_names[index] or ("line_"..tostring(index))
end
local function transport_line_side(name)
  if string.find(name,"left",1,true) then return "left" end
  if string.find(name,"right",1,true) then return "right" end
  return "unknown"
end
`

// topologyCommonLua holds the shared belt topology helpers: engine endpoints, the
// inserter-target fallback, structural belt connections, the component walk, the
// linear stretch walk and the end classification. It follows transportLineLua.
const topologyCommonLua = `
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
  local mixed_entities=0
  for _,e in ipairs(ordered) do
    local node={name=e.name,type=e.type,position=e.position,unitNumber=e.unit_number,direction=e.direction,lanes={}}
    local node_side={}
    local node_mixed=false
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
      if distinct>1 then mixed[side]=true; node_mixed=true end
    end
    if node_mixed then mixed_entities=mixed_entities+1 end
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
  return {tick=game.tick,surface=seed.surface.name,seed=endpoint(seed),maxEntities=limit,truncated=truncated,nodes=nodes,ends=ends,inserters=component_inserters(members,ordered),left=total("left"),right=total("right"),unknown=total("unknown"),mixedEntityCount=mixed_entities}
end`

const beltRunCallLua = `
local seed,err=belt_at(surface_name,seed_x,seed_y)
if not seed then fail(err); return end
local payload=belt_run(seed,limit,include_line_links)`

const beltStretchCallLua = `
local seed,err=belt_at(surface_name,seed_x,seed_y)
if not seed then fail(err); return end
local payload=belt_stretch(seed,limit)`

const beltRunsCallLua = `
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
local payload={beltRuns=output}`

const factoryGraphCallLua = `
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
end`

func beltRunQuery(r *Resolver, surface string, inputX, inputY *float64, unitNumber *string, maxEntities int, includeLineLinks bool) (*BeltRun, error) {
	surface, x, y, err := resolveBeltAddress(r, surface, unitNumber, inputX, inputY)
	if err != nil {
		return nil, err
	}
	if err := topologyBeltParams(surface, x, y, maxEntities); err != nil {
		return nil, err
	}
	lua := topologyChunk(r, "belt_run",
		[]string{luaString(surface), topologyNumber(x), topologyNumber(y), strconv.Itoa(maxEntities), luaBool(includeLineLinks)},
		fmt.Sprintf("local surface_name,seed_x,seed_y,limit,include_line_links=%s,%s,%s,%d,%t\n", luaString(surface), topologyNumber(x), topologyNumber(y), maxEntities, includeLineLinks),
		beltRunCallLua)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltRun](response)
}

// beltStretchQuery walks one linear stretch and totals items per side, counting
// every transport line the engine exposes on each entity, so underground tunnel
// contents and splitter split lines are not dropped.
func beltStretchQuery(r *Resolver, surface string, inputX, inputY *float64, unitNumber *string, maxEntities int) (*BeltStretch, error) {
	surface, x, y, err := resolveBeltAddress(r, surface, unitNumber, inputX, inputY)
	if err != nil {
		return nil, err
	}
	if err := topologyBeltParams(surface, x, y, maxEntities); err != nil {
		return nil, err
	}
	lua := topologyChunk(r, "belt_stretch",
		[]string{luaString(surface), topologyNumber(x), topologyNumber(y), strconv.Itoa(maxEntities)},
		fmt.Sprintf("local surface_name,seed_x,seed_y,limit=%s,%s,%s,%d\n", luaString(surface), topologyNumber(x), topologyNumber(y), maxEntities),
		beltStretchCallLua)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	stretch, err := factoryDecode[BeltStretch](response)
	if err != nil {
		return nil, err
	}
	// An older loaded mod's belt_stretch payload has no mixedEntityCount, and a
	// missing count is indistinguishable from zero. Recompute it from the same
	// node lanes the response carries so the field is correct without waiting
	// for a mod reload; the rule matches BeltStretchTotal.mixed (per belt, per
	// side, across every line that side exposes).
	if stretch != nil {
		stretch.MixedEntityCount = countMixedStretchEntities(stretch)
	}
	return stretch, nil
}

// countMixedStretchEntities counts stretch nodes carrying at least one side
// with more than one distinct prototype/quality combination.
func countMixedStretchEntities(stretch *BeltStretch) int {
	mixed := 0
	for _, node := range stretch.Nodes {
		if node == nil {
			continue
		}
		sides := make(map[string]map[string]bool, 3)
		for _, lane := range node.Lanes {
			if lane == nil {
				continue
			}
			side := lane.Side
			if side == "" {
				side = "unknown"
			}
			seen := sides[side]
			if seen == nil {
				seen = make(map[string]bool, len(lane.Items))
				sides[side] = seen
			}
			for _, stack := range lane.Items {
				if stack == nil {
					continue
				}
				seen[stack.Name+"|"+stack.Quality] = true
			}
		}
		for _, seen := range sides {
			if len(seen) > 1 {
				mixed++
				break
			}
		}
	}
	return mixed
}

func inspectionBeltRuns(r *Resolver, obj *InspectionLogistics, maxEntities int, includeLineLinks bool) ([]*BeltRun, error) {
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
			fmt.Fprintf(&seeds, "{surface=%s,x=%s,y=%s,name=%s,unit=%s},", luaString(e.Surface), topologyNumber(e.Position.X), topologyNumber(e.Position.Y), luaString(e.Name), unit)
		}
	}
	if seeds.Len() == 0 {
		return []*BeltRun{}, nil
	}
	lua := topologyChunk(r, "belt_runs",
		[]string{"{" + seeds.String() + "}", strconv.Itoa(maxEntities), luaBool(includeLineLinks)},
		fmt.Sprintf("local requests,limit,include_line_links={%s},%d,%t\n", seeds.String(), maxEntities, includeLineLinks),
		beltRunsCallLua)
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

// factoryGraphQuery reads one area snapshot. Compact mode emits nodes once and
// edges as endpoint keys, dropping the repeated machine/belt/chest/inserter/fluid
// lists and the fully duplicated edge endpoints.
func factoryGraphQuery(r *Resolver, area *FactoryAreaInput, force, surface string, compact, includeLineLinks bool) (*FactoryGraph, error) {
	if surface == "" || force == "" {
		return nil, errors.New("surface and force are required")
	}
	areaLua, _, err := worldAreaLua(area, 1, 0)
	if err != nil {
		return nil, err
	}
	lua := topologyChunk(r, "factory_graph",
		[]string{luaString(surface), luaString(force), areaLua, luaBool(compact), luaBool(includeLineLinks)},
		fmt.Sprintf("local surface_name,force_name,compact,include_line_links=%s,%s,%t,%t\nlocal area=%s\n", luaString(surface), luaString(force), compact, includeLineLinks, areaLua),
		factoryGraphCallLua)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[FactoryGraph](response)
}
