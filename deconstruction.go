package main

import (
	"errors"
	"fmt"
	"strings"
)

// This file implements deconstruction orders: marking entities for removal by
// the force's construction robots (which leaves ghosts) and canceling those
// orders. Unlike remove and removeMany these mutations do not mine anything,
// need no reach, and return counts of orders placed or canceled. Area requests
// select targets with find_entities_filtered across the requested force AND the
// neutral force (trees, rocks, cliffs and ground items belong to neutral), then
// order_deconstruction per entity. The area path skips characters, ghosts and
// tile ghosts before any order is attempted. The batch path cannot skip a target
// the caller chose explicitly, so a ghost there is cancelled by the engine during
// order_deconstruction: that cancellation counts as the order and must not abort
// the batch (the entity is already invalid when the call returns).

// deconstructArea marks every deconstructible entity in the area for
// deconstruction by the force's construction robots.
func deconstructArea(r *Resolver, input DeconstructAreaInput) (*DeconstructionResult, error) {
	return deconstructionArea(r, input, false)
}

// cancelDeconstructArea cancels deconstruction orders in the area.
func cancelDeconstructArea(r *Resolver, input DeconstructAreaInput) (*DeconstructionResult, error) {
	return deconstructionArea(r, input, true)
}

func deconstructionArea(r *Resolver, input DeconstructAreaInput, cancel bool) (*DeconstructionResult, error) {
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	surface, err := deconstructionSurface(input.Surface)
	if err != nil {
		return nil, err
	}
	force, err := deconstructionForce(input.Force)
	if err != nil {
		return nil, err
	}
	bounds := areaBounds{input.Area.LeftTopX, input.Area.LeftTopY, input.Area.RightBottomX, input.Area.RightBottomY}
	if err := bounds.validate(); err != nil {
		return nil, err
	}
	names, err := deconstructionFilter(input.Names, "names")
	if err != nil {
		return nil, err
	}
	types, err := deconstructionFilter(input.Types, "types")
	if err != nil {
		return nil, err
	}
	limit := 500
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	instant := input.Instant != nil && *input.Instant
	if instant && cancel {
		return nil, errors.New("instant is not supported when canceling deconstruction")
	}
	character, player, err := optionalActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	options := deconstructionAreaOptions{
		Surface:   surface,
		Force:     force,
		Bounds:    bounds,
		Cancel:    cancel,
		Names:     names,
		Types:     types,
		Instant:   instant,
		Limit:     limit,
		Character: character,
		Player:    player,
	}
	response, _, err := executeLua(r.addr, r.password, deconstructionAreaLua(options))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[DeconstructionResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, deconstructionAreaSummary(result, cancel, instant, "area"))
	return result, nil
}

// deconstructionFilter validates the optional prototype-name or entity-type
// filters used to select area targets.
func deconstructionFilter(values []string, field string) ([]string, error) {
	if len(values) > maxBatchTargets {
		return nil, fmt.Errorf("%s must contain at most %d entries", field, maxBatchTargets)
	}
	filtered := make([]string, 0, len(values))
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s entry %d must not be empty", field, i+1)
		}
		filtered = append(filtered, value)
	}
	return filtered, nil
}

// deconstructEntities marks explicit entities for deconstruction.
func deconstructEntities(r *Resolver, input DeconstructEntitiesInput) (*DeconstructionResult, error) {
	return deconstructionEntities(r, input, false)
}

// cancelDeconstructEntities cancels deconstruction orders on explicit entities.
func cancelDeconstructEntities(r *Resolver, input DeconstructEntitiesInput) (*DeconstructionResult, error) {
	return deconstructionEntities(r, input, true)
}

