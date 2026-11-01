package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// maxConnectionSteps bounds the number of route tiles one connection may touch.
// The route is materialized in Lua, so this caps script work and response size.
const maxConnectionSteps = 512

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
	steps := int(dx+dy) + 1
	if steps > maxConnectionSteps {
		return 0, fmt.Errorf("route covers %d tiles, above the %d tile limit; split it into shorter runs", steps, maxConnectionSteps)
	}
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

func connectionLuaPreamble(surface, force, item string, from, to *PositionInput, kind ConnectionKind, bend ConnectionBend, allowFluidAdjacency bool) string {
	n := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
	return fmt.Sprintf(`local surface_name,force_name=%s,%s
local item=%s
local kind=%s
local from_x,from_y=%s,%s
local to_x,to_y=%s,%s
local bend_request=%s
local allow_adjacency=%t
`, luaString(surface), luaString(force), luaString(item), luaString(string(kind)),
		n(from.X), n(from.Y), n(to.X), n(to.Y), luaString(string(bend)), allowFluidAdjacency)
}

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
for _,name in ipairs({"pipe","pipe-to-ground","pump","storage-tank","boiler","offshore-pump","heat-exchanger","generator","steam-engine","steam-turbine"}) do pipe_like[name]=true end
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
  for _,s in ipairs(steps) do
    local found=tile_entities(s.x,s.y)
    local existing=false
    local mismatched=false
    for _,e in pairs(found) do
      if e.valid and e.name==entity_name and e.position.x==s.x and e.position.y==s.y then
        if kind~="BELT" or e.direction==s.direction then existing=true else mismatched=true end
      end
    end
    s.existing=existing
    if mismatched then
      add_conflict(s.x,s.y,"DIRECTION_MISMATCH","existing "..entity_name.." faces another direction",{},false,"")
    elseif not existing then
      if not surface.can_place_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=force} then
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
    if not existing then needed=needed+1 end
  end
  if kind=="PIPE" and not allow_adjacency then
    local seen={}
    for _,s in ipairs(steps) do
      for _,d in ipairs({{1,0},{-1,0},{0,1},{0,-1}}) do
        local nx,ny=s.x+d[1],s.y+d[2]
        if not route_set[nx..":"..ny] then
          for _,e in pairs(tile_entities(nx,ny)) do
            if e.valid and pipe_like[e.name] then
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
	if _, err := connectionRouteSteps(input.From, input.To); err != nil {
		return nil, err
	}
	lua := luaFail + connectionLuaPreamble(input.Surface, input.Force, item, input.From, input.To, input.Kind, bend, input.AllowFluidAdjacency) + connectionLuaHelpers + `
local out_steps={}
for _,s in ipairs(chosen.steps) do out_steps[#out_steps+1]={position={x=s.x,y=s.y},direction=s.direction,existing=s.existing} end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,kind=kind,item=item,from={x=from.x,y=from.y},to={x=to.x,y=to.y},bend=bend_label,possible=#chosen.conflicts==0,needed=chosen.needed,steps=out_steps,conflicts=chosen.conflicts})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ConnectionPlan](response)
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
	if _, err := connectionRouteSteps(input.From, input.To); err != nil {
		return nil, err
	}
	lua := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
`, luaString(player), character) +
		connectionLuaPreamble(input.Surface, input.Force, item, input.From, input.To, input.Kind, bend, input.AllowFluidAdjacency) +
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
if have<chosen.needed then fail("actor has "..have.." "..item.." but the run needs "..chosen.needed) end
for i,s in ipairs(chosen.steps) do
  if s.existing then
    skipped=skipped+1
  else
    local dx=c.position.x-s.x; local dy=c.position.y-s.y
    local ok=true
    if %s then ok=false; fail_at(i,"REACH","out of build reach",{x=s.x,y=s.y}) end
    if ok and inv.get_item_count{name=item,quality="normal"}<1 then ok=false; fail_at(i,"INVENTORY","actor lacks item",{x=s.x,y=s.y}) end
    if ok and not surface.can_place_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=c.force} then ok=false; fail_at(i,"COLLISION","cannot place entity at tile",{x=s.x,y=s.y}) end
    if ok and inv.remove{name=item,quality="normal",count=1}~=1 then ok=false; fail_at(i,"INVENTORY","could not remove item",{x=s.x,y=s.y}) end
    if ok then
      local created,e=pcall(function() return surface.create_entity{name=entity_name,position={x=s.x,y=s.y},direction=s.direction,force=c.force,quality="normal",raise_built=true} end)
      if created and e then
        placed=placed+1
        entities[#entities+1]={name=e.name,type=e.type,position={x=e.position.x,y=e.position.y},unitNumber=e.unit_number}
      else
        inv.insert{name=item,quality="normal",count=1}
        fail_at(i,"ENGINE","entity creation failed"..(created and "" or ": "..tostring(e)),{x=s.x,y=s.y})
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
	if result.Placed > 0 {
		announceMutation(r, input.Message, connectEntitiesSummary(result))
	}
	return result, nil
}

func connectEntitiesSummary(r *ConnectEntitiesResult) string {
	return fmt.Sprintf("placed %d %s from (%g, %g) to (%g, %g)", r.Placed, r.Item, r.From.X, r.From.Y, r.To.X, r.To.Y)
}
