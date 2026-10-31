package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// FactoryArea bounds the surface area included in a factory snapshot.
type FactoryArea struct {
	LeftTopX, LeftTopY, RightBottomX, RightBottomY float64
}

func factoryMachines(r *Resolver, surface, force, status, entityType string, area *FactoryArea, limit, offset int) (string, error) {
	if surface == "" || force == "" {
		return "", errors.New("surface and force are required")
	}
	if limit < 1 || limit > 1000 {
		return "", errors.New("limit must be between 1 and 1000")
	}
	if offset < 0 {
		return "", errors.New("offset must be non-negative")
	}
	areaLua := "nil"
	if area != nil {
		coords := []float64{area.LeftTopX, area.LeftTopY, area.RightBottomX, area.RightBottomY}
		for _, value := range coords {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return "", errors.New("area coordinates must be finite")
			}
		}
		if area.LeftTopX > area.RightBottomX || area.LeftTopY > area.RightBottomY {
			return "", errors.New("area left top must not exceed right bottom")
		}
		areaLua = fmt.Sprintf("{{%s,%s},{%s,%s}}", strconv.FormatFloat(coords[0], 'g', -1, 64), strconv.FormatFloat(coords[1], 'g', -1, 64), strconv.FormatFloat(coords[2], 'g', -1, 64), strconv.FormatFloat(coords[3], 'g', -1, 64))
	}

	lua := luaFail + fmt.Sprintf(`
local surface_name, force_name = %s, %s
local requested_status, requested_type = %s, %s
local area, limit, offset = %s, %d, %d
local surface = game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force = game.forces[force_name]
if not force then fail("force not found"); return end
local types = {"assembling-machine", "furnace", "mining-drill", "lab", "rocket-silo"}
local filter = {type=types, force=force, area=area}
local status_names = {}
for name, value in pairs(defines.entity_status) do status_names[value] = name end
local grouped, matches = {}, {}
for _, entity in pairs(surface.find_entities_filtered(filter)) do
  if requested_type == "" or entity.type == requested_type then
    local name = status_names[entity.status] or "unknown"
    local summary = grouped[entity.type]
    if not summary then
      summary = {type=entity.type, working=0, idle=0, noFuel=0, noPower=0, noIngredients=0, outputFull=0, other=0, total=0}
      grouped[entity.type] = summary
    end
    summary.total = summary.total + 1
    local bucket = "other"
    if name == "working" then bucket = "working"
    elseif name == "normal" or name == "no_recipe" or name == "no_research_in_progress" then bucket = "idle"
    elseif name == "no_fuel" then bucket = "noFuel"
    elseif name == "no_power" or name == "low_power" or name == "not_connected_to_electric_network" then bucket = "noPower"
    elseif name == "no_ingredients" or name == "item_ingredient_shortage" or name == "fluid_ingredient_shortage" or name == "missing_science_packs" or name == "missing_required_fluid" then bucket = "noIngredients"
    elseif name == "full_output" or name == "full_burnt_result_output" or name == "waiting_for_space_in_destination" then bucket = "outputFull" end
    summary[bucket] = summary[bucket] + 1
    if requested_status == "" or name == requested_status then
      matches[#matches+1] = {entity=entity, status=name}
    end
  end
end
table.sort(matches, function(a,b)
  local x, y = a.entity, b.entity
  if x.type ~= y.type then return x.type < y.type end
  if x.name ~= y.name then return x.name < y.name end
  if x.position.x ~= y.position.x then return x.position.x < y.position.x end
  if x.position.y ~= y.position.y then return x.position.y < y.position.y end
  return (x.unit_number or 0) < (y.unit_number or 0)
end)
local machines = {}
for i=offset+1, math.min(#matches, offset+limit) do
  local match = matches[i]
  local entity = match.entity
  local fuel = {}
  local inventory = entity.get_fuel_inventory()
  if inventory then fuel = inventory.get_contents() end
  table.sort(fuel, function(a,b)
    if a.name ~= b.name then return a.name < b.name end
    return a.quality < b.quality
  end)
  local machine = {name=entity.name, type=entity.type, position=entity.position, status=match.status, fuel=fuel,
    surface=surface.name,force=force.name,unitNumber=entity.unit_number,
    energy=entity.energy, electricBufferSize=entity.electric_buffer_size, electricDrain=entity.electric_drain}
  local t = entity.type
  local function contents(inv)
    if not inv then return nil end
    local items=inv.get_contents()
    table.sort(items, function(a,b)
      if a.name ~= b.name then return a.name < b.name end
      return a.quality < b.quality
    end)
    return items
  end
  if t=="furnace" then
    machine.input=contents(entity.get_inventory(defines.inventory.furnace_source))
    machine.output=contents(entity.get_inventory(defines.inventory.furnace_result))
  elseif t=="assembling-machine" or t=="rocket-silo" then
    machine.input=contents(entity.get_inventory(defines.inventory.crafter_input))
    machine.output=contents(entity.get_inventory(defines.inventory.crafter_output))
  elseif t=="lab" then
    machine.input=contents(entity.get_inventory(defines.inventory.lab_input))
  elseif t=="mining-drill" then
    machine.output=contents(entity.get_output_inventory())
    machine.miningProgress=entity.mining_progress
    local target=entity.mining_target
    if target and target.valid then
      machine.miningTarget={name=target.name, position=target.position, amount=target.amount}
    end
  end
  if t=="furnace" or t=="assembling-machine" or t=="rocket-silo" then
    local recipe, quality=entity.get_recipe()
    machine.craftingProgress=entity.crafting_progress
    machine.craftingSpeed=entity.crafting_speed
    if recipe then
      machine.recipeName=recipe.name
      if quality then machine.recipeQuality=quality.name end
      machine.recipeIngredients=recipe.ingredients
    end
  end
  local fluids={}
  for index=1,#entity.fluidbox do
    local fluid=entity.fluidbox[index]
    if fluid then fluids[#fluids+1]={index=index, name=fluid.name, amount=fluid.amount, temperature=fluid.temperature} end
  end
  machine.machineFluids=fluids
  machines[#machines+1] = machine
end
local summaries = {}
for _, summary in pairs(grouped) do summaries[#summaries+1] = summary end
table.sort(summaries, function(a,b) return a.type < b.type end)
rcon.print(helpers.table_to_json({tick=game.tick, surface=surface.name, force=force.name, page_info={total=#matches,offset=offset,limit=limit,has_more=#matches>offset+limit}, machines=machines, summaries=summaries}))`, luaString(surface), luaString(force), luaString(status), luaString(entityType), areaLua, limit, offset)
	response, _, err := executeLua(r.addr, r.password, lua)
	return response, err
}