// deconstructionEntitiesLua builds the batch order/cancel script. It is split
// out so the Lua harness can run it against stubbed entities: the marking path
// must survive an entity that ends during order_deconstruction (entity ghosts
// are cancelled by the engine and cannot hold an order).
func deconstructionEntitiesLua(surface, force string, cancel bool, targets string) string {
	return luaFail + fmt.Sprintf(`local surface_name=%s; local force_name=%s; local cancel=%t; local targets={%s}
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local f=game.forces[force_name]; if not f then fail("force not found"); return end
local orders,skipped=0,0; local errors={}
local function add_error(index,code,message,target) if #errors<20 then errors[#errors+1]={index=index,code=code,message=message,target={surface=surface_name,entity=target.name~="" and target.name or nil,unit_number=target.unit~=0 and target.unit or nil,position=(target.x==target.x and {x=target.x,y=target.y} or nil)}} end end
local function resolve_target(t)
  local e,err
  if t.unit~=0 then
    e=game.get_entity_by_unit_number(t.unit)
    if not e then
      if t.name=="" then return nil,"prototype is not resolvable by unit number alone; pass entity too" end
      for _,candidate in pairs(s.find_entities_filtered{name=t.name}) do
        if candidate.unit_number==t.unit then e=candidate; break end
      end
      if not e then return nil,"entity not found by unit number on that surface" end
    elseif t.name~="" and e.name~=t.name then
      return nil,"entity name does not match unit number"
    end
  else
    if t.name=="" then return nil,"entity name or unit number is required" end
    for _,candidate in pairs(s.find_entities_filtered{name=t.name,position={x=t.x,y=t.y},radius=0.01}) do
      if math.abs(candidate.position.x-t.x)<0.001 and math.abs(candidate.position.y-t.y)<0.001 then
        if e then return nil,"multiple matching entities at center" end
        e=candidate
      end
    end
    if not e then return nil,"entity not found at center" end
  end
  if e.surface~=s then return nil,"entity is not on that surface" end
  return e
end
for i,t in ipairs(targets) do
  local e,err=resolve_target(t)
  if not e then
    skipped=skipped+1; add_error(i,"LOOKUP",err,t)
  else
    local ok_marked,marked=pcall(e.to_be_deconstructed,e)
    marked=ok_marked and marked
    if cancel then
      if not marked then skipped=skipped+1; add_error(i,"VALIDATION","entity is not marked for deconstruction",t)
      else
        -- Wrapped: cancelling can end the entity (placing an order on a ghost
        -- is impossible, but an entity can be gone by the time the order is
        -- written), and an unprotected raise would abort the whole batch.
        local ok_cancel=pcall(function() return e.cancel_deconstruction(f) end)
        if not e.valid then orders=orders+1
        else
          local ok_after,after=pcall(e.to_be_deconstructed,e)
          if ok_cancel and ok_after and after then skipped=skipped+1; add_error(i,"ENGINE","deconstruction order could not be canceled",t)
          else orders=orders+1 end
        end
      end
    elseif marked then
      skipped=skipped+1; add_error(i,"VALIDATION","entity is already marked for deconstruction",t)
    else
      -- Entity ghosts cannot hold an order: the engine cancels the ghost during
      -- order_deconstruction and raises because the entity is invalid
      -- afterwards. Refuse nothing, count the cancellation as the order, and
      -- keep the rest of the batch alive.
      local ok_order=pcall(function() return e.order_deconstruction(f) end)
      if not e.valid then orders=orders+1
      else
        local ok_after,after=pcall(e.to_be_deconstructed,e)
        if ok_order and ok_after and after then orders=orders+1
        else skipped=skipped+1; add_error(i,"ENGINE","engine refused the deconstruction order",t) end
      end
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,orders=orders,skipped=skipped,errors=errors}))`,
		luaString(surface), luaString(force), cancel, targets)
}

func deconstructionEntities(r *Resolver, input DeconstructEntitiesInput, cancel bool) (*DeconstructionResult, error) {
	force, err := deconstructionForce(input.Force)
	if err != nil {
		return nil, err
	}
	if len(input.Targets) < 1 || len(input.Targets) > maxBatchTargets {
		return nil, fmt.Errorf("targets must contain between 1 and %d entries", maxBatchTargets)
	}
	surface := stringValueOr(input.Targets[0].Surface, "nauvis")
	if _, err := deconstructionSurface(surface); err != nil {
		return nil, err
	}
	var targets strings.Builder
	for i, target := range input.Targets {
		if target == nil {
			return nil, fmt.Errorf("target %d is missing", i+1)
		}
		targetSurface := stringValueOr(target.Surface, "nauvis")
		if targetSurface != surface {
			return nil, fmt.Errorf("target %d surface %s differs from %s", i+1, targetSurface, surface)
		}
		unitNumber := stringValueOr(target.UnitNumber, "")
		name := stringValueOr(target.Entity, "")
		x, y := entityRefCoordinates(target)
		unit, err := entityTarget(name, unitNumber, x, y)
		if err != nil {
			return nil, fmt.Errorf("target %d: %w", i+1, err)
		}
		if i > 0 {
			targets.WriteString(",")
		}
		fmt.Fprintf(&targets, "{unit=%d,name=%s,x=%s,y=%s,surface=%s}", unit, luaString(name), luaCoordinate(x), luaCoordinate(y), luaString(surface))
	}
	lua := deconstructionEntitiesLua(surface, force, cancel, targets.String())
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[DeconstructionResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, deconstructionSummary(result, cancel, "targets"))
	return result, nil
}

