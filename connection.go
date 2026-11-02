package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxConnectionSteps bounds the number of route tiles one connection may touch.
// The route is materialized in Lua, so this caps script work and response size.
const maxConnectionSteps = 4096

// connectionItem resolves the item for a kind and applies its default.
func connectionItem(kind ConnectionKind, item *string) (string, error) {
	if item != nil && *item != "" {
		return *item, nil
	}
	switch kind {
	case ConnectionKindBelt:
		return "transport-belt", nil
	case ConnectionKindPipe:
		return "pipe", nil
	case ConnectionKindPowerPoleChain:
		return "medium-electric-pole", nil
	}
	return "", fmt.Errorf("unsupported connection kind %q", kind)
}

// connectionRouteSteps validates a straight or single-bend route and returns
// its tile count, including both endpoints.
func connectionRouteSteps(from, to *PositionInput) (int, error) {
	if from == nil || to == nil {
		return 0, errors.New("from and to are required")
	}
	for _, p := range []*PositionInput{from, to} {
		if math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) {
			return 0, errors.New("from and to must be finite")
		}
	}
	dx := math.Abs(math.Floor(to.X) - math.Floor(from.X))
	dy := math.Abs(math.Floor(to.Y) - math.Floor(from.Y))
	tiles := dx + dy + 1
	if tiles > maxConnectionSteps {
		return 0, fmt.Errorf("route covers %g tiles, above the %d tile limit; split it into shorter runs", tiles, maxConnectionSteps)
	}
	steps := int(tiles)
	return steps, nil
}

func connectionBend(bend ConnectionBend) (ConnectionBend, error) {
	switch bend {
	case "":
		return ConnectionBendAuto, nil
	case ConnectionBendAuto, ConnectionBendHorizontalFirst, ConnectionBendVerticalFirst:
		return bend, nil
	}
	return "", fmt.Errorf("unsupported connection bend %q", bend)
}

func connectionLuaPreamble(surface, force, item string, from, to *PositionInput, kind ConnectionKind, bend ConnectionBend, allowFluidAdjacency, allowBeltAdjacency bool) string {
	n := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
	return fmt.Sprintf(`local surface_name,force_name=%s,%s
local item=%s
local kind=%s
local from_x,from_y=%s,%s
local to_x,to_y=%s,%s
local bend_request=%s
local allow_adjacency=%t
local allow_belt_adjacency=%t
`, luaString(surface), luaString(force), luaString(item), luaString(string(kind)),
		n(from.X), n(from.Y), n(to.X), n(to.Y), luaString(string(bend)), allowFluidAdjacency, allowBeltAdjacency)
}

// pipeLikeTypesLua lists the entity types that are pipe-like by themselves.
// steam-engine and steam-turbine are prototype names, not types (both are
// generator), so they never matched; fluid-port machines are detected through
// their fluidbox pipe connections instead of a type list.
const pipeLikeTypesLua = `{"pipe","pipe-to-ground","pump","storage-tank","boiler","offshore-pump","heat-exchanger","generator","fusion-generator","assembling-machine","furnace","mining-drill","rocket-silo","reactor","fusion-reactor","fluid-wagon","fluid-turret"}`

