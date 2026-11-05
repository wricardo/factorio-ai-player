package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
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
-- LuaForce.get_evolution_factor(surface) is the 2.0 API; there is no
-- evolution_factor member. An unreadable value degrades to 0.
local function evolution_for(f,target_surface)
  local ok,value=pcall(function() return f.get_evolution_factor(target_surface) end)
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

// circuitNetworkLua reads the signals of one entity's circuit network through a
// chosen wire connector. No actor is resolved: the query is purely
// observational, so the entity is located directly on the requested surface
// without reach checks.
//
// The connector defaults to circuit_<color>, which on a combinator is the input
// connector, so passing combinator_output_red (or any other connector id) is how
// the network a combinator drives is read. The reported networkId identifies the
// shared network, and the circuit count comes from the engine's
// connected_circuit_count, which counts the circuits attached to the network
// rather than this entity's own wires.
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
	connector := strings.TrimSpace(stringValueOr(input.Connector, ""))
	return luaFail + targetLocals(target, name, x, y, surface) + luaSignalTypeName + fmt.Sprintf(`local color_name=%s
local connector_name=%s
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
local used_connector=connector_name
if used_connector=="" then used_connector="circuit_"..color_name end
local wire_id=defines.wire_connector_id[used_connector]
if not wire_id then fail("unknown wire connector "..used_connector); return end
local ok_network,network=pcall(function() return e.get_circuit_network(wire_id) end)
if not ok_network then fail("entity has no "..used_connector.." connector"); return end
local network_id,circuit_count
local signals={}
if network then
  network_id=network.network_id
  local ok_count,count=pcall(function() return network.connected_circuit_count end)
  if ok_count and type(count)=="number" then circuit_count=count end
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
        local signal_quality=signal.quality
        if type(signal_quality)~="string" then signal_quality=signal_quality and signal_quality.name or nil end
        signals[#signals+1]={type=signal_type_name(signal.type),name=signal.name,quality=signal_quality,value=entry.count}
      end
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,name=e.name,unitNumber=e.unit_number,color=%s,
 connector=used_connector,connected=network~=nil,networkId=network_id,networkCircuitCount=circuit_count,signals=signals}))
`, luaString(color), luaString(connector), luaString(strings.ToUpper(color))), nil
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

// circuitNetworksQuery groups every circuit network reachable from the entities
// inside an area by the engine's network id, so a circuit setup can be
// understood without reading entities one by one and diffing signals.
func circuitNetworksQuery(r *Resolver, input CircuitNetworksInput) (*CircuitNetworkPage, error) {
	if err := validateChartTagScope(input.Surface, input.Force); err != nil {
		return nil, err
	}
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	area, err := chartTagAreaLua(input.Area)
	if err != nil {
		return nil, err
	}
	if input.Limit < 1 || input.Limit > maxCircuitScan {
		return nil, limitError("limit", maxCircuitScan)
	}
	if input.MemberLimit < 1 || input.MemberLimit > maxCircuitMembers {
		return nil, limitError("memberLimit", maxCircuitMembers)
	}
	lua := luaFail + luaSignalTypeName + fmt.Sprintf(`local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local force=game.forces[%s]; if not force then fail("force not found"); return end
-- Every wire connector id except the copper power links, which carry no circuit
-- signals.
local connector_ids={}
for name,id in pairs(defines.wire_connector_id) do
  if not string.find(name,"copper") then connector_ids[#connector_ids+1]={name=name,id=id} end
end
table.sort(connector_ids,function(a,b) return a.name<b.name end)
local found=surface.find_entities_filtered{area=%s,force=force,limit=%d}
local networks={}
local order={}
local function network_signals(network)
  local signals={}
  local ok,list=pcall(function() return network.signals end)
  if not ok or not list then
    ok,list=pcall(function() return network.get_signals() end)
  end
  if ok and list then
    for _,entry in pairs(list) do
      local signal=entry and entry.signal
      if signal and type(entry.count)=="number" then
        local quality=signal.quality
        if type(quality)~="string" then quality=quality and quality.name or nil end
        signals[#signals+1]={type=signal_type_name(signal.type),name=signal.name,quality=quality,value=entry.count}
      end
    end
  end
  return signals
end
for _,e in ipairs(found) do
  for _,entry in ipairs(connector_ids) do
    local ok_network,network=pcall(function() return e.get_circuit_network(entry.id) end)
    if ok_network and network then
      local key=network.network_id
      local bucket=networks[key]
      if not bucket then
        local color
        local ok_wire,wire_type=pcall(function() return network.wire_type end)
        if ok_wire and wire_type~=nil then
          if wire_type==defines.wire_type.green then color="GREEN"
          elseif wire_type==defines.wire_type.red then color="RED" end
        end
        bucket={networkId=key,color=color,signals=network_signals(network),members={},truncated=false}
        networks[key]=bucket
        order[#order+1]=key
      end
      if #bucket.members<%d then
        bucket.members[#bucket.members+1]={name=e.name,entityType=e.type,unitNumber=e.unit_number,
          surface=e.surface.name,position={x=e.position.x,y=e.position.y},connector=entry.name}
      else
        bucket.truncated=true
      end
    end
  end
end
local out={}
for _,key in ipairs(order) do
  local bucket=networks[key]
  out[#out+1]={networkId=bucket.networkId,color=bucket.color,signals=bucket.signals,
    members=bucket.members,truncated=bucket.truncated}
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,scannedEntities=#found,networks=out}))
`, luaString(input.Surface), luaString(input.Force), area, input.Limit, input.MemberLimit)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	page, err := factoryDecode[CircuitNetworkPage](response)
	if err != nil {
		return nil, err
	}
	normalizeCircuitNetworkPage(page)
	return page, nil
}

