package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

type harvestAreaCommandInput struct {
	Player       *string `json:"player,omitempty"`
	Character    *string `json:"character,omitempty"`
	LeftTopX     float64 `json:"left-top-x"`
	LeftTopY     float64 `json:"left-top-y"`
	RightBottomX float64 `json:"right-bottom-x"`
	RightBottomY float64 `json:"right-bottom-y"`
	Message      *string `json:"message,omitempty"`
}

type pickupAreaCommandInput struct {
	Character    string  `json:"character"`
	LeftTopX     float64 `json:"left-top-x"`
	LeftTopY     float64 `json:"left-top-y"`
	RightBottomX float64 `json:"right-bottom-x"`
	RightBottomY float64 `json:"right-bottom-y"`
	Message      *string `json:"message,omitempty"`
}

func harvestArea(r *Resolver, input HarvestAreaInput) (*HarvestAreaResult, error) {
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	return command[HarvestAreaResult](r, "harvest-area", harvestAreaCommandInput{
		Player:       input.Player,
		Character:    input.Character,
		LeftTopX:     input.Area.LeftTopX,
		LeftTopY:     input.Area.LeftTopY,
		RightBottomX: input.Area.RightBottomX,
		RightBottomY: input.Area.RightBottomY,
		Message:      input.Message,
	})
}

func pickupArea(r *Resolver, input PickupAreaInput) (*PickupAreaResult, error) {
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	return command[PickupAreaResult](r, "pickup-area", pickupAreaCommandInput{
		Character:    input.Character,
		LeftTopX:     input.Area.LeftTopX,
		LeftTopY:     input.Area.LeftTopY,
		RightBottomX: input.Area.RightBottomX,
		RightBottomY: input.Area.RightBottomY,
		Message:      input.Message,
	})
}

func harvestAreaLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("harvest-area", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player controlling a character (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	areaFlags := addAreaBoundFlags(fs)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 {
		return "", errors.New("exactly one of player or character is required")
	}
	bounds := areaFlags.bounds()
	if err := bounds.validate(); err != nil {
		return "", err
	}
	lua := fmt.Sprintf(luaFail+`local player_name,unit=%s,%d; local actor
if unit~=0 then
  actor=game.get_entity_by_unit_number(unit)
  if not actor or actor.type~="character" then fail("character unit not found"); return end
else
  actor=game.get_player(player_name)
  if not actor then fail("player not found"); return end
  if actor.controller_type~=defines.controllers.character or not actor.character then fail("player must control a character"); return end
end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local surface=actor.surface; local area=%s`, luaString(*player), *character, bounds.lua())
	return lua + inventoryItemHelpersLua + `
local before=inventory_item_counts(inv); local targets={}
for _,e in pairs(surface.find_entities_filtered{area=area,type={"tree","simple-entity"}}) do
  local p=e.position
  if p.x>=area[1][1] and p.y>=area[1][2] and p.x<=area[2][1] and p.y<=area[2][2]
      and (e.type=="tree" or string.find(e.name,"rock",1,true)) then targets[#targets+1]=e end
end
table.sort(targets,function(a,b)
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  return a.name<b.name
end)
local trees,rocks,out_of_reach,failed=0,0,0,0
for _,e in ipairs(targets) do
  local kind=e.type
  if not actor.can_reach_entity(e) then
    out_of_reach=out_of_reach+1
  elseif actor.mine_entity(e,false) then
    if kind=="tree" then trees=trees+1 else rocks=rocks+1 end
  else
    failed=failed+1
  end
end
local items=inventory_item_difference(before,inv)

rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,trees_mined=trees,rocks_mined=rocks,out_of_reach=out_of_reach,failed=failed,items=items}))`, nil
}

func pickupAreaLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("pickup-area", flag.ContinueOnError)
	fs.SetOutput(out)
	character := fs.Uint64("character", 0, "Character unit number (required)")
	areaFlags := addAreaBoundFlags(fs)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *character == 0 || *character > math.MaxUint32 {
		return "", errors.New("character unit number from 1 to 4294967295 is required")
	}
	bounds := areaFlags.bounds()
	if err := bounds.validate(); err != nil {
		return "", err
	}
	lua := fmt.Sprintf(luaFail+`local c=game.get_entity_by_unit_number(%d); if not c or c.type~="character" then fail("character not found"); return end
local inv=c.get_main_inventory(); if not inv then fail("character has no main inventory"); return end
local surface=c.surface; local area=%s`, *character, bounds.lua())
	return lua + inventoryItemHelpersLua + `
local candidates={}
for _,e in pairs(surface.find_entities_filtered{area=area,type="item-entity"}) do
  local p=e.position; local stack=e.stack
  if p.x>=area[1][1] and p.y>=area[1][2] and p.x<=area[2][1] and p.y<=area[2][2] and stack and stack.valid_for_read then
    candidates[#candidates+1]=e
  end
end
table.sort(candidates,function(a,b)
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  if a.stack.name~=b.stack.name then return a.stack.name<b.stack.name end
  return a.stack.quality.name<b.stack.quality.name
end)
local picked_up,remaining_items={},{}; local stacks_out_of_reach=0
for _,e in ipairs(candidates) do
  local stack=e.stack; local name,quality,before=stack.name,stack.quality.name,stack.count
  if c.can_reach_entity(e) then
    for i=1,#inv do if not e.valid or not stack.valid_for_read then break end; inv[i].transfer_stack(stack) end
  else
    stacks_out_of_reach=stacks_out_of_reach+1
  end
  local remaining=0
  if e.valid and stack.valid_for_read then remaining=stack.count end
  add_item_count(picked_up,name,quality,before-remaining)
  add_item_count(remaining_items,name,quality,remaining)
  if e.valid and remaining==0 then e.destroy() end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,stacks_found=#candidates,stacks_out_of_reach=stacks_out_of_reach,items_picked_up=sorted_items(picked_up),items_remaining=sorted_items(remaining_items)}))`, nil
}

const inventoryItemHelpersLua = `
local function add_item_count(items,name,quality,count)
  if count<=0 then return end
  local key=name..string.char(31)..quality
  local item=items[key]
  if not item then item={name=name,quality=quality,count=0}; items[key]=item end
  item.count=item.count+count
end
local function sorted_items(items)
  local result={}
  for _,item in pairs(items) do result[#result+1]=item end
  table.sort(result,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
  return result
end
local function inventory_item_counts(inv)
  local counts={}
  for _,item in pairs(inv.get_contents()) do add_item_count(counts,item.name,item.quality or "normal",item.count) end
  return counts
end
local function inventory_item_difference(before,inv)
  local after=inventory_item_counts(inv); local difference={}
  for key,item in pairs(after) do
    local prior=before[key] and before[key].count or 0
    add_item_count(difference,item.name,item.quality,item.count-prior)
  end
  return sorted_items(difference)
end
`