// connectionLuaHelpers defines the route model shared by plan and apply. It
// expects the preamble variables and binds surface, force, proto, entity_name,
// spacing, build, evaluate and pick.
const connectionLuaHelpers = `
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local proto=prototypes.item[item]
if not proto then fail("item prototype not found"); return end
if not proto.place_result then fail("item does not place an entity"); return end
local expected_type={BELT="transport-belt",PIPE="pipe",POWER_POLE_CHAIN="electric-pole"}
local want=expected_type[kind]
if not want then fail("unsupported connection kind"); return end
if proto.place_result.type~=want then fail("item places a "..proto.place_result.type..", not a "..want); return end
local entity_name=proto.place_result.name
local spacing=1
if kind=="POWER_POLE_CHAIN" then
  local reach=nil
  local pole_proto=proto.place_result
  if type(pole_proto.get_max_wire_distance)=="function" then
    local ok,value=pcall(function() return pole_proto.get_max_wire_distance("normal") end)
    if ok then reach=value end
  end
  if type(reach)~="number" then
    local entity_proto=prototypes.entity[entity_name]
    if type(entity_proto.get_max_wire_distance)=="function" then
      local ok,value=pcall(function() return entity_proto.get_max_wire_distance("normal") end)
      if ok then reach=value end
    end
  end
  if type(reach)~="number" or reach<1 then fail("item prototype has no wire reach"); return end
  spacing=math.max(1,math.floor(reach))
end
local from={x=math.floor(from_x)+0.5,y=math.floor(from_y)+0.5}
local to={x=math.floor(to_x)+0.5,y=math.floor(to_y)+0.5}
local function dir_to(a,b)
  if b.x>a.x then return 4 elseif b.x<a.x then return 12 elseif b.y>a.y then return 8 else return 0 end
end
local function build(mode)
  local steps={}
  local function add(x,y) steps[#steps+1]={x=x,y=y} end
  local sx=0; if to.x>from.x then sx=1 elseif to.x<from.x then sx=-1 end
  local sy=0; if to.y>from.y then sy=1 elseif to.y<from.y then sy=-1 end
  add(from.x,from.y)
  if mode=="VERTICAL_FIRST" then
    local y=from.y
    while y~=to.y do y=y+sy; add(from.x,y) end
    local x=from.x
    while x~=to.x do x=x+sx; add(x,to.y) end
  else
    local x=from.x
    while x~=to.x do x=x+sx; add(x,from.y) end
    local y=from.y
    while y~=to.y do y=y+sy; add(to.x,y) end
  end
  for i=1,#steps do
    local d=0
    if i<#steps then d=dir_to(steps[i],steps[i+1]) elseif i>1 then d=dir_to(steps[i-1],steps[i]) end
    steps[i].direction=d
  end
  return steps
end
local function spaced(steps)
  if spacing<=1 then return steps end
  local kept={}
  for i=1,#steps do
    if i==1 or i==#steps or (i-1)%spacing==0 then kept[#kept+1]=steps[i] end
  end
  return kept
end
local function tile_entities(x,y)
  return surface.find_entities_filtered{area={{x-0.5,y-0.5},{x+0.5,y+0.5}}}
end
local function tile_blocks_terrain(x,y)
  local tile=surface.get_tile(x,y)
  if not tile or not tile.valid then return false end
  local tile_mask=tile.prototype.collision_mask
  local layers=tile_mask and tile_mask.layers or {}
  for layer,_ in pairs(proto.place_result.collision_mask.layers) do
    if layers[layer] then return true end
  end
  return false
end
local pipe_like={}
for _,t in ipairs(` + pipeLikeTypesLua + `) do pipe_like[t]=true end
-- A fluid-port machine joins the network through a fluidbox pipe connection. A
-- fluidbox that reports no connection yet is treated as touching the tile,
-- because the engine creates the link when a pipe is placed on its port.
local function fluid_port_like(e,x,y)
  local ok,fb=pcall(function() return e.fluidbox end)
  if not ok or not fb then return false end
  local ok_count,count=pcall(function() return #fb end)
  if not ok_count or type(count)~="number" or count==0 then return false end
  local any=false
  for i=1,count do
    local ok_connections,connections=pcall(function() return fb.get_pipe_connections(i) end)
    if ok_connections and connections then
      for _,connection in pairs(connections) do
        any=true
        local target=connection.target_position
        if target and math.floor(target.x)==math.floor(x) and math.floor(target.y)==math.floor(y) then return true end
      end
    end
  end
  return not any
end
-- A belt pointing into a tile pushes its items there, so a planned tile next to
-- one would side-load foreign items or lose a corner.
local belt_feed_types={["transport-belt"]=true,["underground-belt"]=true,["splitter"]=true,["loader"]=true,["loader-1x1"]=true,["linked-belt"]=true}
local feed_vectors={[0]={0,-1},[4]={1,0},[8]={0,1},[12]={-1,0}}
local function feeds_into(e,fx,fy,tx,ty)
  if not e.valid or not belt_feed_types[e.type] then return false end
  if e.type=="underground-belt" and e.belt_to_ground_type~="output" then return false end
  local vector=feed_vectors[e.direction]
  return vector~=nil and fx+vector[1]==tx and fy+vector[2]==ty
end
local function evaluate(mode)
  local steps=build(mode)
  if kind=="POWER_POLE_CHAIN" then steps=spaced(steps) end
  local route_set={}
  for _,s in ipairs(steps) do route_set[s.x..":"..s.y]=true end
  local conflicts={}
  local needed=0
  local function add_conflict(x,y,ckind,message,blockers,terrain,tile_name)
    if #conflicts>=200 then return end
    conflicts[#conflicts+1]={position={x=x,y=y},kind=ckind,message=message,blockers=blockers or {},terrain=terrain or false,tile=tile_name or ""}
  end
  for index,s in ipairs(steps) do
    local found=tile_entities(s.x,s.y)
    local existing=false
    local existing_direction=nil
    local mismatched=false
    for _,e in pairs(found) do
      if e.valid and e.name==entity_name then
      local box=e.bounding_box
      if s.x>=box.left_top.x and s.x<=box.right_bottom.x and s.y>=box.left_top.y and s.y<=box.right_bottom.y then
        if kind~="BELT" or e.direction==s.direction then
          existing=true; existing_direction=e.direction
        elseif index==#steps and e.direction~=(s.direction+8)%16 then
          -- A run normally ends by side-loading the belt it feeds into, so the
          -- goal tile keeps any belt direction except head-on.
          existing=true; existing_direction=e.direction
        else
          mismatched=true
        end
      end
      end
    end
    s.existing=existing
    if existing and existing_direction then s.direction=existing_direction end
    if mismatched then
      add_conflict(s.x,s.y,"DIRECTION_MISMATCH","existing "..entity_name.." faces another direction",{},false,"")
    elseif not existing then
      -- build_check_type.manual is the player build rule; build_check_type.script
      -- only reports whether a script may create the box, which overlaps obstacles.
      if not surface.can_place_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=force,build_check_type=defines.build_check_type.manual} then
        local names,seen={},{}
        for _,e in pairs(found) do
          if e.valid and not seen[e.name] then seen[e.name]=true; names[#names+1]=e.name end
        end
        table.sort(names)
        local tile=surface.get_tile(s.x,s.y)
        local tile_name=(tile and tile.valid) and tile.name or ""
        add_conflict(s.x,s.y,"COLLISION","cannot place "..entity_name.." at this tile",names,tile_blocks_terrain(s.x,s.y),tile_name)
      end
    end
    if kind=="BELT" and not allow_belt_adjacency and not existing then
      for _,d in ipairs({{1,0},{-1,0},{0,1},{0,-1}}) do
        local nx,ny=s.x+d[1],s.y+d[2]
        if not route_set[nx..":"..ny] then
          local feeding=false
          for _,e in pairs(tile_entities(nx,ny)) do
            if feeds_into(e,nx,ny,s.x,s.y) then feeding=true; break end
          end
          if feeding then
            add_conflict(s.x,s.y,"BELT_ADJACENCY","existing belt at "..nx..","..ny.." feeds into this tile; pass allowBeltAdjacency to merge",{},false,"")
          end
        end
      end
    end
    if not existing then needed=needed+1 end
  end
  if kind=="PIPE" and not allow_adjacency then
    local seen={}
    for _,s in ipairs(steps) do
      for _,d in ipairs({{1,0},{-1,0},{0,1},{0,-1}}) do
        local nx,ny=s.x+d[1],s.y+d[2]
        if not route_set[nx..":"..ny] then
          for _,e in pairs(tile_entities(nx,ny)) do
            if e.valid and (pipe_like[e.type] or fluid_port_like(e,nx,ny)) then
              local key=nx..":"..ny
              if not seen[key] then
                seen[key]=true
                add_conflict(nx,ny,"FLUID_ADJACENCY","pipe network with "..e.name.." touches this route; pass allowFluidAdjacency to merge",{},false,"")
              end
            end
          end
        end
      end
    end
  end
  return {mode=mode,steps=steps,conflicts=conflicts,needed=needed}
end
local function pick()
  if bend_request=="HORIZONTAL_FIRST" or bend_request=="VERTICAL_FIRST" then
    return evaluate(bend_request)
  end
  local h=evaluate("HORIZONTAL_FIRST")
  local v=evaluate("VERTICAL_FIRST")
  if #h.conflicts<=#v.conflicts then return h end
  return v
end
local chosen=pick()
local bend_label=chosen.mode
if from.x==to.x and from.y==to.y then bend_label="HORIZONTAL_FIRST"
elseif from.y==to.y then bend_label="HORIZONTAL_FIRST"
elseif from.x==to.x then bend_label="VERTICAL_FIRST"
end
`