// deconstructionAreaOptions carries the validated inputs of an area request.
type deconstructionAreaOptions struct {
	Surface   string
	Force     string
	Bounds    areaBounds
	Cancel    bool
	Names     []string
	Types     []string
	Instant   bool
	Limit     int
	Character uint64
	Player    string
}

// deconstructionInventoryLua reads main-inventory contents in the 2.0 array
// form ({name=,count=,quality=} entries) and falls back to the dictionary form.
// Counts are aggregated by name and quality so a delta reflects what mining added.
const deconstructionInventoryLua = `
local function deconstruction_item_identity(item)
  local quality=item.quality
  if type(quality)=="table" then quality=quality.name end
  return item.name,quality or "normal"
end
local function deconstruction_add_item(items,name,quality,count)
  if not name or not count or count<=0 then return end
  local key=name..string.char(31)..quality
  local entry=items[key]
  if not entry then entry={name=name,quality=quality,count=0}; items[key]=entry end
  entry.count=entry.count+count
end
local function deconstruction_inventory_counts(inv)
  local counts={}
  local ok,contents=pcall(function() return inv.get_contents() end)
  if not ok or type(contents)~="table" then return counts end
  if contents[1]~=nil then
    for _,item in pairs(contents) do
      local name,quality=deconstruction_item_identity(item)
      deconstruction_add_item(counts,name,quality,item.count)
    end
  else
    for name,count in pairs(contents) do deconstruction_add_item(counts,name,"normal",count) end
  end
  return counts
end
local function deconstruction_sorted_items(items)
  local result={}
  for _,item in pairs(items) do result[#result+1]=item end
  table.sort(result,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
  return result
end
local function deconstruction_inventory_difference(before,inv)
  local after=deconstruction_inventory_counts(inv); local difference={}
  for key,item in pairs(after) do
    local prior=before[key] and before[key].count or 0
    deconstruction_add_item(difference,item.name,item.quality,item.count-prior)
  end
  return deconstruction_sorted_items(difference)
end
`

