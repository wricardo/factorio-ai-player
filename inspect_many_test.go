package main

import (
	"os/exec"
	"strings"
	"testing"
)

// An omitted actor must run the batch as the named force, so the chunk resolves
// game.forces and the response reports that force.
func TestInspectManyQueryWithoutActorUsesForce(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":12,"surface":"nauvis","force":"enemy","results":[]}`)
	page, err := inspectManyQuery(r, InspectManyInput{
		Force:   "enemy",
		Targets: []*EntityRefInput{{UnitNumber: new("803")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Force != "enemy" {
		t.Fatalf("force = %q, want enemy", page.Force)
	}
	lua := <-commands
	if !strings.Contains(lua, "game.forces") || strings.Contains(lua, "game.players") {
		t.Fatalf("a force-only batch must bind game.forces:\n%s", lua)
	}
	if !strings.Contains(lua, "if not p then fail(\"force not found\"); return end") {
		t.Fatalf("an unknown force must fail before the target loop:\n%s", lua)
	}
}

// An actor still wins when a force is also supplied, so an existing caller keeps
// its actor-scoped errors.
func TestInspectManyQueryActorWinsOverForce(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":12,"surface":"nauvis","force":"player","results":[]}`)
	if _, err := inspectManyQuery(r, InspectManyInput{
		Actor:   &ActorInput{Player: new("wricardo")},
		Force:   "enemy",
		Targets: []*EntityRefInput{{UnitNumber: new("803")}},
	}); err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, "game.players") || strings.Contains(lua, "game.forces") {
		t.Fatalf("an actor batch must resolve the actor, not the force:\n%s", lua)
	}
}

// A force-only batch has to produce the same rows as the same force reached
// through a player, which is what makes actor-dropping safe.
func TestInspectManyForceMatchesPlayerForce(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	page, unit := "nauvis", "803"
	targets, err := inspectManyTargets(InspectManyInput{Surface: &page, Targets: []*EntityRefInput{{UnitNumber: &unit}}})
	if err != nil {
		t.Fatal(err)
	}
	player := "wricardo"
	actorLua, err := inspectManyActorLua(&ActorInput{Player: &player}, page)
	if err != nil {
		t.Fatal(err)
	}
	forceLua, err := inspectManyForceLua("player", page)
	if err != nil {
		t.Fatal(err)
	}
	body := inspectManyLua(targets, false, false)
	setup := luaTestEntityStub + `
game.forces={player={name="player"}}
game.players.wricardo={name="wricardo",force=stub_force("player")}
stub_entity("transport-belt","transport-belt",803,{0.5,-20.5},"player",{get_transport_line=function() return {get_contents=function() return {} end} end})
`
	assertions := `
assert(force_page.force=="player", "force-only page force: "..tostring(force_page.force))
assert(actor_page.force==force_page.force, "page force must match the actor force")
assert(#force_page.results==#actor_page.results, "row count must match")
for i=1,#force_page.results do
  local force_row,actor_row=force_page.results[i],actor_page.results[i]
  assert(force_row.index==actor_row.index, "row index "..i)
  assert(force_row.name==actor_row.name, "row name "..i..": "..tostring(force_row.name).." vs "..tostring(actor_row.name))
  assert(force_row.unit_number==actor_row.unit_number, "row unit "..i)
  assert(force_row.error==actor_row.error, "row error "..i)
  assert(force_row.inspection.name==actor_row.inspection.name, "row inspection "..i)
end
printed="ok"
`
	harness := setup + forceLua + body + "local force_page=printed\n" + actorLua + body + "local actor_page=printed\n" + assertions + `assert(printed=="ok")`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// An unknown force must be rejected by the prelude, before any target is
// resolved, so a typo cannot silently inspect as the wrong force.
func TestInspectManyForceRejectsUnknownForceFirst(t *testing.T) {
	luaBin, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	page, unit := "nauvis", "803"
	targets, err := inspectManyTargets(InspectManyInput{Surface: &page, Targets: []*EntityRefInput{{UnitNumber: &unit}}})
	if err != nil {
		t.Fatal(err)
	}
	forceLua, err := inspectManyForceLua("nobody", page)
	if err != nil {
		t.Fatal(err)
	}
	harness := luaTestEntityStub + `
game.forces={player={name="player"}}
` + luaFail + forceLua + inspectManyLua(targets, false, false) + `
assert(printed.error=="force not found", "unexpected payload: "..tostring(printed.error))
assert(printed.results==nil, "no target may be scanned for an unknown force")
`
	out, err := exec.Command(luaBin, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// A blank force name is a caller mistake, not "the default force".
func TestInspectManyForceRejectsEmptyName(t *testing.T) {
	if _, err := inspectManyForceLua("", "nauvis"); err == nil || !strings.Contains(err.Error(), "force must not be empty") {
		t.Fatalf("expected an empty-force refusal, got %v", err)
	}
}
