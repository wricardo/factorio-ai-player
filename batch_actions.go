package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// batchTarget is one validated entity reference ready for Lua.
type batchTarget struct {
	surface     string
	unit        uint64
	name        string
	x, y        float64
	hasPosition bool
}

// batchTargets validates 1..1000 distinct entity references. Duplicates are
// rejected because a repeated target would be fueled or filled twice.
func batchTargets(targets []*EntityRefInput) ([]batchTarget, error) {
	if len(targets) < 1 || len(targets) > maxBatchTargets {
		return nil, fmt.Errorf("targets must contain between 1 and %d entries", maxBatchTargets)
	}
	out := make([]batchTarget, 0, len(targets))
	seen := map[string]int{}
	for i, ref := range targets {
		if ref == nil {
			return nil, fmt.Errorf("target %d is missing", i+1)
		}
		surface := stringValueOr(ref.Surface, "nauvis")
		if surface == "" {
			return nil, fmt.Errorf("target %d surface must not be empty", i+1)
		}
		name := stringValueOr(ref.Entity, "")
		x, y := entityRefCoordinates(ref)
		unit, err := entityTarget(name, stringValueOr(ref.UnitNumber, ""), x, y)
		if err != nil {
			return nil, fmt.Errorf("target %d: %w", i+1, err)
		}
		hasPosition := !math.IsNaN(x) && !math.IsNaN(y)
		if math.IsNaN(x) {
			x = 0
		}
		if math.IsNaN(y) {
			y = 0
		}
		key := fmt.Sprintf("%s|unit|%d", surface, unit)
		if unit == 0 {
			key = fmt.Sprintf("%s|%s|%g|%g", surface, name, x, y)
		}
		if first, ok := seen[key]; ok {
			return nil, fmt.Errorf("target %d duplicates target %d", i+1, first)
		}
		seen[key] = i + 1
		out = append(out, batchTarget{surface: surface, unit: unit, name: name, x: x, y: y, hasPosition: hasPosition})
	}
	return out, nil
}

func batchTargetsLua(targets []batchTarget) string {
	var b strings.Builder
	for i, t := range targets {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "{surface=%s,unit=%d,name=%s,x=%s,y=%s,has_position=%t}", luaString(t.surface), t.unit, luaString(t.name),
			strconv.FormatFloat(t.x, 'g', -1, 64), strconv.FormatFloat(t.y, 'g', -1, 64), t.hasPosition)
	}
	return b.String()
}

// luaTransferStateLua explains a transfer that moved nothing. A limited chest
// bar, a deconstruction mark and a slot filter all surface as capacity 0, so the
// shared helper names the cause instead of leaving the caller with moved: 0.
const luaTransferStateLua = `local function transfer_target_state(e,destination)
  local bar=nil
  if destination and destination.supports_bar() then bar=destination.get_bar() end
  local deconstructed=false
  local ok,value=pcall(function() return e.to_be_deconstructed() end)
  if ok then deconstructed=value end
  return bar,deconstructed
end
local function transfer_block_reason(available,moved,capacity,destination,bar,deconstructed)
  if moved>0 then return nil end
  if available==0 then return "NO_SOURCE" end
  if deconstructed then return "MARKED_FOR_DECONSTRUCTION" end
  if bar~=nil and bar<=1 then return "BAR_LIMITED" end
  if destination and destination.supports_filters() and destination.is_filtered() then return "FILTERED" end
  if capacity==0 then return "NO_CAPACITY" end
  return nil
end
`

// luaBatchEntityIndexLua resolves one batch reference by unit number without
// rescanning the surface per target. Prototypes without the get-by-unit-number
// flag (chests, furnaces, drills, belts) would otherwise cost one full-surface
// scan per target, so one unit->entity index is built per surface and prototype
// name and reused for the rest of the batch. A position hint turns the first
// lookup into a one-tile scan, and unit-number-only references fall back to the
// shared type-filtered scan cache.
const luaBatchEntityIndexLua = `local batch_unit_index_cache={}
local function find_batch_entity(ref)
  local direct=game.get_entity_by_unit_number(ref.unit)
  if direct then return direct end
  local s=game.surfaces[ref.surface]
  if not s then return nil,"surface not found" end
  if ref.name=="" then return find_entity_by_unit_number(s,ref.unit,"") end
  if ref.has_position then
    -- A position-only reference carries unit 0, so requiring a unit-number
    -- match here silently skipped the exact tile and fell through to the
    -- per-prototype index, which could not find unit 0 either. Accept any
    -- candidate at the exact centre when no unit number was given.
    for _,candidate in pairs(s.find_entities_filtered{name=ref.name,position={x=ref.x,y=ref.y},radius=0.01}) do
      if math.abs(candidate.position.x-ref.x)<0.001 and math.abs(candidate.position.y-ref.y)<0.001
        and (ref.unit==0 or candidate.unit_number==ref.unit) then return candidate end
    end
  end
  if ref.unit==0 then return nil,"entity not found at that position on that surface" end
  local key=ref.surface..string.char(31)..ref.name
  local index=batch_unit_index_cache[key]
  if not index then
    index={}
    for _,candidate in pairs(s.find_entities_filtered{name=ref.name}) do
      if candidate.unit_number then index[candidate.unit_number]=candidate end
    end
    batch_unit_index_cache[key]=index
  end
  local e=index[ref.unit]
  if e and not e.valid then index[ref.unit]=nil; e=nil end
  return e
end
`

