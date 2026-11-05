package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// This file implements area construction and logistic network inspection:
// reviving entity ghosts and reading roboport logistic networks. Ghost revival
// is a script shortcut for the building itself: the engine builds the ghost in
// one tick instead of sending construction robots, but this layer charges the
// items the ghost prototype needs, so a build never materializes stock.

// buildGhosts revives the entity ghosts inside an area and charges their cost.
//
// Revival itself is script-level: the engine builds the ghost without asking
// construction robots, so the build happens in one tick. Items are the caller's
// responsibility here: every ghost's prototype cost is withdrawn from the
// actor's main inventory first and then from the optional source entity
// inventory, all or nothing per ghost, and refunded when the engine refuses the
// revival. missing therefore reports the materials that were absent, and a
// blocked tile is reported as COLLISION with the cost refunded. Items that a
// successfully revived entity displaced are reported separately in
// collidedItems and, when an actor is given, moved into its main inventory as
// overflow.
func buildGhosts(r *Resolver, input BuildGhostsInput) (*BuildGhostsResult, error) {
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	surface := stringValueOr(input.Surface, "nauvis")
	if surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	force := stringValueOr(input.Force, "player")
	if force == "" {
		return nil, errors.New("force must not be empty")
	}
	bounds := areaBounds{input.Area.LeftTopX, input.Area.LeftTopY, input.Area.RightBottomX, input.Area.RightBottomY}
	if err := bounds.validate(); err != nil {
		return nil, err
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	mode := input.Mode
	if mode == "" {
		mode = BuildGhostsModeRevive
	}
	if !mode.IsValid() {
		return nil, errors.New("mode must be REVIVE or SILENT")
	}
	if input.Actor == nil && input.Source == nil {
		return nil, errors.New("actor or source is required: a revival consumes the items its ghost prototype needs")
	}
	character, player, err := optionalActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	sourceRefs := "{}"
	inventoryKey := ""
	if input.Source != nil {
		ref := *input.Source
		if ref.Surface == nil || *ref.Surface == "" {
			value := surface
			ref.Surface = &value
		}
		targets, err := batchTargets([]*EntityRefInput{&ref})
		if err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		inventoryKey, err = resolveInventoryKind(string(input.SourceInventory))
		if err != nil {
			return nil, err
		}
		sourceRefs = "{" + batchTargetsLua(targets) + "}"
	}
	response, _, err := executeLua(r.addr, r.password, buildGhostsLua(surface, force, bounds, string(mode), input.Limit, character, player, sourceRefs, inventoryKey))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[BuildGhostsResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, buildGhostsSummary(result))
	return result, nil
}

// optionalActor validates an optional player/character selection and returns
// the character unit number and player name. Both empty means no actor.
func optionalActor(character, player string) (uint64, string, error) {
	if character == "" && player == "" {
		return 0, "", nil
	}
	return settingsActor(character, player)
}

func buildGhostsSummary(result *BuildGhostsResult) string {
	consumed := 0.0
	for _, item := range result.Consumed {
		if item.Count != nil {
			consumed += *item.Count
		}
	}
	return fmt.Sprintf("revived %d ghosts on %s (%s), %d failed, %g items consumed",
		result.Revived, result.Surface, result.Mode, result.Failed, consumed)
}

// buildGhostsLua revives ghosts with the engine's revive or silent_revive while
// charging each ghost's prototype cost from the actor's main inventory and then
// the optional source inventory. raise_revive follows the schema: REVIVE raises
// script_raised_revive, SILENT does not. Items the revived entity collided with
// are reported in collidedItems, never as missing materials, and an actor's
// main inventory receives them as overflow when one is given.
//
// Cost accounting is per ghost and all or nothing: a ghost whose materials are
// short is skipped and reported in missing, while a ghost the engine refuses to
// revive (a blocked tile, for example) gets its items refunded, so no stock is
// lost. Quality ghosts are charged the items of their own quality.
func buildGhostsLua(surface, force string, bounds areaBounds, mode string, limit int, character uint64, player, sourceRefs, inventoryKey string) string {
	revive := "g.revive{raise_revive=true,overflow=overflow}"
	if mode == string(BuildGhostsModeSilent) {
		revive = "g.silent_revive{raise_revive=false,overflow=overflow}"
	}
	source := "local sources={}"
	sourceInventory := ""
	if sourceRefs != "{}" {
		source = "local sources=" + sourceRefs
		sourceInventory = fmt.Sprintf(`
  local okinv,source_inv=pcall(function() return e.get_inventory(defines.inventory.%s) end)
  if not okinv or not source_inv then fail("source entity has no %s inventory"); return end
  withdraw[#withdraw+1]=source_inv`, inventoryKey, inventoryKey)
	}
	return luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area=%s; local limit=%d
local unit,player_name=%d,%s
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local f=game.forces[force_name]; if not f then fail("force not found"); return end
local withdraw={}
local overflow=nil
if unit~=0 then
  local actor=game.get_entity_by_unit_number(unit)
  if not actor or actor.type~="character" then fail("character not found"); return end
  local okmain,main=pcall(function() return actor.get_main_inventory() end)
  if okmain and main then overflow=main; withdraw[#withdraw+1]=main end
elseif player_name~="" then
  local actor_player=game.players[player_name]
  if not actor_player then fail("player not found"); return end
  if actor_player.character then
    local okmain,main=pcall(function() return actor_player.character.get_main_inventory() end)
    if okmain and main then overflow=main; withdraw[#withdraw+1]=main end
  end
end
%s
if #sources>0 then
  local ref=sources[1]
  local e
  if ref.unit~=0 then
    e=game.get_entity_by_unit_number(ref.unit)
    if not e and ref.name~="" and ref.has_position then
      local ss=game.surfaces[ref.surface]
      if ss then
        for _,candidate in pairs(ss.find_entities_filtered{name=ref.name,position={x=ref.x,y=ref.y},radius=0.5}) do
          if candidate.unit_number==ref.unit then e=candidate; break end
        end
      end
    end
    if not e then fail("source entity not found"); return end
    if ref.name~="" and e.name~=ref.name then fail("source entity name does not match unit number"); return end
  else
    local ss=game.surfaces[ref.surface]; if not ss then fail("surface not found"); return end
    for _,candidate in pairs(ss.find_entities_filtered{name=ref.name,position={x=ref.x,y=ref.y},radius=0.5}) do
      if math.abs(candidate.position.x-ref.x)<0.001 and math.abs(candidate.position.y-ref.y)<0.001 then e=candidate; break end
    end
    if not e then fail("source entity not found"); return end
  end%s
end
if #withdraw==0 then fail("actor or source is required: a revival consumes the items its ghost prototype needs"); return end
local ghosts=s.find_entities_filtered{area=area,force=f,type="entity-ghost",limit=limit}
local revived,failed=0,0; local entities={}; local errors={}; local missing={}; local collided_items={}; local consumed={}
local function add_item(target,name,count,quality)
  local key=name..string.char(31)..quality
  local item=target[key]
  if not item then item={name=name,quality=quality,count=0}; target[key]=item end
  item.count=item.count+count
end
local function add_missing(name,count,quality) add_item(missing,name,count,quality) end
local function add_consumed(name,count,quality) add_item(consumed,name,count,quality) end
local function add_collided(collided)
  if type(collided)~="table" then return false end
  local added=false
  for _,item in pairs(collided) do
    local quality=item.quality
    if type(quality)=="table" then quality=quality.name end
    add_item(collided_items,item.name,item.count,quality or "normal")
    added=true
  end
  return added
end
local function prototype_cost(g)
  local cost={}
  local ok,proto=pcall(function() return g.ghost_prototype end)
  if not ok or not proto then return cost end
  local ghost_quality="normal"
  local okq,quality=pcall(function() local q=g.quality; if type(q)=="table" then return q.name end return q end)
  if okq and type(quality)=="string" and quality~="" then ghost_quality=quality end
  local okitems,items=pcall(function() return proto.items_to_place_this end)
  if not okitems or not items then return cost end
  for _,stack in pairs(items) do
    local quality_name=stack.quality
    if type(quality_name)=="table" then quality_name=quality_name.name end
    if type(quality_name)~="string" or quality_name=="" then quality_name=ghost_quality end
    cost[#cost+1]={name=stack.name,count=stack.count,quality=quality_name}
  end
  return cost
end
local function stock_count(item)
  local total=0
  for _,inv in ipairs(withdraw) do
    local ok,count=pcall(function() return inv.get_item_count{name=item.name,quality=item.quality} end)
    if ok and count then total=total+count end
  end
  return total
end
local function take_items(cost,taken)
  for _,item in ipairs(cost) do
    local need=item.count
    for _,inv in ipairs(withdraw) do
      if need<=0 then break end
      local ok,removed=pcall(function() return inv.remove{name=item.name,count=need,quality=item.quality} end)
      if ok and removed and removed>0 then
        taken[#taken+1]={inventory=inv,name=item.name,quality=item.quality,count=removed}
        need=need-removed
      end
    end
  end
end
local function refund_items(taken)
  for i=#taken,1,-1 do
    local entry=taken[i]
    pcall(function() entry.inventory.insert{name=entry.name,count=entry.count,quality=entry.quality} end)
  end
end
for index,g in ipairs(ghosts) do
  if g.valid then
    -- Reviving invalidates the ghost, so snapshot everything read later first.
    local ghost_name=g.name
    local ghost_position=g.position
    local cost=prototype_cost(g)
    local short={}
    if #cost==0 then
      short=nil
    else
      for _,item in ipairs(cost) do
        local have=stock_count(item)
        if have<item.count then short[#short+1]={name=item.name,quality=item.quality,count=item.count-have} end
      end
    end
    if short==nil then
      failed=failed+1
      if #errors<20 then
        errors[#errors+1]={index=index,code="ENGINE",message="ghost prototype cost is unavailable, so the revival cannot be charged",
          target={surface=s.name,entity=ghost_name,position=ghost_position}}
      end
    elseif #short>0 then
      failed=failed+1
      for _,item in ipairs(short) do add_missing(item.name,item.count,item.quality) end
      if #errors<20 then
        errors[#errors+1]={index=index,code="MATERIALS",message="missing materials for the ghost at "..tostring(ghost_position.x)..","..tostring(ghost_position.y),
          target={surface=s.name,entity=ghost_name,position=ghost_position}}
      end
    else
      local taken={}
      take_items(cost,taken)
      local ok,r1,r2=pcall(function() return %s end)
      -- revive and silent_revive both return (collided items, built entity, proxy).
      local collided,built=nil,nil
      if ok then
        collided=r1; built=r2
      end
      add_collided(collided)
      if ok and built and built.valid then
        revived=revived+1
        for _,entry in ipairs(taken) do add_consumed(entry.name,entry.count,entry.quality) end
        entities[#entities+1]={name=built.name,type=built.type,position=built.position,unit_number=tostring(built.unit_number or 0)}
      else
        failed=failed+1
        refund_items(taken)
        if #errors<20 then
          local code,message
          if ok then
            code="COLLISION"
            message="ghost could not be revived because placement is blocked"
          else
            code="ENGINE"
            message=tostring(r1)
          end
          errors[#errors+1]={index=index,code=code,message=message,target={surface=s.name,entity=ghost_name,position=ghost_position}}
        end
      end
    end
  end
end
local function sorted_items(source)
  local list={}
  for _,item in pairs(source) do list[#list+1]=item end
  table.sort(list,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
  return list
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,mode=%s,revived=revived,failed=failed,missing=sorted_items(missing),collidedItems=sorted_items(collided_items),consumed=sorted_items(consumed),entities=entities,errors=errors}))`,
		luaString(surface), luaString(force), bounds.lua(), limit, character, luaString(player), source, sourceInventory,
		revive, luaString(mode))
}

// logisticNetworks reads the logistic networks served by roboports on a
// surface or inside an area. Every network is one snapshot; results are
// paginated by network, not by roboport.
func logisticNetworks(r *Resolver, input *LogisticNetworksInput) (*LogisticNetworkPage, error) {
	if input == nil {
		input = &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 100, IncludeItemStats: true, ItemsLimit: 1000}
	}
	if input.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	if input.Force == "" {
		return nil, errors.New("force must not be empty")
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	if input.ItemsLimit == 0 {
		input.ItemsLimit = 1000
	}
	if input.ItemsLimit < 1 || input.ItemsLimit > maxPageSize {
		return nil, pageLimitError("itemsLimit")
	}
	if input.ItemsOffset < 0 {
		return nil, errors.New("itemsOffset must be non-negative")
	}
	area := "nil"
	if input.Area != nil {
		bounds := areaBounds{input.Area.LeftTopX, input.Area.LeftTopY, input.Area.RightBottomX, input.Area.RightBottomY}
		if err := bounds.validate(); err != nil {
			return nil, err
		}
		area = bounds.lua()
	}
	requested := ""
	if input.NetworkID != nil {
		requested = strings.TrimSpace(*input.NetworkID)
		if requested == "" {
			return nil, errors.New("networkId must not be empty")
		}
		if _, err := strconv.ParseUint(requested, 10, 64); err != nil {
			return nil, errors.New("networkId must be a numeric network id")
		}
	}
	response, _, err := executeLua(r.addr, r.password, logisticNetworksLua(input.Surface, input.Force, area, requested, input.Limit, input.Offset, input.IncludeItemStats, input.ItemsLimit, input.ItemsOffset))
	if err != nil {
		return nil, err
	}
	return factoryDecode[LogisticNetworkPage](response)
}

func logisticNetworksLua(surface, force, area, requested string, limit, offset int, includeItems bool, itemsLimit, itemsOffset int) string {
	return luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area=%s; local limit,offset=%d,%d; local requested=%s
local include_items=%s; local items_limit,items_offset=%d,%d
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local f=game.forces[force_name]; if not f then fail("force not found"); return end
local roboports=s.find_entities_filtered{area=area,force=f,type="roboport"}
local networks={}; local seen={}
for _,rp in pairs(roboports) do
  local ok,net=pcall(function() return rp.logistic_network end)
  if ok and net then
    local okid,id=pcall(function() return net.network_id end)
    if okid and id~=nil then
      local key=tostring(id)
      if not seen[key] then
        seen[key]=true
        if requested=="" or requested==key then networks[#networks+1]={id=key,network=net} end
      end
    end
  end
end
table.sort(networks,function(a,b) local na,nb=tonumber(a.id) or 0,tonumber(b.id) or 0; if na~=nb then return na<nb end return a.id<b.id end)
local function quality_name(quality)
  if type(quality)=="table" then quality=quality.name end
  if quality==nil then return nil end
  return tostring(quality)
end
local function row_for(stats,name,quality)
  local q=quality_name(quality)
  local key=name.."|"..(q or "*")
  local row=stats[key]
  if not row then row={name=name,quality=q}; stats[key]=row end
  return row
end
local function add_list(stats,list,field)
  if type(list)~="table" then return end
  for _,it in pairs(list) do
    if it and it.name and it.count and it.count~=0 then
      local row=row_for(stats,it.name,it.quality)
      row[field]=(row[field] or 0)+it.count
    end
  end
end
local function chest_contents(e)
  if not e or e.type~="logistic-container" then return nil end
  local ok,inv=pcall(function() return e.get_inventory(defines.inventory.chest) end)
  if not ok or not inv then return nil end
  local okc,c=pcall(function() return inv.get_contents() end)
  if not okc or type(c)~="table" then return nil end
  return c
end
local function classify_point(p)
  local e=p.owner
  if e.type=="roboport" then return "roboports" end
  if e.type=="character" then return "characterRequestPoints" end
  local mode=p.mode
  if mode==defines.logistic_mode.storage then return "storageChests" end
  if mode==defines.logistic_mode.buffer then return "bufferChests" end
  if mode==defines.logistic_mode.active_provider then return "activeProviderChests" end
  if mode==defines.logistic_mode.passive_provider then return "passiveProviderChests" end
  if mode==defines.logistic_mode.requester then return "requesterChests" end
  return "other"
end
local total=#networks; local list={}
for i=offset+1,math.min(total,offset+limit) do
  local net=networks[i].network
  local info={id=networks[i].id,force=f.name,surface=s.name,cells=0,contents={},requester_points=0,provider_points=0}
  if net.custom_name~=nil then info.custom_name=net.custom_name end
  local oklimit,robot_limit=pcall(function() return net.robot_limit end); if oklimit and type(robot_limit)=="number" then info.robot_limit=robot_limit else info.robot_limit=0 end
  local okcells,cells=pcall(function() return net.cells end); if okcells and cells then info.cells=#cells end
  local function robots(all_key,available_key)
    local all_count,available_count=0,0
    local okall,all=pcall(function() return net[all_key] end)
    if okall and type(all)=="number" then all_count=all end
    local okav,av=pcall(function() return net[available_key] end)
    if okav and type(av)=="number" then available_count=av end
    return {total=all_count,available=available_count}
  end
  info.logistic_robots=robots("all_logistic_robots","available_logistic_robots")
  info.construction_robots=robots("all_construction_robots","available_construction_robots")
  local okcontents,contents=pcall(function() return net.get_contents() end)
  if okcontents and contents and type(contents)=="table" then
    local items={}
    if contents[1]~=nil then
      for _,item in pairs(contents) do
        items[#items+1]={name=item.name,count=item.count,quality=quality_name(item.quality)}
      end
    else
      for name,count in pairs(contents) do items[#items+1]={name=name,count=count} end
    end
    table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "")<(b.quality or "") end)
    info.contents=items
  end
  local okreq,req=pcall(function() return net.requester_points end); if okreq and type(req)=="table" then info.requester_points=#req end
  local okprov,prov=pcall(function() return net.provider_points end); if okprov and type(prov)=="table" then info.provider_points=#prov end

  local points={roboports=0,requesterChests=0,bufferChests=0,passiveProviderChests=0,activeProviderChests=0,storageChests=0,characterRequestPoints=0,other=0}
  local seen_point={}
  local stats={}
  local function walk(list, role)
    if type(list)~="table" then return end
    for _,p in pairs(list) do
      local e=p.owner
      local key=tostring(e.unit_number or (e.position.x..":"..e.position.y))
      if not seen_point[key] then
        seen_point[key]=true
        local cls=classify_point(p)
        points[cls]=points[cls]+1
      end
      if include_items then
        if role=="provider" or role=="storage" then
          if e.type=="logistic-container" then
            local field=nil
            if role=="storage" then field="storedStorage"
            elseif p.mode==defines.logistic_mode.buffer then field="storedBuffers"
            elseif p.mode==defines.logistic_mode.active_provider then field="storedActiveProviders"
            elseif p.mode==defines.logistic_mode.passive_provider then field="storedPassiveProviders" end
            local c=chest_contents(e)
            if c and field then
              for _,it in pairs(c) do
                if it.name and it.count and it.count~=0 then
                  local row=row_for(stats,it.name,it.quality)
                  row[field]=(row[field] or 0)+it.count
                end
              end
            end
            add_list(stats,p.targeted_items_pickup,"targetedPickup")
          end
        elseif role=="requester" and e.type=="logistic-container" then
          if type(p.filters)=="table" then
            for _,flt in pairs(p.filters) do
              if flt.name then
                local row=row_for(stats,flt.name,flt.quality)
                if flt.max_count==nil then
                  row.unlimitedRequests=(row.unlimitedRequests or 0)+1
                else
                  row.requested=(row.requested or 0)+flt.max_count
                end
                row.requestedMinimum=(row.requestedMinimum or 0)+(flt.count or 0)
              end
            end
          end
          add_list(stats,chest_contents(e),"delivered")
          add_list(stats,p.targeted_items_deliver,"incoming")
        end
      end
    end
  end
  walk(net.provider_points,"provider")
  walk(net.storage_points,"storage")
  walk(net.requester_points,"requester")
  info.points=points

  local deployed,carrying=0,0
  local okrobots,robot_list=pcall(function() return net.logistic_robots end)
  if okrobots and type(robot_list)=="table" then
    deployed=#robot_list
    for _,r in pairs(robot_list) do
      local okinv,inv=pcall(function() return r.get_inventory(defines.inventory.chest) end)
      if okinv and inv then
        local okc,c=pcall(function() return inv.get_contents() end)
        if okc and type(c)=="table" then
          local count=0
          for _,it in pairs(c) do if it and it.count then count=count+it.count end end
          if count>0 then
            carrying=carrying+1
            if include_items then add_list(stats,c,"inFlight") end
          end
        end
      end
    end
  end
  info.deployed_logistic_robots=deployed
  info.carrying_logistic_robots=carrying

  if include_items then add_list(stats,contents,"available") end
  local rows={}; local item_total=0
  for _,row in pairs(stats) do
    row.storedPassiveProviders=row.storedPassiveProviders or 0
    row.storedActiveProviders=row.storedActiveProviders or 0
    row.storedBuffers=row.storedBuffers or 0
    row.storedStorage=row.storedStorage or 0
    row.stored=row.storedPassiveProviders+row.storedActiveProviders+row.storedBuffers+row.storedStorage
    row.available=row.available or 0
    row.requested=row.requested or 0
    row.requestedMinimum=row.requestedMinimum or 0
    row.unlimitedRequests=row.unlimitedRequests or 0
    row.delivered=row.delivered or 0
    row.inFlight=row.inFlight or 0
    row.incoming=row.incoming or 0
    row.targetedPickup=row.targetedPickup or 0
    -- A vanilla 2.0 requester chest compiles to count (the request minimum) with
    -- max_count nil for infinite, so the trigger amount is the minimum; the
    -- maximum is only a fallback for rows that carry no minimum.
    local demand=row.requestedMinimum
    if demand<=0 then demand=row.requested end
    local missing=demand-row.delivered-row.inFlight-row.incoming
    if missing<0 then missing=0 end
    row.missing=missing
    rows[#rows+1]=row
  end
  table.sort(rows,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "")<(b.quality or "") end)
  item_total=#rows
  local page_items={}
  if include_items then
    for j=items_offset+1,math.min(item_total,items_offset+items_limit) do page_items[#page_items+1]=rows[j] end
  end
  info.items=page_items
  info.item_page_info={total=item_total,limit=items_limit,offset=items_offset,has_more=item_total>items_offset+items_limit}
  list[#list+1]=info
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,page_info={total=total,limit=limit,offset=offset,has_more=total>offset+limit},networks=list}))`,
		luaString(surface), luaString(force), area, limit, offset, luaString(requested), strconv.FormatBool(includeItems), itemsLimit, itemsOffset)
}

// upgradeMapping is one validated prototype substitution for upgradeArea.
type upgradeMapping struct {
	From    string
	To      string
	Quality string
}

// validateUpgradeMappings validates the requested prototype substitutions and
// rejects duplicates so every entity matches at most one mapping.
func validateUpgradeMappings(values []*UpgradeMappingInput) ([]upgradeMapping, error) {
	if len(values) == 0 {
		return nil, errors.New("mappings must contain at least one entry")
	}
	if len(values) > maxBatchTargets {
		return nil, fmt.Errorf("mappings must contain at most %d entries", maxBatchTargets)
	}
	result := make([]upgradeMapping, 0, len(values))
	seen := make(map[string]bool, len(values))
	for i, value := range values {
		if value == nil {
			return nil, fmt.Errorf("mapping %d is missing", i+1)
		}
		from := strings.TrimSpace(value.From)
		to := strings.TrimSpace(value.To)
		if from == "" {
			return nil, fmt.Errorf("mapping %d from must not be empty", i+1)
		}
		if to == "" {
			return nil, fmt.Errorf("mapping %d to must not be empty", i+1)
		}
		if seen[from] {
			return nil, fmt.Errorf("mapping %d repeats from %s", i+1, from)
		}
		seen[from] = true
		quality := "normal"
		if value.Quality != nil {
			quality = strings.TrimSpace(*value.Quality)
			if quality == "" {
				return nil, fmt.Errorf("mapping %d quality must not be empty", i+1)
			}
		}
		result = append(result, upgradeMapping{From: from, To: to, Quality: quality})
	}
	return result, nil
}

// upgradeAreaOptions carries the validated inputs of an area upgrade request.
type upgradeAreaOptions struct {
	Surface    string
	Force      string
	Bounds     areaBounds
	Mappings   []upgradeMapping
	Limit      int
	Character  uint64
	Player     string
	CancelOnly bool
}

// upgradeArea marks every entity in the area whose prototype appears in the
// mappings for upgrade to the mapped prototype.
//
// LuaSurface.upgrade_area requires an upgrade planner LuaItemStack, and the 2.0
// script API cannot set mappings on an item stack, so the engine path can only
// replay a planner the player already configured. The per-entity script API
// order_upgrade is the equivalent write: it records the same upgrade order that
// construction robots execute and that constructionOrders(UPGRADE) reports.
func upgradeArea(r *Resolver, input UpgradeAreaInput) (*UpgradeAreaResult, error) {
	mappings, err := validateUpgradeMappings(input.Mappings)
	if err != nil {
		return nil, err
	}
	options, err := upgradeAreaOptionsFrom(input.Surface, input.Force, input.Area, input.Actor, input.Limit, mappings)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, upgradeAreaLua(options))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[UpgradeAreaResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, upgradeAreaSummary(result, false))
	return result, nil
}

// cancelUpgradeArea cancels upgrade orders in the area.
func cancelUpgradeArea(r *Resolver, input CancelUpgradeAreaInput) (*UpgradeAreaResult, error) {
	options, err := upgradeAreaOptionsFrom(input.Surface, input.Force, input.Area, input.Actor, input.Limit, nil)
	if err != nil {
		return nil, err
	}
	options.CancelOnly = true
	response, _, err := executeLua(r.addr, r.password, upgradeAreaLua(options))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[UpgradeAreaResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, upgradeAreaSummary(result, true))
	return result, nil
}

func upgradeAreaOptionsFrom(surface, force string, area *FactoryAreaInput, actor *ActorInput, limit int, mappings []upgradeMapping) (upgradeAreaOptions, error) {
	if area == nil {
		return upgradeAreaOptions{}, errors.New("area is required")
	}
	bounds := areaBounds{area.LeftTopX, area.LeftTopY, area.RightBottomX, area.RightBottomY}
	if err := bounds.validate(); err != nil {
		return upgradeAreaOptions{}, err
	}
	if strings.TrimSpace(surface) == "" {
		return upgradeAreaOptions{}, errors.New("surface must not be empty")
	}
	if strings.TrimSpace(force) == "" {
		return upgradeAreaOptions{}, errors.New("force must not be empty")
	}
	if limit < 1 || limit > maxPageSize {
		return upgradeAreaOptions{}, pageLimitError("limit")
	}
	character, player, err := optionalActorFrom(actor)
	if err != nil {
		return upgradeAreaOptions{}, err
	}
	return upgradeAreaOptions{Surface: surface, Force: force, Bounds: bounds, Mappings: mappings, Limit: limit, Character: character, Player: player}, nil
}

func upgradeAreaSummary(result *UpgradeAreaResult, cancel bool) string {
	if cancel {
		summary := fmt.Sprintf("canceled %d upgrade orders on %s for %s", result.Canceled, result.Surface, result.Force)
		if result.Skipped > 0 {
			summary += fmt.Sprintf(", %d skipped", result.Skipped)
		}
		return summary
	}
	summary := fmt.Sprintf("marked %d entities for upgrade on %s for %s", result.Orders, result.Surface, result.Force)
	if result.Skipped > 0 {
		summary += fmt.Sprintf(", %d skipped", result.Skipped)
	}
	return summary
}

// upgradeAreaLua renders the area upgrade or cancel program. limit bounds the
// entities visited, and rows/errors are capped so one call stays readable.
func upgradeAreaLua(options upgradeAreaOptions) string {
	var mappings strings.Builder
	mappings.WriteString("{")
	for _, mapping := range options.Mappings {
		fmt.Fprintf(&mappings, "[%s]={to=%s,quality=%s},", luaString(mapping.From), luaString(mapping.To), luaString(mapping.Quality))
	}
	mappings.WriteString("}")
	cancel := strconv.FormatBool(options.CancelOnly)
	return luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area=%s; local limit=%d; local cancel=%s
local unit,player_name=%d,%s
local mappings=%s
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local f=game.forces[force_name]; if not f then fail("force not found"); return end
local player_object=nil
if unit~=0 then
  local actor=game.get_entity_by_unit_number(unit)
  if not actor or actor.type~="character" then fail("character not found"); return end
  player_object=actor.player
elseif player_name~="" then
  player_object=game.players[player_name]
  if not player_object then fail("player not found"); return end
end
local rows,errors={},{}
local orders,skipped,canceled=0,0,0
local function skippable(e)
  return e.type=="character" or e.type=="entity-ghost" or e.type=="tile-ghost"
end
local function target_row(e,target,quality,status,message)
  rows[#rows+1]={unitNumber=(e.unit_number and tostring(e.unit_number) or nil),name=e.name,type=e.type,
    position={x=e.position.x,y=e.position.y},target=target,quality=quality,status=status,message=message}
end
local function add_error(index,code,message,e)
  if #errors<20 then
    errors[#errors+1]={index=index,code=code,message=message,target={surface=s.name,entity=e.name,
      position={x=e.position.x,y=e.position.y},unit_number=(e.unit_number and tostring(e.unit_number) or nil)}}
  end
end
local function upgrade_entity(e)
  local mapping=mappings[e.name]
  if not mapping then return end
  local index=#rows+1
  local target,quality=mapping.to,mapping.quality
  if not prototypes.entity[target] then
    skipped=skipped+1
    target_row(e,target,quality,"LOOKUP","entity prototype not found: "..target)
    add_error(index,"LOOKUP","entity prototype not found: "..target,e)
    return
  end
  if not prototypes.quality[quality] then
    skipped=skipped+1
    target_row(e,target,quality,"LOOKUP","quality prototype not found: "..quality)
    add_error(index,"LOOKUP","quality prototype not found: "..quality,e)
    return
  end
  local ok_before,before=pcall(function() return e.to_be_upgraded() end)
  if ok_before and before then
    skipped=skipped+1
    target_row(e,target,quality,"ALREADY_MARKED",nil)
    return
  end
  local ok_order,result=pcall(function()
    return e.order_upgrade{target={name=target,quality=quality},force=f,player=player_object}
  end)
  local ok_after,after=pcall(function() return e.to_be_upgraded() end)
  if ok_order and result and ok_after and after then
    orders=orders+1
    target_row(e,target,quality,"MARKED",nil)
  else
    skipped=skipped+1
    local message=ok_order and "engine did not record the upgrade order" or tostring(result)
    target_row(e,target,quality,"REFUSED",message)
    add_error(index,"ENGINE",message,e)
  end
end
local function cancel_entity(e)
  local ok_state,marked=pcall(function() return e.to_be_upgraded() end)
  if not ok_state or not marked then return end
  local index=#rows+1
  local target,quality="","normal"
  local ok_target,encoded_target,encoded_quality=pcall(function() return e.get_upgrade_target() end)
  if ok_target and encoded_target then target=encoded_target.name end
  if ok_target and encoded_quality then
    if type(encoded_quality)=="table" then quality=encoded_quality.name else quality=tostring(encoded_quality) end
  end
  local ok_cancel,result=pcall(function() return e.cancel_upgrade(f,player_object) end)
  local ok_after,after=pcall(function() return e.to_be_upgraded() end)
  if ok_cancel and result and ok_after and not after then
    canceled=canceled+1
    target_row(e,target,quality,"CANCELED",nil)
  else
    skipped=skipped+1
    local message=ok_cancel and "engine did not cancel the upgrade order" or tostring(result)
    target_row(e,target,quality,"REFUSED",message)
    add_error(index,"ENGINE",message,e)
  end
end
for _,e in ipairs(s.find_entities_filtered{area=area,force=f,skip_fog_of_war=true}) do
  if #rows>=limit then break end
  if e.valid and not skippable(e) then
    if cancel then cancel_entity(e) else upgrade_entity(e) end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,orders=orders,skipped=skipped,
  canceled=canceled,rows=rows,errors=errors}))`,
		luaString(options.Surface), luaString(options.Force), options.Bounds.lua(), options.Limit, cancel,
		options.Character, luaString(options.Player), mappings.String())
}
