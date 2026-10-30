package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// operationsStockLua deliberately observes retrievable storage, not belts or an
// assumed logistic network. Callers choose whether lab inputs belong in scope.
const operationsStockLua = `
local function stock_key(name, quality) return name.."\0"..quality end
local function storage_stock(area, include_labs)
  local totals={}
  local function add(inv)
    if not inv then return end
    for _,s in pairs(inv.get_contents()) do
      local key=stock_key(s.name,s.quality or "normal")
      totals[key]=(totals[key] or 0)+s.count
    end
  end
  local types={"container","logistic-container","infinity-container","character"}
  if include_labs then types[#types+1]="lab" end
  for _,e in pairs(surface.find_entities_filtered{force=force,area=area,type=types}) do
    if e.type=="character" then add(e.get_main_inventory())
    elseif e.type=="lab" then add(e.get_inventory(defines.inventory.lab_input))
    else add(e.get_inventory(defines.inventory.chest)) end
  end
  return totals
end
local function entity_id(e)
  if e.unit_number then return tostring(e.surface.index)..":unit:"..tostring(e.unit_number) end
  return tostring(e.surface.index)..":"..e.type..":"..e.name..":"..string.format("%.17g:%.17g",e.position.x,e.position.y)
end
`

const operationsScienceLua = `
local function missing_packs(lab, research)
  local result={}
  if lab.type~="lab" or not research or research.prototype.research_trigger then return result end
  local accepted={}; for _,name in pairs(lab.prototype.lab_inputs or {}) do accepted[name]=true end
  local inv=lab.get_inventory(defines.inventory.lab_input)
  local counts={}
  if inv then for _,s in pairs(inv.get_contents()) do counts[s.name]=(counts[s.name] or 0)+s.count end end
  for _,ingredient in pairs(research.research_unit_ingredients) do
    local count=counts[ingredient.name] or 0
    if not accepted[ingredient.name] or count<ingredient.amount then
      result[#result+1]={name=ingredient.name,required=ingredient.amount,available=count,
        missing=math.max(0,ingredient.amount-count),supported=accepted[ingredient.name] or false}
    end
  end
  table.sort(result,function(a,b) return a.name<b.name end)
  return result
end
`

func scienceBudgetQuery(r *Resolver, forceName, surfaceName string) (*ScienceBudget, error) {
	if forceName == "" || surfaceName == "" {
		return nil, errors.New("force and surface are required")
	}
	lua := luaFail + fmt.Sprintf(`local surface=game.surfaces[%s]
local force=game.forces[%s]
if not surface then fail("surface not found"); return end
if not force then fail("force not found"); return end
`, luaString(surfaceName), luaString(forceName)) + operationsStockLua + operationsScienceLua + `
local research=force.current_research
local labs=surface.find_entities_filtered{force=force,type="lab"}
local result={tick=game.tick,force=force.name,surface=surface.name,triggerResearch=false,
  labCount=#labs,missingPackLabCount=0,sciencePacks={}}
if research then
  result.technology=research.name
  result.progress=force.research_progress
  result.triggerResearch=research.prototype.research_trigger~=nil
  if not result.triggerResearch then
    result.researchUnits=research.research_unit_count
    result.remainingResearchUnits=math.max(0,result.researchUnits*(1-result.progress))
    local stock=storage_stock(nil,true)
    local by_name={}
    for _,ingredient in pairs(research.research_unit_ingredients) do
      local available=0
      for quality in pairs(prototypes.quality) do available=available+(stock[stock_key(ingredient.name,quality)] or 0) end
      local left=math.ceil(result.remainingResearchUnits*ingredient.amount)
      local row={name=ingredient.name,perResearchUnit=ingredient.amount,nominalPacksLeft=left,
        availableItems=available,nominalShortfall=math.max(0,left-available),missingLabCount=0,unsupportedLabCount=0}
      result.sciencePacks[#result.sciencePacks+1]=row; by_name[row.name]=row
    end
    for _,lab in pairs(labs) do
      local missing=missing_packs(lab,research)
      if #missing>0 then result.missingPackLabCount=result.missingPackLabCount+1 end
      for _,pack in pairs(missing) do
        local row=by_name[pack.name]
        row.missingLabCount=row.missingLabCount+1
        if not pack.supported then row.unsupportedLabCount=row.unsupportedLabCount+1 end
      end
    end
    table.sort(result.sciencePacks,function(a,b) return a.name<b.name end)
  end
end
rcon.print(helpers.table_to_json(result))`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ScienceBudget](response)
}