// deconstructionAreaLua marks, cancels, or instantly removes the entities an
// area request selects. Targets are selected from the requested force and the
// neutral force (trees, rocks, cliffs, ground items), names/types filters are
// applied by find_entities_filtered, characters and ghosts are always skipped,
// and the limit bounds processed targets. Instant mode mines each reachable
// target as the actor; targets it cannot mine have their inventories, belt lane
// contents, inserter hand and placement item spilled onto the ground before
// being destroyed, so instant removal never voids contents. Recovered items
// report the actor inventory delta plus everything spilled.
func deconstructionAreaLua(options deconstructionAreaOptions) string {
	names, types := "nil", "nil"
	if len(options.Names) > 0 {
		names = luaKeys(options.Names)
	}
	if len(options.Types) > 0 {
		types = luaKeys(options.Types)
	}
	header := fmt.Sprintf(`local surface_name,force_name=%s,%s; local area=%s; local limit=%d
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local f=game.forces[force_name]; if not f then fail("force not found"); return end
local names,types=%s,%s
-- Trees, rocks, cliffs and ground items belong to the neutral force, so the
-- scan covers the requested force and neutral (once) instead of missing them.
local forces={f}
local neutral=game.forces.neutral
if neutral and neutral~=f then forces[#forces+1]=neutral end
local function deconstruction_skippable(e)
  return e.type=="character" or e.type=="entity-ghost" or e.type=="tile-ghost"
end
-- limit bounds processed targets, so skipping characters and ghosts never
-- consumes the quota and never becomes a per-target engine error.
local function targets()
  local filter={area=area,force=forces,skip_fog_of_war=true}
  if names then filter.name=names end
  if types then filter.type=types end
  local list={}
  for _,e in ipairs(s.find_entities_filtered(filter)) do
    if e.valid and not deconstruction_skippable(e) then list[#list+1]=e end
    if #list>=limit then break end
  end
  return list
end
`, luaString(options.Surface), luaString(options.Force), options.Bounds.lua(), options.Limit, names, types)
	if options.Instant {
		return luaFail + header + deconstructionInventoryLua + fmt.Sprintf(`local unit,player_name=%d,%s
local actor,player_object
if unit~=0 then
  actor=game.get_entity_by_unit_number(unit)
  if not actor or actor.type~="character" then fail("character not found"); return end
  player_object=actor.player
elseif player_name~="" then
  player_object=game.players[player_name]
  if not player_object then fail("player not found"); return end
  actor=player_object.character
end
local inv=nil
if actor then
  local okinv,main=pcall(function() return actor.get_main_inventory() end)
  if okinv then inv=main end
end
-- Detached characters have no owning player, but the character itself is a
-- LuaControl and can mine. Prefer it; fall back to the player object.
local control=actor or player_object
local before=inv and deconstruction_inventory_counts(inv) or {}
local spilled={}
local spill_position=nil
local function spill_quality(quality)
  if type(quality)=="table" then return quality.name end
  return quality or "normal"
end
-- Destroying an entity that still holds items voids them. Targets the actor
-- cannot mine are emptied onto the ground first so instant removal never
-- destroys containers, modules, fuel, belt lane contents, an inserter's held
-- item, or the entity's own placement item. Items already committed to a
-- machine's crafting progress are not exposed by the engine API and cannot be
-- recovered.
local function spill_stack(name,count,quality)
  if not name or not count or count<=0 or not spill_position then return end
  local quality_name=spill_quality(quality)
  local ok=pcall(function()
    local params={position=spill_position,stack={name=name,count=count,quality=quality_name},enable_looted=false,allow_belts=false}
    -- Spilling with a force marks the item for deconstruction, and without
    -- robots those orders never complete, so only pass it where a logistic
    -- network of the force actually covers the position.
    local ok_cover,cover=pcall(function() return s.find_logistic_network_by_position(spill_position,f) end)
    if ok_cover and cover then params.force=f end
    s.spill_item_stack(params)
  end)
  if ok then
    deconstruction_add_item(spilled,name,quality_name,count)
  end
end
local function spill_inventory(target_inventory)
  local ok_contents,contents=pcall(function() return target_inventory.get_contents() end)
  if not ok_contents or type(contents)~="table" then return end
  if contents[1]~=nil then
    for _,item in pairs(contents) do spill_stack(item.name,item.count,item.quality) end
  else
    for name,count in pairs(contents) do spill_stack(name,count,"normal") end
  end
  target_inventory.clear()
end
local function spill_entity(e)
  spill_position=e.position
  for _,kind in pairs(defines.inventory) do
    local okinv,target_inventory=pcall(function() return e.get_inventory(kind) end)
    if okinv and target_inventory and not target_inventory.is_empty() then
      spill_inventory(target_inventory)
    end
  end
  -- Belt, underground belt and splitter lane contents are not inventories.
  local ok_lines,line_count=pcall(function() return e.get_max_transport_line_index() end)
  if ok_lines and type(line_count)=="number" then
    for index=1,line_count do
      local ok_line,line=pcall(function() return e.get_transport_line(index) end)
      if ok_line and line then
        local ok_contents,contents=pcall(function() return line.get_contents() end)
        if ok_contents then
          for _,item in pairs(contents or {}) do spill_stack(item.name,item.count,item.quality) end
        end
      end
    end
  end
  -- An inserter's held item is a LuaItemStack, not an inventory.
  local held=nil
  local ok_held,held_value=pcall(function() return e.held_stack end)
  if ok_held then held=held_value end
  if held then
    local ok_read,readable=pcall(function() return held.valid_for_read end)
    if ok_read and readable then
      spill_stack(held.name,held.count,held.quality)
      pcall(function() held.clear() end)
    end
  end
  local proto=e.prototype
  if proto and proto.items_to_place_this then
    for _,item in pairs(proto.items_to_place_this) do
      spill_stack(item.name,item.count,item.quality)
    end
  end
  spill_position=nil
end
local removed,skipped=0,0; local errors={}
for index,e in ipairs(targets()) do
  if e.valid and e~=actor and e.type~="character" and e.type~="entity-ghost" and e.type~="tile-ghost" then
    local target={surface=s.name,entity=e.name,position=e.position,unit_number=e.unit_number and tostring(e.unit_number) or nil}
    local mined=false
    if control and actor and `+reachable("actor", "e")+` then
      local okm,result=pcall(function() return control.mine_entity(e) end)
      mined=okm and result==true
    end
    if mined then
      removed=removed+1
    else
      spill_entity(e)
      local okd,destroy_error=pcall(function() e.destroy{raise_destroy=true} end)
      if okd then
        removed=removed+1
      else
        skipped=skipped+1
        if #errors<20 then errors[#errors+1]={index=index,code="ENGINE",message=tostring(destroy_error),target=target} end
      end
    end
  end
end
local items=inv and deconstruction_inventory_difference(before,inv) or {}
if next(spilled)~=nil then
  local merged={}
  for _,item in ipairs(items) do deconstruction_add_item(merged,item.name,item.quality,item.count) end
  for _,item in pairs(spilled) do deconstruction_add_item(merged,item.name,item.quality,item.count) end
  items=deconstruction_sorted_items(merged)
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,orders=0,skipped=skipped,removed=removed,items=items,errors=errors}))`,
			options.Character, luaString(options.Player))
	}
	cancel := options.Cancel
	return luaFail + header + fmt.Sprintf(`local cancel=%t
local orders,skipped=0,0; local errors={}
for index,e in ipairs(targets()) do
  local target={surface=s.name,entity=e.name,position=e.position,unit_number=e.unit_number and tostring(e.unit_number) or nil}
  local ok,message=pcall(function()
    if cancel then
      if not e.to_be_deconstructed() then error("entity is not marked for deconstruction") end
      e.cancel_deconstruction(f)
      if e.to_be_deconstructed() then error("deconstruction order could not be canceled") end
    else
      if e.to_be_deconstructed() then error("entity is already marked for deconstruction") end
      if not e.order_deconstruction(f) then error("engine refused the deconstruction order") end
      if not e.to_be_deconstructed() then error("engine did not record the deconstruction order") end
    end
  end)
  if ok then
    orders=orders+1
  else
    skipped=skipped+1
    if #errors<20 then errors[#errors+1]={index=index,code="ENGINE",message=tostring(message),target=target} end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,orders=orders,skipped=skipped,removed=0,items={},errors=errors}))`, cancel)
}

