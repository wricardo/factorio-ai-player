package main

import (
	"fmt"
	"math"
	"strconv"
)

func safetyQuery(r *Resolver, input *SafetyInput) (*SafetySnapshot, error) {
	lua, err := safetySnapshotLua(input)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[SafetySnapshot](response)
}

// safetySnapshotLua validates the input and builds the single Lua snapshot that
// backs the safety query.
func safetySnapshotLua(input *SafetyInput) (string, error) {
	if input == nil {
		input = &SafetyInput{Surface: "nauvis", Force: "player", EnemyLimit: 100, DamagedLimit: 100, AlertLimit: 100}
	}
	if input.Surface == "" || input.Force == "" {
		return "", fmt.Errorf("surface and force are required")
	}
	for _, page := range []struct {
		name          string
		limit, offset int
	}{
		{"enemy", input.EnemyLimit, input.EnemyOffset},
		{"damaged", input.DamagedLimit, input.DamagedOffset},
		{"alert", input.AlertLimit, input.AlertOffset},
	} {
		if page.limit < 1 || page.limit > maxPageSize {
			return "", fmt.Errorf("%s limit must be between 1 and %d", page.name, maxPageSize)
		}
		if page.offset < 0 {
			return "", fmt.Errorf("%s offset must be non-negative", page.name)
		}
	}
	format := func(n float64) string { return strconv.FormatFloat(n, 'g', -1, 64) }
	finite := func(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
	area := "nil"
	if a := input.Area; a != nil {
		if !finite(a.LeftTopX) || !finite(a.LeftTopY) || !finite(a.RightBottomX) || !finite(a.RightBottomY) {
			return "", fmt.Errorf("area coordinates must be finite")
		}
		if a.LeftTopX > a.RightBottomX || a.LeftTopY > a.RightBottomY {
			return "", fmt.Errorf("area left top must not exceed right bottom")
		}
		area = fmt.Sprintf("{{%s,%s},{%s,%s}}", format(a.LeftTopX), format(a.LeftTopY), format(a.RightBottomX), format(a.RightBottomY))
	}
	position := "nil"
	if p := input.PollutionPosition; p != nil {
		if !finite(p.X) || !finite(p.Y) {
			return "", fmt.Errorf("pollution position must be finite")
		}
		position = fmt.Sprintf("{x=%s,y=%s}", format(p.X), format(p.Y))
	}
	player := "nil"
	if input.Player != nil {
		if *input.Player == "" {
			return "", fmt.Errorf("player must not be empty")
		}
		player = luaString(*input.Player)
	}
	lua := luaFail + fmt.Sprintf(`
local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local force=game.forces[%s]; if not force then fail("force not found"); return end
local area, pollution_position, player_name=%s,%s,%s
local enemy_limit,enemy_offset,damaged_limit,damaged_offset,alert_limit,alert_offset=%d,%d,%d,%d,%d,%d
local player
if player_name then
  player=game.get_player(player_name)
  if not player then fail("player not found"); return end
  if player.force ~= force then fail("player does not belong to selected force"); return end
end
local status_names, alert_names = {}, {}
for k,v in pairs(defines.entity_status) do status_names[v]=k end
for k,v in pairs(defines.alert_type) do alert_names[v]=k end
-- One reference point for ordering threats: the scan area center when bounded,
-- otherwise the force's spawn.
local base_position
if area then
  base_position={x=(area[1][1]+area[2][1])/2,y=(area[1][2]+area[2][2])/2}
else
  local ok,spawn=pcall(function() return force.get_spawn_position(surface) end)
  base_position=(ok and spawn) or {x=0,y=0}
end
local function distance_to_base(position)
  local dx,dy=position.x-base_position.x,position.y-base_position.y
  return math.sqrt(dx*dx+dy*dy)
end
local pollutant=surface.pollutant_type
local function pollution(pos)
  return {position=pos, amount=surface.get_pollution(pos), pollutant=pollutant and pollutant.name or nil}
end
local function entity_data(e)
  local health=e.health
  local status=e.status
  return {name=e.name,type=e.type,force=e.force.name,position=e.position,unitNumber=e.unit_number,
    health=health,maxHealth=health and e.max_health or nil,status=status and status_names[status] or nil,
    statusCode=status,pollution=pollution(e.position),distance=distance_to_base(e.position)}
end
-- Engine-side force filtering: the surface scan only walks enemy forces for
-- threats and the selected force plus its friends for damage, instead of
-- testing every tree, rock and resource tile afterwards.
local enemy_forces,friend_forces={},{}
for _,candidate in pairs(game.forces) do
  if force.is_enemy(candidate) then enemy_forces[#enemy_forces+1]=candidate.name end
  if candidate==force or force.is_friend(candidate) then friend_forces[#friend_forces+1]=candidate.name end
end
local enemies, damaged = {}, {}
if #enemy_forces>0 then
  for _, e in pairs(surface.find_entities_filtered{area=area,force=enemy_forces}) do enemies[#enemies+1]=e end
end
if #friend_forces>0 then
  for _, e in pairs(surface.find_entities_filtered{area=area,force=friend_forces}) do
    if e.health and e.health < e.max_health then damaged[#damaged+1]=e end
  end
end
local function entity_less(a,b)
  if a.type ~= b.type then return a.type < b.type end
  if a.name ~= b.name then return a.name < b.name end
  if a.position.x ~= b.position.x then return a.position.x < b.position.x end
  if a.position.y ~= b.position.y then return a.position.y < b.position.y end
  if a.force.name ~= b.force.name then return a.force.name < b.force.name end
  return (a.unit_number or 0) < (b.unit_number or 0)
end
-- Threats are ranked by distance to the base, so the nearest danger fills the
-- page first instead of whichever prototype sorts first alphabetically.
local function enemy_less(a,b)
  local da,db=distance_to_base(a.position),distance_to_base(b.position)
  if da~=db then return da<db end
  return entity_less(a,b)
end
local function entity_page(matches, limit, offset, less)
  table.sort(matches,less or entity_less)
  local out={}
  for i=offset+1,math.min(#matches,offset+limit) do out[#out+1]=entity_data(matches[i]) end
  return {entities=out,pageInfo={total=#matches,limit=limit,offset=offset,hasMore=#matches>offset+limit}}
end
local alerts, available, reason = {}, false, nil
if not player then reason="No player requested" else
  available=true
  for _, by_type in pairs(player.get_alerts{surface=surface}) do
    for alert_type, group in pairs(by_type) do
      for _, a in pairs(group) do
        local target=a.target and a.target.valid and a.target or nil
        local pos=a.position or (target and target.position or nil)
        local unknown=area ~= nil and pos == nil
        if not area or not pos or (pos.x >= area[1][1] and pos.y >= area[1][2] and pos.x <= area[2][1] and pos.y <= area[2][2]) then
          local icon=a.icon
          alerts[#alerts+1]={type=alert_names[alert_type],typeCode=alert_type,tick=a.tick,position=pos,
            prototype=a.prototype and a.prototype.name or nil,target=target and entity_data(target) or nil,
            iconType=icon and icon.type or nil,iconName=icon and icon.name or nil,iconQuality=icon and icon.quality or nil,
            messageJSON=a.message and helpers.table_to_json({value=a.message}) or nil,areaMatchUnknown=unknown}
        end
      end
    end
  end
end
table.sort(alerts,function(a,b)
  if a.tick ~= b.tick then return a.tick < b.tick end
  if a.typeCode ~= b.typeCode then return a.typeCode < b.typeCode end
  local ax, bx = a.position and a.position.x or -math.huge, b.position and b.position.x or -math.huge
  if ax ~= bx then return ax < bx end
  local ay, by = a.position and a.position.y or -math.huge, b.position and b.position.y or -math.huge
  if ay ~= by then return ay < by end
  if (a.prototype or "") ~= (b.prototype or "") then return (a.prototype or "") < (b.prototype or "") end
  local au,bu=a.target and a.target.unitNumber or 0,b.target and b.target.unitNumber or 0
  if au ~= bu then return au < bu end
  if (a.iconType or "") ~= (b.iconType or "") then return (a.iconType or "") < (b.iconType or "") end
  if (a.iconName or "") ~= (b.iconName or "") then return (a.iconName or "") < (b.iconName or "") end
  if (a.iconQuality or "") ~= (b.iconQuality or "") then return (a.iconQuality or "") < (b.iconQuality or "") end
  return (a.messageJSON or "") < (b.messageJSON or "")
end)
local alert_page={}
for i=alert_offset+1,math.min(#alerts,alert_offset+alert_limit) do alert_page[#alert_page+1]=alerts[i] end
local function number_or_zero(read)
  local ok,value=pcall(read)
  if ok and type(value)=="number" then return value end
  return 0
end
-- Factorio 2.0 exposes LuaForce.get_evolution_factor(surface); there is no
-- evolution_factor member. An unreadable value degrades to 0.
local evolution_factor=number_or_zero(function() return force.get_evolution_factor(surface) end)
local evolution_by_time=number_or_zero(function() return force.get_evolution_factor_by_time(surface) end)
local evolution_by_pollution=number_or_zero(function() return force.get_evolution_factor_by_pollution(surface) end)
local evolution_by_kills=number_or_zero(function() return force.get_evolution_factor_by_killing_spawners(surface) end)
local total_pollution=number_or_zero(function() return surface.get_total_pollution() end)
-- Kills are recorded in FlowStatistics.input_counts: flows added to the kill
-- statistics arrive as inputs, so that map is the authoritative one here.
local kill_counts={}
do
  local ok,statistics=pcall(function() return force.get_kill_count_statistics(surface) end)
  if ok and statistics then
    local okc,counts=pcall(function() return statistics.input_counts end)
    if okc and counts then
      for key,count in pairs(counts) do
        local kill_name=type(key)=="string" and key or (key and key.name)
        if kill_name and type(count)=="number" then kill_counts[#kill_counts+1]={name=kill_name,count=count} end
      end
    end
  end
end
table.sort(kill_counts,function(a,b)
  if a.name~=b.name then return a.name<b.name end
  return a.count<b.count
end)
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface.name,force=force.name,basePosition=base_position,
 pollution=pollution_position and pollution(pollution_position) or nil,
 enemies=entity_page(enemies,enemy_limit,enemy_offset,enemy_less),damaged=entity_page(damaged,damaged_limit,damaged_offset),
 playerAlerts={available=available,unavailableReason=reason,player=player_name,pageInfo={total=#alerts,limit=alert_limit,offset=alert_offset,hasMore=#alerts>alert_offset+alert_limit},alerts=alert_page},
 evolutionFactor=evolution_factor,evolutionByTime=evolution_by_time,evolutionByPollution=evolution_by_pollution,
 evolutionByKills=evolution_by_kills,totalPollution=total_pollution,killCounts=kill_counts}))
`, luaString(input.Surface), luaString(input.Force), area, position, player, input.EnemyLimit, input.EnemyOffset, input.DamagedLimit, input.DamagedOffset, input.AlertLimit, input.AlertOffset)
	return lua, nil
}