// normalizeCircuitNetworkPage sorts signals, members and networks so repeated
// calls compare positionally, and replaces absent lists with empty ones.
func normalizeCircuitNetworkPage(page *CircuitNetworkPage) {
	networks := page.Networks
	if networks == nil {
		networks = []*CircuitNetworkEntry{}
	}
	for _, network := range networks {
		signals := network.Signals
		if signals == nil {
			signals = []*CircuitSignal{}
		}
		sortCircuitSignals(signals)
		network.Signals = signals
		members := network.Members
		if members == nil {
			members = []*CircuitNetworkMember{}
		}
		sort.Slice(members, func(i, j int) bool {
			left, right := unitNumberSortKey(members[i].UnitNumber), unitNumberSortKey(members[j].UnitNumber)
			if left != right {
				return left < right
			}
			if members[i].Connector != members[j].Connector {
				return members[i].Connector < members[j].Connector
			}
			return members[i].Name < members[j].Name
		})
		network.Members = members
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].NetworkID < networks[j].NetworkID })
	page.Networks = networks
}

// unitNumberSortKey orders members without a unit number after every numbered
// entity, keeping repeated calls positionally comparable.
func unitNumberSortKey(value *UnitNumber) uint64 {
	if value == nil {
		return math.MaxUint64
	}
	parsed, err := strconv.ParseUint(string(*value), 10, 64)
	if err != nil {
		return math.MaxUint64
	}
	return parsed
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

func validateChartTagScope(surface, force string) error {
	if strings.TrimSpace(surface) == "" {
		return errors.New("surface must not be empty")
	}
	if strings.TrimSpace(force) == "" {
		return errors.New("force must not be empty")
	}
	return nil
}

func chartTagAreaLua(area *FactoryAreaInput) (string, error) {
	if area == nil {
		return "nil", nil
	}
	values := []float64{area.LeftTopX, area.LeftTopY, area.RightBottomX, area.RightBottomY}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", errors.New("area coordinates must be finite")
		}
	}
	if area.LeftTopX >= area.RightBottomX || area.LeftTopY >= area.RightBottomY {
		return "", errors.New("area must have positive dimensions")
	}
	return fmt.Sprintf("{{x=%s,y=%s},{x=%s,y=%s}}",
		formatFloat(area.LeftTopX), formatFloat(area.LeftTopY),
		formatFloat(area.RightBottomX), formatFloat(area.RightBottomY)), nil
}

