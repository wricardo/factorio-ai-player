package main

// luaOutputInventoryHelper resolves an entity's own output inventory. Factorio
// hands back the burner fuel inventory from get_output_inventory() when an
// entity keeps no separate output buffer (burner mining drills), so reading it
// unguarded reports the drill's coal fuel as an "output" stack while inspect()
// reports the same stack as fuel. A nil result means "no output inventory of
// its own"; callers still read the fuel inventory on their own.
const luaOutputInventoryHelper = `local function output_inventory(entity)
  if not entity then return nil end
  local ok, output = pcall(function() return entity.get_output_inventory() end)
  if not ok or output == nil then return nil end
  local ok_fuel, fuel = pcall(function() return entity.get_fuel_inventory() end)
  if ok_fuel and fuel and (output == fuel or (output.index ~= nil and output.index == fuel.index)) then return nil end
  return output
end
`

// luaInventoryHelpers defines get_inv and inventories(entity), the typed inventory
// lookup shared by inspect, transfer and transferMany. A key is the lowercase
// underscore form of an InventoryKind.
const luaInventoryHelpers = `local function get_inv(entity,key)
  local id=defines.inventory[key]
  if id==nil then return nil end
  local ok,inv=pcall(function() return entity.get_inventory(id) end)
  if not ok then return nil end
  return inv
end
local function inventories(entity)
  local result={}
  local t=entity.type
  if t=="container" or t=="logistic-container" or t=="infinity-container" then result.chest=entity.get_inventory(defines.inventory.chest) end
  if t=="furnace" then
    result.input=entity.get_inventory(defines.inventory.furnace_source)
    result.output=entity.get_inventory(defines.inventory.furnace_result)
  elseif t=="assembling-machine" or t=="rocket-silo" then
    result.input=entity.get_inventory(defines.inventory.crafter_input)
    result.output=entity.get_inventory(defines.inventory.crafter_output)
  elseif t=="lab" then
    result.input=entity.get_inventory(defines.inventory.lab_input)
  end
  if t=="asteroid-collector" then
    result.asteroid_collector_output=get_inv(entity,"asteroid_collector_output")
  end
  if t=="rocket-silo" then
    -- The rocket cargo is separate from the crafting input and is the only
    -- inventory holding items loaded for launch, so a silo with a loaded
    -- rocket otherwise looks empty.
    result.rocket=get_inv(entity,"rocket_silo_rocket")
  end
  if t=="furnace" then
    -- Furnaces keep modules in their own inventory index, not crafter_modules,
    -- so the shared MODULES selector otherwise reports "no such inventory".
    result.modules=entity.get_inventory(defines.inventory.furnace_modules)
  elseif t=="assembling-machine" or t=="rocket-silo" then
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
`
