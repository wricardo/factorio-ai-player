package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// This file implements buildCell: one declarative production cell built from a
// list of entries, with local refs linking later entries to earlier ones.
//
// The call has two phases and the split is deliberate.
//
//  1. Preflight is a single read-only Lua call. It resolves every place item,
//     every recipe and every place tile, checks the actor's main inventory
//     against the items the layout consumes, and reports one error per failing
//     entry. Nothing is written, so a refused layout leaves the world
//     untouched.
//
//  2. Application replays the entries in list order through the existing
//     domain helpers - placeMany for a placement, connectEntities for a run and
//     setRecipe for a recipe - so the cell behaves exactly like the individual
//     calls, including their per-tile error shapes.
//
// There is no transaction: entries applied before a failing entry stay in the
// world. Callers must re-read the world after a failure.
const maxCellEntries = 200

// buildCell validates, preflights and then applies a declarative cell layout.
func buildCell(r *Resolver, input BuildCellInput) (*BuildCellResult, error) {
	if input.Actor == nil {
		return nil, errors.New("actor is required: the cell is paid for from its main inventory")
	}
	surface := input.Surface
	if surface == "" {
		surface = "nauvis"
	}
	if surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	force := input.Force
	if force == "" {
		force = "player"
	}
	if force == "" {
		return nil, errors.New("force must not be empty")
	}
	if len(input.Entries) < 1 || len(input.Entries) > maxCellEntries {
		return nil, fmt.Errorf("entries must contain between 1 and %d items", maxCellEntries)
	}
	entries, err := validateCellEntries(input.Entries)
	if err != nil {
		return nil, err
	}
	result := &BuildCellResult{
		Surface: surface,
		Force:   force,
		DryRun:  input.DryRun,
		Steps:   make([]*BuildCellStep, len(entries)),
	}
	for i, entry := range entries {
		result.Steps[i] = &BuildCellStep{
			Index: i, Ref: entry.ref, Kind: entry.kind,
			Position: entry.position, Conflicts: []*ConnectionConflict{}, Errors: []*BatchError{},
		}
	}
	preflight, err := preflightCell(r, input.Actor, surface, force, entries)
	if err != nil {
		return nil, err
	}
	result.Tick = preflight.Tick
	if len(preflight.Errors) > 0 {
		applyCellErrors(result, preflight.Errors)
		result.Failed = len(preflight.Errors)
		result.Skipped = len(entries)
		return result, nil
	}
	if input.DryRun {
		for _, step := range result.Steps {
			step.Ok = true
		}
		result.Skipped = len(entries)
		return result, nil
	}
	refs := map[string]cellEntity{}
	for i, entry := range entries {
		step := result.Steps[i]
		if result.Failed > 0 {
			result.Skipped++
			continue
		}
		placed, entity, failures, conflicts, err := applyCellEntry(r, input.Actor, surface, force, entry, refs)
		if err != nil {
			// A backend error is not a per-entry verdict: the outcome is unknown,
			// so stop here and report the call as failed without claiming a rollback.
			result.Failed++
			step.Errors = append(step.Errors, &BatchError{Index: i, Code: BatchErrorCodeEngine, Message: err.Error()})
			result.Skipped += len(entries) - i
			break
		}
		step.Applied = true
		step.Placed = placed
		step.Failed = failures
		step.Conflicts = conflicts
		if entity != nil {
			step.UnitNumber = entity.UnitNumber
			step.Name = &entity.Name
			refs[entry.ref] = cellEntity{unitNumber: entity.UnitNumber, name: entity.Name}
		}
		if failures > 0 || len(conflicts) > 0 {
			step.Ok = false
			result.Failed++
			message := "step failed; remaining entries were not applied"
			step.Message = &message
			result.Skipped += len(entries) - i - 1
			continue
		}
		step.Ok = true
		result.Placed += placed
	}
	result.Completed = result.Failed == 0 && result.Skipped == 0
	if result.Placed > 0 {
		announceMutation(r, input.Message, fmt.Sprintf("cell built on %s: %d entities in %d steps, %d failed",
			surface, result.Placed, len(entries)-result.Skipped, result.Failed))
	}
	return result, nil
}

