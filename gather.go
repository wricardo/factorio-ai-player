package main

import (
	"errors"
	"fmt"
)

const (
	maxMineAreaCount    = 10000
	maxGatherItemsCount = 4294967295
)

// gatherActorPrelude resolves the acting player or character. It defines the
// inventory inv, the actor p, the character c (the sort origin), the batch
// error helpers and the area filters shared by mineArea and gatherItems.
func gatherActorPrelude(character uint64, player string, area areaBounds) string {
	return luaFail + fmt.Sprintf(luaActionActor, character, luaString(player)) + fmt.Sprintf(`local inv=p.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local c; if unit~=0 then c=p else c=p.character; if not c then fail("player has no character"); return end end
local surface=c.surface; local area=%s
local errors={}; local failed=0
local function add_error(index,code,message,e)
  failed=failed+1
  if #errors<20 then
    local pos=e.position
    errors[#errors+1]={index=index,code=code,message=message,target={surface=surface.name,entity=e.name,unitNumber=e.unit_number,position={x=pos.x,y=pos.y}}}
  end
end
local function in_area(e) local q=e.position; return q.x>=area[1][1] and q.y>=area[1][2] and q.x<=area[2][1] and q.y<=area[2][2] end
local cx,cy=c.position.x,c.position.y
-- sorted_candidates lists the entities matching filter inside the area that
-- also pass extra, nearest to the actor first; ties break on x then y.
local function sorted_candidates(filter,extra)
  filter.area=area
  local list={}
  for _,e in pairs(surface.find_entities_filtered(filter)) do
    if in_area(e) and (not extra or extra(e)) then
      local q=e.position; local dx,dy=q.x-cx,q.y-cy
      list[#list+1]={e=e,d=dx*dx+dy*dy,x=q.x,y=q.y}
    end
  end
  table.sort(list,function(a,b)
    if a.d~=b.d then return a.d<b.d end
    if a.x~=b.x then return a.x<b.x end
    return a.y<b.y
  end)
  return list
end
`, area.lua())
}

func gatherAreaBounds(area *FactoryAreaInput) (areaBounds, error) {
	if area == nil {
		return areaBounds{}, errors.New("area is required")
	}
	bounds := areaBounds{area.LeftTopX, area.LeftTopY, area.RightBottomX, area.RightBottomY}
	return bounds, bounds.validate()
}

// mineArea sweeps resource tiles nearest-first until the actor's inventory has
// gained count items.
func mineArea(r *Resolver, input MineAreaInput) (*MineAreaResult, error) {
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	bounds, err := gatherAreaBounds(input.Area)
	if err != nil {
		return nil, err
	}
	if input.Count < 1 || input.Count > maxMineAreaCount {
		return nil, fmt.Errorf("count must be from 1 through %d", maxMineAreaCount)
	}
	resource := "nil"
	if input.Resource != nil {
		if *input.Resource == "" {
			return nil, errors.New("resource must not be empty")
		}
		resource = luaString(*input.Resource)
	}
	lua := gatherActorPrelude(character, player, bounds) + inventoryItemHelpersLua + fmt.Sprintf(`
local requested=%d; local resource=%s
local products_by_name={}
local function item_products(name)
  local cached=products_by_name[name]
  if cached then return cached end
  local list={}
  local proto=prototypes.entity[name]
  local mineable=proto and proto.mineable_properties
  if mineable and mineable.products then
    for _,product in ipairs(mineable.products) do if product.type=="item" then list[#list+1]=product.name end end
  end
  products_by_name[name]=list
  return list
end
if resource then
  local proto=prototypes.entity[resource]
  if not proto or proto.type~="resource" then fail("resource is not a resource entity prototype"); return end
  if #item_products(resource)==0 then fail("resource does not yield an item"); return end
end
local function product_total(products)
  local n=0
  for _,name in ipairs(products) do n=n+inv.get_item_count{name=name,quality="normal"} end
  return n
end
local function inventory_blocked(products)
  for _,name in ipairs(products) do if inv.get_insertable_count{name=name,quality="normal"}==0 then return true end end
  return false
end
local filter={type="resource"}; if resource then filter.name=resource end
local candidates=sorted_candidates(filter)
local before_all=inventory_item_counts(inv)
local gained,touched,depleted=0,0,0
local inventory_full=false
for index,entry in ipairs(candidates) do
  if gained>=requested or inventory_full then break end
  local e=entry.e
  local products=item_products(e.name)
  if #products>0 and e.valid then
    touched=touched+1
    while e.valid and gained<requested do
      local amount_before=e.amount
      local total_before=product_total(products)
      local mined=p.mine_entity(e,false)
      -- mine_entity's return value alone is not proof of a state change: the
      -- tile must have left the world or its amount shrunk.
      local changed=(not e.valid) or (e.amount or 0)<amount_before
      local delta=product_total(products)-total_before
      if delta>0 then gained=gained+delta end
      if not changed then
        if inventory_blocked(products) then inventory_full=true
        else add_error(index,"ENGINE","engine refused to mine resource tile"..(mined and " although it reported success" or ""),e) end
        break
      end
    end
    if not e.valid then depleted=depleted+1 end
  end
end
local stop="AREA_EXHAUSTED"
if gained>=requested then stop="COUNT_REACHED" elseif inventory_full then stop="INVENTORY_FULL" end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,resource=resource,requested=requested,gained=gained,stop_reason=stop,tiles_found=#candidates,tiles_touched=touched,tiles_depleted=depleted,failed=failed,errors=errors,items=inventory_item_difference(before_all,inv)}))`,
		input.Count, resource)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[MineAreaResult](response)
	if err != nil {
		return nil, err
	}
	if result.Gained > 0 {
		announceMutation(r, input.Message, fmt.Sprintf("mined %s from %d tiles on %s (%s)", numberValue(&result.Gained), result.TilesTouched, result.Surface, result.StopReason))
	}
	return result, nil
}