// operationsInspectionLua resolves the same identity in a fresh snapshot. Exact
// center matching avoids silently inspecting a neighbor after an entity vanishes.
func operationsInspectionLua(obj *EntityInspection) (string, error) {
	if obj == nil || obj.Surface == nil || *obj.Surface == "" || obj.Force == nil || *obj.Force == "" || obj.Name == nil || *obj.Name == "" || obj.Position == nil {
		return "", errors.New("inspection surface, force, name, and position are required")
	}
	x, y := obj.Position.X, obj.Position.Y
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return "", errors.New("inspection position must be finite")
	}
	return luaFail + fmt.Sprintf(`local surface=game.surfaces[%s]
local force=game.forces[%s]
if not surface then fail("surface not found"); return end
if not force then fail("force not found"); return end
local position={x=%s,y=%s}
local target
for _,e in pairs(surface.find_entities_filtered{name=%s,force=force,position=position,radius=0.01}) do
  if e.position.x==position.x and e.position.y==position.y then
    if target then fail("multiple matching entities at center"); return end
    target=e
  end
end
if not target then fail("entity not found at center on force"); return end
`, luaString(*obj.Surface), luaString(*obj.Force), strconv.FormatFloat(x, 'g', -1, 64), strconv.FormatFloat(y, 'g', -1, 64), luaString(*obj.Name)), nil
}