func connectionPlanQuery(r *Resolver, input *ConnectionPlanInput) (*ConnectionPlan, error) {
	if input == nil || input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	item, err := connectionItem(input.Kind, input.Item)
	if err != nil {
		return nil, err
	}
	bend, err := connectionBend(input.Bend)
	if err != nil {
		return nil, err
	}
	astar, err := validateRouteOptions(input.Kind, planRouteOptions(input))
	if err != nil {
		return nil, err
	}
	from, to, _, toLane, err := resolveConnectionEndpoints(r, input.Surface, input.Force, item, input.From, input.To, input.FromEntity, input.ToEntity)
	if err != nil {
		return nil, err
	}
	if astar {
		if err := routeEndpoints(from, to); err != nil {
			return nil, err
		}
		plan, err := connectionPlanAStar(r, connectionRequest{
			surface: input.Surface, force: input.Force, from: from, to: to, kind: input.Kind, item: item,
			allowAdjacency: input.AllowFluidAdjacency, allowBeltAdjacency: input.AllowBeltAdjacency, options: planRouteOptions(input),
		})
		if err != nil {
			return nil, err
		}
		plan.Lane = connectionPlanLane(plan.Steps, toLane)
		return plan, nil
	}
	if _, err := connectionRouteSteps(from, to); err != nil {
		return nil, err
	}
	lua := luaFail + connectionLuaPreamble(input.Surface, input.Force, item, from, to, input.Kind, bend, input.AllowFluidAdjacency, input.AllowBeltAdjacency) + connectionLuaHelpers + `
local out_steps={}
for _,s in ipairs(chosen.steps) do out_steps[#out_steps+1]={position={x=s.x,y=s.y},direction=s.direction,existing=s.existing} end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,kind=kind,item=item,from={x=from.x,y=from.y},to={x=to.x,y=to.y},bend=bend_label,possible=#chosen.conflicts==0,needed=chosen.needed,steps=out_steps,conflicts=chosen.conflicts})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	plan, err := factoryDecode[ConnectionPlan](response)
	if err != nil {
		return nil, err
	}
	plan.Routing = ConnectionRoutingDirect
	plan.StopReason = ConnectionStopReasonDirect
	plan.Cost = float64(plan.Needed)
	plan.Lane = connectionPlanLane(plan.Steps, toLane)
	for _, step := range plan.Steps {
		if !step.Existing {
			step.Cost = routeCostTile
		}
	}
	return plan, nil
}

// connectEntities plans and applies one run. It refuses to place anything when
// the plan has conflicts or the actor lacks the items, so a refusal leaves the
// world unchanged.
func connectEntities(r *Resolver, input ConnectEntitiesInput) (*ConnectEntitiesResult, error) {
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	if input.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	item, err := connectionItem(input.Kind, input.Item)
	if err != nil {
		return nil, err
	}
	bend, err := connectionBend(input.Bend)
	if err != nil {
		return nil, err
	}
	astar, err := validateRouteOptions(input.Kind, applyRouteOptions(&input))
	if err != nil {
		return nil, err
	}
	from, to, _, _, err := resolveConnectionEndpoints(r, input.Surface, input.Force, item, input.From, input.To, input.FromEntity, input.ToEntity)
	if err != nil {
		return nil, err
	}
	input.From, input.To = from, to
	if astar {
		return connectEntitiesAStar(r, input, item)
	}
	if _, err := connectionRouteSteps(input.From, input.To); err != nil {
		return nil, err
	}
	lua := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
`, luaString(player), character) +
		connectionLuaPreamble(input.Surface, input.Force, item, input.From, input.To, input.Kind, bend, input.AllowFluidAdjacency, input.AllowBeltAdjacency) +
		connectionLuaHelpers + fmt.Sprintf(`
if c.surface.name~=surface.name then fail("actor is on another surface"); return end
if c.force.name~=force.name then fail("actor is on another force"); return end
local placed,skipped,failed=0,0,0
local entities,errors={},{}
local force_name_actual=c.force.name
local function fail_at(i,code,message,position)
  failed=failed+1
  if #errors<20 then errors[#errors+1]={index=i,code=code,message=message,target={surface=surface.name,entity=entity_name,position=position}} end
end
if #chosen.conflicts>0 then
  rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force_name_actual,kind=kind,item=item,from={x=from.x,y=from.y},to={x=to.x,y=to.y},bend=bend_label,possible=false,placed=0,skipped=0,failed=0,entities={},conflicts=chosen.conflicts,errors={}})
  return
end
local have=inv.get_item_count{name=item,quality="normal"}
if have<chosen.needed then fail("actor has "..have.." "..item.." but the run needs "..chosen.needed); return end
for i,s in ipairs(chosen.steps) do
  if s.existing then
    skipped=skipped+1
  else
    local dx=c.position.x-s.x; local dy=c.position.y-s.y
    local ok=true
    if %s then ok=false; fail_at(i,"REACH","out of build reach",{x=s.x,y=s.y}) end
    if ok and inv.get_item_count{name=item,quality="normal"}<1 then ok=false; fail_at(i,"INVENTORY","actor lacks item",{x=s.x,y=s.y}) end
    if ok and inv.remove{name=item,quality="normal",count=1}~=1 then ok=false; fail_at(i,"INVENTORY","could not remove item",{x=s.x,y=s.y}) end
    if ok then
      local created,e=pcall(function() return surface.create_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=c.force,quality="normal",raise_built=true} end)
      if created and e then
        placed=placed+1
        entities[#entities+1]={name=e.name,type=e.type,position={x=e.position.x,y=e.position.y},unitNumber=e.unit_number}
      else
        inv.insert{name=item,quality="normal",count=1}
        -- Entity creation is the success test; can_place_entity only explains a refusal.
        if not created then
          fail_at(i,"ENGINE","entity creation failed: "..tostring(e),{x=s.x,y=s.y})
        elseif surface.can_place_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=c.force,build_check_type=defines.build_check_type.manual} then
          fail_at(i,"ENGINE","entity creation failed",{x=s.x,y=s.y})
        else
          fail_at(i,"COLLISION","cannot place entity at tile",{x=s.x,y=s.y})
        end
      end
    end
  end
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force_name_actual,kind=kind,item=item,from={x=from.x,y=from.y},to={x=to.x,y=to.y},bend=bend_label,possible=true,placed=placed,skipped=skipped,failed=failed,entities=entities,conflicts={},errors=errors})`,
		distanceViolation("dx", "dy", "c.build_distance"))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ConnectEntitiesResult](response)
	if err != nil {
		return nil, err
	}
	result.Routing = ConnectionRoutingDirect
	result.StopReason = ConnectionStopReasonDirect
	if result.Placed > 0 {
		announceMutation(r, input.Message, connectEntitiesSummary(result))
	}
	return result, nil
}

