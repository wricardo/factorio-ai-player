package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// inspectionLogistics reads actual inserter targets in one game snapshot.
func inspectionLogistics(r *Resolver, obj *EntityInspection) (*InspectionLogistics, error) {
	if obj == nil || obj.Surface == "" || obj.Force == "" || obj.Name == nil || *obj.Name == "" || obj.Position == nil || math.IsNaN(obj.Position.X) || math.IsInf(obj.Position.X, 0) || math.IsNaN(obj.Position.Y) || math.IsInf(obj.Position.Y, 0) {
		return nil, errors.New("inspection surface, force, name, and finite position are required")
	}
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name,entity_name=%s,%s,%s
local position={x=%s,y=%s}
`, luaString(obj.Surface), luaString(obj.Force), luaString(*obj.Name), strconv.FormatFloat(obj.Position.X, 'g', -1, 64), strconv.FormatFloat(obj.Position.Y, 'g', -1, 64)) + `
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local target
for _,candidate in pairs(surface.find_entities_filtered{name=entity_name,position=position,radius=0.01,force=force}) do
  if candidate.position.x==position.x and candidate.position.y==position.y then
    if target then fail("multiple matching entities at center"); return end
    target=candidate
  end
end
if not target then fail("entity not found at center on force"); return end
local statuses={}; for k,v in pairs(defines.entity_status) do statuses[v]=k end
local function endpoint(e)
  if not e or not e.valid then return nil end
  local obj={name=e.name,type=e.type,surface=e.surface.name,position=e.position,unitNumber=e.unit_number,lanes={}}
  local ok,status=pcall(function() return e.status end); if ok then obj.status=statuses[status] end
  if e.type=="transport-belt" or e.type=="underground-belt" or e.type=="splitter" or e.type=="loader" or e.type=="loader-1x1" or e.type=="linked-belt" then
    for j=1,e.get_max_transport_line_index() do
      local items=e.get_transport_line(j).get_contents()
      table.sort(items,function(a,b) if a.name~=b.name then return a.name<b.name end return (a.quality or "")<(b.quality or "") end)
      obj.lanes[#obj.lanes+1]={index=j,items=items}
    end
  elseif e.type=="inserter" then
    obj.pickupPosition=e.pickup_position; obj.dropPosition=e.drop_position
    local stack=e.held_stack
    if stack and stack.valid_for_read then obj.heldStack={name=stack.name,count=stack.count,quality=stack.quality.name} end
  end
  return obj
end
local result={}
for _,e in pairs(surface.find_entities_filtered{type="inserter"}) do
  local pickup,drop=e.pickup_target,e.drop_target
  if pickup==target or drop==target then
    result[#result+1]={inserter=endpoint(e),pickup=endpoint(pickup),drop=endpoint(drop)}
  end
end
table.sort(result,function(a,b)
  local x,y=a.inserter,b.inserter
  if x.position.x~=y.position.x then return x.position.x<y.position.x end
  if x.position.y~=y.position.y then return x.position.y<y.position.y end
  return (x.unitNumber or 0)<(y.unitNumber or 0)
end)
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,inserters=result})`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[InspectionLogistics](response)
}