// gatherItems moves one item from tiered sources in an area into the actor's
// main inventory until count items were gathered.
func gatherItems(r *Resolver, input GatherItemsInput) (*GatherItemsResult, error) {
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	bounds, err := gatherAreaBounds(input.Area)
	if err != nil {
		return nil, err
	}
	if input.Item == "" {
		return nil, errors.New("item must not be empty")
	}
	quality, err := resolveQuality(input.Quality)
	if err != nil {
		return nil, err
	}
	if input.Count < 1 || input.Count > maxGatherItemsCount {
		return nil, fmt.Errorf("count must be from 1 through %d", maxGatherItemsCount)
	}
	includeInfinity := input.IncludeInfinityContainers != nil && *input.IncludeInfinityContainers
	lua := gatherActorPrelude(character, player, bounds) + fmt.Sprintf(`
local item=%s; local quality=%s; local want=%d; local include_infinity=%t
if not prototypes.item[item] then fail("item prototype not found"); return end
if not prototypes.quality[quality] then fail("quality not found"); return end
local spec={name=item,quality=quality}
local gathered=0; local stop=nil
local tier_names={"CONTAINER","MACHINE_OUTPUT","BELT","LOOSE"}
local tier_moved={}; local tier_sources={}
for _,t in ipairs(tier_names) do tier_moved[t]=0; tier_sources[t]=0 end
local sources={}; local sources_truncated=false
local function same_force(e) return e.force==p.force end
local function record(tier,e,moved)
  gathered=gathered+moved
  tier_moved[tier]=tier_moved[tier]+moved; tier_sources[tier]=tier_sources[tier]+1
  if #sources<50 then
    local pos=e.position
    sources[#sources+1]={tier=tier,entity=e.name,unitNumber=e.unit_number,position={x=pos.x,y=pos.y},moved=moved}
  else sources_truncated=true end
  if gathered>=want then stop="COUNT_REACHED"
  elseif inv.get_insertable_count(spec)==0 then stop="INVENTORY_FULL" end
end
-- room is how many more items to move from one source whose stock is available.
local function room(available) return math.min(want-gathered,available,inv.get_insertable_count(spec)) end

-- take_from_inventory moves up to limit matching items stack by stack so item
-- data such as health or spoilage travels with the stack.
local function take_from_inventory(src,limit)
  local moved=0
  for si=1,#src do
    if moved>=limit then break end
    local stack=src[si]
    if stack.valid_for_read and stack.name==item and stack.quality.name==quality then
      for dj=1,#inv do
        if moved>=limit or not stack.valid_for_read then break end
        local before=stack.count
        inv[dj].transfer_stack(stack,math.min(limit-moved,before))
        local after=0; if stack.valid_for_read then after=stack.count end
        moved=moved+(before-after)
      end
    end
  end
  return moved
end
local function gather_inventories(tier,list,get_inventory)
  for _,entry in ipairs(list) do
    if stop then return end
    local e=entry.e
    local src=e.valid and get_inventory(e)
    if src then
      local available=src.get_item_count(spec)
      if available>0 then
        local moved=take_from_inventory(src,room(available))
        if moved>0 then record(tier,e,moved) end
      end
    end
  end
end
if inv.get_insertable_count(spec)==0 then stop="INVENTORY_FULL" end
if not stop then
  local types={"container","logistic-container"}
  if include_infinity then types[#types+1]="infinity-container" end
  gather_inventories("CONTAINER",sorted_candidates({type=types},same_force),function(e) return e.get_inventory(defines.inventory.chest) end)
end
if not stop then
  gather_inventories("MACHINE_OUTPUT",sorted_candidates({type={"furnace","assembling-machine","rocket-silo","mining-drill"}},same_force),function(e)
    local ok,output=pcall(function() return e.get_output_inventory() end)
    if ok then return output end
    return nil
  end)
end
if not stop then
  for index,entry in ipairs(sorted_candidates({type={"transport-belt","underground-belt","splitter"}},same_force)) do
    if stop then break end
    local e=entry.e
    local lines={}; local available=0
    for li=1,e.get_max_transport_line_index() do
      local line=e.get_transport_line(li)
      local n=line.get_item_count(spec)
      if n>0 then lines[#lines+1]={line=line,n=n}; available=available+n end
    end
    if available>0 then
      local moved=0
      for _,entry_line in ipairs(lines) do
        local limit=math.min(want-gathered-moved,entry_line.n,inv.get_insertable_count(spec))
        if limit>0 then
          local removed=entry_line.line.remove_item{name=item,quality=quality,count=limit}
          if removed>0 then
            local inserted=inv.insert{name=item,quality=quality,count=removed}
            moved=moved+inserted
            if inserted<removed then
              surface.spill_item_stack{position=c.position,stack={name=item,quality=quality,count=removed-inserted},enable_looted=true}
              add_error(index,"INVENTORY","inventory rejected items; remainder spilled near the actor",e)
              break
            end
          end
        end
      end
      if moved>0 then record("BELT",e,moved) end
    end
  end
end
-- take_stack moves up to the needed count out of one ground or held stack.
local function take_stack(tier,e,stack)
  local available=stack.count
  local limit=room(available)
  if limit>0 then
    local inserted=inv.insert{name=item,quality=quality,count=limit}
    if inserted>0 then
      -- Record first: emptying a ground item stack destroys its entity.
      record(tier,e,inserted)
      stack.count=available-inserted
    end
  end
end
local function matching(stack) return stack and stack.valid_for_read and stack.name==item and stack.quality.name==quality end
if not stop then
  for _,entry in ipairs(sorted_candidates({type="item-entity"},function(e) return matching(e.stack) end)) do
    if stop then break end
    local e=entry.e
    take_stack("LOOSE",e,e.stack)
    if e.valid and not matching(e.stack) then e.destroy() end
  end
end
if not stop then
  for _,entry in ipairs(sorted_candidates({type="inserter"},function(e) return same_force(e) and matching(e.held_stack) end)) do
    if stop then break end
    take_stack("LOOSE",entry.e,entry.e.held_stack)
  end
end
if not stop then stop="AREA_EXHAUSTED" end
local tiers={}
for _,t in ipairs(tier_names) do tiers[#tiers+1]={tier=t,sources=tier_sources[t],moved=tier_moved[t]} end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,item=item,quality=quality,requested=want,gathered=gathered,stop_reason=stop,tiers=tiers,sources=sources,sources_truncated=sources_truncated,failed=failed,errors=errors}))`,
		luaString(input.Item), luaString(quality), input.Count, includeInfinity)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[GatherItemsResult](response)
	if err != nil {
		return nil, err
	}
	if result.Gathered > 0 {
		announceMutation(r, input.Message, fmt.Sprintf("gathered %s %s (%s)", numberValue(&result.Gathered), result.Item, result.StopReason))
	}
	return result, nil
}
