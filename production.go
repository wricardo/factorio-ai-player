package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// inventoryKindKeys maps typed inventory selectors to the backend inventory
// keys understood by productionLua.
var inventoryKindKeys = map[string]string{
	"INPUT":                "input",
	"OUTPUT":               "output",
	"FUEL":                 "fuel",
	"CHEST":                "chest",
	"MODULES":              "modules",
	"TURRET_AMMO":          "turret_ammo",
	"ROBO_PORT_ROBOT":      "roboport_robot",
	"ROBO_PORT_MATERIAL":   "roboport_material",
	"CARGO_WAGON":          "cargo_wagon",
	"CAR_TRUNK":            "car_trunk",
	"SPIDER_TRUNK":         "spider_trunk",
	"ARTILLERY_WAGON_AMMO": "artillery_wagon_ammo",
}

// inventoryKeys are the backend keys accepted by the transfer command. Both
// the historical hyphenated spellings and the typed underscore keys work.
var inventoryKeys = func() map[string]bool {
	keys := []string{
		"input", "output", "fuel", "chest", "modules", "turret_ammo",
		"roboport_robot", "roboport_material", "cargo_wagon", "car_trunk",
		"spider_trunk", "artillery_wagon_ammo",
	}
	set := make(map[string]bool, len(keys)*2)
	for _, key := range keys {
		set[key] = true
		set[strings.ReplaceAll(key, "_", "-")] = true
	}
	return set
}()