// batchPrelude resolves the actor, its main inventory, the item and the
// per-target helpers. It binds p (actor), c (acting character), inv, item,
// quality, refs, fail_at and resolve_target.
func batchPrelude(character uint64, player, item, quality string, targets []batchTarget) string {
	return luaFail + fmt.Sprintf(luaActionActor, character, luaString(player)) +
		fmt.Sprintf(`local inv=p.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local item=%s; local quality=%s
if not prototypes.item[item] then fail("item prototype not found"); return end
if not prototypes.quality[quality] then fail("quality not found"); return end
local c; if unit~=0 then c=p else c=p.character; if not c then fail("player has no character"); return end end
local refs={%s}
`, luaString(item), luaString(quality), batchTargetsLua(targets)) + luaEntityResolve + luaBatchEntityIndexLua + `
local failed=0
local errors={}
local results={}
local function fail_at(i,code,message,ref,e)
  failed=failed+1
  if #errors<20 then
    local target={surface=ref.surface,entity=(e and e.name) or ref.name}
    if e then target.unitNumber=e.unit_number; target.position={x=e.position.x,y=e.position.y} elseif ref.unit==0 then target.position={x=ref.x,y=ref.y} else target.unitNumber=ref.unit end
    errors[#errors+1]={index=i,code=code,message=message,target=target}
  end
end
-- resolve_target finds one entity and applies the shared force, surface and
-- reach checks.
local function resolve_target(i,ref)
  local e,err=find_batch_entity(ref)
  if not e then fail_at(i,"LOOKUP",err or "entity not found by unit number on that surface",ref,nil); return nil end
  if e.force~=p.force then fail_at(i,"LOOKUP","entity is not on the actor's force",ref,nil); return nil end
  if ref.name~="" and e.name~=ref.name then fail_at(i,"LOOKUP","entity name does not match unit number",ref,nil); return nil end
  if c.surface~=e.surface then fail_at(i,"VALIDATION","actor is on another surface",ref,e); return nil end
  if ` + reachViolation("c", "e") + ` then fail_at(i,"REACH","entity is out of reach",ref,e); return nil end
  return e
end
`
}

// fuelMany fuels explicit entities in one call. Per-target semantics mirror
// fuel: the full count is inserted or the target fails with a typed error.
func fuelMany(r *Resolver, input FuelManyInput) (*FuelManyResult, error) {
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	targets, err := batchTargets(input.Targets)
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
	// topUpTo switches fuelMany from "insert exactly countPerTarget" to "fill
	// each target up to topUpTo items", which is idempotent: a second identical
	// call adds nothing and reports the target as skipped.
	topUp := int64(-1)
	if input.TopUpTo != nil {
		if *input.TopUpTo < 1 {
			return nil, errors.New("topUpTo must be at least 1")
		}
		topUp = int64(*input.TopUpTo)
	} else if input.CountPerTarget < 1 {
		return nil, errors.New("countPerTarget must be at least 1")
	}
	lua := batchPrelude(character, player, input.Item, quality, targets) + fmt.Sprintf(`
local count=%d
local top_up=%d
local stack={name=item,quality=quality}
local total_moved,fueled,skipped=0,0,0
for i,ref in ipairs(refs) do
  local e=resolve_target(i,ref)
  if e then
    local fuel=e.get_fuel_inventory()
    if not fuel then fail_at(i,"VALIDATION","entity has no fuel inventory",ref,e)
    else
      local requested=count
      if top_up>0 then
        requested=top_up-fuel.get_item_count(stack)
        if requested<0 then requested=0 end
      end
      if top_up>0 and requested==0 then
        skipped=skipped+1
        results[#results+1]={index=i,entity=e.name,unitNumber=e.unit_number,position={x=e.position.x,y=e.position.y},requested=0,moved=0,skipped=true}
      elseif inv.get_item_count(stack)<requested then fail_at(i,"INVENTORY","actor lacks requested fuel",ref,e)
      elseif fuel.get_insertable_count(stack)<requested then fail_at(i,"INVENTORY","fuel inventory cannot accept requested items",ref,e)
      else
        local removed=inv.remove{name=item,quality=quality,count=requested}
        if removed~=requested then
          if removed>0 then inv.insert{name=item,quality=quality,count=removed} end
          fail_at(i,"ENGINE","could not remove requested fuel",ref,e)
        else
          local inserted=fuel.insert{name=item,quality=quality,count=removed}
          if inserted<removed then inv.insert{name=item,quality=quality,count=removed-inserted} end
          if inserted==0 then
            fail_at(i,"ENGINE","fuel inventory rejected item; fuel returned",ref,e)
          else
            fueled=fueled+1; total_moved=total_moved+inserted
            results[#results+1]={index=i,entity=e.name,unitNumber=e.unit_number,position={x=e.position.x,y=e.position.y},requested=requested,moved=inserted,skipped=false}
          end
        end
      end
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,item=item,quality=quality,fueled=fueled,failed=failed,skipped=skipped,moved=total_moved,results=results,errors=errors}))`, input.CountPerTarget, topUp)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[FuelManyResult](response)
	if err != nil {
		return nil, err
	}
	if result.Moved > 0 {
		announceMutation(r, input.Message, fmt.Sprintf("fueled %d of %d entities with %d %s", result.Fueled, len(targets), result.Moved, result.Item))
	}
	return result, nil
}

