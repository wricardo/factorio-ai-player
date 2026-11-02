package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

// luaUndergroundInfoLua reports an underground belt half's ground type and the
// unit number of the half it is paired with. On 2.0.77 those reads cannot be
// assumed: LuaEntity.neighbours for a fresh half is not always a plain table
// usable with pairs (it can be an array-like LuaCustomTable or userdata), and
// LuaEntity values are userdata rather than tables. Every access is therefore
// probed, and the neighbour list is walked by length first, then by pairs.
const luaUndergroundInfoLua = `local function underground_neighbour_unit(nb)
  local function unit_of(n)
    local ok_unit,unit=pcall(function() if n and n.valid then return n.unit_number end end)
    if ok_unit then return unit end
    return nil
  end
  local function at(list,index)
    local ok_item,item=pcall(function() return list[index] end)
    if ok_item then return item end
    return nil
  end
  local paired=nil
  local ok_len,len=pcall(function() return #nb end)
  if ok_len and len and len>0 then
    for i=1,len do
      local unit=unit_of(at(nb,i))
      if unit then paired=unit; break end
    end
    if paired~=nil then return paired end
  end
  pcall(function()
    for _,n in pairs(nb) do
      local unit=unit_of(n)
      if unit then paired=unit; return end
    end
  end)
  return paired
end
local function underground_info(e)
  if not e or e.type~="underground-belt" then return nil,nil end
  local underground_type=nil
  local ok_type,ground_type=pcall(function() return e.belt_to_ground_type end)
  if ok_type and ground_type then underground_type=string.upper(tostring(ground_type)) end
  local ok_neighbours,nb=pcall(function() return e.neighbours end)
  if not ok_neighbours or nb==nil then return underground_type,nil end
  return underground_type,underground_neighbour_unit(nb)
end
`

// luaTileOccupants finds the entities whose collision mask can obstruct a new
// build on the tile under a position. create_entity ignores collision boxes, so
// a plain place must refuse an occupied tile explicitly: without that check the
// engine stacks a second entity on the same tile (a burner inserter buried
// under a new electric inserter that can never be fuelled again).
//
// The rule is the engine's own: a candidate blocks only when its collision mask
// intersects the mask of the prototype being placed. Corpses (tree stumps,
// remnants, biter corpses) use the doodad layer with colliding_with_tiles_only,
// so they never collide with a belt or a pipe and must not be reported as
// occupants (factorio-ai-player-sth). Ghosts, tile ghosts, item stacks, item
// request proxies and resources are not obstacles either.
//
// Occupancy is tested against the whole tile under the position, not the point:
// find_entities_filtered{position=} only returns entities whose own collision
// box contains the point, so an entity whose box clips the tile from a
// neighbouring tile is invisible to it but still blocks the footprint.
const luaTileOccupants = `local function tile_entities(surface,position)
  local found={}
  for _,candidate in pairs(surface.find_entities_filtered{position=position,limit=16}) do
    if candidate.valid then found[#found+1]=candidate end
  end
  return found
end
local function tile_area_entities(surface,position)
  local left=math.floor(position.x); local top=math.floor(position.y)
  local found={}
  for _,candidate in pairs(surface.find_entities_filtered{area={{left-0.5,top-0.5},{left+1.5,top+1.5}},limit=64}) do
    if candidate.valid then found[#found+1]=candidate end
  end
  return found
end
-- A candidate's collision mask decides whether it obstructs the prototype whose
-- mask is layers. An unknown prototype stays an occupant so a broken mask read
-- can never stack entities; no layers at all means the caller has no prototype,
-- and then the type rule below is the best available answer.
local function tile_blocks_build(candidate,layers)
  if not layers then return true end
  local ok_mask,mask=pcall(function() return candidate.prototype.collision_mask end)
  if not ok_mask or not mask or not mask.layers then return true end
  for layer in pairs(layers) do
    if mask.layers[layer] then return true end
  end
  return false
end
local function tile_occupants(surface,position,layers)
  local occupants={}
  local left=math.floor(position.x); local top=math.floor(position.y)
  local right,bottom=left+1,top+1
  for _,candidate in ipairs(tile_area_entities(surface,position)) do
    local kind=candidate.type
    if kind~="entity-ghost" and kind~="tile-ghost" and kind~="item-entity" and kind~="item-request-proxy" and kind~="resource" then
      local box=candidate.bounding_box
      if box.left_top.x<right and box.right_bottom.x>left and box.left_top.y<bottom and box.right_bottom.y>top then
        if tile_blocks_build(candidate,layers) then occupants[#occupants+1]=candidate.name end
      end
    end
  end
  return occupants
end
local function tile_unit_set(surface,position)
  local units={}
  for _,candidate in ipairs(tile_entities(surface,position)) do
    local unit=candidate.unit_number
    if unit then units[unit]=true end
  end
  return units
end
local function new_tile_entity(surface,position,before)
  for _,candidate in ipairs(tile_entities(surface,position)) do
    local unit=candidate.unit_number
    if unit and not before[unit] then return candidate end
  end
  return nil
end
`