// resolveInventoryKind turns a typed selector into its backend inventory key,
// rejecting unknown enum values before any game command runs.
func resolveInventoryKind(kind string) (string, error) {
	key, ok := inventoryKindKeys[kind]
	if !ok {
		names := make([]string, 0, len(inventoryKindKeys))
		for name := range inventoryKindKeys {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("inventoryKind must be one of %s", strings.Join(names, ", "))
	}
	return key, nil
}

// productionLua inspects an entity or transfers existing stacks in one game tick.
func productionLua(command string, args []string, out io.Writer) (string, error) {
	if command != "inspect" && command != "transfer" {
		return "", errors.New("unknown production command")
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	character := fs.Uint64("character", 0, "Character unit number, alternative to player")
	player := fs.String("player", "", "Player whose force and inventory are used (required)")
	surface := fs.String("surface", "nauvis", "Surface name")
	entity := fs.String("entity", "", "Entity prototype name (required unless a unit number is given)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Entity unit number, alternative to entity and x/y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Entity unit number, alternative to entity and x/y")
	x := fs.Float64("x", math.NaN(), "Entity center X (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Entity center Y (required unless a unit number is given)")
	var inventory, inventoryKind, direction, item, quality *string
	var count *uint64
	if command == "transfer" {
		inventory = fs.String("inventory", "", "Entity inventory: input, output, fuel, chest, modules, turret-ammo, roboport-robot, roboport-material, cargo-wagon, car-trunk, spider-trunk, artillery-wagon-ammo")
		inventoryKind = fs.String("inventoryKind", "", "Typed inventory selector, an alternative to inventory")
		direction = fs.String("direction", "", "Transfer direction: to-entity or to-player (required)")
		item = fs.String("item", "", "Item name (required)")
		quality = fs.String("quality", "normal", "Item quality prototype name (default normal)")
		count = fs.Uint64("count", 0, "Requested item count (required)")
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || (*player == "") == (*character == 0) || *character > math.MaxUint32 || *surface == "" {
		return "", errors.New("exactly one of player or character and a surface are required; no positional arguments allowed")
	}
	targetUnit, err := entityTarget(*entity, unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	prefix := luaFail + fmt.Sprintf(`local p
if %s~="" then p=game.get_player(%s); if not p then fail("player not found"); return end
else p=game.get_entity_by_unit_number(%d); if not p or p.type~="character" then fail("character not found"); return end end
`, luaString(*player), luaString(*player), *character) +
		targetLocals(targetUnit, *entity, *x, *y, *surface) + luaEntityResolve + luaInventoryHelpers
	if command == "inspect" {
		// Inspection is read-only, so it resolves neutral and enemy entities
		// instead of demanding the actor's force the way transfer does.
		return prefix + resolveTargetAnyForceLua() + entityMetaFieldsLua + luaInspectSnapshot +
			"rcon.print(helpers.table_to_json(inspect_snapshot(e,s)))\n", nil
	}
	prefix += resolveTargetLua()
	if *inventoryKind != "" {
		if *inventory != "" {
			return "", errors.New("give exactly one of inventory or inventoryKind")
		}
		key, err := resolveInventoryKind(*inventoryKind)
		if err != nil {
			return "", err
		}
		*inventory = key
	}
	if *inventory == "" {
		return "", errors.New("inventory or inventoryKind is required")
	}
	if !inventoryKeys[*inventory] {
		return "", errors.New("inventory must be input, output, fuel, chest, modules, turret-ammo, roboport-robot, roboport-material, cargo-wagon, car-trunk, spider-trunk, or artillery-wagon-ammo")
	}
	if *direction != "to-entity" && *direction != "to-player" {
		return "", errors.New("direction must be to-entity or to-player")
	}
	if *quality == "" {
		return "", errors.New("quality must not be empty")
	}
	if *item == "" || *count == 0 || *count > math.MaxUint32 {
		return "", errors.New("item and count between 1 and 4294967295 are required")
	}
	outputDirection := "TO_ENTITY"
	if *direction == "to-player" {
		outputDirection = "TO_ACTOR"
	}
	prefix += `local c; if p.object_name=="LuaEntity" then c=p else c=p.character; if not c then fail("player has no character"); return end end; if c.surface~=s or ` + reachViolation("c", "e") + ` then fail("entity is out of reach"); return end` + "\n"
	return prefix + luaTransferStateLua + fmt.Sprintf(`local target_name=%s; target_name=string.gsub(target_name,"-","_"); local target=inventories(e)[target_name]; if not target then fail("entity does not have requested inventory"); return end
local player_inventory=p.get_main_inventory(); if not player_inventory then fail("player has no main inventory"); return end
local source,destination=player_inventory,target
if %s=="to-player" then source,destination=target,player_inventory end
local item=%s; local quality=%s; local requested=%d
if not prototypes.item[item] then fail("item prototype not found"); return end
if not prototypes.quality[quality] then fail("quality not found"); return end
local spec={name=item,quality=quality}
local available=source.get_item_count(spec)
local capacity=destination.get_insertable_count(spec)
local remaining=math.min(requested,available,capacity)
local moved=0
local last=#destination
if destination.supports_bar() then last=math.min(last,destination.get_bar()-1) end
for i=1,#source do
  if remaining==0 then break end
  local stack=source[i]
  if stack.valid_for_read and stack.name==item and stack.quality.name==quality then
    for j=1,last do
      if remaining==0 or not stack.valid_for_read then break end
      local before=stack.count
      destination[j].transfer_stack(stack,math.min(remaining,before))
      local after=0; if stack.valid_for_read then after=stack.count end
      local transferred=before-after
      moved=moved+transferred; remaining=remaining-transferred
    end
  end
end
local bar,deconstructed=transfer_target_state(e,destination)
local reason=transfer_block_reason(available,moved,capacity,destination,bar,deconstructed)
rcon.print(helpers.table_to_json({tick=game.tick,entity=e.name,position=e.position,surface=s.name,force=p.force.name,inventory=%s,transfer_direction=%s,item=item,quality=quality,requested=requested,available=available,capacity=capacity,moved=moved,bar=bar,to_be_deconstructed=deconstructed,reason=reason}))`, luaString(*inventory), luaString(*direction), luaString(*item), luaString(*quality), *count, luaString(*inventory), luaString(outputDirection)), nil
}
