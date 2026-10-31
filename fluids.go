package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// This file implements direct fluid manipulation (SET/ADD/CLEAR on one
// reachable entity) and a read-only aggregate of fluid contents across a
// surface. Both go through the engine's fluid box API so the reported amounts
// are exactly what the simulation holds, never a re-derivation from recipes.

// fluidWriteModes maps the GraphQL enum to the mode string reported by the
// engine-side write. Lookup also validates the enum.
var fluidWriteModes = map[FluidWriteMode]string{
	FluidWriteModeSet:   "SET",
	FluidWriteModeAdd:   "ADD",
	FluidWriteModeClear: "CLEAR",
}

// setFluid writes into one entity's fluid boxes. SET empties every box first so
// the engine clamps the requested amount to capacity; ADD reports the amount the
// engine actually accepted; CLEAR ignores the fluid name and empties the boxes.
func setFluid(r *Resolver, input SetFluidInput) (*FluidWriteResult, error) {
	mode, ok := fluidWriteModes[input.Mode]
	if !ok {
		return nil, errors.New("mode must be SET, ADD, or CLEAR")
	}
	fluid := stringValueOr(input.Fluid, "")
	amount := 0.0
	if input.Amount != nil {
		amount = *input.Amount
	}
	if mode != "CLEAR" {
		if strings.TrimSpace(fluid) == "" {
			return nil, errors.New("fluid is required for SET and ADD")
		}
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			return nil, errors.New("amount must be a finite number of zero or more")
		}
	}
	temperature := math.NaN()
	if input.Temperature != nil {
		if math.IsNaN(*input.Temperature) || math.IsInf(*input.Temperature, 0) {
			return nil, errors.New("temperature must be finite")
		}
		temperature = *input.Temperature
	}
	target, err := settingsTargetFromRef(input.Actor, input.Target)
	if err != nil {
		return nil, err
	}
	prefix, err := target.lua()
	if err != nil {
		return nil, err
	}
	lua := setFluidLua(prefix, mode, fluid, amount, temperature)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[FluidWriteResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fluidWriteSummary(result))
	return result, nil
}

// setFluidLua renders the engine program for a fluid write. prefix binds the
// actor and resolves the target into `e` with reach already enforced.
func setFluidLua(prefix, mode, fluid string, amount, temperature float64) string {
	temperatureLua := "nil"
	if !math.IsNaN(temperature) {
		temperatureLua = formatFloat(temperature)
	}
	return luaFail + prefix + fmt.Sprintf(`local mode = %s
local fluid_name = %s
local amount, temperature = %s, %s
if fluid_name ~= "" and not prototypes.fluid[fluid_name] then fail("unknown fluid: "..fluid_name); return end
local function read_fluid_contents()
  local ok, contents = pcall(function() return e.get_fluid_contents() end)
  if not ok then fail("read fluid contents: "..tostring(contents)); return nil end
  if type(contents) ~= "table" then return {} end
  return contents
end
local function fluid_amount(value)
  if type(value) == "table" then return value.amount or 0 end
  return value or 0
end
local function total_of(contents, name)
  local total = 0
  for key, value in pairs(contents or {}) do
    if name == "" or key == name then total = total + fluid_amount(value) end
  end
  return total
end
local before_contents = read_fluid_contents()
if not before_contents then return end
local before = total_of(before_contents, fluid_name)
local function total_capacity()
  local total, count = 0, 0
  local ok_count, raw_count = pcall(function() return e.fluids_count end)
  if ok_count and type(raw_count) == "number" then count = raw_count end
  for index = 1, count do
    local ok, capacity = pcall(function() return e.fluidbox.get_capacity(index) end)
    if ok and type(capacity) == "number" then total = total + capacity end
  end
  return total
end
local capacity = total_capacity()
local function clear_fluid()
  local ok, message = pcall(function() e.clear_fluid_inside() end)
  if not ok then fail("clear fluid: "..tostring(message)); return false end
  return true
end
local function insert_fluid()
  local params = {name=fluid_name, amount=amount}
  if temperature ~= nil then params.temperature = temperature end
  local ok, inserted = pcall(function() return e.insert_fluid(params) end)
  if not ok then fail("insert fluid: "..tostring(inserted)); return nil end
  if type(inserted) ~= "number" then return 0 end
  return inserted
end
local inserted = 0
if mode == "CLEAR" then
  if not clear_fluid() then return end
elseif mode == "SET" then
  if not clear_fluid() then return end
  inserted = insert_fluid()
  if inserted == nil then return end
else
  inserted = insert_fluid()
  if inserted == nil then return end
end
local after_contents = read_fluid_contents()
if not after_contents then return end
local after = total_of(after_contents, fluid_name)
local ok_boxes, boxes = pcall(function() return e.fluids_count end)
if not ok_boxes or type(boxes) ~= "number" then boxes = 0 end
rcon.print(helpers.table_to_json({tick=game.tick, surface=e.surface.name, force=e.force.name, name=e.name, unitNumber=(e.unit_number and tostring(e.unit_number) or nil), position={x=e.position.x,y=e.position.y}, fluid=(fluid_name ~= "" and fluid_name or nil), before=before, after=after, capacity=capacity, mode=mode, boxesAffected=boxes, inserted=inserted}))`,
		luaString(mode), luaString(fluid), formatFloat(amount), temperatureLua)
}

// fluidWriteSummary renders the in-game notification for a fluid write.
func fluidWriteSummary(result *FluidWriteResult) string {
	if result == nil {
		return "fluid write failed"
	}
	if result.Fluid == nil || *result.Fluid == "" {
		return fmt.Sprintf("%s %s fluid boxes%s: %.0f to %.0f of %.0f", result.Mode, result.Name, positionText(result.Position), result.Before, result.After, result.Capacity)
	}
	return fmt.Sprintf("%s %s on %s%s: %.0f to %.0f of %.0f", result.Mode, *result.Fluid, result.Name, positionText(result.Position), result.Before, result.After, result.Capacity)
}

