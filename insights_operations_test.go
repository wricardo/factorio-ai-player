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
  for i=1,#ids do fb[i]={name="water",amount=amounts[ids[i]],temperature=100+ids[i]} end
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
local members=0
for _,segment in ipairs(upstream.fluidSegments) do members=members+#segment.members end
assert(members==3,"every visited box must be listed as a segment member")
assert(upstream.fluidSegments[1].temperature==110,"segment temperature must come from the member boxes")
assert(upstream.fluidSegments[1].members[1].unitNumber=="1" and upstream.fluidSegments[1].members[1].boxIndex==1)
assert(#both.fluidSegments==2 and both.fluidSegments[2].temperature==120)
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

// ghostsQuery must send no area at all when the caller omits it, so the engine
// scans the whole surface, and must keep the bounded form byte-for-byte when an
// area is given. The scripted RCON stands in for the engine; the engine-level
// "no area means every entity" rule is 2.0.77 behavior, so these tests pin the
// request shape and the decoded page instead.
func TestGhostsAreaIsOptional(t *testing.T) {
	const wholeSurface = `{"tick":105,"surface":"nauvis","force":"player","pageInfo":{"total":3,"limit":10,"offset":0,"hasMore":false},"ghostEntries":[{"id":"1:unit:1","kind":"entity-ghost","name":"transport-belt","quality":"normal","position":{"x":1.5,"y":1.5},"placementAlternatives":[],"requestedMaterials":[],"buildable":true}]}`
	r, recorder := serveScriptedRCON(t, []scriptRule{{"storage_stock(area,false)", wholeSurface}})
	page, err := ghostsQuery(r, &GhostsInput{Surface: "nauvis", Force: "player", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Area != nil {
		t.Fatalf("whole-surface page must report a null area: %+v", page.Area)
	}
	if page.PageInfo == nil || page.PageInfo.Total != 3 || len(page.GhostEntries) != 1 {
		t.Fatalf("whole-surface page: %+v", page)
	}
	if got := recorder.seen("local area,limit,offset=nil,10,0"); got != 1 {
		t.Fatalf("whole-surface scan must pass a nil area, saw %d calls", got)
	}
	if got := recorder.seen("local area,limit,offset={{"); got != 0 {
		t.Fatalf("whole-surface scan must not invent bounds, saw %d bounded calls", got)
	}

	bounded, boundedRecorder := serveScriptedRCON(t, []scriptRule{{"storage_stock(area,false)", wholeSurface}})
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}
	if _, err := ghostsQuery(bounded, &GhostsInput{Surface: "nauvis", Force: "player", Area: area, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if got := boundedRecorder.seen("local area,limit,offset={{0,0},{10,10}},10,0"); got != 1 {
		t.Fatalf("bounded scan must keep the existing area literal, saw %d calls", got)
	}
}

func TestGhostsValidation(t *testing.T) {
	r, recorder := serveScriptedRCON(t, nil)
	tooWide := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: maxAreaTiles + 1, RightBottomY: 10}
	cases := []struct {
		name  string
		input *GhostsInput
		want  string
	}{
		{"nil input", nil, "surface, force, and input are required"},
		{"empty surface", &GhostsInput{Force: "player", Limit: 10}, "surface, force, and input are required"},
		{"area too wide", &GhostsInput{Surface: "nauvis", Force: "player", Area: tooWide, Limit: 10}, "no greater than 1024"},
		{"zero limit", &GhostsInput{Surface: "nauvis", Force: "player"}, "limit must be between 1 and 10000"},
		{"high limit", &GhostsInput{Surface: "nauvis", Force: "player", Limit: maxPageSize + 1}, "limit must be between 1 and 10000"},
		{"negative offset", &GhostsInput{Surface: "nauvis", Force: "player", Limit: 10, Offset: -1}, "offset must be non-negative"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := ghostsQuery(r, test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
	if got := len(recorder.commands); got != 0 {
		t.Fatalf("invalid ghost reads must fail before any RCON call, saw %d", got)
	}
}

// The ghost body must hand its whole-surface scope to find_entities_filtered by
// omitting the area key, and still bound both the scan and the page when a box
// is supplied. A fake surface returns ghosts inside and outside the box so the
// totals prove the difference.
func TestOperationsGhostsAreaFilter(t *testing.T) {
	interpreter, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	fixture := `
defines={inventory={chest=1},build_check_type={manual=1,script=2,ghost_revive=3}}
game={tick=50}
local surface={name="nauvis",index=1}
local force={name="player"}
local ghost_filter
local ghosts={
  {type="entity-ghost",unit_number=1,name="entity-ghost",ghost_name="transport-belt",quality={name="normal"},position={x=1.5,y=1.5},item_requests={},ghost_prototype={items_to_place_this={{name="transport-belt",count=1}}},surface=surface},
  {type="entity-ghost",unit_number=2,name="entity-ghost",ghost_name="inserter",quality={name="normal"},position={x=100.5,y=100.5},item_requests={},ghost_prototype={items_to_place_this={{name="inserter",count=1}}},surface=surface},
}
local function inside(e,area)
  if not area then return true end
  return e.position.x>=area[1][1] and e.position.x<=area[2][1] and e.position.y>=area[1][2] and e.position.y<=area[2][2]
end
local rock={type="simple-entity",name="big-sand-rock",quality={name="normal"},position={x=1.5,y=1.5},valid=true}
surface.can_place_entity=function(spec)
  if spec.position and math.abs(spec.position.x-1.5)<0.01 and math.abs(spec.position.y-1.5)<0.01 then return false end
  return true
end
surface.find_entities_filtered=function(filter)
  if filter.area and not filter.type and not filter.name then
    local a=filter.area
    if a[1][1]<=rock.position.x and rock.position.x<=a[2][1] and a[1][2]<=rock.position.y and rock.position.y<=a[2][2] then return {rock} end
    return {}
  end
  if filter.type and type(filter.type)=="table" and filter.type[1]=="entity-ghost" then
    ghost_filter=filter
    local out={}
    for _,e in ipairs(ghosts) do if inside(e,filter.area) then out[#out+1]=e end end
    return out
  end
  return {}
end
helpers={table_to_json=function(value) observed=value; return "ok" end}
rcon={print=function() end}
local function run(area,limit,offset)
`
	checks := `
  return {filter=ghost_filter,total=#matches,entries=entries}
end
local whole=run(nil,10,0)
assert(whole.filter.area==nil,"whole-surface scan must omit the area key")
assert(whole.total==2 and #whole.entries==2,"whole-surface scan must see ghosts outside any box")
assert(whole.entries[1].buildable==false ,"no stock means unbuildable")
local function entry_named(target)
  for _,row in ipairs(whole.entries) do if row.name==target then return row end end
end
local belt=entry_named("transport-belt"); local inserter=entry_named("inserter")
assert(belt and belt.blocked==true,"a colliding simple entity must mark the ghost blocked")
assert(belt.blockingName=="big-sand-rock","the blocking entity must be named")
assert(inserter and inserter.blocked==false,"a clear tile must stay unblocked")
assert(inserter.blockingName==nil,"an unblocked ghost must not name a blocker")
local paged=run(nil,1,1)
assert(paged.total==2 and #paged.entries==1,"whole-surface total must count every ghost while the page stays bounded")
local boxed=run({{0,0},{10,10}},10,0)
assert(boxed.filter.area~=nil,"supplied area must reach the engine filter")
assert(boxed.total==1 and #boxed.entries==1,"a box must only match ghosts inside it")
`
	cmd := exec.Command(interpreter, "-")
	cmd.Stdin = strings.NewReader(fixture + operationsStockLua + operationsGhostsLua + checks)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ghost area filter: %v\n%s", err, out)
	}
}
