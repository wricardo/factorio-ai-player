package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

func powerInsightsArguments(input *PowerInsightsInput) (string, error) {
	if input.Surface == "" || input.Force == "" {
		return "", errors.New("surface and force are required")
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return "", pageLimitError("limit")
	}
	if input.Offset < 0 {
		return "", errors.New("offset must be non-negative")
	}
	area := "nil"
	if input.Area != nil {
		coords := []float64{input.Area.LeftTopX, input.Area.LeftTopY, input.Area.RightBottomX, input.Area.RightBottomY}
		for _, value := range coords {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return "", errors.New("area coordinates must be finite")
			}
		}
		if coords[0] > coords[2] || coords[1] > coords[3] {
			return "", errors.New("area left top must not exceed right bottom")
		}
		area = fmt.Sprintf("{{%s,%s},{%s,%s}}", strconv.FormatFloat(coords[0], 'g', -1, 64), strconv.FormatFloat(coords[1], 'g', -1, 64), strconv.FormatFloat(coords[2], 'g', -1, 64), strconv.FormatFloat(coords[3], 'g', -1, 64))
	}
	return fmt.Sprintf(`
local surface = game.surfaces[%s]
if not surface then fail("surface not found"); return end
local force = game.forces[%s]
if not force then fail("force not found"); return end
local area, limit, offset = %s, %d, %d
`, luaString(input.Surface), luaString(input.Force), area, input.Limit, input.Offset), nil
}

// powerFilterLua validates the optional consumer filters and emits their Lua
// locals. electricNetworks takes only networkId because the other filters
// describe single entities; rejecting them keeps an ignored filter from looking
// like an applied one.
func powerFilterLua(input *PowerInsightsInput, consumers bool) (string, error) {
	status := ""
	if input.Status != nil {
		status = *input.Status
		if status == "" {
			return "", errors.New("status must not be empty")
		}
	}
	network := uint64(0)
	if input.NetworkID != nil {
		parsed, err := strconv.ParseUint(*input.NetworkID, 10, 32)
		if err != nil || parsed == 0 {
			return "", errors.New("networkId must be a positive electric network id")
		}
		network = parsed
	}
	if !consumers {
		if status != "" || input.ConnectedToProducingNetwork != nil || input.OnlyIssues {
			return "", errors.New("status, connectedToProducingNetwork and onlyIssues apply to powerConsumers only")
		}
	}
	connected := "nil"
	if input.ConnectedToProducingNetwork != nil {
		connected = strconv.FormatBool(*input.ConnectedToProducingNetwork)
	}
	precision, err := productionPrecision(input.Window)
	if err != nil {
		return "", err
	}
	// powerConsumers rows are instantaneous, so the network window is a
	// documented no-op there; flow_precision stays defined for the shared prelude.
	_ = consumers
	return fmt.Sprintf(`local want_status, want_connected, want_network, only_issues = %s, %s, %d, %t
local flow_precision = defines.flow_precision_index.%s
`, luaString(status), connected, network, input.OnlyIssues, precision), nil
}

// productionPrecision maps an optional production window to the engine flow
// precision index. Electric networks average their flow statistics over it, so
// capacity headroom and load share the window the caller asked for; an absent
// window means ONE_MINUTE.
func productionPrecision(window *ProductionWindow) (string, error) {
	if window == nil {
		return "one_minute", nil
	}
	switch *window {
	case ProductionWindowOneMinute:
		return "one_minute", nil
	case ProductionWindowTenMinutes:
		return "ten_minutes", nil
	case ProductionWindowOneHour:
		return "one_hour", nil
	default:
		return "", fmt.Errorf("invalid production window %q", *window)
	}
}