// cellEntry is one validated entry, reduced to the action it performs.
type cellEntry struct {
	ref        string
	kind       string
	place      *CellPlaceInput
	connection *CellConnectionInput
	recipe     *CellRecipeInput
	position   *Position
}

// cellEntity is a resolved local reference.
type cellEntity struct {
	unitNumber *string
	name       string
}

// cellPreflight is the read-only preflight verdict.
type cellPreflight struct {
	Tick   float64
	Errors []*BatchError
}

// validateCellEntries enforces the structural rules before any backend call:
// unique refs, exactly one action per entry, a resolvable recipe target and
// finite geometry.
func validateCellEntries(input []*BuildCellEntryInput) ([]cellEntry, error) {
	entries := make([]cellEntry, 0, len(input))
	refs := map[string]bool{}
	for i, raw := range input {
		if raw == nil {
			return nil, fmt.Errorf("entry %d is missing", i)
		}
		ref := strings.TrimSpace(raw.Ref)
		if ref == "" {
			return nil, fmt.Errorf("entry %d ref must not be empty", i)
		}
		if refs[ref] {
			return nil, fmt.Errorf("ref %q is used twice; refs must be unique within a call", ref)
		}
		actions := 0
		for _, set := range []bool{raw.Place != nil, raw.Connection != nil, raw.Recipe != nil} {
			if set {
				actions++
			}
		}
		if actions != 1 {
			return nil, fmt.Errorf("entry %d (%s) must set exactly one of place, connection or recipe", i, ref)
		}
		entry := cellEntry{ref: ref}
		switch {
		case raw.Place != nil:
			place := *raw.Place
			if strings.TrimSpace(place.Item) == "" {
				return nil, fmt.Errorf("entry %d (%s) place item must not be empty", i, ref)
			}
			if place.Position == nil {
				return nil, fmt.Errorf("entry %d (%s) place position is required", i, ref)
			}
			if err := validateCellPosition(place.Position); err != nil {
				return nil, fmt.Errorf("entry %d (%s): %w", i, ref, err)
			}
			if place.Direction < 0 || place.Direction > 15 {
				return nil, fmt.Errorf("entry %d (%s) direction must be between 0 and 15", i, ref)
			}
			entry.kind = "PLACE"
			entry.place = &place
			entry.position = &Position{X: place.Position.X, Y: place.Position.Y}
		case raw.Connection != nil:
			connection := *raw.Connection
			if err := validateCellConnection(connection); err != nil {
				return nil, fmt.Errorf("entry %d (%s): %w", i, ref, err)
			}
			entry.kind = "CONNECTION"
			entry.connection = &connection
			entry.position = &Position{X: connection.To.X, Y: connection.To.Y}
		default:
			recipe := *raw.Recipe
			if strings.TrimSpace(recipe.Recipe) == "" {
				return nil, fmt.Errorf("entry %d (%s) recipe must not be empty", i, ref)
			}
			if (recipe.Ref == nil) == (recipe.Target == nil) {
				return nil, fmt.Errorf("entry %d (%s) recipe needs exactly one of ref or target", i, ref)
			}
			if recipe.Ref != nil {
				target := strings.TrimSpace(*recipe.Ref)
				if target == "" {
					return nil, fmt.Errorf("entry %d (%s) recipe ref must not be empty", i, ref)
				}
				if !refs[target] {
					return nil, fmt.Errorf("entry %d (%s) recipe ref %q must name an earlier entry", i, ref, target)
				}
				recipe.Ref = &target
			}
			entry.kind = "RECIPE"
			entry.recipe = &recipe
		}
		refs[ref] = true
		entries = append(entries, entry)
	}
	return entries, nil
}

func validateCellPosition(p *PositionInput) error {
	if p == nil {
		return errors.New("position is required")
	}
	if isNonFinite(p.X) || isNonFinite(p.Y) {
		return errors.New("position must be finite")
	}
	return nil
}

func isNonFinite(v float64) bool {
	return v != v || v > 1e308 || v < -1e308
}

