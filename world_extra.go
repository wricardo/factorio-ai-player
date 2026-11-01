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
local red_network=network_for(defines.wire_connector_id.circuit_red)
local green_network=network_for(defines.wire_connector_id.circuit_green)
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
        local signal_quality=signal.quality
        if type(signal_quality)~="string" then signal_quality=signal_quality and signal_quality.name or nil end
        signals[#signals+1]={type=signal.type or "item",name=signal.name,quality=signal_quality,value=entry.count}
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
	return `local function serialize_tag(tag)
  local icon
  if tag.icon then
    local icon_type=tag.icon.type
    if not icon_type or icon_type=="" then icon_type="item" end
    icon={type=icon_type,name=tag.icon.name}
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
	if input.Limit < 1 || input.Limit > 1000 {
		return nil, errors.New("limit must be between 1 and 1000")
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
