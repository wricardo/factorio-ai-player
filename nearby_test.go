package main

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestNearbyPaginationUsesDeterministicTotalOrder(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	entities := `{
  {name="b",type="tree",position={x=1,y=0},force={name="player"}},
  {name="a",type="tree",position={x=0,y=1},force={name="player"}},
  {name="a",type="tree",position={x=0,y=-1},unit_number=5,force={name="player"}},
  {name="a",type="tree",position={x=-1,y=0},unit_number=5,force={name="player"}},
  {name="z",type="container",position={x=0,y=0},unit_number=9,force={name="player"}}
}`
	tests := []struct {
		order string
		want  []string
	}{
		{"distance", []string{"z:0:0:9", "a:0:1:nil", "b:1:0:nil", "a:0:-1:5", "a:-1:0:5"}},
		{"unit-number", []string{"a:0:1:nil", "b:1:0:nil", "a:0:-1:5", "a:-1:0:5", "z:0:0:9"}},
	}
	for _, tc := range tests {
		t.Run(tc.order, func(t *testing.T) {
			for offset := 0; offset < len(tc.want); offset += 2 {
				page := tc.want[offset:min(offset+2, len(tc.want))]
				expected := make([]string, len(page))
				for i, value := range page {
					expected[i] = luaString(value)
				}
				harness := fmt.Sprintf(`local source=%s
local expected={%s}
game={tick=10,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  if filter.force~=nil then assert(filter.force=="player", "force filter passed to the engine") end
  return source
end}}}
helpers={table_to_json=function(value)
  assert(value.page_info.total==5)
  assert(value.page_info.offset==%d)
  assert(value.page_info.limit==2)
  assert(value.page_info.has_more==(%t))
  assert(#value.entities==#expected)
  for i,e in ipairs(value.entities) do
    local key=e.name..":"..e.position.x..":"..e.position.y..":"..tostring(e.unit_number)
    assert(key==expected[i], "row "..i.." got "..key.." want "..expected[i])
    assert(e.tick==10, "row "..i.." must carry the page tick")
    assert(e.force=="player", "row "..i.." must carry its force")
  end
  return "ok"
end}
rcon={print=function(value) assert(value=="ok") end}
`, entities, strings.Join(expected, ","), offset, offset+2 < len(tc.want))
				out, err := exec.Command(luaBin, "-e", harness+nearbyLua("nauvis", 0, 0, 10, "", "", "player", tc.order, offset, 2, 0)).CombinedOutput()
				if err != nil {
					t.Fatalf("offset %d: %v\n%s", offset, err, out)
				}
			}
		})
	}
}

// TestNearbyUnitNumberReachesLua proves the GraphQL input field maps onto the
// backend command as --unitNumber and that the anchor wins over position: the
// point defaults are always present, so both arrive and only the unit is used.
func TestNearbyUnitNumberReachesLua(t *testing.T) {
	response := `{"tick":7,"surface":"nauvis","page_info":{"total":0,"limit":5,"offset":0,"has_more":false},"entities":[]}`
	unitNumber := "12888"
	tests := []struct {
		name     string
		unit     *string
		position *PositionInput
		contains []string
		excludes []string
	}{
		{
			name:     "unit with position",
			unit:     &unitNumber,
			position: &PositionInput{X: 4, Y: 5},
			contains: []string{"game.get_entity_by_unit_number(12888)", `find_entity_by_unit_number(s,12888,"")`},
			excludes: []string{"local cx,cy=4,5"},
		},
		{
			name:     "position only keeps the point scan",
			position: &PositionInput{X: 4, Y: 5},
			contains: []string{"local cx,cy=4,5"},
			excludes: []string{"game.get_entity_by_unit_number", "find_entity_by_unit_number"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeRCON(t, response)
			result, err := nearby(fake.resolver, &NearbyInput{UnitNumber: tc.unit, Position: tc.position})
			if err != nil {
				t.Fatal(err)
			}
			if result.Tick != 7 {
				t.Fatalf("tick = %v", result.Tick)
			}
			commands := fake.commands()
			if len(commands) != 1 {
				t.Fatalf("rcon calls = %d, want 1", len(commands))
			}
			for _, want := range tc.contains {
				if !strings.Contains(commands[0], want) {
					t.Fatalf("command missing %q:\n%s", want, commands[0])
				}
			}
			for _, unwanted := range tc.excludes {
				if strings.Contains(commands[0], unwanted) {
					t.Fatalf("command must not contain %q:\n%s", unwanted, commands[0])
				}
			}
		})
	}
}

