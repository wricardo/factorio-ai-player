package main

import (
	"os/exec"
	"strings"
	"testing"
)

// These tests exercise traversal/accounting decisions rather than substituting
// for a live Factorio API smoke. Fixtures model shared segment storage and
// independently connected pump ports, where double counting is easy to miss.
func TestOperationsFluidTraversal(t *testing.T) {
	interpreter, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	fixture := `
local surface={name="nauvis",index=1}
local force={name="player"}
game={tick=100}
local observed
helpers={table_to_json=function(value) observed=value; return "ok" end}
rcon={print=function() end}
local function entity(unit,ids)
  local e={valid=true,unit_number=unit,type="pipe",name="pipe",surface=surface,force=force,position={x=unit,y=0}}
  local fb={owner=e,valid=true}
  e.fluidbox=fb
  local capacities={[10]=300,[20]=200}
  local amounts={[10]=150,[20]=25}
  for i=1,#ids do fb[i]={name="water",amount=amounts[ids[i]]} end
  fb.get_capacity=function(index) return capacities[ids[index]] end
  fb.get_fluid_segment_id=function(index) return ids[index] end
  fb.get_fluid_segment_contents=function(index) return {water=amounts[ids[index]]} end
  local ports={}; for i=1,#ids do ports[i]={} end
  fb.get_pipe_connections=function(index) return ports[index] end
  return e,ports
end
local pipe,pp=entity(1,{10})
local tank,tp=entity(2,{10})
local pump,mp=entity(3,{10,20}); pump.type="pump"; pump.name="pump"
local outlet,op=entity(4,{20})
local function connect(ap,ai,bp,bi,a,b)
  ap[ai][#ap[ai]+1]={target=b.fluidbox,target_fluidbox_index=bi,flow_direction="input-output",connection_type="normal"}
  bp[bi][#bp[bi]+1]={target=a.fluidbox,target_fluidbox_index=ai,flow_direction="input-output",connection_type="normal"}
end
connect(pp,1,tp,1,pipe,tank)
connect(tp,1,mp,1,tank,pump)
connect(mp,2,op,1,pump,outlet)
local function traverse(target,max_boxes)
`
	checks := `
return observed
end
local upstream=traverse(pipe,100)
assert(upstream.visitedBoxes==3,"must visit connected pipe, tank and pump input only")
assert(#upstream.fluidSegments==1,"shared storage counted once")
assert(upstream.totalCapacity==300 and upstream.totalAmount==150,"shared segment totals must not multiply per box")
assert(not upstream.truncated)
for _,box in ipairs(upstream.fluidBoxes) do assert(box.id~="1:unit:3:box:2","pump output must not bridge input") end
local both=traverse(pump,100)
assert(both.visitedBoxes==5 and #both.rootBoxIds==2,"pump inspection seeds both separate boxes")
assert(#both.fluidSegments==2 and both.totalAmount==175 and both.totalCapacity==500)
for _,edge in ipairs(both.fluidConnections) do
  assert(not(edge.fromBoxId=="1:unit:3:box:1" and edge.toBoxId=="1:unit:3:box:2"),"no invented internal pump connection")
end
local bounded=traverse(pipe,1)
assert(bounded.truncated and bounded.visitedBoxes==1)
assert(bounded.totalAmount==150 and bounded.totalCapacity==300,"bounded traversal still reports complete visited segment storage")
local dry,ports=entity(5,{10})
dry.fluidbox.get_fluid_segment_id=function() return nil end
dry.fluidbox.get_fluid_segment_contents=function() error("non-segment boxes must not request segment contents") end
dry.fluidbox.get_capacity=function() return 50 end
dry.fluidbox[1]=nil
-- Preserve the engine's box count independently of fluid presence.
setmetatable(dry.fluidbox,{__len=function() return 1 end})
local empty=traverse(dry,100)
assert(empty.visitedBoxes==1 and empty.totalAmount==0 and empty.totalCapacity==50)
assert(#empty.fluidSegments==1 and #empty.fluidSegments[1].segmentFluids==0)
`
	cmd := exec.Command(interpreter, "-")
	cmd.Stdin = strings.NewReader(fixture + operationsStockLua + operationsFluidNetworkLua + checks)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fluid traversal: %v\n%s", err, out)
	}
}

func TestOperationsMissingScience(t *testing.T) {
	interpreter, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	fixture := `
defines={inventory={lab_input=1}}
local research={prototype={},research_unit_ingredients={{name="red",amount=2},{name="green",amount=1}}}
local lab={type="lab",prototype={lab_inputs={"red","green"}},get_inventory=function()
  return {get_contents=function() return {{name="red",quality="normal",count=1},{name="red",quality="uncommon",count=1}} end}
end}
`
	checks := `
local missing=missing_packs(lab,research)
assert(#missing==1 and missing[1].name=="green" and missing[1].missing==1 and missing[1].supported)
lab.prototype.lab_inputs={"green"}
missing=missing_packs(lab,research)
assert(#missing==2,"unsupported ingredient remains actionable even with stock present")
assert(missing[2].name=="red" and not missing[2].supported and missing[2].missing==0)
assert(#missing_packs(lab,nil)==0)
assert(#missing_packs({type="assembling-machine"},research)==0)
research.prototype.research_trigger={type="craft-item"}
assert(#missing_packs(lab,research)==0)
`
	cmd := exec.Command(interpreter, "-")
	cmd.Stdin = strings.NewReader(fixture + operationsScienceLua + checks)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("missing science: %v\n%s", err, out)
	}
}
