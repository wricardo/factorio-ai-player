package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// inspectionLogistics reads actual inserter targets in one game snapshot.
// Inserters are scanned in a small area around the inspected entity instead of
// the whole surface, and pickup/drop resolve through the shared topology
// helper so idle inserters still report their wiring.
func inspectionLogistics(r *Resolver, obj *EntityInspection) (*InspectionLogistics, error) {
	if obj == nil || obj.Surface == "" || obj.Force == "" || obj.Name == nil || *obj.Name == "" || obj.Position == nil || math.IsNaN(obj.Position.X) || math.IsInf(obj.Position.X, 0) || math.IsNaN(obj.Position.Y) || math.IsInf(obj.Position.Y, 0) {
		return nil, errors.New("inspection surface, force, name, and finite position are required")
	}
	lua := luaFail + transportLineLua + topologyCommonLua + fmt.Sprintf(`local surface_name,force_name,entity_name=%s,%s,%s
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
local bounds=target.bounding_box
local result={}
for _,e in pairs(surface.find_entities_filtered{area={{bounds.left_top.x-1.5,bounds.left_top.y-1.5},{bounds.right_bottom.x+1.5,bounds.right_bottom.y+1.5}},type="inserter"}) do
  local pickup,drop=inserter_targets(e)
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
