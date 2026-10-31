package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

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
	var direction *string
	var directionCode *int
	var fastReplace *bool
	var entity *string
	var count *int
	if command == "place" {
		direction = fs.String("direction", "north", "Entity direction: north, east, south, west")
		directionCode = fs.Int("directionCode", -1, "Engine direction 0 through 15; overrides direction")
		fastReplace = fs.Bool("fastReplace", false, "Fast-replace a colliding entity, spilling its contents")
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
	if directionCode != nil && *directionCode != -1 && (*directionCode < 0 || *directionCode > 15) {
		return "", errors.New("directionCode must be between 0 and 15")
	}
	if command == "place" && (math.IsNaN(*x) || math.IsInf(*x, 0) || math.IsNaN(*y) || math.IsInf(*y, 0)) {
		return "", errors.New("place requires finite x and y")
	}
	actor := fmt.Sprintf(luaFail+luaActionActor+`local inv=p.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end; local item=%s; local quality=%s; if not prototypes.quality[quality] then fail("quality not found"); return end; `,
		*character, luaString(*player), luaString(*item), luaString(*quality))
	if command == "place" {
		prefix := actor + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end; local position={x=%s,y=%s}; local c; if unit~=0 then c=p else c=p.character; if not c then fail("player has no character"); return end end; if c.surface~=s then fail("actor is on another surface"); return end; local dx=c.position.x-position.x; local dy=c.position.y-position.y; if dx*dx+dy*dy>c.build_distance*c.build_distance then fail("position is out of build reach"); return end; `,
			luaString(*surface), luaCoordinate(*x), luaCoordinate(*y))
		directions := map[string]int{"north": 0, "east": 4, "south": 8, "west": 12}
		dir := 0
		if *directionCode >= 0 {
			dir = *directionCode
		} else {
			mapped, ok := directions[*direction]
			if !ok {
				return "", errors.New("direction must be north, east, south, or west")
			}
			dir = mapped
		}
		check := fmt.Sprintf(`if not s.can_place_entity{name=name,position=position,direction=%d,force=p.force} then fail("cannot place entity at position"); return end; `, dir)
		replace := ""
		if *fastReplace {
			check = ""
			replace = ",fast_replace=true,spill=true"
		}
		return prefix + fmt.Sprintf(`local proto=prototypes.item[item]; if not proto or not proto.place_result then fail("item does not place an entity"); return end; local name=proto.place_result.name; local stack={name=item,quality=quality}; if inv.get_item_count(stack)<1 then fail("player lacks item"); return end; %sif inv.remove{name=item,quality=quality,count=1}~=1 then fail("could not remove item"); return end; local ok,e=pcall(function() return s.create_entity{name=name,position=position,direction=%d,quality=quality,force=p.force,raise_built=true%s} end); if not ok then inv.insert{name=item,quality=quality,count=1}; fail("entity creation failed: "..tostring(e)); return end; if not e then inv.insert{name=item,quality=quality,count=1}; fail("entity creation failed; item returned"); return end; rcon.print(helpers.table_to_json({name=e.name,position=e.position,surface=s.name,direction=e.direction,unit_number=e.unit_number,item=item}))`, check, dir, replace), nil
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
local dx=c.position.x-e.position.x; local dy=c.position.y-e.position.y; if dx*dx+dy*dy>c.build_distance*c.build_distance then fail("position is out of build reach"); return end
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
