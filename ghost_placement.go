package main

import (
	"errors"
	"fmt"
	"strings"
)

// This file plans builds: it creates entity and tile ghosts at explicit map
// positions so an agent can lay out a design without round-tripping a
// blueprint string. Nothing is built: construction robots or buildGhosts
// must still realise the plan.

// ghostMaxEntities and ghostMaxTiles bound one placeGhosts call. Blueprint
// creation uses the same ceilings, so a plan can be exported afterwards.
const (
	ghostMaxEntities = maxBlueprintEntities
	ghostMaxTiles    = maxBlueprintTiles
)

// placeGhosts creates the requested ghosts, continuing past per-entry
// failures.
func placeGhosts(r *Resolver, input PlaceGhostsInput) (*GhostPlacementResult, error) {
	if len(input.Entities) > ghostMaxEntities {
		return nil, fmt.Errorf("at most %d entities are allowed", ghostMaxEntities)
	}
	if len(input.Tiles) > ghostMaxTiles {
		return nil, fmt.Errorf("at most %d tiles are allowed", ghostMaxTiles)
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
		if entity.Position == nil {
			return nil, fmt.Errorf("entity %d position is required", i+1)
		}
		if entity.Direction < 0 || entity.Direction > 15 {
			return nil, fmt.Errorf("entity %d direction must be between 0 and 15", i+1)
		}
		quality := stringValueOr(entity.Quality, "")
		recipe := stringValueOr(entity.Recipe, "")
		recipeQuality := stringValueOr(entity.RecipeQuality, "")
		if recipeQuality != "" && recipe == "" {
			return nil, fmt.Errorf("entity %d recipeQuality requires recipe", i+1)
		}
		if i > 0 {
			entities.WriteString(",")
		}
		fmt.Fprintf(&entities, "{name=%s,x=%s,y=%s,direction=%d,quality=%s,recipe=%s,recipe_quality=%s}",
			luaString(entity.Name),
			luaCoordinate(entity.Position.X), luaCoordinate(entity.Position.Y), entity.Direction,
			luaOptionalString(quality), luaOptionalString(recipe), luaOptionalString(recipeQuality))
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
		fmt.Fprintf(&tiles, "{name=%s,x=%d,y=%d}", luaString(tile.Name), tile.X, tile.Y)
	}
	surface := stringOr(input.Surface, "nauvis")
	force := stringOr(input.Force, "player")
	lua := luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local f=game.forces[%s]; if not f then fail("force not found"); return end
local entities={%s}
local tiles={%s}
local entities_created,tiles_created,failed=0,0,0
local ghosts={}; local errors={}
local function add_error(index,code,message,name,x,y)
  failed=failed+1
  if #errors<20 then
    errors[#errors+1]={index=index,code=code,message=message,target={surface=s.name,entity=name,position={x=x,y=y}}}
  end
end
local function record(ghost,inner_name,quality)
  ghosts[#ghosts+1]={name=ghost.name,type=ghost.type,inner_name=inner_name,
    quality=(ghost.valid and ghost.quality and ghost.quality.name) or quality,
    position={x=ghost.position.x,y=ghost.position.y},unit_number=ghost.unit_number}
end
for i,e in ipairs(entities) do
  if not prototypes.entity[e.name] then
    add_error(i,"LOOKUP","entity prototype not found",e.name,e.x,e.y)
  elseif e.quality and not prototypes.quality[e.quality] then
    add_error(i,"LOOKUP","quality prototype not found",e.name,e.x,e.y)
  elseif e.recipe and not prototypes.recipe[e.recipe] then
    add_error(i,"LOOKUP","recipe prototype not found",e.name,e.x,e.y)
  elseif e.recipe_quality and not prototypes.quality[e.recipe_quality] then
    add_error(i,"LOOKUP","recipe quality prototype not found",e.name,e.x,e.y)
  else
    local params={name="entity-ghost",inner_name=e.name,position={x=e.x,y=e.y},force=f,direction=e.direction}
    if e.quality then params.quality=e.quality end
    local ok,ghost=pcall(function() return s.create_entity(params) end)
    if not ok then
      add_error(i,"ENGINE",tostring(ghost),e.name,e.x,e.y)
    elseif not ghost or not ghost.valid then
      add_error(i,"ENGINE","engine did not create the ghost",e.name,e.x,e.y)
    elseif e.recipe then
      local okr,err=pcall(function() return ghost.set_recipe(e.recipe,e.recipe_quality) end)
      if not okr then
        ghost.destroy()
        add_error(i,"ENGINE","could not set recipe: "..tostring(err),e.name,e.x,e.y)
      else
        entities_created=entities_created+1
        record(ghost,e.name,e.quality)
      end
    else
      entities_created=entities_created+1
      record(ghost,e.name,e.quality)
    end
  end
end
for i,t in ipairs(tiles) do
  local index=#entities+i
  if not prototypes.tile[t.name] then
    add_error(index,"LOOKUP","tile prototype not found",t.name,t.x,t.y)
  else
    local ok,ghost=pcall(function() return s.create_entity{name="tile-ghost",inner_name=t.name,position={x=t.x,y=t.y},force=f} end)
    if not ok then
      add_error(index,"ENGINE",tostring(ghost),t.name,t.x,t.y)
    elseif not ghost or not ghost.valid then
      add_error(index,"ENGINE","engine did not create the tile ghost",t.name,t.x,t.y)
    else
      tiles_created=tiles_created+1
      record(ghost,t.name,nil)
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,
  entities_created=entities_created,tiles_created=tiles_created,failed=failed,
  errors=errors,ghosts=ghosts}))`,
		luaString(surface), luaString(force), entities.String(), tiles.String())
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[GhostPlacementResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, ghostPlacementSummary(result))
	return result, nil
}

func ghostPlacementSummary(result *GhostPlacementResult) string {
	total := result.EntitiesCreated + result.TilesCreated
	return fmt.Sprintf("placed %d ghosts on %s (%d failed)", total, result.Surface, result.Failed)
}

// luaOptionalString renders a Go string as a Lua string literal, or nil so
// optional fields drop out of the table entirely.
func luaOptionalString(value string) string {
	if value == "" {
		return "nil"
	}
	return luaString(value)
}
