package main

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// entityCountHarness runs entityCountLua against a stubbed surface holding the
// given catalog, honouring the name and type filters the same way the engine
// does. It returns the printed table so a test can assert grouping.
func entityCountHarness(catalog string) string {
	return `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
defines={entity_status={working=4,no_fuel=53,disabled=6}}
prototypes={entity={
  ["boiler"]={type="boiler"},
  ["steam-engine"]={type="steam-engine"},
  ["stone-furnace"]={type="furnace"},
}}
game={tick=11,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  local catalog=` + catalog + `
  local out={}
  for _,e in ipairs(catalog) do
    local ok=true
    if filter.type~=nil then
      local type_ok=false
      if type(filter.type)=="table" then
        for _,kind in ipairs(filter.type) do if e.type==kind then type_ok=true end end
      elseif e.type==filter.type then type_ok=true end
      ok=ok and type_ok
    end
    if filter.name~=nil then
      local name_ok=false
      if type(filter.name)=="table" then
        for _,want in ipairs(filter.name) do if e.name==want then name_ok=true end end
      elseif e.name==filter.name then name_ok=true end
      ok=ok and name_ok
    end
    if ok then out[#out+1]=e end
  end
  return out
end}}}`
}

// TestEntityCountLuaGroupsByStatus covers the whole-surface tally: rows are one
// per name/type by default, split per engine status when requested, sorted by
// count, and the total sums every group.
func TestEntityCountLuaGroupsByStatus(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	catalog := `{
  {name="boiler",type="boiler",status=53},
  {name="boiler",type="boiler",status=53},
  {name="steam-engine",type="steam-engine",status=4},
  {name="steam-engine",type="steam-engine",status=4},
  {name="steam-engine",type="steam-engine",status=4},
}`
	for _, tc := range []struct {
		name      string
		input     EntityCountInput
		wantTotal int
		wantRows  []string
	}{
		{
			name:      "merged",
			input:     EntityCountInput{Surface: "nauvis"},
			wantTotal: 5,
			wantRows:  []string{"steam-engine:steam-engine:3:", "boiler:boiler:2:"},
		},
		{
			name:      "by status",
			input:     EntityCountInput{Surface: "nauvis", GroupByStatus: true},
			wantTotal: 5,
			wantRows:  []string{"steam-engine:steam-engine:3:working", "boiler:boiler:2:no_fuel"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := entityCountHarness(catalog) + "\n" + entityCountLua(tc.input, nil, nil, nil) + `
assert(printed and not printed.error, "unexpected error: "..tostring(printed and printed.error))
assert(printed.total==` + strconv.Itoa(tc.wantTotal) + `, "total "..tostring(printed.total))
local got={}
for _,row in ipairs(printed.rows) do
  got[#got+1]=row.name..":"..row.type..":"..tostring(row.count)..":"..tostring(row.status_name or "")
end
assert(#got==` + strconv.Itoa(len(tc.wantRows)) + `, "rows = "..table.concat(got,","))
for i,want in ipairs({` + luaQuoteList(tc.wantRows) + `}) do
  assert(got[i]==want, "row "..i.." = "..got[i]..", want "..want)
end
`
			out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// TestEntityCountLuaFiltersByNamesAndTypes keeps the exact filters honest: two
// names in one call tally both, and an unknown prototype type is rejected.
func TestEntityCountLuaFiltersByNamesAndTypes(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	catalog := `{
  {name="boiler",type="boiler",status=4},
  {name="steam-engine",type="steam-engine",status=4},
  {name="stone-furnace",type="furnace",status=53},
}`
	harness := entityCountHarness(catalog) + "\n" +
		entityCountLua(EntityCountInput{Surface: "nauvis"}, []string{"boiler", "stone-furnace"}, nil, nil) + `
assert(printed and not printed.error, "unexpected error: "..tostring(printed and printed.error))
assert(printed.total==2, "total "..tostring(printed.total))
assert(#printed.rows==2, "rows "..#printed.rows)
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestEntityCountLuaRejectsUnknownFilters keeps typo filters from reading as
// "this entity does not exist": an unknown name or a prototype name passed as a
// type must fail the call instead of returning an empty tally.
func TestEntityCountLuaRejectsUnknownFilters(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	for _, tc := range []struct {
		name  string
		input EntityCountInput
		names []string
		types []string
		want  string
	}{
		{name: "unknown name", input: EntityCountInput{Surface: "nauvis"}, names: []string{"bogus-entity"}, want: "unknown entity name: bogus-entity"},
		{name: "prototype name as type", input: EntityCountInput{Surface: "nauvis"}, types: []string{"stone-furnace"}, want: "pass it as name"},
		{name: "unknown type", input: EntityCountInput{Surface: "nauvis"}, types: []string{"bogus-type"}, want: "unknown entity type: bogus-type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := entityCountHarness("{}") + "\n" + entityCountLua(tc.input, tc.names, tc.types, nil) + `
assert(printed and printed.error, "expected a rejected filter")
assert(string.find(printed.error, ` + luaString(tc.want) + `, 1, true), "unexpected message: "..tostring(printed.error))
`
			out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// TestEntityCountLuaAreaIsOptional pins the difference from a capped area scan:
// an omitted area searches the whole surface, and a supplied one bounds the
// engine filter without any 256-tile cap.
func TestEntityCountLuaAreaIsOptional(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	harness := entityCountHarness("{}") + `
local seen
local original=game.surfaces.nauvis.find_entities_filtered
game.surfaces.nauvis.find_entities_filtered=function(filter) seen=filter; return original(filter) end
` + entityCountLua(EntityCountInput{Surface: "nauvis"}, nil, nil, nil) + `
assert(seen.area==nil, "whole-surface scan must not set filter.area")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	bounded := entityCountHarness("{}") + `
local seen
local original=game.surfaces.nauvis.find_entities_filtered
game.surfaces.nauvis.find_entities_filtered=function(filter) seen=filter; return original(filter) end
` + entityCountLua(EntityCountInput{Surface: "nauvis"}, nil, nil, &areaBounds{LeftTopX: -1000, LeftTopY: -1000, RightBottomX: 1000, RightBottomY: 1000}) + `
assert(seen.area~=nil, "bounded scan must set filter.area")
assert(seen.area[1][1]==-1000 and seen.area[2][1]==1000, "area bounds not passed through")
`
	out, err = exec.Command(luaBin, "-e", bounded).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestEntitiesLuaAreaIsOptionalAndRowsCarryStatus covers the two listing
// changes: an omitted area scans the whole surface instead of failing, and each
// row reports the engine status code, its name and health.
func TestEntitiesLuaAreaIsOptionalAndRowsCarryStatus(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	lua := entitiesLua(EntitiesInput{Surface: "nauvis", Limit: 10}, nil)
	harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
defines={entity_status={working=4,no_fuel=53}}
prototypes={entity={}}
local seen
game={tick=11,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  seen=filter
  return {
    {name="boiler",type="boiler",unit_number=1,position={x=11.5,y=-45},force={name="player"},direction=0,status=53,health=200},
  }
end}}}
` + lua + `
assert(printed and not printed.error, "unexpected error: "..tostring(printed and printed.error))
assert(seen.area==nil, "whole-surface scan must not set filter.area")
local row=printed.entities[1]
assert(row.status==53, "status "..tostring(row.status))
assert(row.status_name=="no_fuel", "status_name "..tostring(row.status_name))
assert(row.health==200, "health "..tostring(row.health))
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestOptionalAreaAllowsLargeBoundsWithoutCap pins the validation split: reads
// that scan linearly accept any positive area, while the capped writers keep
// their area-size limit.
func TestOptionalAreaAllowsLargeBoundsWithoutCap(t *testing.T) {
	large := &FactoryAreaInput{LeftTopX: -5000, LeftTopY: -5000, RightBottomX: 5000, RightBottomY: 5000}
	if _, err := optionalArea(large); err != nil {
		t.Fatalf("optionalArea rejected a large positive area: %v", err)
	}
	if _, err := optionalArea(nil); err != nil {
		t.Fatalf("optionalArea rejected an omitted area: %v", err)
	}
	empty := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 0, RightBottomY: 8}
	if _, err := optionalArea(empty); err == nil {
		t.Fatal("optionalArea accepted zero-width bounds")
	}
	if _, err := inputArea(large); err == nil {
		t.Fatal("inputArea lost its area-size cap")
	}
}

func luaQuoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, luaString(value))
	}
	return strings.Join(quoted, ",")
}
