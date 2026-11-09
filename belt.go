package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"
)

// maxBeltPurgeTiles bounds how many belt tiles one beltPurge call may change.
// The sweep is a single snapshot, so the cap keeps that snapshot affordable.
const maxBeltPurgeTiles = 1000

// beltLaneSelectionLua maps a requested lane side to the engine transport-line
// indexes of one belt. It must follow transportLineLua. An empty request keeps
// the historical both-lane sweep over indexes 1 and 2, so a caller that omits
// lanes behaves exactly like the pre-lane implementation. A requested side the
// belt does not expose is returned as an error message instead of aborting.
const beltLaneSelectionLua = `local function belt_lane_indexes(e,requested)
  if not requested or #requested==0 then return {1,2} end
  local by_side={}
  for i=1,e.get_max_transport_line_index() do
    local side=transport_line_side(transport_line_name(i))
    if not by_side[side] then by_side[side]={} end
    by_side[side][#by_side[side]+1]=i
  end
  local wanted={}
  for _,lane in ipairs(requested) do wanted[lane]=true end
  local indexes={}
  for _,side in ipairs({"left","right"}) do
    if wanted[side] then
      local found=by_side[side]
      if not found then return nil,"belt has no "..side.." lane" end
      for _,i in ipairs(found) do indexes[#indexes+1]=i end
    end
  end
  return indexes
end
`

// parseBeltLanes reads the comma-separated --lanes flag. An empty value means
// both lanes; duplicate entries are ignored, because the same lane twice would
// otherwise drain a line twice for one caller mistake.
func parseBeltLanes(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	lanes := make([]string, 0, 2)
	for _, part := range strings.Split(value, ",") {
		lane := strings.ToLower(strings.TrimSpace(part))
		switch lane {
		case "":
			return nil, errors.New("lanes must be a comma-separated list of left and right")
		case "left", "right":
			if !seen[lane] {
				seen[lane] = true
				lanes = append(lanes, lane)
			}
		default:
			return nil, fmt.Errorf("lanes must be a comma-separated list of left and right, got %q", lane)
		}
	}
	return lanes, nil
}

