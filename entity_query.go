package main

import (
	"errors"
	"fmt"
)

// luaValidateEntityType rejects a filter value that cannot match anything
// before the engine silently returns no rows. The engine's type filter compares
// prototype *types*, so a prototype name such as stone-furnace matches nothing;
// an unknown string is a caller mistake too. The engine gives no error for
// either, so the check runs here.
//
// Many strings are both an engine type and a prototype name (transport-belt,
// inserter, container, furnace), which is why the engine-type set is checked
// first: only a prototype name that is not an engine type gets the name hint.
const luaValidateEntityType = `local function validate_entity_type(kind)
  if kind==nil or kind=="" then return true end
  for _,prototype in pairs(prototypes.entity) do
    if prototype.type==kind then return true end
  end
  if prototypes.entity[kind] then
    fail("entity type filter must be an engine type, not the prototype name '"..kind.."'; pass it as name")
    return false
  end
  fail("unknown entity type: "..kind)
  return false
end
`

// entitiesQuery pages entities on a surface with optional prototype filters.
// An omitted area scans the whole surface: the engine search is linear in the
// matches and the page bounds the payload, so an area cap would only force
// callers to stitch a base together out of arbitrary tiles.
func entitiesQuery(r *Resolver, input EntitiesInput) (*EntityPage, error) {
	bounds, err := optionalArea(input.Area)
	if err != nil {
		return nil, err
	}
	if input.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	response, _, err := executeLua(r.addr, r.password, entitiesLua(input, bounds))
	if err != nil {
		return nil, err
	}
	return factoryDecode[EntityPage](response)
}

// entitiesLua builds the paged scan, bounded by an area only when one was
// given. The type filter is validated in Lua first, so a prototype name or an
// unknown type fails instead of returning an empty page. Every row carries the
// engine status code and name plus health, so a listing answers what each
// entity is doing without one inspect call per row.
func entitiesLua(input EntitiesInput, bounds *areaBounds) string {
	filter := ""
	kind := ""
	if input.Name != nil && *input.Name != "" {
		filter += fmt.Sprintf("filter.name=%s; ", luaString(*input.Name))
	}
	if input.Type != nil && *input.Type != "" {
		kind = *input.Type
		filter += fmt.Sprintf("filter.type=%s; ", luaString(kind))
	}
	if input.Force != nil && *input.Force != "" {
		filter += fmt.Sprintf("filter.force=%s; ", luaString(*input.Force))
	}
	area := ""
	if bounds != nil {
		area = fmt.Sprintf("filter.area=%s; ", bounds.lua())
	}
	return luaFail + luaValidateEntityType + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
if not validate_entity_type(%s) then return end
local status_names={}
for name,value in pairs(defines.entity_status) do status_names[value]=name end
local filter={}; %s%s
local found=s.find_entities_filtered(filter)
table.sort(found,function(a,b) if a.position.y~=b.position.y then return a.position.y<b.position.y end if a.position.x~=b.position.x then return a.position.x<b.position.x end return (a.unit_number or 0)<(b.unit_number or 0) end)
local page={}
for i=%d+1, math.min(#found, %d+%d) do
  local e=found[i]
  local entry={tick=game.tick,name=e.name,type=e.type,position=e.position,surface=s.name,force=e.force and e.force.name or nil,direction=e.direction,unit_number=e.unit_number,status=e.status,status_name=status_names[e.status] or "unknown",health=e.health}
  if e.type=="resource" then entry.amount=e.amount end
  page[#page+1]=entry
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,page_info={total=#found,limit=%d,offset=%d,has_more=#found>%d},entities=page}))`,
		luaString(input.Surface), luaString(kind), area, filter, input.Offset, input.Offset, input.Limit,
		input.Limit, input.Offset, input.Offset+input.Limit)
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
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	lua := luaFail + transportLineLua + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local function belt_line(e,index)
  local items=e.get_transport_line(index).get_contents()
  local name=transport_line_name(index)
  local seen={}; local distinct=0
  for _,stack in ipairs(items) do
    local key=tostring(stack.name).."|"..tostring(stack.quality or "normal")
    if not seen[key] then seen[key]=true; distinct=distinct+1 end
  end
  return {items=items,mixed=distinct>1,line_name=name,side=transport_line_side(name)}
end
local found=s.find_entities_filtered{area=%s,type="transport-belt"}
table.sort(found,function(a,b) if a.position.y~=b.position.y then return a.position.y<b.position.y end return a.position.x<b.position.x end)
local page={}
for i=%d+1, math.min(#found, %d+%d) do
  local e=found[i]
  page[#page+1]={name=e.name,position=e.position,direction=e.direction,unit_number=e.unit_number,line1=belt_line(e,1),line2=belt_line(e,2)}
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

// optionalArea accepts an omitted area as "the whole surface" and otherwise
// only checks that the bounds are finite and positive, with no size cap.
func optionalArea(area *FactoryAreaInput) (*areaBounds, error) {
	if area == nil {
		return nil, nil
	}
	bounds := areaBounds{
		LeftTopX:     area.LeftTopX,
		LeftTopY:     area.LeftTopY,
		RightBottomX: area.RightBottomX,
		RightBottomY: area.RightBottomY,
	}
	if err := bounds.validateShape(); err != nil {
		return nil, err
	}
	return &bounds, nil
}
