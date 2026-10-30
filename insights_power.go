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
	if input.Limit < 1 || input.Limit > 1000 {
		return "", errors.New("limit must be between 1 and 1000")
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

func electricNetworksQuery(r *Resolver, input *PowerInsightsInput) (*ElectricNetworkPage, error) {
	if input == nil {
		input = &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 100}
	}
	args, err := powerInsightsArguments(input)
	if err != nil {
		return nil, err
	}
	lua := luaFail + args + `
local by_id, networks = {}, {}
for _, pole in pairs(surface.find_entities_filtered{type="electric-pole", force=force, area=area}) do
  local id = pole.electric_network_id
  if id then
    local network = by_id[id]
    if not network then
      local stats = pole.electric_network_statistics
      local consumption, production = 0, 0
      for name in pairs(stats.input_counts) do
        consumption = consumption + stats.get_flow_count{name=name, category="input", precision_index=defines.flow_precision_index.one_minute} * 60
      end
      for name in pairs(stats.output_counts) do
        production = production + stats.get_flow_count{name=name, category="output", precision_index=defines.flow_precision_index.one_minute} * 60
      end
      network = {numericId=id, id=tostring(id), polesInArea=0, productionWatts=production, consumptionWatts=consumption}
      by_id[id] = network
      networks[#networks+1] = network
    end
    network.polesInArea = network.polesInArea + 1
  end
end
table.sort(networks, function(a,b) return a.numericId < b.numericId end)
local page = {}
for i=offset+1, math.min(#networks, offset+limit) do page[#page+1] = networks[i] end
rcon.print(helpers.table_to_json{tick=game.tick, surface=surface.name, force=force.name, total=#networks, limit=limit, offset=offset, networks=page})`
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
	lua := luaFail + args + `
local infrastructure = {generator=true, ["burner-generator"]=true, ["solar-panel"]=true, boiler=true, reactor=true, ["heat-interface"]=true, ["fusion-reactor"]=true, ["fusion-generator"]=true}
local function get(f)
  local ok, value = pcall(f)
  if ok then return value end
end
local matches = {}
for _, e in pairs(surface.find_entities_filtered{force=force, area=area}) do
  local electric = get(function() return e.prototype.electric_energy_source_prototype end)
  if electric or infrastructure[e.type] then matches[#matches+1] = e end
end
table.sort(matches, function(a,b)
  if a.type ~= b.type then return a.type < b.type end
  if a.name ~= b.name then return a.name < b.name end
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  return (a.unit_number or 0) < (b.unit_number or 0)
end)
local statuses = {}
for name, value in pairs(defines.entity_status) do statuses[value] = name end
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
  local network = get(function() return e.electric_network_id end)
  local unit = e.unit_number
  local status = get(function() return e.status end)
  local burner = get(function() return e.burner end)
  local electric = get(function() return e.prototype.electric_energy_source_prototype end)
  local connected
  if electric or e.type == "generator" or e.type == "burner-generator" or e.type == "solar-panel" or e.type == "fusion-generator" then
    connected = get(function() return e.is_connected_to_electric_network() end)
  end
  page[#page+1] = {name=e.name, type=e.type, position=e.position, unitNumber=unit and tostring(unit), status=status and statuses[status], networkId=network and tostring(network), connectedToProducingNetwork=connected,
    energyJoules=get(function() return e.energy end), electricBufferJoules=get(function() return e.electric_buffer_size end), electricDrainJoulesPerTick=get(function() return e.electric_drain end),
    temperatureCelsius=get(function() return e.temperature end), burningFuelJoules=burner and burner.remaining_burning_fuel, powerFluids=fluid_list}
end
rcon.print(helpers.table_to_json{tick=game.tick, surface=surface.name, force=force.name, total=#matches, limit=limit, offset=offset, powerEntities=page})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[PowerEntityPage](response)
}
