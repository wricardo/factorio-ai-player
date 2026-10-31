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
  {name="b",type="tree",position={x=1,y=0}},
  {name="a",type="tree",position={x=0,y=1}},
  {name="a",type="tree",position={x=0,y=-1},unit_number=5},
  {name="a",type="tree",position={x=-1,y=0},unit_number=5},
  {name="z",type="container",position={x=0,y=0},unit_number=9}
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
game={tick=10,surfaces={nauvis={name="nauvis",find_entities_filtered=function() return source end}}}
helpers={table_to_json=function(value)
  assert(value.page_info.total==5)
  assert(value.page_info.offset==%d)
  assert(value.page_info.limit==2)
  assert(value.page_info.has_more==(%t))
  assert(#value.entities==#expected)
  for i,e in ipairs(value.entities) do
    local key=e.name..":"..e.position.x..":"..e.position.y..":"..tostring(e.unit_number)
    assert(key==expected[i], "row "..i.." got "..key.." want "..expected[i])
  end
  return "ok"
end}
rcon={print=function(value) assert(value=="ok") end}
`, entities, strings.Join(expected, ","), offset, offset+2 < len(tc.want))
				out, err := exec.Command(luaBin, "-e", harness+nearbyLua("nauvis", 0, 0, 10, "", "", tc.order, offset, 2)).CombinedOutput()
				if err != nil {
					t.Fatalf("offset %d: %v\n%s", offset, err, out)
				}
			}
		})
	}
}