// connectEntitiesAStar plans an A* route, refuses on any conflict, and places the
// planned steps. Both phases are separate reads; the placement phase re-checks
// every tile.
func connectEntitiesAStar(r *Resolver, input ConnectEntitiesInput, item string) (*ConnectEntitiesResult, error) {
	if err := routeEndpoints(input.From, input.To); err != nil {
		return nil, err
	}
	plan, err := connectionPlanAStar(r, connectionRequest{
		surface: input.Surface, force: input.Force, from: input.From, to: input.To, kind: input.Kind, item: item,
		allowAdjacency: input.AllowFluidAdjacency, allowBeltAdjacency: input.AllowBeltAdjacency, options: applyRouteOptions(&input),
	})
	if err != nil {
		return nil, err
	}
	if !plan.Possible {
		return &ConnectEntitiesResult{
			Tick: plan.Tick, Surface: plan.Surface, Force: plan.Force, Kind: plan.Kind, Item: plan.Item,
			From: plan.From, To: plan.To, Bend: plan.Bend, Possible: false,
			Routing: ConnectionRoutingAstar, StopReason: plan.StopReason,
			Entities: []*PlacedEntity{}, Conflicts: plan.Conflicts, Errors: []*BatchError{},
		}, nil
	}
	result, err := applyRoutePlan(r, input, plan)
	if err != nil {
		return nil, err
	}
	if result.Placed > 0 {
		announceMutation(r, input.Message, connectEntitiesSummary(result))
	}
	return result, nil
}

