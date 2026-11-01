package main

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
`