func chartTagSerializationLua() string {
	return luaSignalTypeName + `local function serialize_tag(tag)
  local icon
  if tag.icon then
    icon={type=signal_type_name(tag.icon.type),name=tag.icon.name}
    local quality=tag.icon.quality
    if quality then icon.quality=type(quality)=="string" and quality or quality.name end
  end
  return {tagNumber=tostring(tag.tag_number),position=tag.position,text=tag.text or "",icon=icon}
end
`
}

// chartTagsQuery lists one stable page of force-owned custom map markers.
func chartTagsQuery(r *Resolver, input ChartTagsInput) (*ChartTagPage, error) {
	if err := validateChartTagScope(input.Surface, input.Force); err != nil {
		return nil, err
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be zero or greater")
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	area, err := chartTagAreaLua(input.Area)
	if err != nil {
		return nil, err
	}
	lua := luaFail + fmt.Sprintf(`local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local force=game.forces[%s]; if not force then fail("force not found"); return end
local area=%s
local found=force.find_chart_tags(surface,area)
table.sort(found,function(a,b) return a.tag_number<b.tag_number end)
%slocal offset=%d
local limit=%d
local tags={}
local last=math.min(#found,offset+limit)
for i=offset+1,last do tags[#tags+1]=serialize_tag(found[i]) end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,
  pageInfo={total=#found,limit=limit,offset=offset,hasMore=#found>last},tags=tags}))
`, luaString(input.Surface), luaString(input.Force), area, chartTagSerializationLua(), input.Offset, input.Limit)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ChartTagPage](response)
}

// addChartTag adds a force-visible marker at an arbitrary map position.
func addChartTag(r *Resolver, input AddChartTagInput) (*ChartTagMutationResult, error) {
	if err := validateChartTagScope(input.Surface, input.Force); err != nil {
		return nil, err
	}
	if input.Position == nil {
		return nil, errors.New("position is required")
	}
	if err := validateAgentCoordinates("chart tag position", input.Position.X, input.Position.Y); err != nil {
		return nil, err
	}
	var fields []string
	if input.Text != nil && strings.TrimSpace(*input.Text) != "" {
		fields = append(fields, "text="+luaString(*input.Text))
	}
	if input.Icon != nil {
		icon, err := signalLua(*input.Icon)
		if err != nil {
			return nil, fmt.Errorf("icon: %w", err)
		}
		fields = append(fields, "icon="+icon)
	}
	if len(fields) == 0 {
		return nil, errors.New("nonblank text or icon is required")
	}
	fields = append([]string{fmt.Sprintf("position={x=%s,y=%s}", formatFloat(input.Position.X), formatFloat(input.Position.Y))}, fields...)
	lua := luaFail + fmt.Sprintf(`local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local force=game.forces[%s]; if not force then fail("force not found"); return end
local tag=force.add_chart_tag(surface,{%s})
if not tag then fail("chart tag was not added"); return end
%srcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,tag=serialize_tag(tag)}))
`, luaString(input.Surface), luaString(input.Force), strings.Join(fields, ","), chartTagSerializationLua())
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ChartTagMutationResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, "marked "+result.Surface+positionText(result.Tag.Position)+": "+result.Tag.Text)
	return result, nil
}

// removeChartTag removes a force-owned marker and returns its final snapshot.
func removeChartTag(r *Resolver, input RemoveChartTagInput) (*ChartTagMutationResult, error) {
	if err := validateChartTagScope(input.Surface, input.Force); err != nil {
		return nil, err
	}
	number, err := strconv.ParseUint(input.TagNumber, 10, 32)
	if err != nil {
		return nil, errors.New("tagNumber must be an unsigned 32-bit integer")
	}
	lua := luaFail + fmt.Sprintf(`local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local force=game.forces[%s]; if not force then fail("force not found"); return end
local wanted=%d
local found
for _,tag in pairs(force.find_chart_tags(surface)) do
  if tag.tag_number==wanted then found=tag; break end
end
if not found then fail("chart tag not found on surface for force"); return end
%slocal snapshot=serialize_tag(found)
found.destroy()
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,tag=snapshot}))
`, luaString(input.Surface), luaString(input.Force), number, chartTagSerializationLua())
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ChartTagMutationResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, "removed map marker "+result.Tag.TagNumber+" from "+result.Surface)
	return result, nil
}
