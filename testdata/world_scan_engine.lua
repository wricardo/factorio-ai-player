-- Minimal Factorio 2.0.77 runtime stub used by the world_scan replay tests
-- (factoryAlerts, mapArea and safety). It models a tiny surface so the shipped
-- Lua runs for real: entity prototypes with footprints, a status-bearing world,
-- force filters, tiles, and the helpers/rcon the queries print through.
local function encode(value)
  local kind = type(value)
  if kind == "nil" then
    return "null"
  elseif kind == "boolean" then
    return tostring(value)
  elseif kind == "number" then
    if value == math.floor(value) and math.abs(value) < 1e15 then
      return string.format("%d", value)
    end
    return string.format("%.14g", value)
  elseif kind == "string" then
    return '"' .. value:gsub('[%z\1-\31\\"]', function(c)
      return string.format("\\u%04x", string.byte(c))
    end) .. '"'
  elseif kind == "table" then
    if next(value) == nil then
      return "[]"
    end
    if #value > 0 then
      local parts = {}
      for index = 1, #value do
        parts[index] = encode(value[index])
      end
      return "[" .. table.concat(parts, ",") .. "]"
    end
    local parts = {}
    for key, item in pairs(value) do
      parts[#parts + 1] = encode(key) .. ":" .. encode(item)
    end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  error("cannot encode " .. kind)
end

helpers = {table_to_json = encode}
rcon = {print = function(message)
  io.write(message, "\n")
end}

defines = {
  entity_status = {working = 1, normal = 2, no_fuel = 3, no_power = 4, no_minable_resources = 5,
    broken = 6, fluid_ingredient_shortage = 7, marked_for_deconstruction = 8, no_ammo = 9},
  alert_type = {entity_destroyed = 1},
  build_check_type = {script = 1, manual = 2, ghost_revive = 3},
  flow_precision_index = {},
  inventory = {},
}

local function method(value)
  return function()
    return value
  end
end

local function prototype(name, data)
  data.name = name
  data.collision_mask = data.collision_mask or {layers = {}}
  data.get_supply_area_distance = data.get_supply_area_distance or method(nil)
  data.get_max_wire_distance = data.get_max_wire_distance or method(nil)
  data.get_crafting_speed = data.get_crafting_speed or method(nil)
  data.get_max_health = data.get_max_health or method(100)
  data.get_mining_drill_radius = data.get_mining_drill_radius or method(data.mining_drill_radius)
  data.get_max_energy_usage = data.get_max_energy_usage or method(nil)
  return data
end

local entity_prototypes = {
  ["stone-furnace"] = prototype("stone-furnace", {type = "furnace", tile_width = 2, tile_height = 2}),
  ["assembling-machine-1"] = prototype("assembling-machine-1", {type = "assembling-machine", tile_width = 3, tile_height = 3}),
  ["electric-mining-drill"] = prototype("electric-mining-drill", {type = "mining-drill", tile_width = 3, tile_height = 3, mining_drill_radius = 2.5}),
  boiler = prototype("boiler", {type = "boiler", tile_width = 3, tile_height = 2}),
  inserter = prototype("inserter", {type = "inserter"}),
  ["transport-belt"] = prototype("transport-belt", {type = "transport-belt"}),
  ["small-biter"] = prototype("small-biter", {type = "unit"}),
  radar = prototype("radar", {type = "radar", tile_width = 3, tile_height = 3}),
  ["electric-pole"] = prototype("electric-pole", {type = "electric-pole", tile_width = 1, tile_height = 1,
    get_supply_area_distance = method(9)}),
  ["iron-ore"] = prototype("iron-ore", {type = "resource", mineable_properties = {minable = true, infinite = false}}),
  ["crude-oil"] = prototype("crude-oil", {type = "resource", mineable_properties = {minable = true, infinite = true}}),
}

local function tile(x, y)
  return {
    valid = true,
    name = "grass-1",
    position = {x = x, y = y},
    prototype = {collision_mask = {layers = {}}},
    collides_with = function()
      return false
    end,
    prototype_fluid = nil,
  }
end

local player_force, enemy_force
player_force = {
  name = "player",
  is_enemy = function(other)
    return other ~= nil and other.name == "enemy"
  end,
  is_friend = function(other)
    return other ~= nil and other.name == "player"
  end,
  get_spawn_position = function()
    return {x = 0, y = 0}
  end,
  get_evolution_factor = function()
    return 0
  end,
  get_evolution_factor_by_time = function()
    return 0
  end,
  get_evolution_factor_by_pollution = function()
    return 0
  end,
  get_evolution_factor_by_killing_spawners = function()
    return 0
  end,
  recipes = {},
}
enemy_force = {name = "enemy", is_enemy = player_force.is_enemy, is_friend = player_force.is_friend}

local entity_meta = {__index = function(_, key)
  if key == "ghost_prototype" then
    error("ghost_prototype can only be used on entity ghosts")
  end
  return nil
end}

local function new_entity(proto, position, data)
  local entity = data or {}
  entity.prototype = entity_prototypes[proto]
  entity.name = proto
  entity.type = entity.prototype.type
  entity.position = position
  entity.unit_number = entity.unit_number or 1
  entity.direction = entity.direction or 0
  entity.force = entity.force or player_force
  entity.valid = true
  return setmetatable(entity, entity_meta)
end

local world = {
  new_entity("assembling-machine-1", {x = 1.5, y = 1.5}, {status = 1}),
  new_entity("stone-furnace", {x = 6, y = 2}, {status = 3}),
  new_entity("inserter", {x = 47.5, y = -48.5}, {status = 4, unit_number = 901}),
  new_entity("electric-mining-drill", {x = 2.0, y = 1.5}, {status = 1, unit_number = 902}),
  new_entity("electric-mining-drill", {x = -48, y = -30}, {status = 5, unit_number = 198, direction = 4}),
  new_entity("boiler", {x = 10.5, y = 4.5}, {status = 3, unit_number = 903}),
  new_entity("radar", {x = -6.5, y = -6.5}, {unit_number = 904}),
  new_entity("small-biter", {x = -40.5, y = 3.5}, {status = 1, force = enemy_force, health = 12, max_health = 15, unit_number = 905}),
  new_entity("iron-ore", {x = 1.5, y = 1.5}, {amount = 1000}),
  new_entity("iron-ore", {x = 2.5, y = 1.5}, {amount = 2000}),
  new_entity("crude-oil", {x = 8.5, y = 8.5}, {amount = 250}),
  new_entity("electric-pole", {x = 10, y = 10}, {unit_number = 906}),
}

local function matches_force(entity, filter)
  if filter == nil then
    return true
  end
  if type(filter) == "table" and filter.name then
    return entity.force == filter or (entity.force and entity.force.name == filter.name)
  end
  for _, name in ipairs(filter) do
    if entity.force and entity.force.name == name then
      return true
    end
  end
  return false
end

local function matches_type(entity, filter)
  if filter == nil then
    return true
  end
  if type(filter) == "table" then
    for _, name in ipairs(filter) do
      if entity.type == name then
        return true
      end
    end
    return false
  end
  return entity.type == filter
end

local function in_area(position, area)
  if area == nil then
    return true
  end
  return position.x >= area[1][1] and position.y >= area[1][2] and position.x <= area[2][1] and position.y <= area[2][2]
end

local surface
surface = {
  name = "nauvis",
  pollutant_type = {name = "pollution"},
  find_entities_filtered = function(filter)
    local out = {}
    for _, entity in ipairs(world) do
      local ok = matches_force(entity, filter.force) and matches_type(entity, filter.type) and in_area(entity.position, filter.area)
      if ok and filter.name ~= nil then
        if type(filter.name) == "table" then
          ok = false
          for _, name in ipairs(filter.name) do
            if entity.name == name then
              ok = true
            end
          end
        else
          ok = entity.name == filter.name
        end
      end
      if ok and filter.position ~= nil then
        local radius = filter.radius or 0
        local dx, dy = entity.position.x - filter.position.x, entity.position.y - filter.position.y
        ok = math.sqrt(dx * dx + dy * dy) <= radius
      end
      if ok and filter.collision_mask ~= nil then
        ok = false
      end
      if ok then
        out[#out + 1] = entity
      end
    end
    return out
  end,
  find_tiles_filtered = function()
    return {}
  end,
  get_tile = function(x, y)
    return tile(x, y)
  end,
  can_place_entity = function(filter)
    return filter.position.x < 4
  end,
  count_entities_filtered = function(filter)
    return #surface.find_entities_filtered(filter)
  end,
  get_pollution = function()
    return 0
  end,
  get_total_pollution = function()
    return 0
  end,
}

local character = {
  type = "character",
  unit_number = 1,
  position = {x = 0, y = 0},
  surface = surface,
  force = player_force,
  get_main_inventory = function()
    return {get_contents = function()
      return {}
    end}
  end,
  crafting_queue = {},
  can_reach_entity = function()
    return true
  end,
}

local player_object = {
  name = "wricardo",
  connected = true,
  controller_type = 1,
  character = character,
  force = player_force,
  surface = surface,
  position = {x = 0, y = 0},
  get_alerts = function()
    return {}
  end,
}

game = {
  tick = 1234,
  surfaces = {nauvis = surface},
  forces = {player = player_force, enemy = enemy_force},
  players = {},
  get_player = function()
    return player_object
  end,
  get_entity_by_unit_number = function()
    return nil
  end,
}

prototypes = {entity = entity_prototypes, item = {}}
