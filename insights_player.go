package main

import (
	"errors"
	"fmt"
	"strconv"
)

// playerInsight observes one named player's character without changing game state.
func playerInsight(r *Resolver, input *PlayerInsightInput) (*PlayerInsight, error) {
	if input == nil || input.Player == "" {
		return nil, errors.New("player is required")
	}
	if input.Recipe != nil && *input.Recipe == "" {
		return nil, errors.New("recipe must not be empty")
	}
	target := "nil"
	if input.Target != nil {
		if input.Target.UnitNumber != nil {
			n, err := strconv.ParseUint(*input.Target.UnitNumber, 10, 32)
			if err != nil || n == 0 {
				return nil, errors.New("target unitNumber must be from 1 to 4294967295")
			}
			target = "{unit=" + strconv.FormatUint(n, 10) + "}"
		} else if input.Target.Position != nil {
			target = fmt.Sprintf("{position={x=%s,y=%s}}", strconv.FormatFloat(input.Target.Position.X, 'g', -1, 64), strconv.FormatFloat(input.Target.Position.Y, 'g', -1, 64))
		} else {
			return nil, errors.New("target requires unitNumber or position")
		}
	}
	recipe := "nil"
	if input.Recipe != nil {
		recipe = luaString(*input.Recipe)
	}
	lua := luaFail + fmt.Sprintf(`
local p=game.get_player(%s)
if not p then fail("player not found"); return end
local c=p.character
local s=c and c.surface or p.surface
local pos=c and c.position or p.position
local result={tick=game.tick,player=p.name,connected=p.connected,controllerType=p.controller_type,
  surface=s.name,force=p.force.name,position=pos,hasCharacter=c~=nil}
local inv
if c then
  inv=c.get_main_inventory()
  if inv then
    result.items=inv.get_contents()
    table.sort(result.items,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
  end
  result.queue=c.crafting_queue or {}
  result.queueSize=c.crafting_queue_size
  result.queueProgress=c.crafting_queue_progress
end
local recipe_name=%s
if recipe_name then
  local recipe=p.force.recipes[recipe_name]
  if not recipe then fail("recipe not found for player's force"); return end
  local info={name=recipe.name,enabled=recipe.enabled,category=recipe.category,energy=recipe.energy,
    hiddenFromPlayerCrafting=recipe.prototype.hidden_from_player_crafting,ingredients={},products=recipe.products}
  if c then info.craftableCount=c.get_craftable_count(recipe.name) end
  local counts={}
  if inv then for _,stack in pairs(result.items) do counts[stack.name]=(counts[stack.name] or 0)+stack.count end end
  for _,ingredient in ipairs(recipe.ingredients) do
    local item={name=ingredient.name,type=ingredient.type,required=ingredient.amount}
    if ingredient.type~="item" then item.unavailableReason="Fluid ingredients are not held in the character main inventory"
    elseif not inv then item.unavailableReason="Character main inventory is unavailable"
    else item.available=counts[ingredient.name] or 0 end
    info.ingredients[#info.ingredients+1]=item
  end
  result.recipe=info
end
local target=%s
if target then
  local reach={}
  if target.unit then
    local entity=game.get_entity_by_unit_number(target.unit)
    if not entity or not entity.valid then fail("target entity not found"); return end
    reach.targetName=entity.name
    reach.targetPosition=entity.position
    if not c then reach.unavailableReason="Player has no character"
    elseif entity.surface~=c.surface then reach.canReachEntity=false; reach.unavailableReason="Target is on another surface"
    else reach.canReachEntity=c.can_reach_entity(entity) end
    if entity.surface==s then
      local dx,dy=entity.position.x-pos.x,entity.position.y-pos.y
      reach.distance=math.sqrt(dx*dx+dy*dy)
    end
  else
    reach.targetPosition=target.position
    local dx,dy=target.position.x-pos.x,target.position.y-pos.y
    reach.distance=math.sqrt(dx*dx+dy*dy)
    reach.unavailableReason="Factorio exposes entity reachability, not position reachability"
  end
  result.reach=reach
end
rcon.print(helpers.table_to_json(result))`, luaString(input.Player), recipe, target)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[PlayerInsight](response)
}