// fluidStockQuery aggregates fluid contents of every fluid box on a surface for
// one force (plus the neutral force) and returns independently paginated
// locations and totals.
func fluidStockQuery(r *Resolver, input *FluidStockInput) (*FluidStockPage, error) {
	if input == nil {
		input = &FluidStockInput{Surface: "nauvis", Force: "player", Limit: 100, TotalsLimit: 100}
	}
	if strings.TrimSpace(input.Surface) == "" {
		return nil, errors.New("surface must not be empty")
	}
	if strings.TrimSpace(input.Force) == "" {
		return nil, errors.New("force must not be empty")
	}
	if input.Limit < 1 || input.Limit > 1000 || input.TotalsLimit < 1 || input.TotalsLimit > 1000 {
		return nil, errors.New("limit and totalsLimit must be between 1 and 1000")
	}
	if input.Offset < 0 || input.TotalsOffset < 0 {
		return nil, errors.New("offset and totalsOffset must be non-negative")
	}
	fluid := stringValueOr(input.Fluid, "")
	if input.Fluid != nil && strings.TrimSpace(fluid) == "" {
		return nil, errors.New("fluid must not be empty")
	}
	area := "nil"
	if input.Area != nil {
		bounds := areaBounds{input.Area.LeftTopX, input.Area.LeftTopY, input.Area.RightBottomX, input.Area.RightBottomY}
		if err := bounds.validate(); err != nil {
			return nil, err
		}
		area = bounds.lua()
	}
	lua := fluidStockLua(input.Surface, input.Force, fluid, area, input.Limit, input.Offset, input.TotalsLimit, input.TotalsOffset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[FluidStockPage](response)
}

// fluidStockLua renders the read-only fluid aggregation program. area is either
// "nil" (whole surface) or a bounds constructor.
func fluidStockLua(surface, force, fluid, area string, limit, offset, totalsLimit, totalsOffset int) string {
	return luaFail + fmt.Sprintf(`local surface_name, force_name, fluid_name = %s, %s, %s
local area, limit, offset, totals_limit, totals_offset = %s, %d, %d, %d, %d
local surface = game.surfaces[surface_name]
if not surface then fail("surface not found: "..surface_name); return end
local force = game.forces[force_name]
if not force then fail("force not found: "..force_name); return end
if fluid_name ~= "" and not prototypes.fluid[fluid_name] then fail("unknown fluid: "..fluid_name); return end
local function contents_items(contents)
  local items = {}
  for name, value in pairs(contents or {}) do
    local amount, temp = value, nil
    if type(value) == "table" then amount, temp = value.amount, value.temperature end
    if (amount or 0) > 0 then items[#items+1] = {name=name, amount=amount, temperature=temp} end
  end
  return items
end
local function fluids_of(entity)
  local ok_count, raw_count = pcall(function() return entity.fluids_count end)
  if not ok_count or type(raw_count) ~= "number" or raw_count <= 0 then return {} end
  local ok, contents = pcall(function() return entity.get_fluid_contents() end)
  if not ok or type(contents) ~= "table" then return {} end
  return contents_items(contents)
end
local entries, totals_by_key = {}, {}
local scan_forces = {force_name}
if force_name ~= "neutral" then scan_forces[#scan_forces+1] = "neutral" end
for _, name in ipairs(scan_forces) do
  local scan_force = game.forces[name]
  if scan_force then
    for _, entity in ipairs(surface.find_entities_filtered{force=scan_force, area=area}) do
      if entity.valid and entity.type ~= "entity-ghost" and entity.type ~= "tile-ghost" then
        for _, fluid in ipairs(fluids_of(entity)) do
          if fluid_name == "" or fluid.name == fluid_name then
            local key = fluid.name.."\0stored"
            local total = totals_by_key[key]
            if not total then
              total = {fluid=fluid.name, category="stored", amount=0}
              totals_by_key[key] = total
            end
            total.amount = total.amount + fluid.amount
            entries[#entries+1] = {category="stored", entityName=entity.name, entityType=entity.type,
              position={x=entity.position.x, y=entity.position.y}, inventoryKind="fluid",
              fluid=fluid.name, amount=fluid.amount, temperature=fluid.temperature}
          end
        end
      end
    end
  end
end
table.sort(entries, function(a, b)
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  if a.fluid ~= b.fluid then return a.fluid < b.fluid end
  if a.entityName ~= b.entityName then return a.entityName < b.entityName end
  return a.entityType < b.entityType
end)
local totals = {}
for _, total in pairs(totals_by_key) do totals[#totals+1] = total end
table.sort(totals, function(a, b)
  if a.fluid ~= b.fluid then return a.fluid < b.fluid end
  return a.category < b.category
end)
local page, totals_page = {}, {}
for index = offset+1, math.min(#entries, offset+limit) do page[#page+1] = entries[index] end
for index = totals_offset+1, math.min(#totals, totals_offset+totals_limit) do totals_page[#totals_page+1] = totals[index] end
rcon.print(helpers.table_to_json({tick=game.tick, surface=surface.name, force=force.name, location_page_info={total=#entries,limit=limit,offset=offset,has_more=#entries>offset+limit}, total_page_info={total=#totals,limit=totals_limit,offset=totals_offset,has_more=#totals>totals_offset+totals_limit}, locations=page, totals=totals_page}))`,
		luaString(surface), luaString(force), luaString(fluid), area, limit, offset, totalsLimit, totalsOffset)
}
