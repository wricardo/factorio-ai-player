package main

import (
	"errors"
	"fmt"
	"math"
)

// transferBetween moves one item between two explicitly addressed entity
// inventories in a single RCON call. It has no actor: both inventories are
// resolved from their own references, so a chest can feed a chest, a furnace
// output can feed a chest, or a machine input can be filled from a provider
// chest without routing the stack through a character. Reach therefore never
// applies, matching buildGhosts and the other actor-free mutations.
func transferBetween(r *Resolver, input TransferBetweenInput) (*TransferBetweenResult, error) {
	fromUnit, fromName, fromX, fromY, fromSurface, err := entityRefTarget(input.From)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	toUnit, toName, toX, toY, toSurface, err := entityRefTarget(input.To)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	if input.Item == "" {
		return nil, errors.New("item must not be empty")
	}
	quality, err := resolveQuality(input.Quality)
	if err != nil {
		return nil, err
	}
	count := input.Count
	if math.IsNaN(count) || math.IsInf(count, 0) || count < 1 || count > math.MaxUint32 || count != math.Trunc(count) {
		return nil, errors.New("count must be a whole number from 1 through 4294967295")
	}
	fromKind := InventoryKindChest
	if input.FromInventory != nil {
		fromKind = *input.FromInventory
	}
	toKind := fromKind
	if input.ToInventory != nil {
		toKind = *input.ToInventory
	}
	fromKey, err := resolveInventoryKind(string(fromKind))
	if err != nil {
		return nil, fmt.Errorf("fromInventory: %w", err)
	}
	toKey, err := resolveInventoryKind(string(toKind))
	if err != nil {
		return nil, fmt.Errorf("toInventory: %w", err)
	}
	lua := luaFail +
		entityRefPrelude("from", fromUnit, fromName, fromX, fromY, fromSurface) +
		entityRefPrelude("to", toUnit, toName, toX, toY, toSurface) +
		luaInventoryHelpers + luaTransferStateLua + fmt.Sprintf(`
local item=%s; local quality=%s; local requested=%d
if not prototypes.item[item] then fail("item prototype not found"); return end
if not prototypes.quality[quality] then fail("quality not found"); return end
local spec={name=item,quality=quality}
local source=inventories(from)[%s]
if not source then fail("from entity does not have requested inventory"); return end
local destination=inventories(to)[%s]
if not destination then fail("to entity does not have requested inventory"); return end
local available=source.get_item_count(spec)
local capacity=destination.get_insertable_count(spec)
local remaining=math.min(requested,available,capacity)
local moved=0; local stacks=0
local last=#destination
if destination.supports_bar() then last=math.min(last,destination.get_bar()-1) end
for i=1,#source do
  if remaining==0 then break end
  local stack=source[i]
  if stack.valid_for_read and stack.name==item and stack.quality.name==quality then
    for j=1,last do
      if remaining==0 or not stack.valid_for_read then break end
      local before=stack.count
      destination[j].transfer_stack(stack,math.min(remaining,before))
      local after=0; if stack.valid_for_read then after=stack.count end
      local transferred=before-after
      if transferred>0 then stacks=stacks+1 end
      moved=moved+transferred; remaining=remaining-transferred
    end
  end
end
local bar,deconstructed=transfer_target_state(to,destination)
local reason=transfer_block_reason(available,moved,capacity,destination,bar,deconstructed)
rcon.print(helpers.table_to_json({tick=game.tick,item=item,quality=quality,requested=requested,available=available,capacity=capacity,moved=moved,transferredStacks=stacks,fromUnitNumber=tostring(from.unit_number),toUnitNumber=tostring(to.unit_number),fromName=from.name,toName=to.name,reason=reason}))`,
		luaString(input.Item), luaString(quality), uint64(count), luaString(fromKey), luaString(toKey))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[TransferBetweenResult](response)
	if err != nil {
		return nil, err
	}
	result.FromInventory = fromKind
	result.ToInventory = toKind
	if result.Moved > 0 {
		announceMutation(r, input.Message, fmt.Sprintf("moved %g %s from %s to %s", result.Moved, result.Item, result.FromName, result.ToName))
	}
	return result, nil
}
