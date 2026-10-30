package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func stringOr(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

// createBlueprint exports a blueprint string from the entities and optional
// tiles inside an area using a temporary script-owned blueprint stack.
func createBlueprint(r *Resolver, input CreateBlueprintInput) (*BlueprintResult, error) {
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	bounds := areaBounds{
		LeftTopX:     input.Area.LeftTopX,
		LeftTopY:     input.Area.LeftTopY,
		RightBottomX: input.Area.RightBottomX,
		RightBottomY: input.Area.RightBottomY,
	}
	if err := bounds.validate(); err != nil {
		return nil, err
	}
	surface := stringOr(input.Surface, "nauvis")
	force := stringOr(input.Force, "player")
	lua := luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local f=game.forces[%s]; if not f then fail("force not found"); return end
local area=%s
local inv=game.create_inventory(1); local stack=inv[1]
if not stack.set_stack{name="blueprint",count=1} then inv.destroy(); fail("could not create blueprint stack"); return end
local included=stack.create_blueprint{surface=s,force=f,area=area,always_include_tiles=%t,include_entities=true,include_modules=%t,include_station_names=%t,include_trains=%t,include_fuel=%t}
local count=stack.get_blueprint_entity_count()
local ok,str=pcall(function() return stack.export_stack() end)
if not ok then inv.destroy(); fail("could not export blueprint: "..tostring(str)) end
inv.destroy()
rcon.print(helpers.table_to_json({surface=s.name,force=f.name,entity_count=count,blueprint=str}))`,
		luaString(surface), luaString(force), bounds.lua(),
		input.IncludeTiles, input.IncludeModules, input.IncludeStationNames, input.IncludeTrains, input.IncludeFuel)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[BlueprintResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("created blueprint with %d entities on %s", result.EntityCount, result.Surface))
	return result, nil
}

// placeBlueprint imports a blueprint string and builds its ghosts.
func placeBlueprint(r *Resolver, input PlaceBlueprintInput) (*BlueprintPlaceResult, error) {
	if input.Blueprint == "" {
		return nil, errors.New("blueprint must not be empty")
	}
	if len(input.Blueprint) > 2_000_000 {
		return nil, errors.New("blueprint exceeds 2000000 characters")
	}
	if math.IsNaN(input.X) || math.IsInf(input.X, 0) || math.IsNaN(input.Y) || math.IsInf(input.Y, 0) {
		return nil, errors.New("coordinates must be finite")
	}
	direction := 0
	if input.Direction != nil && *input.Direction != "" {
		var ok bool
		direction, ok = placeDirections[*input.Direction]
		if !ok {
			return nil, errors.New("direction must be north, east, south, or west")
		}
	}
	surface := stringOr(input.Surface, "nauvis")
	force := stringOr(input.Force, "player")
	lua := luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local f=game.forces[%s]; if not f then fail("force not found"); return end
local inv=game.create_inventory(1); local stack=inv[1]
if not stack.set_stack{name="blueprint",count=1} then inv.destroy(); fail("could not create blueprint stack"); return end
local rc=stack.import_stack(%s)
if rc~=0 then inv.destroy(); fail("invalid blueprint string: "..rc); return end
if not stack.is_blueprint_setup() then inv.destroy(); fail("blueprint is not set up"); return end
local ok,built=pcall(function() return stack.build_blueprint{surface=s,force=f,position={x=%s,y=%s},direction=%d,build_mode=defines.build_mode.normal,raise_built=true} end)
if not ok then inv.destroy(); fail("could not place blueprint: "..tostring(built)); return end
local entities={}
for _,e in ipairs(built) do entities[#entities+1]={name=e.name,type=e.type,position=e.position,unit_number=e.unit_number} end
inv.destroy()
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,ghosts=#built,entities=entities}))`,
		luaString(surface), luaString(force), luaString(input.Blueprint),
		strconv.FormatFloat(input.X, 'g', -1, 64), strconv.FormatFloat(input.Y, 'g', -1, 64), direction)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[BlueprintPlaceResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("placed %d blueprint ghosts on %s", result.Ghosts, result.Surface))
	return result, nil
}