// beltLaneNames converts the GraphQL lane enum into the names the Lua chunk
// selects, rejecting duplicates the same way the CLI flag does.
func beltLaneNames(lanes []BeltLane) ([]string, error) {
	if len(lanes) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(lanes))
	for _, lane := range lanes {
		var name string
		switch lane {
		case BeltLaneLeft:
			name = "left"
		case BeltLaneRight:
			name = "right"
		default:
			return nil, fmt.Errorf("lanes must be LEFT or RIGHT, got %q", string(lane))
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

// beltLaneListLua renders the lane names as a Lua local, or nil when the caller
// wants both lanes.
func beltLaneListLua(lanes []string) string {
	if len(lanes) == 0 {
		return "nil"
	}
	quoted := make([]string, len(lanes))
	for i, lane := range lanes {
		quoted[i] = luaString(lane)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// beltTakeRequest flattens BeltTakeInput for the CLI backend. lanes travels as a
// comma-separated flag value because runCommand emits one flag per field.
type beltTakeRequest struct {
	Actor   *ActorInput     `json:"actor,omitempty"`
	Target  *EntityRefInput `json:"target,omitempty"`
	Item    string          `json:"item"`
	Quality *string         `json:"quality,omitempty"`
	Count   *float64        `json:"count,omitempty"`
	Lanes   string          `json:"lanes,omitempty"`
	Message *string         `json:"message,omitempty"`
}

// beltTakeMutation runs beltTake with the lane list the schema accepts and keeps
// the generated announcement of every other action mutation.
func beltTakeMutation(r *Resolver, input BeltTakeInput) (*ActionResult, error) {
	lanes, err := beltLaneNames(input.Lanes)
	if err != nil {
		return nil, err
	}
	return command[ActionResult](r, "belt-take", beltTakeRequest{
		Actor:   input.Actor,
		Target:  input.Target,
		Item:    input.Item,
		Quality: input.Quality,
		Count:   input.Count,
		Lanes:   strings.Join(lanes, ","),
		Message: input.Message,
	})
}

// beltTakeLua moves normal-quality items from a reachable belt into the acting
// character's inventory.
func beltTakeLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("belt-take", flag.ContinueOnError)
	fs.SetOutput(out)
	player := fs.String("player", "", "Player name (alternative to --character)")
	character := fs.Uint64("character", 0, "Character unit number (alternative to --player)")
	entity := fs.String("entity", "", "Transport belt prototype name: transport-belt, fast-transport-belt, express-transport-belt or turbo-transport-belt")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Belt unit number, alternative to x and y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Belt unit number, alternative to x and y")
	x := fs.Float64("x", math.NaN(), "Belt center X (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Belt center Y (required unless a unit number is given)")
	surface := fs.String("surface", "", "Target surface name (defaults to the actor's surface)")
	item := fs.String("item", "", "Normal-quality item to pick up")
	quality := fs.String("quality", "normal", "Item quality prototype name (default normal)")
	count := fs.Uint64("count", 1, "Maximum items to pick up")
	lanes := fs.String("lanes", "", "Comma-separated lanes to take from: left, right, or both (default both)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || *item == "" || *count == 0 || *count > math.MaxUint32 {
		return "", errors.New("exactly one of player or character, item, and positive count are required")
	}
	if *quality == "" {
		return "", errors.New("quality must not be empty")
	}
	selectedLanes, err := parseBeltLanes(*lanes)
	if err != nil {
		return "", err
	}
	// The prototype name is not restricted here: every transport belt tier has
	// the same entity type, and the type check below is what keeps out
	// splitters, loaders and linked belts. A unit-number-only target resolves
	// through the shared unit-number index instead of a hard-coded name.
	targetUnit, err := entityTarget(*entity, unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	prefix := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local c
if unit~=0 then
  c=game.get_entity_by_unit_number(unit); if not c or c.type~="character" then fail("character not found"); return end
else
  local pl=game.get_player(player); if not pl then fail("player not found"); return end
  c=pl.character; if not c then fail("player has no character"); return end
end
local p=c
`, luaString(*player), *character)
	prefix += targetLocals(targetUnit, *entity, *x, *y, *surface) + "if surface_name==\"\" then surface_name=c.surface.name end\n" + luaEntityResolve + resolveTargetLua()
	prefix += `if e.surface~=c.surface then fail("belt is on another surface than the actor"); return end
`
	prefix += transportLineLua + beltLaneSelectionLua + fmt.Sprintf(`if e.type~="transport-belt" then fail("entity is not a transport belt"); return end
if %s then fail("belt is out of reach"); return end
local inv=c.get_main_inventory(); if not inv then fail("character has no main inventory"); return end
local name=%s; local quality=%s; if not prototypes.quality[quality] then fail("quality not found"); return end
local requested=%s
local indexes,lane_error=belt_lane_indexes(e,requested); if not indexes then fail(lane_error); return end
local requested_count=%d; local capacity=inv.get_insertable_count{name=name,quality=quality}; local remaining=math.min(requested_count,capacity); local moved=0
for _,i in ipairs(indexes) do if remaining>0 then local removed=e.get_transport_line(i).remove_item{name=name,quality=quality,count=remaining}; if removed>0 then local inserted=inv.insert{name=name,quality=quality,count=removed}; moved=moved+inserted; remaining=remaining-removed; if inserted<removed then c.surface.spill_item_stack{position=c.position,stack={name=name,quality=quality,count=removed-inserted},enable_looted=true}; fail("inventory rejected items; remainder spilled near character"); return end end end end
rcon.print(helpers.table_to_json({position=e.position,item=name,requested=requested_count,moved=moved,unit_number=e.unit_number}))`, reachViolation("c", "e"), luaString(*item), luaString(*quality), beltLaneListLua(selectedLanes), *count)
	return prefix, nil
}

// beltPurgeInputError validates the mutually exclusive addressing before any
// backend call, so a bad request never spends a game round trip.
func beltPurgeInputError(input BeltPurgeInput) error {
	if input.Item == "" {
		return errors.New("item must not be empty")
	}
	if input.Quality != nil && *input.Quality == "" {
		return errors.New("quality must not be empty")
	}
	if (input.Area == nil) == (input.Target == nil) {
		return errors.New("exactly one of area or target is required")
	}
	limit := maxBeltPurgeTiles
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > maxBeltPurgeTiles {
		return limitError("limit", maxBeltPurgeTiles)
	}
	if input.Surface != nil && *input.Surface == "" {
		return errors.New("surface must not be empty")
	}
	return nil
}

// beltPurge removes one item and quality from selected belt lanes in one chunk,
// reporting how many belts were visited and changed. It never inserts anything,
// so items are only ever destroyed by the engine's own remove_item.
func beltPurge(r *Resolver, input BeltPurgeInput) (*BeltPurgeResult, error) {
	actor, err := defaultActor(r, input.Actor)
	if err != nil {
		return nil, err
	}
	input.Actor = actor
	if err := beltPurgeInputError(input); err != nil {
		return nil, err
	}
	lanes, err := beltLaneNames(input.Lanes)
	if err != nil {
		return nil, err
	}
	limit := maxBeltPurgeTiles
	if input.Limit != nil {
		limit = *input.Limit
	}
	quality := "normal"
	if input.Quality != nil {
		quality = *input.Quality
	}
	surface := "nauvis"
	if input.Surface != nil && *input.Surface != "" {
		surface = *input.Surface
	}
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	var targetLua string
	if input.Target != nil {
		if input.Target.Surface != nil && *input.Target.Surface != "" {
			surface = *input.Target.Surface
		}
		name, unitNumber := "", ""
		if input.Target.Entity != nil {
			name = *input.Target.Entity
		}
		if input.Target.UnitNumber != nil {
			unitNumber = *input.Target.UnitNumber
		}
		x, y := entityRefCoordinates(input.Target)
		unit, err := entityTarget(name, unitNumber, x, y)
		if err != nil {
			return nil, err
		}
		targetLua = targetLocals(unit, name, x, y, surface) + luaEntityResolve + resolveTargetLua() + `if e.type~="transport-belt" then fail("entity is not a transport belt"); return end
purge_belt(e)
local result_surface=e.surface.name
`
	}
	sweep := targetLua
	if input.Area != nil {
		areaLua, _, err := worldAreaBoundsLua(input.Area, maxAreaTiles)
		if err != nil {
			return nil, err
		}
		sweep = fmt.Sprintf(`local surface_name=%s
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local area=%s
local found=s.find_entities_filtered{area=area,type="transport-belt"}
for _,e in ipairs(found) do
  purge_belt(e)
  if tiles_changed>=limit then break end
end
local result_surface=s.name
`, luaString(surface), areaLua)
	}
	chunk := luaFail + settingsActorPrefix(character, player) + fmt.Sprintf(`local name=%s; local quality=%s; if not prototypes.quality[quality] then fail("quality not found"); return end
local requested=%s; local limit=%d; local max_count=1000000000
local tiles_visited=0; local tiles_changed=0; local removed=0
`, luaString(input.Item), luaString(quality), beltLaneListLua(lanes), limit)
	chunk += transportLineLua + beltLaneSelectionLua + `local function purge_belt(e)
  tiles_visited=tiles_visited+1
  local indexes=belt_lane_indexes(e,requested)
  if not indexes then return end
  local changed=false
  for _,i in ipairs(indexes) do
    local taken=e.get_transport_line(i).remove_item{name=name,quality=quality,count=max_count}
    if taken>0 then removed=removed+taken; changed=true end
  end
  if changed then tiles_changed=tiles_changed+1 end
end
` + sweep + `rcon.print(helpers.table_to_json({tick=game.tick,surface=result_surface,tiles_visited=tiles_visited,tiles_changed=tiles_changed,removed=removed}))`
	response, _, err := executeLua(r.addr, r.password, chunk)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[BeltPurgeResult](response)
	if err != nil {
		return nil, err
	}
	if result.Surface == "" {
		result.Surface = surface
	}
	announceMutation(r, input.Message, fmt.Sprintf("purged %d %s %s from %d of %d belt tiles on %s", result.Removed, quality, input.Item, result.TilesChanged, result.TilesVisited, result.Surface))
	return result, nil
}