func validateCellConnection(in CellConnectionInput) error {
	if in.From == nil || in.To == nil {
		return errors.New("connection needs from and to")
	}
	if err := validateCellPosition(in.From); err != nil {
		return err
	}
	if err := validateCellPosition(in.To); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = ConnectionKindBelt
	}
	if in.Routing == "" {
		in.Routing = ConnectionRoutingDirect
	}
	switch in.Kind {
	case ConnectionKindBelt, ConnectionKindPipe, ConnectionKindPowerPoleChain:
	default:
		return fmt.Errorf("unsupported connection kind %q", in.Kind)
	}
	switch in.Routing {
	case ConnectionRoutingDirect, ConnectionRoutingAstar:
	default:
		return fmt.Errorf("unsupported routing %q", in.Routing)
	}
	return nil
}

// preflightCell runs the single read-only check for the whole layout.
func preflightCell(r *Resolver, actor *ActorInput, surface, force string, entries []cellEntry) (*cellPreflight, error) {
	character, player, err := settingsActorFrom(actor)
	if err != nil {
		return nil, err
	}
	var places, recipes strings.Builder
	placeItems := make([]string, 0, len(entries))
	for i, entry := range entries {
		switch entry.kind {
		case "PLACE":
			if len(placeItems) > 0 {
				places.WriteString(",")
			}
			quality := "normal"
			if entry.place.Quality != nil && *entry.place.Quality != "" {
				quality = *entry.place.Quality
			}
			fmt.Fprintf(&places, "{index=%d,item=%s,quality=%s,x=%s,y=%s,fast_replace=%t}",
				i, luaString(entry.place.Item), luaString(quality),
				strconv.FormatFloat(entry.place.Position.X, 'g', -1, 64),
				strconv.FormatFloat(entry.place.Position.Y, 'g', -1, 64),
				entry.place.FastReplace)
			placeItems = append(placeItems, entry.place.Item)
		case "RECIPE":
			if recipes.Len() > 0 {
				recipes.WriteString(",")
			}
			fmt.Fprintf(&recipes, "{index=%d,recipe=%s}", i, luaString(entry.recipe.Recipe))
		}
	}
	lua := luaFail + luaTileOccupants + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
if c.surface~=s then fail("actor is on another surface"); return end
local places={%s}
local recipes={%s}
local errors={}
local function fail_at(index,code,message)
  if #errors<20 then errors[#errors+1]={index=index,code=code,message=message} end
end
local needed={}
-- Footprints already claimed by earlier entries of this layout, so two entries
-- cannot overlap each other before anything is written.
local claimed={}
local function claimed_by(area)
  for _,entry in ipairs(claimed) do
    local other=entry.area
    if other[1][1]<area[2][1] and other[2][1]>area[1][1] and other[1][2]<area[2][2] and other[2][2]>area[1][2] then
      return entry.index
    end
  end
  return nil
end
for _,p in ipairs(places) do
  local proto=prototypes.item[p.item]
  if not proto or not proto.place_result then
    fail_at(p.index,"PROTOTYPE","item does not place an entity")
  else
    needed[p.item]=(needed[p.item] or 0)+1
    local layers=(prototypes.entity[proto.place_result.name] and prototypes.entity[proto.place_result.name].collision_mask or {}).layers
    if not p.fast_replace then
      local blockers=tile_occupants(s,{x=p.x,y=p.y},layers)
      if #blockers>0 then fail_at(p.index,"COLLISION","tile is occupied by "..table.concat(blockers,", ")) end
      -- The centre tile can be free while a multi-tile entry's footprint covers a
      -- neighbour. Refuse that here, before any entry is written, so a rejected
      -- cell still leaves the world unchanged.
      local footprint=footprint_occupants(s,proto.place_result.name,{x=p.x,y=p.y},layers)
      if #footprint>0 then fail_at(p.index,"COLLISION","footprint overlaps "..table.concat(footprint,", ")) end
      local entity_proto=prototypes.entity[proto.place_result.name]
      local cbox=entity_proto and entity_proto.collision_box
      if cbox then
        local area={{p.x+cbox.left_top.x,p.y+cbox.left_top.y},{p.x+cbox.right_bottom.x,p.y+cbox.right_bottom.y}}
        local clash=claimed_by(area)
        if clash then fail_at(p.index,"COLLISION","footprint overlaps entry "..clash) end
        claimed[#claimed+1]={index=p.index,area=area}
      end
    end
  end
end
for item,count in pairs(needed) do
  local have=inv.get_item_count{name=item,quality="normal"}
  if have<count then
    fail_at(0,"INVENTORY","actor has "..have.." "..item.." but the layout needs "..count)
  end
end
for _,r in ipairs(recipes) do
  if not prototypes.recipe[r.recipe] then fail_at(r.index,"RECIPE","recipe not found") end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=c.force.name,errors=errors}))`,
		luaString(player), character, luaString(surface), places.String(), recipes.String())
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[cellPreflight](response)
}

// applyCellErrors folds preflight errors onto the steps they belong to.
func applyCellErrors(result *BuildCellResult, failures []*BatchError) {
	for _, failure := range failures {
		index := failure.Index
		if index < 0 || index >= len(result.Steps) {
			index = 0
		}
		step := result.Steps[index]
		step.Ok = false
		step.Errors = append(step.Errors, failure)
		if step.Message == nil {
			message := failure.Message
			step.Message = &message
		}
	}
}

// applyCellEntry replays one entry through the existing domain helper for its
// action, so a cell step behaves exactly like the standalone mutation.
func applyCellEntry(r *Resolver, actor *ActorInput, surface, force string, entry cellEntry, refs map[string]cellEntity) (int, *PlacedEntity, int, []*ConnectionConflict, error) {
	switch entry.kind {
	case "PLACE":
		place := entry.place
		input := PlaceManyInput{
			Actor:   actor,
			Surface: &surface,
			Item:    place.Item,
			Quality: place.Quality,
			Placements: []*PlacementInput{{
				Position:    place.Position,
				Direction:   place.Direction,
				FastReplace: &place.FastReplace,
			}},
		}
		result, err := placeMany(r, input)
		if err != nil {
			return 0, nil, 0, nil, err
		}
		if len(result.Entities) > 0 {
			return result.Placed, result.Entities[0], result.Failed, nil, nil
		}
		return result.Placed, nil, result.Failed, nil, nil
	case "CONNECTION":
		connection := entry.connection
		kind := connection.Kind
		if kind == "" {
			kind = ConnectionKindBelt
		}
		routing := connection.Routing
		if routing == "" {
			routing = ConnectionRoutingDirect
		}
		input := ConnectEntitiesInput{
			Actor: actor, Surface: surface, Force: force,
			From: connection.From, To: connection.To, Kind: kind, Item: connection.Item,
			Direction: connection.Direction, Bend: connection.Bend, Routing: routing,
			AllowUnderground: connection.AllowUnderground, UndergroundItem: connection.UndergroundItem,
			MaxUndergroundDistance: connection.MaxUndergroundDistance, AvoidAreas: connection.AvoidAreas,
			PreferExisting: connection.PreferExisting, AllowBeltAdjacency: connection.AllowBeltAdjacency,
			AllowFluidAdjacency: connection.AllowFluidAdjacency,
		}
		result, err := connectEntities(r, input)
		if err != nil {
			return 0, nil, 0, nil, err
		}
		var last *PlacedEntity
		if len(result.Entities) > 0 {
			last = result.Entities[len(result.Entities)-1]
		}
		return result.Placed, last, result.Failed, result.Conflicts, nil
	default:
		recipe := entry.recipe
		target := recipe.Target
		if recipe.Ref != nil {
			entity, ok := refs[*recipe.Ref]
			if !ok {
				return 0, nil, 0, nil, fmt.Errorf("recipe ref %q has no placed entity", *recipe.Ref)
			}
			if entity.unitNumber != nil {
				name := entity.name
				target = &EntityRefInput{Entity: &name, UnitNumber: entity.unitNumber}
			}
			if target == nil {
				return 0, nil, 0, nil, fmt.Errorf("recipe ref %q did not resolve to a unit number", *recipe.Ref)
			}
		}
		drain := recipe.Drain
		input := SetRecipeInput{Actor: actor, Target: target, Recipe: recipe.Recipe, Drain: &drain}
		if _, err := setRecipe(r, input); err != nil {
			return 0, nil, 0, nil, err
		}
		return 0, nil, 0, nil, nil
	}
}
