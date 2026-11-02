-- Minimal Factorio runtime used by place_replay_test.go. It models the engine
-- behaviour the place mutation must not misreport: can_place_entity rejects a
-- box submitted at integer coordinates because it straddles four tiles, while
-- create_entity snaps the entity to the nearest tile centre and succeeds.
-- PLACE_SCENARIO selects the world:
--   snap         - empty ground; an integer position is accepted after the snap
--   blocked      - the target tile is genuinely occupied by another entity
--   engine       - create_entity raises a scripted engine error
--   replace      - the target tile holds a fast-replaceable chest with contents
--   unreplaceable - the target tile holds an entity that cannot be fast-replaced
--   replace_belt - the target tile holds a replaceable belt (can_place_entity is false)
--   commit_then_raise - create_entity commits the entity, then raises; the
--                 mutation must report the creation and must not refund the item
--   direction    - asserts create_entity received direction 8, so the mutation
--                 can be checked to pass a direction through unchanged
local scenario = os.getenv("PLACE_SCENARIO") or "snap"
local explode = scenario == "engine"
local commit_then_raise = scenario == "commit_then_raise"
-- Entities the engine created before it raised, keyed by tile.
local committed = {}
-- What fast-replacing the target tile returns to the actor, and the stack that
-- proves the return happened instead of a ground spill.
local replaced_contents = {}
local returned_stack = nil
if scenario == "replace" then
  replaced_contents = {["wooden-chest"] = 1, ["iron-plate"] = 3}
  returned_stack = "wooden-chest"
elseif scenario == "replace_belt" then
  replaced_contents = {["transport-belt"] = 1, ["iron-plate"] = 2}
  returned_stack = "transport-belt"
end
-- Set when the operation ran with a player actor, so the closure below can check
-- that the player was passed to the engine.
local player_used = false

local function is_centre(value)
  return math.abs(value - math.floor(value) - 0.5) < 1e-6
end

local function centre(value)
  return math.floor(value) + 0.5
end

local function tile_key(position)
  return string.format("%d,%d", math.floor(centre(position.x)), math.floor(centre(position.y)))
end

local occupied = {}
if scenario == "blocked" or scenario == "unreplaceable" or scenario == "replace" or scenario == "replace_belt" or scenario == "stack" then
  occupied["9,9"] = true
end
local replaceable = scenario == "replace" or scenario == "replace_belt"
-- The entity already standing on the occupied tile. A plain placement must find
-- it before create_entity, because create_entity ignores collision boxes in the
-- live engine and would stack a second entity on the same tile.
local occupant = nil
-- Collision masks from the base game: a building collides with built objects,
-- a corpse collides with tiles only and therefore blocks nothing.
local MASKS = {
  ["building"] = {layers = {item = true, meltable = true, object = true, player = true, water_tile = true, is_object = true, is_lower_object = true}},
  ["belt"] = {layers = {floor = true, meltable = true, object = true, transport_belt = true, water_tile = true}},
  ["corpse"] = {layers = {doodad = true}, colliding_with_tiles_only = true},
}

local function entity_at(name, kind, position, box, mask)
  return {
    valid = true,
    name = name,
    type = kind,
    position = position,
    bounding_box = {left_top = {x = position.x - box / 2, y = position.y - box / 2}, right_bottom = {x = position.x + box / 2, y = position.y + box / 2}},
    prototype = {collision_mask = MASKS[mask]},
  }
end