// transferMany moves one item between the actor and each explicit target with
// the same per-target semantics as transfer.
func transferMany(r *Resolver, input TransferManyInput) (*TransferManyResult, error) {
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	targets, err := batchTargets(input.Targets)
	if err != nil {
		return nil, err
	}
	key, err := resolveInventoryKind(string(input.Inventory))
	if err != nil {
		return nil, err
	}
	var direction string
	switch input.Direction {
	case TransferDirectionToEntity:
		direction = "to-entity"
	case TransferDirectionToActor:
		direction = "to-player"
	default:
		return nil, fmt.Errorf("unsupported transfer direction %q", input.Direction)
	}
	if input.Item == "" {
		return nil, errors.New("item must not be empty")
	}
	quality, err := resolveQuality(input.Quality)
	if err != nil {
		return nil, err
	}
	count := input.CountPerTarget
	if math.IsNaN(count) || math.IsInf(count, 0) || count < 1 || count > math.MaxUint32 || count != math.Trunc(count) {
		return nil, errors.New("countPerTarget must be a whole number from 1 through 4294967295")
	}
	lua := batchPrelude(character, player, input.Item, quality, targets) + luaInventoryHelpers + luaTransferStateLua + fmt.Sprintf(`
local inventory_key=%s; local direction=%s; local requested=%d
local spec={name=item,quality=quality}
local total_moved,stacks=0,0
local player_inventory=inv
for i,ref in ipairs(refs) do
  local e=resolve_target(i,ref)
  if e then
    local target=inventories(e)[inventory_key]
    if not target then
      fail_at(i,"VALIDATION","entity does not have requested inventory",ref,e)
    else
      local source,destination=player_inventory,target
      if direction=="to-player" then source,destination=target,player_inventory end
      local available=source.get_item_count(spec)
      local capacity=destination.get_insertable_count(spec)
      local remaining=math.min(requested,available,capacity)
      local moved=0
      local last=#destination
      if destination.supports_bar() then last=math.min(last,destination.get_bar()-1) end
      for si=1,#source do
        if remaining==0 then break end
        local stack=source[si]
        if stack.valid_for_read and stack.name==item and stack.quality.name==quality then
          for dj=1,last do
            if remaining==0 or not stack.valid_for_read then break end
            local before=stack.count
            destination[dj].transfer_stack(stack,math.min(remaining,before))
            local after=0; if stack.valid_for_read then after=stack.count end
            local transferred=before-after
            if transferred>0 then stacks=stacks+1 end
            moved=moved+transferred; remaining=remaining-transferred
          end
        end
      end
      total_moved=total_moved+moved
      local bar,deconstructed=transfer_target_state(e,destination)
      local reason=transfer_block_reason(available,moved,capacity,destination,bar,deconstructed)
      results[#results+1]={index=i,entity=e.name,unitNumber=e.unit_number,position={x=e.position.x,y=e.position.y},requested=requested,available=available,capacity=capacity,moved=moved,bar=bar,to_be_deconstructed=deconstructed,reason=reason}
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,item=item,quality=quality,moved=total_moved,transferredStacks=stacks,failed=failed,results=results,errors=errors}))`,
		luaString(key), luaString(direction), uint64(count))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[TransferManyResult](response)
	if err != nil {
		return nil, err
	}
	result.Inventory = input.Inventory
	result.Direction = input.Direction
	if result.Moved > 0 {
		announceMutation(r, input.Message, fmt.Sprintf("transferred %s %s across %d of %d entities", numberValue(&result.Moved), result.Item, len(result.Results), len(targets)))
	}
	return result, nil
}
