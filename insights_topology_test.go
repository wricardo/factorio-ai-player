package main

import (
	"math"
	"os/exec"
	"strings"
	"testing"
)

func TestTopologyBeltParams(t *testing.T) {
	cases := []struct {
		name    string
		surface string
		x, y    float64
		limit   int
		valid   bool
	}{
		{"minimum", "nauvis", -1.5, 0, 1, true},
		{"maximum", "nauvis", 0, 2.5, 1000, true},
		{"missing surface", "", 0, 0, 1, false},
		{"nan x", "nauvis", math.NaN(), 0, 1, false},
		{"infinite x", "nauvis", math.Inf(1), 0, 1, false},
		{"nan y", "nauvis", 0, math.NaN(), 1, false},
		{"infinite y", "nauvis", 0, math.Inf(-1), 1, false},
		{"zero limit", "nauvis", 0, 0, 0, false},
		{"negative limit", "nauvis", 0, 0, -1, false},
		{"excess limit", "nauvis", 0, 0, 1001, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := topologyBeltParams(tc.surface, tc.x, tc.y, tc.limit)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

// Exercise the traversal itself against directed engine-connection fixtures.
// The live Factorio smoke remains responsible for validating the engine API.
func TestTopologyDirectedTraversal(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
defines={entity_status={}}
local surface={index=1,name="nauvis"}
surface.find_entities_filtered=function() return {} end
game={tick=123,surfaces={nauvis=surface}}
local function entity(id,t,x)
  local e={valid=true,unit_number=id,type=t or "transport-belt",name=t or "transport-belt",surface=surface,position={x=x or id,y=0},belt_neighbours={inputs={},outputs={}}}
  local line={owner=e,input_lines={},output_lines={},get_contents=function() return {} end}
  e.get_transport_line=function() return line end
  e.get_max_transport_line_index=function() return 1 end
  return e,line
end
local function connect(a,b)
  a.belt_neighbours.outputs[#a.belt_neighbours.outputs+1]=b
  b.belt_neighbours.inputs[#b.belt_neighbours.inputs+1]=a
end
`
	assertions := `
local function contains(nodes,id)
  for _,n in ipairs(nodes) do if n.unitNumber==id then return true end end
  return false
end
-- Spatial proximity has no influence: only declared engine connections count.
local a=entity(1,"splitter",0)
local b=entity(2,nil,100)
local c=entity(3,nil,200)
local unrelated=entity(99,nil,0.1)
connect(a,b); connect(a,c)
local run=belt_run(b,10)
assert(#run.nodes==3 and #run.edges==2 and not run.truncated)
assert(not contains(run.nodes,99))
assert(#run.sources==1 and run.sources[1].unitNumber==1)
assert(#run.sinks==2 and contains(run.sinks,2) and contains(run.sinks,3))
-- Node bound never invents terminal nodes at an unvisited continuation.
local bounded=belt_run(a,1)
assert(#bounded.nodes==1 and bounded.truncated and #bounded.boundaryEdges==2)
assert(#bounded.sinks==0 and #bounded.sources==1)
for _,edge in ipairs(bounded.boundaryEdges) do assert(edge.boundary) end
-- Cycles terminate, preserve directed edges, and have no structural terminals.
connect(b,a); connect(c,a)
local cycle=belt_run(a,10)
assert(#cycle.nodes==3 and #cycle.edges==4 and not cycle.truncated)
assert(#cycle.sources==0 and #cycle.sinks==0)
local cut_cycle=belt_run(a,1)
assert(cut_cycle.truncated and #cut_cycle.sources==0 and #cut_cycle.sinks==0)
-- Underground and linked pairs need no adjacent map tiles or belt_neighbours.
local u=entity(10,"underground-belt",1000)
local v=entity(11,"underground-belt",1010)
u.belt_to_ground_type="input"; v.belt_to_ground_type="output"; u.neighbours=v; v.neighbours=u
local under=belt_run(v,10)
assert(#under.nodes==2 and #under.edges==1 and under.edges[1].kind=="underground")
assert(under.edges[1].from.unitNumber==10 and under.edges[1].to.unitNumber==11)
local l=entity(12,"linked-belt",2000)
local r=entity(13,"linked-belt",9000)
l.linked_belt_type="input"; r.linked_belt_type="output"; l.linked_belt_neighbour=r; r.linked_belt_neighbour=l
local linked=belt_run(r,10)
assert(#linked.nodes==2 and #linked.edges==1 and linked.edges[1].kind=="linked")
assert(linked.edges[1].from.unitNumber==12 and linked.edges[1].to.unitNumber==13)
-- Engine transport-line links also establish directed connectivity.
local t,tl=entity(20)
local z,zl=entity(21)
tl.output_lines={zl}; zl.input_lines={tl}
local transport=belt_run(z,10)
assert(#transport.nodes==2 and #transport.edges==1)
assert(transport.edges[1].from.unitNumber==20 and transport.edges[1].to.unitNumber==21)
local isolated=belt_run(unrelated,10)
assert(#isolated.nodes==1 and not isolated.truncated and #isolated.edges==0)
assert(#isolated.sources==1 and #isolated.sinks==1)
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + topologyCommonLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("directed topology regression: %v\n%s", err, output)
	}
}