func connectEntitiesSummary(r *ConnectEntitiesResult) string {
	return fmt.Sprintf("placed %d %s from (%g, %g) to (%g, %g)", r.Placed, r.Item, r.From.X, r.From.Y, r.To.X, r.To.Y)
}

// connectionEndpointReference turns one entity endpoint into the Lua reference
// the resolver reads, and validates the side and lane hints.
func connectionEndpointReference(name string, endpoint *ConnectionEndpointInput) (string, error) {
	if endpoint == nil || endpoint.Entity == nil {
		return "nil", nil
	}
	side := ""
	if endpoint.Side != nil {
		side = strings.ToLower(strings.TrimSpace(*endpoint.Side))
	}
	switch side {
	case "", "auto":
		side = ""
	case "north", "east", "south", "west":
	default:
		return "", fmt.Errorf("%s entity side must be auto, north, east, south or west", name)
	}
	lane := ""
	if endpoint.Lane != nil {
		lane = strings.ToLower(strings.TrimSpace(*endpoint.Lane))
	}
	switch lane {
	case "", "auto":
		lane = ""
	case "left", "right":
	default:
		return "", fmt.Errorf("%s entity lane must be auto, left or right", name)
	}
	surface, entityName, unitNumber, x, y := entityRefFields(endpoint.Entity)
	parts := []string{}
	if surface != nil && *surface != "" {
		parts = append(parts, "surface="+luaString(*surface))
	}
	if entityName != nil && *entityName != "" {
		parts = append(parts, "name="+luaString(*entityName))
	}
	if unitNumber != nil && *unitNumber != "" {
		if _, err := strconv.ParseUint(*unitNumber, 10, 32); err != nil {
			return "", fmt.Errorf("%s entity unitNumber must be an entity unit number from 1 to 4294967295", name)
		}
		parts = append(parts, "unitNumber="+luaString(*unitNumber))
	} else if x != nil && y != nil {
		for _, v := range []float64{*x, *y} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return "", fmt.Errorf("%s entity position must be finite", name)
			}
		}
		parts = append(parts, fmt.Sprintf("position={x=%s,y=%s}", topologyNumber(*x), topologyNumber(*y)))
	} else {
		return "", fmt.Errorf("%s entity needs a unitNumber or a position", name)
	}
	if side != "" {
		parts = append(parts, "side="+luaString(side))
	}
	if lane != "" {
		parts = append(parts, "lane="+luaString(lane))
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// connectionAnchorLua resolves an entity endpoint to the tile the route should
// use and the orientation that feeds or is fed by the entity. A tile already
// holding the route entity is used as-is; otherwise the adjacent free tile on the
// preferred side, else the side facing the other endpoint, is chosen.
const connectionAnchorLua = `
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local item_proto=prototypes.item[item_name]
if not item_proto or not item_proto.place_result then fail("item does not place an entity"); return end
local entity_name=item_proto.place_result.name
local function find_entity(ref,role)
  if not ref then return nil end
  local target=nil
  if ref.unitNumber then
    local found=game.get_entity_by_unit_number(tonumber(ref.unitNumber))
    if found and found.valid then target=found end
  elseif ref.position then
    for _,candidate in pairs(surface.find_entities_filtered{position={x=ref.position.x,y=ref.position.y},force=force,limit=16}) do
      if candidate.valid then
        local box=candidate.bounding_box
        if ref.position.x>=box.left_top.x-0.001 and ref.position.x<=box.right_bottom.x+0.001
          and ref.position.y>=box.left_top.y-0.001 and ref.position.y<=box.right_bottom.y+0.001 then
          target=candidate
          break
        end
      end
    end
  end
  if not target then fail(role.." entity endpoint not found") end
  if ref.name and target.name~=ref.name then fail(role.." entity endpoint is a "..target.name..", not "..ref.name) end
  return target
end
local function entity_center(e)
  local box=e.bounding_box
  return {x=(box.left_top.x+box.right_bottom.x)/2,y=(box.left_top.y+box.right_bottom.y)/2}
end
local side_order={"north","east","south","west"}
local side_vectors={north={0,-1},east={1,0},south={0,1},west={-1,0}}
local side_directions={north=0,east=4,south=8,west=12}
local function anchor(entity,role,ref,other)
  if entity.name==entity_name then
    return {x=entity.position.x,y=entity.position.y,direction=entity.direction,existing=true,side=""}
  end
  local box=entity.bounding_box
  local center=entity_center(entity)
  local cx=math.floor(center.x)+0.5
  local cy=math.floor(center.y)+0.5
  local best,best_score=nil,-1
  for index,side in ipairs(side_order) do
    local vector=side_vectors[side]
    local x,y
    if side=="north" then x,y=cx,box.left_top.y-0.5
    elseif side=="south" then x,y=cx,box.right_bottom.y+0.5
    elseif side=="west" then x,y=box.left_top.x-0.5,cy
    else x,y=box.right_bottom.x+0.5,cy end
    local outward=side_directions[side]
    local direction=outward
    if role=="to" then direction=(outward+8)%16 end
    local occupied,existing=false,false
    for _,candidate in pairs(surface.find_entities_filtered{position={x=x,y=y},limit=8}) do
      if candidate.valid then
        local candidate_box=candidate.bounding_box
        if x>=candidate_box.left_top.x and x<=candidate_box.right_bottom.x and y>=candidate_box.left_top.y and y<=candidate_box.right_bottom.y then
          if candidate.name==entity_name then existing=true else occupied=true end
        end
      end
    end
    -- build_check_type.manual is the player build rule; build_check_type.script
    -- only reports whether a script may create the box, which overlaps obstacles.
    local usable=existing or (not occupied and surface.can_place_entity{name=entity_name,position={x=x,y=y},direction=direction,force=force,build_check_type=defines.build_check_type.manual})
    local toward=false
    if other then
      toward=(x-center.x)*(other.x-center.x)+(y-center.y)*(other.y-center.y)>0
    end
    local score=0
    if ref.side==side then score=score+8 end
    if usable then score=score+4 end
    if toward then score=score+2 end
    if score>best_score then
      best_score=score
      best={x=x,y=y,direction=direction,existing=existing,side=side}
    end
  end
  if not best or best_score<4 then fail(role.." entity has no free adjacent tile for "..entity_name) end
  return best
end
local from_entity=find_entity(from_ref,"from")
local to_entity=find_entity(to_ref,"to")
local from_reference=from_position
local to_reference=to_position
if from_entity then from_reference=entity_center(from_entity) end
if to_entity then to_reference=entity_center(to_entity) end
local result={}
if from_entity then
  local chosen=anchor(from_entity,"from",from_ref,to_reference)
  result.from={x=chosen.x,y=chosen.y,direction=chosen.direction,existing=chosen.existing,side=chosen.side}
end
if to_entity then
  local chosen=anchor(to_entity,"to",to_ref,from_reference)
  result.to={x=chosen.x,y=chosen.y,direction=chosen.direction,existing=chosen.existing,side=chosen.side}
end
rcon.print(helpers.table_to_json(result))
`

type connectionAnchor struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Direction int     `json:"direction"`
	Existing  bool    `json:"existing"`
	Side      string  `json:"side"`
}