func electricNetworksQuery(r *Resolver, input *PowerInsightsInput) (*ElectricNetworkPage, error) {
	if input == nil {
		input = &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 100}
	}
	args, err := powerInsightsArguments(input)
	if err != nil {
		return nil, err
	}
	filters, err := powerFilterLua(input, false)
	if err != nil {
		return nil, err
	}
	lua := luaFail + args + filters + `
local producers = {generator=true, ["burner-generator"]=true, ["solar-panel"]=true, ["fusion-generator"]=true, ["electric-energy-interface"]=true}
local status_names = {}
for name, code in pairs(defines.entity_status) do status_names[code] = name end
local capacity_by_network = {}
local function network_entry(id)
  local entry = capacity_by_network[id]
  if not entry then entry = {capacity=0, charge=0, buffer=0, lowPower=0}; capacity_by_network[id] = entry end
  return entry
end
-- Runtime energy values are joules per tick, so x60 converts them to watts.
for _, entity in pairs(surface.find_entities_filtered{force=force, area=area}) do
  local id = entity.electric_network_id
  if id then
    local entry = network_entry(id)
    if producers[entity.type] then
      local ok, max = pcall(function() return entity.prototype.get_max_energy_production(entity.quality) end)
      if ok and max and max > 0 then entry.capacity = entry.capacity + max * 60 end
    elseif entity.type == "accumulator" then
      entry.charge = entry.charge + (entity.energy or 0)
      local ok, capacity = pcall(function() return entity.prototype.electric_energy_source_prototype.buffer_capacity end)
      if ok and capacity then entry.buffer = entry.buffer + capacity end
    end
    if status_names[entity.status] == "low_power" then entry.lowPower = entry.lowPower + 1 end
  end
end
local by_id, networks = {}, {}
for _, pole in pairs(surface.find_entities_filtered{type="electric-pole", force=force, area=area}) do
  local id = pole.electric_network_id
  if id and (want_network == 0 or id == want_network) then
    local network = by_id[id]
    if not network then
      local stats = pole.electric_network_statistics
      local consumption, production = 0, 0
      for name in pairs(stats.input_counts) do
        consumption = consumption + stats.get_flow_count{name=name, category="input", precision_index=flow_precision} * 60
      end
      for name in pairs(stats.output_counts) do
        production = production + stats.get_flow_count{name=name, category="output", precision_index=flow_precision} * 60
      end
      local capacity = capacity_by_network[id] or {capacity=0, charge=0, buffer=0, lowPower=0}
      network = {numericId=id, id=tostring(id), polesInArea=0, productionWatts=production, consumptionWatts=consumption,
        maxProductionWatts=capacity.capacity, accumulatorChargeJoules=capacity.charge,
        accumulatorCapacityJoules=capacity.buffer, lowPowerConsumers=capacity.lowPower}
      if capacity.capacity > 0 then network.satisfaction = consumption / capacity.capacity end
      by_id[id] = network
      networks[#networks+1] = network
    end
    network.polesInArea = network.polesInArea + 1
  end
end
table.sort(networks, function(a,b) return a.numericId < b.numericId end)
local page = {}
for i=offset+1, math.min(#networks, offset+limit) do page[#page+1] = networks[i] end
rcon.print(helpers.table_to_json{tick=game.tick, surface=surface.name, force=force.name, pageInfo={total=#networks,limit=limit,offset=offset,hasMore=#networks>offset+limit}, networks=page})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ElectricNetworkPage](response)
}

func powerConsumersQuery(r *Resolver, input *PowerInsightsInput) (*PowerEntityPage, error) {
	if input == nil {
		input = &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 100}
	}
	args, err := powerInsightsArguments(input)
	if err != nil {
		return nil, err
	}
	filters, err := powerFilterLua(input, true)
	if err != nil {
		return nil, err
	}
	lua := luaFail + args + filters + `
local infrastructure = {generator=true, ["burner-generator"]=true, ["solar-panel"]=true, boiler=true, reactor=true, ["heat-interface"]=true, ["fusion-reactor"]=true, ["fusion-generator"]=true}
local function get(f)
  local ok, value = pcall(f)
  if ok then return value end
end
local statuses = {}
for name, value in pairs(defines.entity_status) do statuses[value] = name end
if want_status ~= "" then
  local known = false
  for _, name in pairs(statuses) do if name == want_status then known = true end end
  if not known then fail("unknown entity status: "..want_status); return end
