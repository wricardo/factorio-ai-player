package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// surfaceQueryLua lists every surface with its index, planet name (when the
// running version exposes it), total pollution and the force's evolution
// factor. Sorting happens on the Go side so the response order is stable.
func surfaceQueryLua(force string) (string, error) {
	if force == "" {
		return "", errors.New("force is required")
	}
	return luaFail + fmt.Sprintf(`local force=game.forces[%s]; if not force then fail("force not found"); return end
-- LuaForce.evolution_factor(surface) is a method in Factorio 2.0; the bare
-- field form is the fallback, and an unreadable value degrades to 0.
local function evolution_for(f,target_surface)
  local ok,value=pcall(function() return f.evolution_factor(target_surface) end)
  if ok and type(value)=="number" then return value end
  ok,value=pcall(function() return f.evolution_factor end)
  if ok and type(value)=="number" then return value end
  return 0
end
local surfaces={}
for _,surface in pairs(game.surfaces) do
  local planet
  local pok,p=pcall(function() return surface.planet end)
  if pok and p then planet=p.name end
  local total
  local tok,t=pcall(function() return surface.get_total_pollution() end)
  if tok then total=t end
  surfaces[#surfaces+1]={name=surface.name,index=surface.index,planet=planet,totalPollution=total,evolutionFactor=evolution_for(force,surface)}
end
rcon.print(helpers.table_to_json({tick=game.tick,force=force.name,surfaces=surfaces}))
`, luaString(force)), nil
}

// surfacesQuery reads every surface; force supplies evolution metadata.
func surfacesQuery(r *Resolver, force string) (*SurfacePage, error) {
	lua, err := surfaceQueryLua(force)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	page, err := factoryDecode[SurfacePage](response)
	if err != nil {
		return nil, err
	}
	sort.Slice(page.Surfaces, func(i, j int) bool { return page.Surfaces[i].Index < page.Surfaces[j].Index })
	return page, nil
}

// circuitNetworkLua reads the signals of one entity's red or green circuit
// network. No actor is resolved: the query is purely observational, so the
// entity is located directly on the requested surface without reach checks.
func circuitNetworkLua(input CircuitNetworkInput) (string, error) {
	surfaceField, nameField, unitField, xField, yField := entityRefFields(input.Target)
	surface := stringValueOr(surfaceField, "nauvis")
	if surface == "" {
		return "", errors.New("surface must not be empty")
	}
	name := stringValueOr(nameField, "")
	unit := stringValueOr(unitField, "")
	x, y := math.NaN(), math.NaN()
	if xField != nil {
		x = *xField
	}
	if yField != nil {
		y = *yField
	}
	target, err := entityTarget(name, unit, x, y)
	if err != nil {
		return "", err
	}
	color := strings.ToLower(strings.TrimSpace(input.Color.String()))
	if color != "red" && color != "green" {
		return "", errors.New(`color must be "red" or "green"`)
	}
	return luaFail + targetLocals(target, name, x, y, surface) + fmt.Sprintf(`local color_name=%s
local surface=game.surfaces[surface_name]; if not surface then fail("surface not found"); return end
local e
if target_unit~=0 then
  e=game.get_entity_by_unit_number(target_unit)
  if not e then
    if target_name=="" then fail("prototype is not resolvable by unit number alone; pass entity too"); return end
    for _,candidate in pairs(surface.find_entities_filtered{name=target_name}) do
      if candidate.unit_number==target_unit then e=candidate; break end
    end
    if not e then fail("entity not found by unit number on that surface"); return end
  end
  if target_name~="" and e.name~=target_name then fail("entity name does not match unit number"); return end
  if e.surface~=surface then fail("entity is not on the requested surface"); return end
else
  if target_name=="" then fail("entity name or unit number is required"); return end
  for _,candidate in pairs(surface.find_entities_filtered{name=target_name,position=target_position,radius=0.01}) do
    local c=candidate.position
    if math.abs(c.x-target_position.x)<0.001 and math.abs(c.y-target_position.y)<0.001 then
      if e then fail("multiple matching entities at center"); return end
      e=candidate
    end
  end
  if not e then fail("entity not found at center on the surface"); return end
end
local function network_for(wire)
  local ok,net=pcall(function() return e.get_circuit_network(wire) end)
  if ok then return net end
  return nil
end
local function connection_count(net)
  if not net then return 0 end
  local ok,count=pcall(function() return net.connected_circuit_count end)
  if ok and type(count)=="number" then return count end
  return 0
end
local red_network=network_for(defines.wire_type.red)
local green_network=network_for(defines.wire_type.green)
local network=color_name=="red" and red_network or green_network
local signals={}
if network then
  -- Factorio 2.0 exposes the network's signal array as the signals property;
  -- get_signals() only exists in later versions, so it is a guarded fallback.
  local ok,list=pcall(function() return network.signals end)
  if not ok or not list then
    ok,list=pcall(function() return network.get_signals() end)
  end
  if ok and list then
    for _,entry in pairs(list) do
      local signal=entry and entry.signal
      if signal and type(entry.count)=="number" then
        signals[#signals+1]={type=signal.type,name=signal.name,quality=signal.quality and signal.quality.name or nil,value=entry.count}
      end
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,name=e.name,unitNumber=e.unit_number,color=color_name,
 connected=network~=nil,redConnections=connection_count(red_network),greenConnections=connection_count(green_network),signals=signals}))
`, luaString(color)), nil
}

// circuitNetworkQuery reads one entity's selected circuit network signals.
func circuitNetworkQuery(r *Resolver, input CircuitNetworkInput) (*CircuitNetworkSignals, error) {
	lua, err := circuitNetworkLua(input)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[CircuitNetworkSignals](response)
	if err != nil {
		return nil, err
	}
	signals := result.Signals
	if signals == nil {
		signals = []*CircuitSignal{}
	}
	sortCircuitSignals(signals)
	result.Signals = signals
	return result, nil
}

// sortCircuitSignals orders signals by type, name, quality (unqualified first)
// and value so the response is deterministic.
func sortCircuitSignals(signals []*CircuitSignal) {
	sort.Slice(signals, func(i, j int) bool {
		a, b := signals[i], signals[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		aq, bq := stringValue(a.Quality), stringValue(b.Quality)
		if aq != bq {
			return aq < bq
		}
		return a.Value < b.Value
	})
}
