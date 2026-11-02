package main

import (
	"errors"
	"fmt"
	"math"
)

const inspectionLogisticsCallLua = `
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
local result={}
local seen={}
local bounds=target.bounding_box
local scan_area={{bounds.left_top.x-1.5,bounds.left_top.y-1.5},{bounds.right_bottom.x+1.5,bounds.right_bottom.y+1.5}}
-- An arm reaches the tile containing its pickup/drop position, which carries a
-- sub-tile offset, so the belts around the inspected entity are indexed by tile
-- instead of relying on a point search that never answers for a belt.
local belt_index=belt_tile_index(surface.find_entities_filtered{area=scan_area,type=belt_types})
local self_entry=nil
if target.type=="inserter" then
  local pickup,drop=inserter_targets(target,belt_index)
  seen[key(target)]=true
  self_entry={inserter=endpoint(target),pickup=endpoint(pickup),drop=endpoint(drop)}
end
local neighbours={}
for _,e in pairs(surface.find_entities_filtered{area=scan_area,type="inserter"}) do
  if not seen[key(e)] then
    local pickup,drop=inserter_targets(e,belt_index)
    if pickup==target or drop==target then
      seen[key(e)]=true
      neighbours[#neighbours+1]={inserter=endpoint(e),pickup=endpoint(pickup),drop=endpoint(drop)}
    end
  end
end
table.sort(neighbours,function(a,b)
  local x,y=a.inserter,b.inserter
  if x.position.x~=y.position.x then return x.position.x<y.position.x end
  if x.position.y~=y.position.y then return x.position.y<y.position.y end
  return (x.unitNumber or 0)<(y.unitNumber or 0)
end)
-- The inspected inserter is always first: it is the entity the caller asked about.
if self_entry then result[#result+1]=self_entry end
for _,entry in ipairs(neighbours) do result[#result+1]=entry end
local payload={tick=game.tick,surface=surface.name,inserters=result}`

// inspectionLogistics reads actual inserter targets in one game snapshot.
// Inserters are scanned in a small area around the inspected entity instead of
// the whole surface, and pickup/drop resolve through the shared topology helper
// so idle inserters still report their wiring. Inspecting an inserter returns its
// own pickup and drop first, because the neighbour scan cannot see them.
func inspectionLogistics(r *Resolver, obj *EntityInspection) (*InspectionLogistics, error) {
	if obj == nil || obj.Surface == "" || obj.Force == "" || obj.Name == nil || *obj.Name == "" || obj.Position == nil || math.IsNaN(obj.Position.X) || math.IsInf(obj.Position.X, 0) || math.IsNaN(obj.Position.Y) || math.IsInf(obj.Position.Y, 0) {
		return nil, errors.New("inspection surface, force, name, and finite position are required")
	}
	lua := topologyChunk(r, "inspection_logistics",
		[]string{luaString(obj.Surface), luaString(obj.Force), luaString(*obj.Name),
			fmt.Sprintf("{x=%s,y=%s}", topologyNumber(obj.Position.X), topologyNumber(obj.Position.Y))},
		fmt.Sprintf(`
local surface_name,force_name,entity_name=%s,%s,%s
local position={x=%s,y=%s}
`, luaString(obj.Surface), luaString(obj.Force), luaString(*obj.Name), topologyNumber(obj.Position.X), topologyNumber(obj.Position.Y)),
		inspectionLogisticsCallLua)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[InspectionLogistics](response)
}
