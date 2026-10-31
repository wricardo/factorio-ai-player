package main

import (
	"errors"
	"fmt"
)

// constructionOrders lists entities that the engine's construction manager has
// registered for deconstruction or upgrade. The engine filter matches only
// marked entities, so an area-less scan is cheap even on a large surface.
func constructionOrders(r *Resolver, input *ConstructionOrdersInput) (*ConstructionOrderPage, error) {
	if input == nil {
		input = &ConstructionOrdersInput{Surface: "nauvis", Limit: 100}
	}
	if input.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	if input.Limit < 1 || input.Limit > 1000 {
		return nil, errors.New("limit must be between 1 and 1000")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	state := input.State
	if state == "" {
		state = ConstructionOrderStateDeconstruction
	}
	if !state.IsValid() {
		return nil, fmt.Errorf("state must be DECONSTRUCTION or UPGRADE, got %q", state)
	}
	filterKey := "to_be_deconstructed"
	if state == ConstructionOrderStateUpgrade {
		filterKey = "to_be_upgraded"
	}
	areaLua := "nil"
	if input.Area != nil {
		bounds, err := inputArea(input.Area)
		if err != nil {
			return nil, err
		}
		areaLua = bounds.lua()
	}
	lua := luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local area=%s
local filter={%s=true}
if area then filter.area=area end
local found=s.find_entities_filtered(filter)
table.sort(found,function(a,b)
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  return (a.unit_number or 0)<(b.unit_number or 0)
end)
local page={}
for i=%d+1, math.min(#found, %d+%d) do
  local e=found[i]
  local entry={name=e.name,type=e.type,position=e.position,surface=s.name,direction=e.direction,unit_number=e.unit_number,force=e.force and e.force.name or nil}
  if %t then
    local ok,target,quality=pcall(function() return e.get_upgrade_target() end)
    if ok and target then entry.upgradeTarget=target.name; entry.upgradeQuality=quality and quality.name or nil end
  end
  page[#page+1]=entry
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,state=%s,page_info={total=#found,limit=%d,offset=%d,has_more=#found>%d},orders=page}))`,
		luaString(input.Surface), areaLua, filterKey, input.Offset, input.Offset, input.Limit,
		state == ConstructionOrderStateUpgrade, luaString(string(state)),
		input.Limit, input.Offset, input.Offset+input.Limit)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ConstructionOrderPage](response)
}
