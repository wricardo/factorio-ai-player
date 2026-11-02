package main

import "fmt"

// nearbyLua finds entities around a position, orders them deterministically,
// and returns one page with the true match count. Ordering happens in Lua so the
// pagination is stable for a single snapshot; limit is capped by the caller.
// Every row carries its force and the page tick, and force narrows the engine
// filter when given, so one scan can separate own, neutral and enemy entities.
func nearbyLua(surface string, x, y, radius float64, name, kind, force, ordering string, offset, limit int) string {
	return luaFail + luaValidateEntityType + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
if not validate_entity_type(%s) then return end
local cx,cy=%s,%s
local filter={position={x=cx,y=cy},radius=%s}
local name=%s; local kind=%s; local required_force=%s
if name~="" then filter.name=name end
if kind~="" then filter.type=kind end
if required_force~="" then filter.force=required_force end
local found=s.find_entities_filtered(filter)
local items={}
for _,e in ipairs(found) do
  local dx=e.position.x-cx; local dy=e.position.y-cy
  items[#items+1]={e=e,d=dx*dx+dy*dy}
end
local function tie_less(a,b)
  local au,bu=a.e.unit_number or 0,b.e.unit_number or 0
  if au~=bu then return au<bu end
  if a.e.name~=b.e.name then return a.e.name<b.e.name end
  if a.e.position.y~=b.e.position.y then return a.e.position.y<b.e.position.y end
  if a.e.position.x~=b.e.position.x then return a.e.position.x<b.e.position.x end
  return (a.e.type or "")<(b.e.type or "")
end
local ordering=%s
if ordering=="unit-number" or ordering=="unit_number" then
  table.sort(items,tie_less)
else
  table.sort(items,function(a,b) if a.d~=b.d then return a.d<b.d end return tie_less(a,b) end)
end
local offset=%d; local limit=%d
local entities={}
local first=offset+1; local last=math.min(#items,offset+limit)
for i=first,last do
  local e=items[i].e
  local entry={tick=game.tick,name=e.name,type=e.type,position=e.position,force=e.force and e.force.name or nil,direction=e.direction,unit_number=e.unit_number,distance=math.sqrt(items[i].d)}
  if e.type=="resource" then entry.amount=e.amount end
  entities[#entities+1]=entry
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,page_info={total=#items,limit=limit,offset=offset,has_more=#items>last},entities=entities}))`,
		luaString(surface), luaString(kind), formatFloat(x), formatFloat(y), formatFloat(radius),
		luaString(name), luaString(kind), luaString(force), luaString(ordering), offset, limit)
}