// createBlueprintFromEntities builds a blueprint from explicit blueprint-relative
// entities and tiles without reading the world.
func createBlueprintFromEntities(r *Resolver, input CreateBlueprintFromEntitiesInput) (*BlueprintFromEntitiesResult, error) {
	if len(input.Entities) > 1000 {
		return nil, errors.New("at most 1000 entities are allowed")
	}
	if len(input.Tiles) > 2000 {
		return nil, errors.New("at most 2000 tiles are allowed")
	}
	if len(input.Entities)+len(input.Tiles) == 0 {
		return nil, errors.New("at least one entity or tile is required")
	}
	var entities, tiles strings.Builder
	for i, entity := range input.Entities {
		if entity == nil {
			return nil, fmt.Errorf("entity %d is missing", i+1)
		}
		if entity.Name == "" {
			return nil, fmt.Errorf("entity %d name must not be empty", i+1)
		}
		if math.IsNaN(entity.X) || math.IsInf(entity.X, 0) || math.IsNaN(entity.Y) || math.IsInf(entity.Y, 0) {
			return nil, fmt.Errorf("entity %d coordinates must be finite", i+1)
		}
		if i > 0 {
			entities.WriteString(",")
		}
		fmt.Fprintf(&entities, "{entity_number=%d,name=%s,position={x=%s,y=%s}",
			i+1, luaString(entity.Name),
			strconv.FormatFloat(entity.X, 'g', -1, 64), strconv.FormatFloat(entity.Y, 'g', -1, 64))
		if entity.Direction != nil && *entity.Direction != "" {
			direction, ok := placeDirections[*entity.Direction]
			if !ok {
				return nil, fmt.Errorf("entity %d direction must be north, east, south, or west", i+1)
			}
			fmt.Fprintf(&entities, ",direction=%d", direction)
		}
		if entity.Quality != nil && *entity.Quality != "" {
			fmt.Fprintf(&entities, ",quality=%s", luaString(*entity.Quality))
		}
		if entity.Recipe != nil && *entity.Recipe != "" {
			fmt.Fprintf(&entities, ",recipe=%s", luaString(*entity.Recipe))
			if entity.RecipeQuality != nil && *entity.RecipeQuality != "" {
				fmt.Fprintf(&entities, ",recipe_quality=%s", luaString(*entity.RecipeQuality))
			}
		}
		if entity.Mirror != nil {
			fmt.Fprintf(&entities, ",mirror=%t", *entity.Mirror)
		}
		entities.WriteString("}")
	}
	for i, tile := range input.Tiles {
		if tile == nil {
			return nil, fmt.Errorf("tile %d is missing", i+1)
		}
		if tile.Name == "" {
			return nil, fmt.Errorf("tile %d name must not be empty", i+1)
		}
		if i > 0 {
			tiles.WriteString(",")
		}
		fmt.Fprintf(&tiles, "{name=%s,position={x=%d,y=%d}}", luaString(tile.Name), tile.X, tile.Y)
	}
	lua := luaFail + fmt.Sprintf(`local entities={%s}
local tiles={%s}
local inv=game.create_inventory(1); local stack=inv[1]
if not stack.set_stack{name="blueprint",count=1} then inv.destroy(); fail("could not create blueprint stack"); return end
local ok,err=pcall(function()
  stack.set_blueprint_entities(entities)
  if #tiles>0 then stack.set_blueprint_tiles(tiles) end
end)
if not ok then inv.destroy(); fail("could not set blueprint: "..tostring(err)); return end
local count=stack.get_blueprint_entity_count()
local ok2,str=pcall(function() return stack.export_stack() end)
if not ok2 then inv.destroy(); fail("could not export blueprint: "..tostring(str)) end
inv.destroy()
rcon.print(helpers.table_to_json({entity_count=count,tile_count=#tiles,blueprint=str}))`,
		entities.String(), tiles.String())
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[BlueprintFromEntitiesResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("created blueprint with %d entities and %d tiles", result.EntityCount, result.TileCount))
	return result, nil
}
