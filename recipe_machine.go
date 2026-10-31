package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

// machineDrainLua holds the drain helpers shared by set-recipe and
// clear-recipe. Factorio 2.0 keeps stacks that are valid ingredients of the new
// recipe, so set_recipe's return value alone cannot be trusted as the drained
// contents: the caller removes the machine input and output itself, before the
// recipe changes, and treats whatever the engine still returns as extra.
const machineDrainLua = `local function machine_crafting_inventories(machine)
  local inventories={}
  for _,id in ipairs({defines.inventory.crafter_input,defines.inventory.crafter_output}) do
    local ok,inv=pcall(function() return machine.get_inventory(id) end)
    if ok and inv then inventories[#inventories+1]=inv end
  end
  return inventories
end
local function machine_has_items(machine)
  for _,inv in ipairs(machine_crafting_inventories(machine)) do
    if not inv.is_empty() then return true end
  end
  return false
end
local function machine_has_fluids(machine)
  local ok_fluidbox,fluidboxes=pcall(function() return machine.fluidbox end)
  if not ok_fluidbox or fluidboxes==nil then return false end
  local ok_count,count=pcall(function() return #fluidboxes end)
  if ok_count and type(count)=="number" then
    for i=1,count do
      local ok_index,fluid=pcall(function() return fluidboxes[i] end)
      if ok_index and fluid then return true end
    end
    return false
  end
  for i=1,10 do
    local ok_index,fluid=pcall(function() return fluidboxes[i] end)
    if not ok_index then break end
    if fluid then return true end
  end
  return false
end
local function machine_quality_name(quality)
  if type(quality)=="table" then return quality.name end
  return quality or "normal"
end
local function machine_store(target,actor,position,insert_items,name,quality,count)
  local added=0
  if insert_items then
    local inventory=actor.get_main_inventory()
    if inventory then added=inventory.insert{name=name,quality=quality,count=count} end
  end
  local remaining=count-added
  if remaining>0 then
    actor.surface.spill_item_stack{position=position,stack={name=name,quality=quality,count=remaining},enable_looted=true,force=actor.force}
  end
  target[#target+1]={name=name,quality=quality,count=count}
end
local function machine_drain(machine,target,actor,position,insert_items)
  for _,inventory in ipairs(machine_crafting_inventories(machine)) do
    for _,item in pairs(inventory.get_contents()) do
      local quality=machine_quality_name(item.quality)
      local count=item.count
      pcall(function() inventory.remove{name=item.name,quality=quality,count=count} end)
      machine_store(target,actor,position,insert_items,item.name,quality,count)
    end
  end
end
local function machine_merge_engine_items(machine,target,actor,position,insert_items,items)
  for _,item in pairs(items or {}) do
    machine_store(target,actor,position,insert_items,item.name,machine_quality_name(item.quality),item.count)
  end
end
local function machine_aggregate(items)
  local aggregates={}
  for _,item in pairs(items) do
    local key=item.name..string.char(31)..item.quality
    local entry=aggregates[key]
    if not entry then entry={name=item.name,quality=item.quality,count=0}; aggregates[key]=entry end
    entry.count=entry.count+item.count
  end
  local result={}
  for _,entry in pairs(aggregates) do result[#result+1]=entry end
  table.sort(result,function(a,b) if a.name~=b.name then return a.name<b.name end return a.quality<b.quality end)
  return result
end
`

// machineRecipeLua sets a recipe on a crafting machine. Machines that cannot be
// crafting machines are refused, the recipe must be enabled and inside the
// machine's crafting categories, and the caller picks what happens to items and
// fluids the machine is already holding:
//
//   - abort: input/output and fluids must be empty, otherwise the command fails.
//   - to-actor: fluids must be empty; the machine input and output are drained
//     into the acting character's main inventory before the recipe changes, and
//     anything that does not fit is spilled near the actor.
//   - spill: machine fluids are cleared, the input and output are drained, and
//     every drained stack is spilled at the machine. This is destructive, so it
//     is never implicit.
//
// The drain is explicit because Factorio 2.0 keeps stacks that are valid
// ingredients of the new recipe, so set_recipe's return value is not the
// machine's drained contents. Whatever the engine still returns is merged into
// the reported list.
func machineRecipeLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("set-recipe", flag.ContinueOnError)
	fs.SetOutput(out)
	character := fs.Uint64("character", 0, "Character unit number (required)")
	entity := fs.String("entity", "", "Crafting machine entity name (required unless a unit number is given)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Crafting machine unit number, alternative to entity and x/y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Crafting machine unit number, alternative to entity and x/y")
	x := fs.Float64("x", math.NaN(), "Entity center X (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Entity center Y (required unless a unit number is given)")
	recipe := fs.String("recipe", "", "Enabled recipe name (required)")
	recipeQuality := fs.String("recipeQuality", "normal", "Recipe quality prototype name (default normal)")
	drain := fs.String("drain", "abort", "Machine contents handling: abort, to-actor, or spill")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *character == 0 || *character > math.MaxUint32 || *recipe == "" {
		return "", errors.New("character and recipe are required")
	}
	if *recipeQuality == "" {
		return "", errors.New("recipeQuality must not be empty")
	}
	mode, err := normalizeRecipeDrain(*drain)
	if err != nil {
		return "", err
	}
	targetUnit, err := entityTarget(*entity, unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	prefix := luaFail + fmt.Sprintf(`local c=game.get_entity_by_unit_number(%d); if not c or c.type~="character" then fail("character not found"); return end
local p=c
`, *character) + targetLocals(targetUnit, *entity, *x, *y, "") + "surface_name=c.surface.name\n" + luaEntityResolve + resolveTargetLua() + `if e.type~="assembling-machine" then fail("assembling machine not found"); return end
if ` + reachViolation("c", "e") + ` then fail("machine out of reach"); return end
` + machineDrainLua
	return prefix + fmt.Sprintf(`local recipe=c.force.recipes[%s]; if not recipe or not recipe.enabled then fail("recipe not found or not enabled"); return end
if not e.prototype.crafting_categories[recipe.category] then fail("machine cannot use recipe category"); return end
if e.is_crafting() then fail("machine has an active craft"); return end
local quality=%s
if not prototypes.quality[quality] then fail("recipe quality not found"); return end
local drain=%s
if drain=="abort" then
  if machine_has_items(e) then fail("empty machine input and output before changing recipe"); return end
  if machine_has_fluids(e) then fail("empty machine fluids before changing recipe"); return end
elseif drain=="to-actor" then
  if machine_has_fluids(e) then fail("machine holds fluids; use drain spill to clear them"); return end
else
  local cleared=pcall(function() e.clear_fluid_inside() end)
  if not cleared or machine_has_fluids(e) then fail("machine fluids could not be cleared"); return end
end
local drained={}
local spill_position=e.position; local insert_items=drain=="to-actor"
if insert_items then spill_position=c.position end
if drain~="abort" then machine_drain(e,drained,c,spill_position,insert_items) end
local ok,engine_returned=pcall(function() return e.set_recipe(recipe.name,quality) end)
if not ok then fail("recipe could not be set: "..tostring(engine_returned)); return end
machine_merge_engine_items(e,drained,c,spill_position,insert_items,engine_returned)
rcon.print(helpers.table_to_json({entity=e.name,position=e.position,surface=s.name,recipe=recipe.name,quality=quality,drain=drain,returned=machine_aggregate(drained),unit_number=e.unit_number}))`, luaString(*recipe), luaString(*recipeQuality), luaString(mode)), nil
}
