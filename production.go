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
`+targetLocals(targetUnit, *entity, *x, *y, *surface)+luaEntityResolve+resolveTargetLua()+`local function get_inv(entity,key)
  local id=defines.inventory[key]
  if id==nil then return nil end
  local ok,inv=pcall(function() return entity.get_inventory(id) end)
  if not ok then return nil end
  return inv
end
local function inventories(entity)
  local result={}
  local t=entity.type
  if t=="container" or t=="logistic-container" then result.chest=entity.get_inventory(defines.inventory.chest) end
  if t=="furnace" then
    result.input=entity.get_inventory(defines.inventory.furnace_source)
    result.output=entity.get_inventory(defines.inventory.furnace_result)
  elseif t=="assembling-machine" or t=="rocket-silo" then
    result.input=entity.get_inventory(defines.inventory.crafter_input)
    result.output=entity.get_inventory(defines.inventory.crafter_output)
  elseif t=="lab" then
    result.input=entity.get_inventory(defines.inventory.lab_input)
  end
  if t=="assembling-machine" or t=="rocket-silo" then
    result.modules=entity.get_inventory(defines.inventory.crafter_modules)
  elseif t=="lab" then
    result.modules=entity.get_inventory(defines.inventory.lab_modules)
  elseif t=="beacon" then
    result.modules=entity.get_inventory(defines.inventory.beacon_modules)
  elseif t=="mining-drill" then
    result.modules=entity.get_inventory(defines.inventory.mining_drill_modules)
  end
  if t=="ammo-turret" or t=="artillery-turret" then result.turret_ammo=entity.get_inventory(defines.inventory.turret_ammo) end
  if t=="roboport" then
    result.roboport_robot=get_inv(entity,"roboport_robot")
    result.roboport_material=get_inv(entity,"roboport_material")
  elseif t=="cargo-wagon" then result.cargo_wagon=get_inv(entity,"cargo_wagon")
  elseif t=="car" then result.car_trunk=get_inv(entity,"car_trunk")
  elseif t=="spider-vehicle" then result.spider_trunk=get_inv(entity,"spider_trunk")
  elseif t=="artillery-wagon" then result.artillery_wagon_ammo=get_inv(entity,"artillery_wagon_ammo") end
  if t=="character" then
    result.main=entity.get_inventory(defines.inventory.character_main)
    result.guns=entity.get_inventory(defines.inventory.character_guns)
    result.ammo=entity.get_inventory(defines.inventory.character_ammo)
    result.armor=entity.get_inventory(defines.inventory.character_armor)
    result.trash=entity.get_inventory(defines.inventory.character_trash)
  end
  result.fuel=entity.get_fuel_inventory()
  return result
end
`, luaString(*player), luaString(*player), *character)
	if command == "inspect" {
		return prefix + entityMetaFieldsLua + `local result={name=e.name,type=e.type,position=e.position,surface=s.name,force=e.force.name,health=e.health,max_health=e.max_health,inventories={}}
for name,inv in pairs(inventories(e)) do result.inventories[name]=inv.get_contents() end
for name,value in pairs(entity_meta_fields(e)) do result[name]=value end
if e.status then
  result.status=e.status
  for name,value in pairs(defines.entity_status) do if value==e.status then result.status_name=name; break end end
end
if e.type=="furnace" or e.type=="assembling-machine" or e.type=="rocket-silo" then
  local recipe=e.get_recipe(); if recipe then result.recipe=recipe.name end
  result.crafting_progress=e.crafting_progress
end
if e.type=="transport-belt" then
  result.transport_lines={e.get_transport_line(1).get_contents(),e.get_transport_line(2).get_contents()}
end
local ok_marked,marked=pcall(function() return e.to_be_deconstructed() end); result.to_be_deconstructed=ok_marked and marked or false
if e.type=="inserter" then
  result.pickup_position=e.pickup_position
  result.drop_position=e.drop_position
  result.held_stack=e.held_stack.valid_for_read and {name=e.held_stack.name,count=e.held_stack.count} or nil
end
if e.type=="mining-drill" then
  result.mining_progress=e.mining_progress
  local target=e.mining_target
  if target then result.mining_target={name=target.name,position=target.position,amount=target.amount} end
  local resources={}
  for _,resource in pairs(s.find_entities_filtered{area=e.mining_area,type="resource"}) do
    resources[#resources+1]={name=resource.name,position=resource.position,amount=resource.amount}
  end
  result.mining_resources=resources
end
local stats=e.force.get_item_production_statistics(s)
if stats then result.force_item_production={produced=stats.input_counts,consumed=stats.output_counts} end
local fluids={}
for name,amount in pairs(e.get_fluid_contents()) do fluids[#fluids+1]={name=name,amount=amount} end
table.sort(fluids,function(a,b) return a.name<b.name end)
result.fluids=fluids
rcon.print(helpers.table_to_json(result))`, nil
	}
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
	return prefix + fmt.Sprintf(`local target_name=%s; target_name=string.gsub(target_name,"-","_"); local target=inventories(e)[target_name]; if not target then fail("entity does not have requested inventory"); return end
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
rcon.print(helpers.table_to_json({entity=e.name,position=e.position,surface=s.name,inventory=%s,transfer_direction=%s,item=item,quality=quality,requested=requested,available=available,capacity=capacity,moved=moved}))`, luaString(*inventory), luaString(*direction), luaString(*item), luaString(*quality), *count, luaString(*inventory), luaString(outputDirection)), nil
}
