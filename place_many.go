package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var placeDirections = map[string]int{"north": 0, "east": 4, "south": 8, "west": 12}

// placeMany places one normal-quality item at each requested position. Per
// position failures are counted and reported instead of aborting the batch.
func placeMany(r *Resolver, input PlaceManyInput) (*PlaceManyResult, error) {
	player, character := "", uint64(0)
	if input.Player != nil {
		player = *input.Player
	}
	if input.Character != nil {
		parsed, err := strconv.ParseUint(*input.Character, 10, 32)
		if err != nil || parsed == 0 {
			return nil, errors.New("character must be a unit number from 1 to 4294967295")
		}
		character = parsed
	}
	if (player == "") == (character == 0) {
		return nil, errors.New("exactly one of player or character is required")
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
	if len(input.Placements) < 1 || len(input.Placements) > 1000 {
		return nil, errors.New("placements must contain between 1 and 1000 entries")
	}
	var placements strings.Builder
	for i, placement := range input.Placements {
		if placement == nil {
			return nil, fmt.Errorf("placement %d is missing", i+1)
		}
		if math.IsNaN(placement.X) || math.IsInf(placement.X, 0) || math.IsNaN(placement.Y) || math.IsInf(placement.Y, 0) {
			return nil, fmt.Errorf("placement %d coordinates must be finite", i+1)
		}
		direction := 0
		if placement.Direction != nil && *placement.Direction != "" {
			var ok bool
			direction, ok = placeDirections[*placement.Direction]
			if !ok {
				return nil, fmt.Errorf("placement %d direction must be north, east, south, or west", i+1)
			}
		}
		if i > 0 {
			placements.WriteString(",")
		}
		fmt.Fprintf(&placements, "{x=%s,y=%s,direction=%d}",
			strconv.FormatFloat(placement.X, 'g', -1, 64),
			strconv.FormatFloat(placement.Y, 'g', -1, 64), direction)
	}
	lua := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
if c.surface~=s then fail("actor is on another surface"); return end
local item=%s; local proto=prototypes.item[item]; if not proto or not proto.place_result then fail("item does not place an entity"); return end; local name=proto.place_result.name
local placements={%s}
local placed,failed=0,0; local errors={}
local function fail_at(i,reason) failed=failed+1; if #errors<20 then errors[#errors+1]="placement "..i..": "..reason end end
for i,p in ipairs(placements) do
  local ok=true
  local dx=c.position.x-p.x; local dy=c.position.y-p.y
  if dx*dx+dy*dy>c.build_distance*c.build_distance then ok=false; fail_at(i,"out of build reach") end
  if ok and inv.get_item_count({name=item,quality="normal"})<1 then ok=false; fail_at(i,"actor lacks item") end
  if ok and not s.can_place_entity{name=name,position={x=p.x,y=p.y},direction=p.direction,force=c.force} then ok=false; fail_at(i,"cannot place entity") end
  if ok and inv.remove{name=item,quality="normal",count=1}~=1 then ok=false; fail_at(i,"could not remove item") end
  if ok then
    local e=s.create_entity{name=name,position={x=p.x,y=p.y},direction=p.direction,force=c.force,raise_built=true}
    if e then placed=placed+1 else inv.insert{name=item,quality="normal",count=1}; fail_at(i,"entity creation failed") end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,item=item,placed=placed,failed=failed,errors=errors}))`,
		luaString(player), character, luaString(surface), luaString(input.Item), placements.String())
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[PlaceManyResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, placeManySummary(result))
	return result, nil
}

func placeManySummary(r *PlaceManyResult) string {
	return fmt.Sprintf("placed %d of %d %s on %s", r.Placed, r.Placed+r.Failed, r.Item, r.Surface)
}