end
local function facts(e, electric)
  local network = get(function() return e.electric_network_id end)
  local status = get(function() return e.status end)
  local connected
  if electric or e.type == "generator" or e.type == "burner-generator" or e.type == "solar-panel" or e.type == "fusion-generator" then
    connected = get(function() return e.is_connected_to_electric_network() end)
  end
  return {network=network, status=status and statuses[status], connected=connected}
end
local issue_statuses = {no_power=true, low_power=true, not_plugged_in_electric_network=true}
local filtering = want_status ~= "" or want_connected ~= nil or want_network ~= 0 or only_issues
local function fuel_energy(fuel)
  local joules = 0
  for _, stack in pairs(fuel) do
    local prototype = prototypes.item[stack.name]
    if prototype and prototype.fuel_value then joules = joules + prototype.fuel_value * stack.count end
  end
  return joules
end
local function burner_state(entity)
  local ok_burner, burner = pcall(function() return entity.burner end)
  if not ok_burner or not burner then return nil, nil end
  local fuel = {}
  local ok_inventory, inventory = pcall(function() return entity.get_fuel_inventory() end)
  if ok_inventory and inventory then fuel = inventory.get_contents() end
  table.sort(fuel, function(a,b) if a.name ~= b.name then return a.name < b.name end return a.quality < b.quality end)
  local burning = burner.remaining_burning_fuel
  local seconds
  local ok_usage, usage = pcall(function() return entity.prototype.get_max_energy_usage(entity.quality) end)
  if ok_usage and usage and usage > 0 then
    seconds = ((burning or 0) + fuel_energy(fuel)) / (usage * 60)
  end
  return fuel, seconds
end
local matches = {}
for _, e in pairs(surface.find_entities_filtered{force=force, area=area}) do
  local electric = get(function() return e.prototype.electric_energy_source_prototype end)
  if electric or infrastructure[e.type] then
    local keep = true
    if filtering then
      local f = facts(e, electric)
      if want_status ~= "" and f.status ~= want_status then keep = false end
      if keep and want_connected ~= nil and f.connected ~= want_connected then keep = false end
      if keep and want_network ~= 0 and f.network ~= want_network then keep = false end
      if keep and only_issues then
        local issue = issue_statuses[f.status or ""] or f.connected == false
        if not issue then
          local fuel = burner_state(e)
          issue = fuel ~= nil and #fuel == 0
        end
        if not issue then keep = false end
      end
    end
    if keep then matches[#matches+1] = e end
  end
end
table.sort(matches, function(a,b)
  if a.type ~= b.type then return a.type < b.type end
  if a.name ~= b.name then return a.name < b.name end
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  return (a.unit_number or 0) < (b.unit_number or 0)
end)
local page = {}
for i=offset+1, math.min(#matches, offset+limit) do
  local e = matches[i]
  local fluid_list = {}
  local box = get(function() return e.fluidbox end)
  if box then
    for index=1,#box do
      local fluid = box[index]
      if fluid then fluid_list[#fluid_list+1] = {box=index, name=fluid.name, amount=fluid.amount, temperatureCelsius=fluid.temperature} end
    end
  end
  local unit = e.unit_number
  local burner = get(function() return e.burner end)
  local fuel, seconds = burner_state(e)
  local f = facts(e, get(function() return e.prototype.electric_energy_source_prototype end))
  page[#page+1] = {name=e.name, type=e.type, position=e.position, unitNumber=unit and tostring(unit), status=f.status, networkId=f.network and tostring(f.network), connectedToProducingNetwork=f.connected,
    energyJoules=get(function() return e.energy end), electricBufferJoules=get(function() return e.electric_buffer_size end), electricDrainJoulesPerTick=get(function() return e.electric_drain end),
    temperatureCelsius=get(function() return e.temperature end), burningFuelJoules=burner and burner.remaining_burning_fuel,
    fuel=fuel or {}, estimatedSecondsOfFuel=seconds, powerFluids=fluid_list}
end
rcon.print(helpers.table_to_json{tick=game.tick, surface=surface.name, force=force.name, pageInfo={total=#matches,limit=limit,offset=offset,hasMore=#matches>offset+limit}, powerEntities=page})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[PowerEntityPage](response)
}