// resolveConnectionEndpoints turns entity endpoints into tile positions in one
// read. Both endpoints are resolved together so each side can prefer the side
// facing the other one.
func resolveConnectionEndpoints(r *Resolver, surface, force, item string, from, to *PositionInput, fromEntity, toEntity *ConnectionEndpointInput) (*PositionInput, *PositionInput, string, string, error) {
	if fromEntity == nil && toEntity == nil {
		return from, to, "", "", nil
	}
	if fromEntity != nil && from != nil {
		return nil, nil, "", "", errors.New("provide from or fromEntity, not both")
	}
	if toEntity != nil && to != nil {
		return nil, nil, "", "", errors.New("provide to or toEntity, not both")
	}
	if fromEntity == nil && from == nil {
		return nil, nil, "", "", errors.New("from or fromEntity is required")
	}
	if toEntity == nil && to == nil {
		return nil, nil, "", "", errors.New("to or toEntity is required")
	}
	fromRef, err := connectionEndpointReference("from", fromEntity)
	if err != nil {
		return nil, nil, "", "", err
	}
	toRef, err := connectionEndpointReference("to", toEntity)
	if err != nil {
		return nil, nil, "", "", err
	}
	positionLua := func(p *PositionInput) string {
		if p == nil {
			return "nil"
		}
		return fmt.Sprintf("{x=%s,y=%s}", topologyNumber(p.X), topologyNumber(p.Y))
	}
	toLane := ""
	if toEntity != nil && toEntity.Lane != nil {
		toLane = strings.ToLower(strings.TrimSpace(*toEntity.Lane))
		if toLane == "auto" {
			toLane = ""
		}
	}
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name,item_name=%s,%s,%s
local from_ref,to_ref=%s,%s
local from_position,to_position=%s,%s
`, luaString(surface), luaString(force), luaString(item), fromRef, toRef, positionLua(from), positionLua(to)) + connectionAnchorLua
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, nil, "", "", err
	}
	resolved, err := factoryDecode[struct {
		From *connectionAnchor `json:"from"`
		To   *connectionAnchor `json:"to"`
	}](response)
	if err != nil {
		return nil, nil, "", "", err
	}
	resultFrom, resultTo := from, to
	if fromEntity != nil {
		if resolved.From == nil {
			return nil, nil, "", "", errors.New("from entity endpoint was not resolved")
		}
		resultFrom = &PositionInput{X: resolved.From.X, Y: resolved.From.Y}
	}
	if toEntity != nil {
		if resolved.To == nil {
			return nil, nil, "", "", errors.New("to entity endpoint was not resolved")
		}
		resultTo = &PositionInput{X: resolved.To.X, Y: resolved.To.Y}
	}
	return resultFrom, resultTo, "", toLane, nil
}

// connectionPlanLane reports which lane of an existing goal belt receives the run
// when the route ends by side-loading it. Parallel arrivals are a straight feed
// and head-on arrivals are rejected, so only perpendicular arrivals have a lane.
func connectionPlanLane(steps []*ConnectionStep, requested string) *string {
	if len(steps) < 2 {
		return nil
	}
	last, previous := steps[len(steps)-1], steps[len(steps)-2]
	if !last.Existing {
		return nil
	}
	goal := (last.Direction / 4) % 4
	arrival := (previous.Direction / 4) % 4
	if goal%2 == arrival%2 {
		return nil
	}
	if requested == "left" || requested == "right" {
		lane := requested
		return &lane
	}
	travel := [4][2]int{{0, -1}, {1, 0}, {0, 1}, {-1, 0}}
	t, a := travel[goal], travel[arrival]
	feeder := [2]int{-a[0], -a[1]}
	left := [2]int{t[1], -t[0]}
	lane := "right"
	if feeder == left {
		lane = "left"
	}
	return &lane
}
