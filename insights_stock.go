package main

import (
	"fmt"
	"math"
	"strconv"
)

// stockQuery observes supported inventories and belt lines without moving items.
func stockQuery(r *Resolver, input *StockInput) (*StockPage, error) {
	if input == nil {
		input = &StockInput{Surface: "nauvis", Force: "player", Limit: 100, TotalsLimit: 100}
	}
	if input.Surface == "" || input.Force == "" {
		return nil, fmt.Errorf("surface and force are required")
	}
	if input.Limit < 1 || input.Limit > 1000 || input.TotalsLimit < 1 || input.TotalsLimit > 1000 {
		return nil, fmt.Errorf("limit and totalsLimit must be between 1 and 1000")
	}
	if input.Offset < 0 || input.TotalsOffset < 0 {
		return nil, fmt.Errorf("offset and totalsOffset must be non-negative")
	}
	item := ""
	if input.Item != nil {
		item = *input.Item
		if item == "" {
			return nil, fmt.Errorf("item must not be empty")
		}
	}
	area := "nil"
	if input.Area != nil {
		a := input.Area
		values := []float64{a.LeftTopX, a.LeftTopY, a.RightBottomX, a.RightBottomY}
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("area coordinates must be finite")
			}
		}
		if values[0] > values[2] || values[1] > values[3] {
			return nil, fmt.Errorf("area left top must not exceed right bottom")
		}
		area = fmt.Sprintf("{{%s,%s},{%s,%s}}", strconv.FormatFloat(values[0], 'g', -1, 64), strconv.FormatFloat(values[1], 'g', -1, 64), strconv.FormatFloat(values[2], 'g', -1, 64), strconv.FormatFloat(values[3], 'g', -1, 64))
	}
	lua := luaFail + fmt.Sprintf(`
local surface_name, force_name, item = %s, %s, %s
local area, limit, offset, totals_limit, totals_offset = %s, %d, %d, %d, %d
local surface = game.surfaces[surface_name]
if not surface then fail("surface not found: "..surface_name); return end
local force = game.forces[force_name]
if not force then fail("force not found: "..force_name); return end
local function in_area(p)
  return not area or (p.x >= area[1][1] and p.y >= area[1][2] and p.x <= area[2][1] and p.y <= area[2][2])
end
local entries, totals_by_key = {}, {}
local function record(category, name, kind, player, pos, inventory, contents)
  for _, stack in pairs(contents) do
    if item == "" or stack.name == item then
      local quality = stack.quality
      local key = stack.name.."\0"..quality.."\0"..category
      local total = totals_by_key[key]
      if not total then
        total = {item=stack.name, quality=quality, category=category, count=0}
        totals_by_key[key] = total
      end
      total.count = total.count + stack.count
      entries[#entries+1] = {category=category, entityName=name, entityType=kind, player=player,
        position={x=pos.x,y=pos.y}, inventoryKind=inventory,
        stack={name=stack.name, quality=quality, count=stack.count}}
    end
  end
end
local specs = {
  ["container"]={{"chest",defines.inventory.chest}},
  ["logistic-container"]={{"chest",defines.inventory.chest},{"trash",defines.inventory.logistic_container_trash}},
  ["infinity-container"]={{"chest",defines.inventory.chest}},
  ["assembling-machine"]={{"input",defines.inventory.crafter_input},{"output",defines.inventory.crafter_output},{"modules",defines.inventory.crafter_modules},{"trash",defines.inventory.crafter_trash},{"dump",defines.inventory.assembling_machine_dump}},
  ["furnace"]={{"input",defines.inventory.crafter_input},{"output",defines.inventory.crafter_output},{"modules",defines.inventory.crafter_modules},{"trash",defines.inventory.crafter_trash}},
  ["rocket-silo"]={{"input",defines.inventory.crafter_input},{"output",defines.inventory.crafter_output},{"modules",defines.inventory.crafter_modules},{"trash",defines.inventory.crafter_trash},{"rocket",defines.inventory.rocket_silo_rocket},{"silo-trash",defines.inventory.rocket_silo_trash}},
  ["lab"]={{"input",defines.inventory.lab_input},{"modules",defines.inventory.lab_modules},{"trash",defines.inventory.lab_trash}},
  ["mining-drill"]={{"modules",defines.inventory.mining_drill_modules}},
  ["agricultural-tower"]={{"input",defines.inventory.agricultural_tower_input},{"output",defines.inventory.agricultural_tower_output}},
  ["asteroid-collector"]={{"output",defines.inventory.asteroid_collector_output}},
}
local belt_types = { ["transport-belt"]=true, ["underground-belt"]=true, ["splitter"]=true, ["loader"]=true, ["loader-1x1"]=true, ["linked-belt"]=true }
local entities = surface.find_entities_filtered{force=force, area=area}
table.sort(entities, function(a,b)
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  if a.type ~= b.type then return a.type < b.type end
  if a.name ~= b.name then return a.name < b.name end
  return (a.unit_number or 0) < (b.unit_number or 0)
end)
local lines, line_comparisons = {}, 0
for _, entity in ipairs(entities) do
  if in_area(entity.position) then
    local list = specs[entity.type]
    local seen = {}
    local function inventory(inv, kind)
      if inv then
        local index = inv.index
        if index and seen[index] then return end
        if index then seen[index] = true end
        record("stored", entity.name, entity.type, nil, entity.position, kind, inv.get_contents())
      end
    end
    if list then
      for _, spec in ipairs(list) do inventory(entity.get_inventory(spec[2]), spec[1]) end
    end
    if entity.type == "mining-drill" then inventory(entity.get_output_inventory(), "output") end
    if entity.type == "inserter" then
      local stack = entity.held_stack
      if stack and stack.valid_for_read then
        record("in-transit", entity.name, entity.type, nil, entity.position, "held", {{name=stack.name, quality=stack.quality.name, count=stack.count}})
      end
    end
    inventory(entity.get_fuel_inventory(), "fuel")
    inventory(entity.get_burnt_result_inventory(), "burnt-result")
    if belt_types[entity.type] then
      for i=1,entity.get_max_transport_line_index() do
        local line = entity.get_transport_line(i)
        local duplicate = false
        for _, prior in ipairs(lines) do
          line_comparisons = line_comparisons + 1
          if line_comparisons > 1000000 then
            fail("stock belt scan exceeds 1000000 line comparisons; restrict area")
            return
          end
          if line.line_equals(prior) then duplicate = true; break end
        end
        if not duplicate then
          lines[#lines+1] = line
          record("in-transit", entity.name, entity.type, nil, entity.position, "line-"..i, line.get_contents())
        end
      end
    end
  end
end
for _, entity in pairs(surface.find_entities_filtered{type="item-entity", area=area}) do
  local stack = entity.stack
  if in_area(entity.position) and stack and stack.valid_for_read then
    record("ground", entity.name, entity.type, nil, entity.position, "ground", {{name=stack.name, quality=stack.quality.name, count=stack.count}})
  end
end
for _, player in pairs(game.players) do
  local character = player.character
  local p = character and character.position or player.physical_position
  local s = character and character.surface or player.physical_surface
  if player.force == force and s == surface and in_area(p) then
    local inv = character and character.get_main_inventory() or player.get_main_inventory()
    if inv then record("player", character and character.name or "player", "player", player.name, p, "main", inv.get_contents()) end
  end
end
table.sort(entries, function(a,b)
  if a.category ~= b.category then return a.category < b.category end
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  if a.entityType ~= b.entityType then return a.entityType < b.entityType end
  if a.entityName ~= b.entityName then return a.entityName < b.entityName end
  if (a.player or "") ~= (b.player or "") then return (a.player or "") < (b.player or "") end
  if a.inventoryKind ~= b.inventoryKind then return a.inventoryKind < b.inventoryKind end
  if a.stack.name ~= b.stack.name then return a.stack.name < b.stack.name end
  return a.stack.quality < b.stack.quality
end)
local totals = {}
for _, total in pairs(totals_by_key) do totals[#totals+1] = total end
table.sort(totals, function(a,b)
  if a.item ~= b.item then return a.item < b.item end
  if a.quality ~= b.quality then return a.quality < b.quality end
  return a.category < b.category
end)
local page, totals_page = {}, {}
for i=offset+1,math.min(#entries,offset+limit) do page[#page+1] = entries[i] end
for i=totals_offset+1,math.min(#totals,totals_offset+totals_limit) do totals_page[#totals_page+1] = totals[i] end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,total=#entries,limit=limit,offset=offset,locations=page,totalsTotal=#totals,totalsLimit=totals_limit,totalsOffset=totals_offset,totals=totals_page}))`, luaString(input.Surface), luaString(input.Force), luaString(item), area, input.Limit, input.Offset, input.TotalsLimit, input.TotalsOffset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[StockPage](response)
}
