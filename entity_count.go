package main

import (
	"errors"
	"fmt"
	"strings"
)

// entityCountPayload is the one-call tally the count query decodes.
type entityCountPayload struct {
	Tick    float64           `json:"tick"`
	Surface string            `json:"surface"`
	Total   int               `json:"total"`
	Rows    []*EntityCountRow `json:"rows"`
}

// entityCountQuery tallies entities on a surface by prototype name, engine
// type and optionally engine status in one engine scan. It exists because
// "how many X exist" and "which X are dry" are the questions a listing cannot
// answer without paging the whole surface, and because an area requirement
// would force a caller to stitch the base together out of arbitrary tiles.
func entityCountQuery(r *Resolver, input EntityCountInput) ([]*EntityCountRow, error) {
	if input.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	names, err := cleanEntityCountFilter("name", input.Names)
	if err != nil {
		return nil, err
	}
	types, err := cleanEntityCountFilter("type", input.Types)
	if err != nil {
		return nil, err
	}
	bounds, err := optionalArea(input.Area)
	if err != nil {
		return nil, err
	}
	if input.Force != nil && *input.Force == "" {
		return nil, errors.New("force must not be empty when given")
	}
	response, _, err := executeLua(r.addr, r.password, entityCountLua(input, names, types, bounds))
	if err != nil {
		return nil, err
	}
	payload, err := factoryDecode[entityCountPayload](response)
	if err != nil {
		return nil, err
	}
	return payload.Rows, nil
}

// cleanEntityCountFilter rejects blank filter entries before any game call; the
// Lua side then rejects names and types the game does not know instead of
// silently returning an empty tally.
func cleanEntityCountFilter(kind string, values []string) ([]string, error) {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s filter entries must not be blank", kind)
		}
		cleaned = append(cleaned, value)
	}
	return cleaned, nil
}

// entityCountLua builds the whole-surface tally. Unknown names and types fail
// the call, and groupByStatus splits each name/type group into one row per
// engine status so a caller can see how many entities are dry, unpowered or
// idle without inspecting them one by one.
func entityCountLua(input EntityCountInput, names, types []string, bounds *areaBounds) string {
	filter := ""
	area := ""
	if bounds != nil {
		area = fmt.Sprintf("filter.area=%s; ", bounds.lua())
	}
	if len(names) > 0 {
		filter += fmt.Sprintf("filter.name=%s; ", luaStringList(names))
	}
	if len(types) > 0 {
		filter += fmt.Sprintf("filter.type=%s; ", luaStringList(types))
	}
	if input.Force != nil && *input.Force != "" {
		filter += fmt.Sprintf("filter.force=%s; ", luaString(*input.Force))
	}
	validate := ""
	if len(names) > 0 {
		validate += fmt.Sprintf(`local names=%s
for i=1,#names do if not prototypes.entity[names[i]] then fail("unknown entity name: "..names[i]); return end end
`, luaStringList(names))
	}
	if len(types) > 0 {
		validate += fmt.Sprintf(`local types=%s
for i=1,#types do if not validate_entity_type(types[i]) then return end end
`, luaStringList(types))
	}
	return luaFail + luaValidateEntityType + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local filter={}; %s
%s
local status_names={}
for name,value in pairs(defines.entity_status) do status_names[value]=name end
local groups, rows = {}, {}
for _,e in ipairs(s.find_entities_filtered(filter)) do
  local status=e.status
  local key=e.name.."\0"..e.type.."\0"..tostring(status)
  local row=groups[key]
  if not row then
    row={name=e.name,type=e.type,status=status,status_name=status_names[status] or "unknown",count=0}
    groups[key]=row
    rows[#rows+1]=row
  end
  row.count=row.count+1
end
table.sort(rows,function(a,b)
  if a.count~=b.count then return a.count>b.count end
  if a.name~=b.name then return a.name<b.name end
  if a.type~=b.type then return a.type<b.type end
  return (a.status or 0)<(b.status or 0)
end)
local by_status=%t
if not by_status then
  local merged, merged_rows = {}, {}
  for _,row in ipairs(rows) do
    local key=row.name.."\0"..row.type
    local target=merged[key]
    if not target then
      target={name=row.name,type=row.type,count=0}
      merged[key]=target
      merged_rows[#merged_rows+1]=target
    end
    target.count=target.count+row.count
  end
  rows=merged_rows
  table.sort(rows,function(a,b)
    if a.count~=b.count then return a.count>b.count end
    if a.name~=b.name then return a.name<b.name end
    return a.type<b.type
  end)
end
local total=0
for _,row in ipairs(rows) do total=total+row.count end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,total=total,rows=rows}))`,
		luaString(input.Surface), area+filter, validate, input.GroupByStatus)
}