// TestNearbyLuaUnitNumberAnchorsCenter proves the scan center comes from the
// anchored entity's position and its own row sits at distance 0.
func TestNearbyLuaUnitNumberAnchorsCenter(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
local anchor={name="stone-furnace",type="furnace",position={x=10.5,y=20.5},force={name="player"},unit_number=12888,surface={name="nauvis"}}
game={tick=3,get_entity_by_unit_number=function(unit)
  assert(unit==12888, "unit number reaches the engine lookup")
  return anchor
end,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  assert(filter.position.x==10.5 and filter.position.y==20.5, "center must be the anchored position")
  assert(filter.radius==5, "radius filter")
  return {anchor}
end}}}
prototypes={entity={}}
` + nearbyLua("nauvis", 0, 0, 5, "", "", "", "distance", 0, 10, 12888) + `
assert(printed.entities and #printed.entities==1, "one row")
assert(printed.entities[1].unit_number==12888, "anchor row")
assert(printed.entities[1].distance==0, "anchor row distance must be 0")
assert(printed.page_info.total==1, "true total")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestNearbyLuaUnitNumberFallbackScansSurface proves the surface scan resolves
// engine lookups the get-by-unit-number path does not cover.
func TestNearbyLuaUnitNumberFallbackScansSurface(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
local anchor={name="stone-furnace",type="furnace",position={x=-3,y=4},force={name="player"},unit_number=12888,surface={name="nauvis"}}
local other={name="inserter",type="inserter",position={x=1,y=1},force={name="player"},unit_number=9,surface={name="nauvis"}}
game={tick=4,get_entity_by_unit_number=function() return nil end,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  if filter.invert then return {other,anchor} end
  assert(filter.position.x==-3 and filter.position.y==4, "center must come from the scanned anchor")
  return {anchor,other}
end}}}
prototypes={entity={}}
` + nearbyLua("nauvis", 0, 0, 5, "", "", "", "distance", 0, 10, 12888) + `
assert(printed.entities and #printed.entities==2, "two rows")
assert(printed.entities[1].unit_number==12888, "anchor sorts first at distance 0")
assert(printed.entities[1].distance==0, "anchor distance 0")
assert(printed.entities[2].distance==5, "other row distance")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestNearbyLuaUnitNumberErrors proves a missing anchor and a foreign-surface
// anchor fail with actionable, surface-qualified messages.
func TestNearbyLuaUnitNumberErrors(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	tests := []struct {
		name   string
		entity string
		want   []string
	}{
		{name: "missing", entity: "nil", want: []string{"entity not found by unit number on that surface"}},
		{name: "foreign surface", entity: `{name="stone-furnace",type="furnace",position={x=1,y=1},force={name="player"},unit_number=12888,surface={name="vulcanus"}}`, want: []string{"entity 12888 is on surface vulcanus, not nauvis"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			harness := `local printed
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) printed=value end}
local anchor=` + tc.entity + `
game={tick=5,get_entity_by_unit_number=function() return anchor end,surfaces={nauvis={name="nauvis",find_entities_filtered=function(filter)
  if filter.invert then return {} end
  error("radius scan must not run without a resolvable anchor")
end}}}
prototypes={entity={}}
` + nearbyLua("nauvis", 0, 0, 5, "", "", "", "distance", 0, 10, 12888) + `
assert(printed.error, "expected an error message")
assert(#printed==1, "no rows on failure")
for _,want in ipairs({` + luaStringList(tc.want) + `}) do
  assert(string.find(printed.error, want, 1, true), "unexpected message: "..tostring(printed.error))
end
`
			out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}
