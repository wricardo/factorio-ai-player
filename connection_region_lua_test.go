package main

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// routeRegionLua must return every grid it promises and omit the ones that do not
// apply. An empty table used to reach table.concat with a nil row and made every
// connectionPlan call fail, and an encoded {} could not decode into a string
// slice.
func TestRouteRegionGridsAreComplete(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={build_check_type={manual="manual"}}
local surface={name="nauvis"}
surface.can_place_entity=function() return true end
surface.find_entities_filtered=function() return {} end
game={tick=1,surfaces={nauvis=surface},forces={player={name="player"}}}
local function blocks(kind, name, reach)
  local proto={type=kind,name=name}
  if reach then proto.max_underground_distance=reach end
  return {place_result=proto}
end
prototypes={item={["transport-belt"]=blocks("transport-belt","transport-belt"),["pipe"]=blocks("pipe","pipe"),
  ["underground-belt"]=blocks("underground-belt","underground-belt",5),["pipe-to-ground"]=blocks("pipe-to-ground","pipe-to-ground",10)}}
captured=nil
helpers={table_to_json=function(value) captured=value return "" end}
rcon={print=function() end}
local function grid_count(grid)
  if type(grid)~="table" then return -1 end
  return #grid
end
local function grid_width(grid)
  if type(grid)~="table" or #grid==0 then return 0 end
  return #grid[1]
end
`
	type testCase struct {
		name       string
		item       string
		tunnel     string
		pipe       bool
		feedRows   int
		spanRows   int
		openTunnel int
	}
	cases := []testCase{
		{"pipe without tunnel", "pipe", "", true, 0, 0, 0},
		{"pipe with pipe-to-ground", "pipe", "pipe-to-ground", true, 0, 2, 2},
		{"belt without tunnel", "transport-belt", "", false, 2, 0, 0},
		{"belt with underground", "transport-belt", "underground-belt", false, 2, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunk := routeRegionLua("nauvis", "player", tc.item, tc.tunnel, tc.pipe, 0, 0, 3, 2)
			check := `
assert(grid_count(captured.open)==2,"open rows")
assert(grid_width(captured.open)==3,"open columns")
assert(grid_count(captured.fluid)==2,"fluid rows")
assert(grid_count(captured.tunnel)==2,"tunnel rows")
assert(grid_count(captured.openTunnel)==` + strconv.Itoa(tc.openTunnel) + `,"openTunnel rows")
` + gridCheck("feed", tc.feedRows) + gridCheck("spanX", tc.spanRows) + gridCheck("spanY", tc.spanRows)
			cmd := exec.Command(lua, "-")
			cmd.Stdin = strings.NewReader(fixture + chunk + check)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("region grids regression: %v\n%s", err, output)
			}
		})
	}
}

// gridCheck asserts an optional grid: absent when it does not apply, and a full
// grid of rows when it does.
func gridCheck(field string, rows int) string {
	if rows == 0 {
		return `assert(captured.` + field + `==nil,"` + field + ` must be omitted")` + "\n"
	}
	return "assert(grid_count(captured." + field + ")==" + strconv.Itoa(rows) + `,"` + field + ` rows "..tostring(grid_count(captured.` + field + ")))\n"
}
