package main

import (
	"errors"
	"fmt"
	"strconv"
)

// placeTile replaces one tile using a tile item after checking the item's
// prototype placement condition. Base tile items have condition size 1.
func placeTile(r *Resolver, input PlaceTileInput) (*PlaceTileResult, error) {
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	surface := "nauvis"
	if input.Surface != nil {
		if *input.Surface == "" {
			return nil, errors.New("surface must not be empty")
		}
		surface = *input.Surface
	}
	if input.Item == "" {
		return nil, errors.New("item must not be empty")
	}
	quality, err := resolveQuality(input.Quality)
	if err != nil {
		return nil, err
	}
	if input.Position == nil {
		return nil, errors.New("position is required")
	}
	lua := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local position={x=%s,y=%s}; local item=%s; local quality=%s; if not prototypes.quality[quality] then fail("quality not found"); return end
local tile_item=prototypes.item[item]; if not tile_item then fail("item prototype not found"); return end
local placement=tile_item.place_as_tile_result; if not placement then fail("item does not place a tile"); return end
local result_tile=placement.result; if not result_tile then fail("tile result not found"); return end
local size=placement.condition_size or 1; if size~=1 then fail("tile condition size is not supported"); return end
local actor; local c
if unit~=0 then
  actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else
  actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end
end
if c.surface~=s then fail("actor is on another surface"); return end
local dx=c.position.x-position.x; local dy=c.position.y-position.y
if %s then fail("position is out of build reach"); return end
local tile=s.get_tile(position); if not tile then fail("tile is not available at position"); return end
if tile.name==result_tile.name then fail("tile already matches at position; no item consumed"); return end
local previous_tile=tile.name
local previous_mineable=tile.prototype.mineable_properties
local allowed
local conditions=placement.tile_condition
if conditions and #conditions>0 then
  allowed=false
  for _,condition in ipairs(conditions) do if condition.name==tile.name then allowed=true; break end end
else
  local hit=false
  local connector=placement.condition
  if connector and connector.layers then
    for layer,enabled in pairs(connector.layers) do if enabled and tile.collides_with(layer) then hit=true; break end end
  end
  allowed=(hit==(placement.invert or false))
end
if not allowed then fail("tile cannot be placed on current tile"); return end
local tile_layers=(result_tile.collision_mask and result_tile.collision_mask.layers) or {}
local blocker
for _,e in pairs(s.find_entities_filtered{area={{x=position.x-0.5,y=position.y-0.5},{x=position.x+0.5,y=position.y+0.5}}}) do
  if e.valid and e.type~="character" and e.type~="resource" and e.type~="entity-ghost" and e.type~="tile-ghost" then
    local mask=e.prototype.collision_mask
    for layer,enabled in pairs((mask and mask.layers) or {}) do
      if enabled and tile_layers[layer] then blocker=e.name; break end
    end
    if blocker then break end
  end
end
if blocker then fail("tile position is blocked by "..blocker); return end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
if inv.get_item_count({name=item,quality=quality})<1 then fail("actor lacks item"); return end
if inv.remove{name=item,quality=quality,count=1}~=1 then fail("could not remove item"); return end
s.set_tiles({{name=result_tile.name,position=position}})
local placed=s.get_tile(position); if not placed or placed.name~=result_tile.name then inv.insert{name=item,quality=quality,count=1}; fail("tile placement failed; item returned"); return end
-- Manual play returns the mined product of the replaced tile; the engine's
-- set_tiles destroys it, so mine it into the actor inventory explicitly.
local items={}
if previous_mineable and previous_mineable.minable then
  for _,product in pairs(previous_mineable.products or {}) do
    if product.type=="item" then
      local amount=math.floor(product.amount or 0)
      if amount>0 then
        local inserted=inv.insert{name=product.name,quality="normal",count=amount}
        if inserted>0 then items[#items+1]={name=product.name,quality="normal",count=inserted} end
      end
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=c.force.name,tile=placed.name,previous_tile=previous_tile,position=position,items=items}))`,
		luaString(player), character, luaString(surface),
		strconv.FormatFloat(input.Position.X, 'g', -1, 64), strconv.FormatFloat(input.Position.Y, 'g', -1, 64), luaString(input.Item), luaString(quality),
		distanceViolation("dx", "dy", "c.build_distance"))
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[PlaceTileResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, "placed tile "+result.Tile+positionText(result.Position))
	return result, nil
}