func inspectionMissingSciencePacks(r *Resolver, obj *EntityInspection) ([]*MissingSciencePack, error) {
	if obj != nil && obj.Type != nil && *obj.Type != "lab" {
		return []*MissingSciencePack{}, nil
	}
	lua, err := operationsInspectionLua(obj)
	if err != nil {
		return nil, err
	}
	lua += operationsScienceLua + `rcon.print(helpers.table_to_json{missingSciencePacks=missing_packs(target,force.current_research)})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[struct {
		Packs []*MissingSciencePack `json:"missingSciencePacks"`
	}](response)
	if err != nil {
		return nil, err
	}
	if result.Packs == nil {
		return []*MissingSciencePack{}, nil
	}
	return result.Packs, nil
}

func ghostsQuery(r *Resolver, input *GhostsInput) (*GhostsPage, error) {
	if input == nil || input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface, force, and input are required")
	}
	area, responseArea, err := worldAreaLua(input.Area, input.Limit, input.Offset)
	if err != nil {
		return nil, err
	}
	lua := luaFail + fmt.Sprintf(`local surface=game.surfaces[%s]
local force=game.forces[%s]
if not surface then fail("surface not found"); return end
if not force then fail("force not found"); return end
local area,limit,offset=%s,%d,%d
`, luaString(input.Surface), luaString(input.Force), area, input.Limit, input.Offset) + operationsStockLua + `
local stock=storage_stock(area,false)
local matches=surface.find_entities_filtered{force=force,area=area,type={"entity-ghost","tile-ghost","item-request-proxy"}}
table.sort(matches,function(a,b) return entity_id(a)<entity_id(b) end)
local entries={}
local function material(name,quality,count)
  local available=stock[stock_key(name,quality)] or 0
  return {name=name,quality=quality,count=count,available=available,missing=math.max(0,count-available)}
end
for i=offset+1,math.min(#matches,offset+limit) do
  local e=matches[i]
  local row={id=entity_id(e),unitNumber=e.unit_number,kind=e.type,name=e.name,quality=e.quality.name,
    position=e.position,placementAlternatives={},requestedMaterials={},buildable=false}
  local required={}
  if e.type=="entity-ghost" or e.type=="item-request-proxy" then
    for _,s in pairs(e.item_requests) do
      local quality=s.quality or "normal"
      local key=stock_key(s.name,quality)
      local prior=required[key]
      if prior then prior.count=prior.count+s.count
      else required[key]={name=s.name,quality=quality,count=s.count} end
    end
  end
  local requests_ok=true
  for key,s in pairs(required) do
    row.requestedMaterials[#row.requestedMaterials+1]=material(s.name,s.quality,s.count)
    if (stock[key] or 0)<s.count then requests_ok=false end
  end
  table.sort(row.requestedMaterials,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
  if e.type=="item-request-proxy" then
    local owner=e.proxy_target
    if owner and owner.valid then row.name=owner.name; row.quality=owner.quality.name end
    row.buildable=requests_ok
  else
    row.name=e.ghost_name
    local placeable=e.ghost_prototype and e.ghost_prototype.items_to_place_this
    for _,s in ipairs(placeable or {}) do
      row.placementAlternatives[#row.placementAlternatives+1]=material(s.name,row.quality,s.count)
      local key=stock_key(s.name,row.quality)
      local requested=required[key]
      if requests_ok and (stock[key] or 0)>=s.count+(requested and requested.count or 0) then row.buildable=true end
    end
  end
  entries[#entries+1]=row
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=` + responseArea + `,
  total=#matches,limit=limit,offset=offset,ghostEntries=entries})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[GhostsPage](response)
}

func inspectionFluidNetwork(r *Resolver, obj *EntityInspection, maxBoxes int) (*FluidNetwork, error) {
	if maxBoxes < 1 || maxBoxes > 10000 {
		return nil, errors.New("maxBoxes must be between 1 and 10000")
	}
	lua, err := operationsInspectionLua(obj)
	if err != nil {
		return nil, err
	}
	lua += operationsStockLua + fmt.Sprintf("local max_boxes=%d\n", maxBoxes) + operationsFluidNetworkLua
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[FluidNetwork](response)
}

const operationsFluidNetworkLua = `
local result={tick=game.tick,surface=surface.name,maxBoxes=max_boxes,visitedBoxes=0,truncated=false,
  rootBoxIds={},fluidEntities={},fluidBoxes={},fluidConnections={},fluidSegments={},totalAmount=0,totalCapacity=0}
local queue,seen,entities,segments,edges={},{},{},{},{}
local function box_id(e,index) return entity_id(e)..":box:"..index end
local function enqueue(e,index)
  local id=box_id(e,index)
  if seen[id] then return id end
  if #queue>=max_boxes then result.truncated=true; return nil end
  seen[id]=true; queue[#queue+1]={entity=e,index=index,id=id}
  return id
end
for i=1,#target.fluidbox do
  result.rootBoxIds[#result.rootBoxIds+1]=box_id(target,i)
  enqueue(target,i)
end
local cursor=1
while cursor<=#queue do
  local node=queue[cursor]; cursor=cursor+1
  local e,index=node.entity,node.index
  local eid=entity_id(e)
  if not entities[eid] then
    entities[eid]=true
    result.fluidEntities[#result.fluidEntities+1]={id=eid,unitNumber=e.unit_number,name=e.name,type=e.type,
      surface=e.surface.name,force=e.force.name,position=e.position}
  end
  local fb=e.fluidbox
  local engine_id=fb.get_fluid_segment_id(index)
  local sid=engine_id and (tostring(e.surface.index)..":segment:"..tostring(engine_id)) or (node.id..":storage")
  result.fluidBoxes[#result.fluidBoxes+1]={id=node.id,entityId=eid,index=index,segmentId=sid}
  if not segments[sid] then
    segments[sid]=true
    local segment={id=sid,engineSegmentId=engine_id and tostring(engine_id) or nil,capacity=fb.get_capacity(index),segmentFluids={}}
    local contents=engine_id and fb.get_fluid_segment_contents(index) or nil
    if contents then
      for name,amount in pairs(contents) do segment.segmentFluids[#segment.segmentFluids+1]={name=name,amount=amount} end
    else
      local fluid=fb[index]
      if fluid then segment.segmentFluids[1]={name=fluid.name,amount=fluid.amount} end
    end
    table.sort(segment.segmentFluids,function(a,b) return a.name<b.name end)
    result.totalCapacity=result.totalCapacity+segment.capacity
    for _,fluid in ipairs(segment.segmentFluids) do result.totalAmount=result.totalAmount+fluid.amount end
    result.fluidSegments[#result.fluidSegments+1]=segment
  end
  for _,connection in pairs(fb.get_pipe_connections(index)) do
    local other=connection.target
    local other_index=connection.target_fluidbox_index
    if other and other.valid and other_index then
      local other_entity=other.owner
      if other_entity and other_entity.valid then
        local other_id=enqueue(other_entity,other_index)
        if other_id then
          -- Keep engine direction on each directed port; never infer a path
          -- through another box of the same owner, especially pumps.
          local direction=connection.flow_direction or "input-output"
          local kind=connection.connection_type or "normal"
          local key=node.id.."\0"..other_id.."\0"..direction.."\0"..kind
          if not edges[key] then
            edges[key]=true
            result.fluidConnections[#result.fluidConnections+1]={fromBoxId=node.id,toBoxId=other_id,flowDirection=direction,connectionType=kind}
          end
        end
      end
    end
  end
end
result.visitedBoxes=#queue
local function by_id(a,b) return a.id<b.id end
table.sort(result.rootBoxIds)
table.sort(result.fluidEntities,by_id)
table.sort(result.fluidBoxes,by_id)
table.sort(result.fluidSegments,by_id)
table.sort(result.fluidConnections,function(a,b)
  if a.fromBoxId~=b.fromBoxId then return a.fromBoxId<b.fromBoxId end
  if a.toBoxId~=b.toBoxId then return a.toBoxId<b.toBoxId end
  if a.flowDirection~=b.flowDirection then return a.flowDirection<b.flowDirection end
  return a.connectionType<b.connectionType
end)
rcon.print(helpers.table_to_json(result))
`