// deconstructionAreaSummary describes an area request, including instant
// removals and their recovered items.
func deconstructionAreaSummary(result *DeconstructionResult, cancel, instant bool, scope string) string {
	if instant {
		summary := fmt.Sprintf("removed %d entities instantly (%s) on %s for %s",
			result.Removed, scope, result.Surface, result.Force)
		if len(result.Items) > 0 {
			summary += fmt.Sprintf(", recovered %d item stacks", len(result.Items))
		}
		if result.Skipped > 0 {
			summary += fmt.Sprintf(", %d skipped", result.Skipped)
		}
		return summary
	}
	return deconstructionSummary(result, cancel, scope)
}

func deconstructionSummary(result *DeconstructionResult, cancel bool, scope string) string {
	verb := "marked"
	if cancel {
		verb = "canceled"
	}
	summary := fmt.Sprintf("%s %d deconstruction orders (%s) on %s for %s",
		verb, result.Orders, scope, result.Surface, result.Force)
	if result.Skipped > 0 {
		summary += fmt.Sprintf(", %d skipped", result.Skipped)
	}
	return summary
}

func deconstructionSurface(surface string) (string, error) {
	if surface == "" {
		return "", errors.New("surface must not be empty")
	}
	return surface, nil
}

func deconstructionForce(force *string) (string, error) {
	name := "player"
	if force != nil {
		name = *force
	}
	if name == "" {
		return "", errors.New("force must not be empty")
	}
	return name, nil
}