local world = {}
if occupied["9,9"] then
  local inserter = scenario == "blocked" or scenario == "stack"
  if inserter then
    occupant = entity_at("burner-inserter", "inserter", {x = 9.5, y = 9.5}, 1, "building")
  else
    occupant = entity_at("wooden-chest", "container", {x = 9.5, y = 9.5}, 1, "building")
  end
  world[#world + 1] = occupant
end
-- An ore patch is not a placement obstacle: belts and most entities are legal
-- over resources, and can_place_entity agrees. The occupancy guard must ignore
-- resources or the whole patch becomes unbuildable (factorio-ai-player-y18).
if scenario == "resource" then
  occupant = entity_at("iron-ore", "resource", {x = 9.5, y = 9.5}, 1, "building")
  occupant.amount = 500
  world[#world + 1] = occupant
end
-- A harvested tree leaves a tree-09-stump corpse with a 0.8 collision box on the
-- tile. Corpses collide with tiles only, so place must build over one
-- (factorio-ai-player-sth).
if scenario == "corpse" then
  world[#world + 1] = entity_at("tree-09-stump", "corpse", {x = 9.5, y = 9.5}, 0.8, "corpse")
end
-- Totals the actor received from fast replaces.
local returned = {}

local player_force = {name = "player"}
local surface = {name = "nauvis"}

function surface.can_place_entity(params)
  if explode then
    return false
  end
  if not is_centre(params.position.x) or not is_centre(params.position.y) then
    return false
  end
  return occupied[tile_key(params.position)] ~= true
end

local function box_contains(box, position)
  return position.x >= box.left_top.x and position.x <= box.right_bottom.x and position.y >= box.left_top.y and position.y <= box.right_bottom.y
end

local function box_overlaps(box, area)
  return box.left_top.x < area[2][1] and box.right_bottom.x > area[1][1] and box.left_top.y < area[2][2] and box.right_bottom.y > area[1][2]
end

function surface.find_entities_filtered(spec)
  local found = {}
  for _, entity in pairs(committed) do
    found[#found + 1] = entity
  end
  for _, entity in ipairs(world) do
    found[#found + 1] = entity
  end
  local selected = {}
  for _, entity in ipairs(found) do
    local keep = true
    if spec.type and entity.type ~= spec.type then
      keep = false
    end
    if keep and spec.position and not box_contains(entity.bounding_box, spec.position) then
      keep = false
    end
    if keep and spec.area and not box_overlaps(entity.bounding_box, spec.area) then
      keep = false
    end
    if keep then
      selected[#selected + 1] = entity
    end
  end
  return selected
end

function surface.can_fast_replace(params)
  assert(params.name ~= nil and params.position ~= nil, "can_fast_replace needs a name and position")
  assert(params.force == player_force, "can_fast_replace must check the actor force")
  return replaceable and occupied[tile_key(params.position)] == true
end

function surface.create_entity(params)
  if explode then
    error("scripted engine failure")
  end
  if params.fast_replace then
    -- The engine only returns the replaced entity and its contents to an actor;
    -- without character or player it spills them on the ground instead.
    -- character is declared after this closure, so identify it by shape.
    assert(params.character ~= nil and params.character.type == "character" and params.character.position.x == 12.5,
      "a fast replace must pass the acting character, not spill")
    if player_used then
      assert(params.player ~= nil and params.player.name == "wricardo",
        "a fast replace by a player must pass that player, not spill")
    else
      assert(params.player == nil, "a character fast replace must not pass a player")
    end
    for name, count in pairs(replaced_contents) do
      returned[name] = (returned[name] or 0) + count
    end
  end
  if scenario == "direction" then
    -- A direction-carrying entity is created with the code the caller asked for;
    -- the mutation must report that engine value, not the requested one.
    assert(params.direction == 8, "place must pass the requested direction to create_entity, got " .. tostring(params.direction))
  end
  local key = tile_key(params.position)
  if occupied[key] then
    if params.fast_replace then
      if not replaceable then
        return nil
      end
    else
      -- The live engine ignores collision boxes when no fast replace is asked
      -- for and stacks a second entity on the same tile. Treat that as the
      -- failure the placement code must prevent.
      error("engine stacked a second entity on tile " .. key)
    end
  end
  local x, y = centre(params.position.x), centre(params.position.y)
  local entity = {
    valid = true,
    name = params.name,
    type = "container",
    position = {x = x, y = y},
    direction = params.direction,
    unit_number = math.floor(x) * 1000 + math.floor(y),
    bounding_box = {left_top = {x = x - 0.5, y = y - 0.5}, right_bottom = {x = x + 0.5, y = y + 0.5}},
    prototype = {collision_mask = MASKS.building},
  }
  if commit_then_raise then
    -- The engine can raise after the entity exists; the mutation must detect it
    -- instead of refunding the item and reporting a failure.
    committed[tile_key(params.position)] = entity
    error("scripted engine failure after commit")
  end
  return entity
end

local inventory = {count = 5}

function inventory.get_item_count(filter)
  if filter.quality ~= "normal" then
    return 0
  end
  return inventory.count
end

function inventory.remove(filter)
  local count = filter.count or 1
  if inventory.count < count then
    return 0
  end
  inventory.count = inventory.count - count
  return count
end

function inventory.insert(filter)
  if next(committed) ~= nil then
    error("item refunded after a committed creation")
  end
  inventory.count = inventory.count + (filter.count or 1)
  return filter.count or 1
end

local character = {
  type = "character",
  object_name = "LuaEntity",
  position = {x = 12.5, y = 10.5},
  build_distance = 20,
  surface = surface,
  force = player_force,
}

function character.get_main_inventory()
  return inventory
end

-- A minimal LuaPlayer: the place mutation reads the main inventory from the
-- player, then the character from it.
local player = {
  name = "wricardo",
  object_name = "LuaPlayer",
  character = character,
  force = player_force,
  -- executeGameOperation prepends a snapshot prelude that reads the player's
  -- surface and force names.
  surface = surface,
}
function player.get_main_inventory()
  return inventory
end

game = {
  -- place reports the tick it created the entity in so a caller can tell a real
  -- placement from an ack that happened in no tick.
  tick = 1024,
  surfaces = {nauvis = surface},
  get_entity_by_unit_number = function(unit)
    if unit == 4 then
      return character
    end
    return nil
  end,
  get_player = function(name)
    if name == "wricardo" then
      player_used = true
      return player
    end
    return nil
  end,
}

defines = {build_check_type = {manual = 1, script = 2, script_ghost = 3}}

prototypes = {
  item = {["iron-chest"] = {place_result = {name = "iron-chest"}}, inserter = {place_result = {name = "inserter"}}, ["transport-belt"] = {place_result = {name = "transport-belt"}}},
  entity = {
    ["iron-chest"] = {type = "container", collision_mask = MASKS.building},
    inserter = {type = "inserter", collision_mask = MASKS.building},
    ["transport-belt"] = {type = "transport-belt", collision_mask = MASKS.belt},
  },
  quality = {normal = {}, rare = {}},
}

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
  local message = message
  io.write(tostring(message), "\n")
  -- The fixture cannot run code after the command, so the fast-replace result
  -- is asserted when the result is printed.
  if returned_stack then
    assert(returned[returned_stack] == 1, "the replaced entity must return to the actor, not spill")
  end
  
end}