const luaActionActor = `local unit=%d; local p; if unit~=0 then p=game.get_entity_by_unit_number(unit); if not p or p.type~="character" then fail("character not found"); return end else p=game.get_player(%s); if not p then fail("player not found"); return end end; `

// actionLua runs checks and inventory changes in the same game tick.
// A failed mutation or rollback can still leave partial changes.
func actionLua(command string, args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player name (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	surface := fs.String("surface", "nauvis", "Surface name")
	x := fs.Float64("x", math.NaN(), "Entity center X (required)")
	y := fs.Float64("y", math.NaN(), "Entity center Y (required)")
	item := fs.String("item", "", "Normal-quality inventory item (required)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Target entity unit number, alternative to entity and x/y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Target entity unit number, alternative to entity and x/y")
	quality := fs.String("quality", "normal", "Item quality prototype name (default normal)")
	var direction *int
	var fastReplace *bool
	var entity *string
	var count *int
	var undergroundType *string
	if command == "place" {
		direction = fs.Int("direction", 0, "Engine direction 0 through 15")
		fastReplace = fs.Bool("fastReplace", false, "Fast-replace a fast-replaceable entity, returning it and its contents to the actor")
		undergroundType = fs.String("undergroundType", "", "Underground belt half: input or output")
	} else {
		entity = fs.String("entity", "", "Target entity name (required unless a unit number is given)")
		count = fs.Int("count", 1, "Number of fuel items to transfer")
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || *surface == "" || *item == "" {
		return "", errors.New("exactly one of player or character, surface, and item are required")
	}
	if *quality == "" {
		return "", errors.New("quality must not be empty")
	}
	if direction != nil && (*direction < 0 || *direction > 15) {
		return "", errors.New("direction must be between 0 and 15")
	}
	forcedType := ""
	if undergroundType != nil {
		forcedType = *undergroundType
	}
	if forcedType != "" && forcedType != "input" && forcedType != "output" {
		return "", errors.New("undergroundType must be input or output")
	}
	if command == "place" && (math.IsNaN(*x) || math.IsInf(*x, 0) || math.IsNaN(*y) || math.IsInf(*y, 0)) {
		return "", errors.New("place requires finite x and y")
	}
	actor := fmt.Sprintf(luaFail+luaActionActor+`local inv=p.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end; local item=%s; local quality=%s; if not prototypes.quality[quality] then fail("quality not found"); return end; `,
		*character, luaString(*player), luaString(*item), luaString(*quality))
	if command == "place" {
		prefix := actor + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end; local position={x=%s,y=%s}; local c; if unit~=0 then c=p else c=p.character; if not c then fail("player has no character"); return end end; if c.surface~=s then fail("actor is on another surface"); return end; local dx=c.position.x-position.x; local dy=c.position.y-position.y; if %s then fail("position is out of build reach"); return end; `,
			luaString(*surface), luaCoordinate(*x), luaCoordinate(*y), distanceViolation("dx", "dy", "c.build_distance"))
		dir := *direction
		classify := fmt.Sprintf(`if s.can_place_entity{name=name,position=position,direction=%d,force=p.force,build_check_type=defines.build_check_type.manual} then fail("entity creation failed; item returned"); return end; fail("cannot place entity at position"); return;`, dir)
		// create_entity ignores collision boxes, so a fast replace must first be
		// proven replaceable; otherwise the new entity stacks on whatever is
		// already there. Passing the acting character (and the player when one is
		// selected) makes the engine return the replaced entity and its contents
		// to that actor instead of spilling them on the ground.
		replace := ""
		if *fastReplace {
			replace = fmt.Sprintf(`
params.fast_replace=true; params.spill=true; params.character=c
if unit==0 then params.player=p end
if not (s.can_fast_replace{name=name,position=position,direction=%d,force=p.force} or s.can_place_entity{name=name,position=position,direction=%d,force=p.force,build_check_type=defines.build_check_type.manual}) then fail("cannot fast-replace at position: nothing fast-replaceable there and the tile is blocked"); return end`, dir, dir)
		}
		// A plain place must not stack a second entity on an occupied tile:
		// create_entity ignores collision boxes. Refuse the spot explicitly so
		// the caller gets a collision error instead of a silently buried entity.
		occupied := ""
		if !*fastReplace {
			occupied = `local layers=(prototypes.entity[name] and prototypes.entity[name].collision_mask or {}).layers; local blockers=tile_occupants(s,position,layers); if #blockers>0 then fail("cannot place entity at position: tile is occupied by "..table.concat(blockers,", ")); return end`
		}
		helpers := luaTileOccupants + luaUndergroundInfoLua
		return helpers + prefix + fmt.Sprintf(`local proto=prototypes.item[item]; if not proto or not proto.place_result then fail("item does not place an entity"); return end; local name=proto.place_result.name; local forced_type=%s
%s
local params={name=name,position=position,direction=%d,quality=quality,force=p.force,raise_built=true}
if forced_type~="" then params.type=forced_type end%s
local stack={name=item,quality=quality}
if inv.get_item_count(stack)<1 then fail("player lacks item"); return end
if inv.remove{name=item,quality=quality,count=1}~=1 then fail("could not remove item"); return end
local before=tile_unit_set(s,position)
local ok,err=pcall(function() return s.create_entity(params) end)
local e=nil; if ok then e=err end
-- The engine can raise or return nil after committing the entity; refunding
-- then would duplicate the item and report a failure that did not happen.
if not e then e=new_tile_entity(s,position,before) end
if not e then inv.insert{name=item,quality=quality,count=1}; if not ok then fail("entity creation failed: "..tostring(err)); return end; %s end
local underground_type,paired_unit_number=underground_info(e); rcon.print(helpers.table_to_json({tick=game.tick,name=e.name,position=e.position,surface=s.name,force=c.force.name,direction=e.direction,unit_number=e.unit_number,item=item,underground_type=underground_type,paired_unit_number=paired_unit_number}))`, luaString(forcedType), occupied, dir, replace, classify), nil
	}
	if *count < 1 {
		return "", errors.New("positive count is required")
	}
	targetName := ""
	if entity != nil {
		targetName = *entity
	}
	targetUnit, err := entityTarget(targetName, unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	prefix := actor + targetLocals(targetUnit, targetName, *x, *y, *surface) + luaEntityResolve + resolveTargetLua() + `local c; if unit~=0 then c=p else c=p.character; if not c then fail("player has no character"); return end end
if c.surface~=s then fail("actor is on another surface"); return end
local dx=c.position.x-e.position.x; local dy=c.position.y-e.position.y; if ` + distanceViolation("dx", "dy", "c.build_distance") + ` then fail("position is out of build reach"); return end
local fuel=e.get_fuel_inventory(); if not fuel then fail("entity has no fuel inventory"); return end
`
	return prefix + fmt.Sprintf(`local stack={name=item,quality=quality}; if inv.get_item_count(stack)<%d then fail("player lacks requested fuel"); return end; if fuel.get_insertable_count(stack)<%d then fail("fuel inventory cannot accept requested items"); return end; local removed=inv.remove{name=item,quality=quality,count=%d}; if removed~=%d then if removed>0 then inv.insert{name=item,quality=quality,count=removed} end; fail("could not remove requested fuel"); return end; local inserted=fuel.insert{name=item,quality=quality,count=removed}; if inserted<removed then inv.insert{name=item,quality=quality,count=removed-inserted} end; if inserted==0 then fail("fuel inventory rejected item; fuel returned"); return end; rcon.print(helpers.table_to_json({name=e.name,position=e.position,surface=s.name,item=item,inserted=inserted,requested=removed}))`, *count, *count, *count, *count), nil
}

func craftLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("craft", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player name (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	recipe := fs.String("recipe", "", "Recipe name (required)")
	count := fs.Uint64("count", 1, "Number of recipe crafts to queue")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || *recipe == "" || *count == 0 || *count > math.MaxUint32 {
		return "", errors.New("exactly one of player or character, recipe, and count between 1 and 4294967295 are required")
	}
	return fmt.Sprintf(luaFail+luaActionActor+`local name=%s; local recipe=p.force.recipes[name]; if not recipe then fail("recipe not found"); return end; if not recipe.enabled then fail("recipe is not enabled"); return end; local requested=%d; local started=p.begin_crafting{recipe=name,count=requested,silent=true}; if started==0 then fail("could not queue recipe; check ingredients and actor crafting capability"); return end; local result={recipe=name,requested=requested,started=started,queue_size=p.crafting_queue_size}; if unit~=0 then result.character=unit else result.player=p.name end; rcon.print(helpers.table_to_json(result))`, *character, luaString(*player), luaString(*recipe), *count), nil
}
