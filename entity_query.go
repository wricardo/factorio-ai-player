package main

import (
	"errors"
	"fmt"
)

// entitiesQuery pages entities in an area with optional prototype filters.
func entitiesQuery(r *Resolver, input EntitiesInput) (*EntityPage, error) {
	bounds, err := inputArea(input.Area)
	if err != nil {
		return nil, err
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
	filter := ""
	if input.Name != nil && *input.Name != "" {
		filter += fmt.Sprintf("filter.name=%s; ", luaString(*input.Name))
	}
	if input.Type != nil && *input.Type != "" {
		filter += fmt.Sprintf("filter.type=%s; ", luaString(*input.Type))
	}
	lua := luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local area=%s; local filter={area=area}; %s
local found=s.find_entities_filtered(filter)
table.sort(found,function(a,b) if a.position.y~=b.position.y then return a.position.y<b.position.y end if a.position.x~=b.position.x then return a.position.x<b.position.x end return (a.unit_number or 0)<(b.unit_number or 0) end)
local page={}
for i=%d+1, math.min(#found, %d+%d) do
  local e=found[i]
  local entry={name=e.name,type=e.type,position=e.position,surface=s.name,direction=e.direction,unit_number=e.unit_number}
  if e.type=="resource" then entry.amount=e.amount end
  page[#page+1]=entry
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,page_info={total=#found,limit=%d,offset=%d,has_more=#found>%d},entities=page}))`,
		luaString(input.Surface), bounds.lua(), filter, input.Offset, input.Offset, input.Limit,
		input.Limit, input.Offset, input.Offset+input.Limit)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[EntityPage](response)
}

// beltsQuery pages transport belts and their lane contents in an area.
func beltsQuery(r *Resolver, input BeltsInput) (*BeltPage, error) {
	bounds, err := inputArea(input.Area)
	if err != nil {
		return nil, err
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
	lua := luaFail + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local found=s.find_entities_filtered{area=%s,type="transport-belt"}
table.sort(found,function(a,b) if a.position.y~=b.position.y then return a.position.y<b.position.y end return a.position.x<b.position.x end)
local page={}
for i=%d+1, math.min(#found, %d+%d) do
  local e=found[i]
  page[#page+1]={position=e.position,direction=e.direction,unit_number=e.unit_number,line1={items=e.get_transport_line(1).get_contents()},line2={items=e.get_transport_line(2).get_contents()}}
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,page_info={total=#found,limit=%d,offset=%d,has_more=#found>%d},belts=page}))`,
		luaString(input.Surface), bounds.lua(), input.Offset, input.Offset, input.Limit,
		input.Limit, input.Offset, input.Offset+input.Limit)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltPage](response)
}

func inputArea(area *FactoryAreaInput) (areaBounds, error) {
	if area == nil {
		return areaBounds{}, errors.New("area is required")
	}
	bounds := areaBounds{
		LeftTopX:     area.LeftTopX,
		LeftTopY:     area.LeftTopY,
		RightBottomX: area.RightBottomX,
		RightBottomY: area.RightBottomY,
	}
	if err := bounds.validate(); err != nil {
		return bounds, err
	}
	return bounds, nil
}
