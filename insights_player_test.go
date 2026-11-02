package main

import (
	"strings"
	"testing"
)

// entity + position is the common identity form, so a target that names a
// prototype must resolve that entity and answer engine reachability instead of
// falling back to the position-only explanation.
func TestPlayerInsightEntityPositionTarget(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":9,"player":"wricardo","connected":true,"controllerType":1,"surface":"nauvis","force":"player","position":{"x":40,"y":-6.5},"hasCharacter":true,"reach":{"targetName":"boiler","targetPosition":{"x":45,"y":-6.5},"canReachEntity":true,"distance":5}}`)
	result, err := playerInsight(r, &PlayerInsightInput{
		Player: "wricardo",
		Target: &EntityRefInput{Entity: new("boiler"), Position: &PositionInput{X: 45, Y: -6.5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		`local target={position={x=45,y=-6.5},name=` + luaString("boiler") + `,surface=nil}`,
		`surface.find_entities_filtered{name=target.name,position=target.position,radius=0.01}`,
		`reach.canReachEntity=c.can_reach_entity(entity)`,
		`reach.targetName=entity.name`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("playerInsight Lua missing %q\n%s", want, lua)
		}
	}
	if result.Reach == nil || result.Reach.TargetName == nil || *result.Reach.TargetName != "boiler" {
		t.Fatalf("target name: %+v", result.Reach)
	}
	if result.Reach.CanReachEntity == nil || !*result.Reach.CanReachEntity {
		t.Fatalf("canReachEntity: %+v", result.Reach)
	}
	if result.Reach.Distance == nil || *result.Reach.Distance != 5 || result.Reach.UnavailableReason != nil {
		t.Fatalf("reach facts: %+v", result.Reach)
	}
}

// A position-only target keeps the honest explanation, and a named target whose
// prototype is not there fails instead of silently reporting a point.
func TestPlayerInsightPositionOnlyStaysPointLike(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":9,"player":"wricardo","connected":true,"controllerType":1,"surface":"nauvis","force":"player","position":{"x":40,"y":-6.5},"hasCharacter":true,"reach":{"targetPosition":{"x":45,"y":-6.5},"distance":5,"unavailableReason":"Factorio exposes entity reachability, not position reachability"}}`)
	result, err := playerInsight(r, &PlayerInsightInput{
		Player: "wricardo",
		Target: &EntityRefInput{Position: &PositionInput{X: 45, Y: -6.5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, `local target={position={x=45,y=-6.5},name=nil,surface=nil}`) {
		t.Fatalf("position-only target must not name an entity:\n%s", lua)
	}
	if !strings.Contains(lua, `reach.unavailableReason="Factorio exposes entity reachability, not position reachability"`) {
		t.Fatalf("position-only explanation missing:\n%s", lua)
	}
	if result.Reach == nil || result.Reach.TargetName != nil || result.Reach.CanReachEntity != nil {
		t.Fatalf("position-only reach: %+v", result.Reach)
	}
	if result.Reach.UnavailableReason == nil || !strings.Contains(*result.Reach.UnavailableReason, "not position reachability") {
		t.Fatalf("unavailable reason: %+v", result.Reach.UnavailableReason)
	}
}

// unitNumber and position stay mutually exclusive, and a named target still
// carries its surface for the scan.
func TestPlayerInsightTargetValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	if _, err := playerInsight(r, &PlayerInsightInput{
		Player: "wricardo",
		Target: &EntityRefInput{UnitNumber: new("42"), Position: &PositionInput{X: 1, Y: 1}},
	}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutually exclusive error, got %v", err)
	}
	served, commands := serveLuaOnce(t, `{"tick":9,"player":"wricardo","connected":true,"controllerType":1,"surface":"nauvis","force":"player","position":{"x":40,"y":-6.5},"hasCharacter":true}`)
	if _, err := playerInsight(served, &PlayerInsightInput{
		Player: "wricardo",
		Target: &EntityRefInput{Entity: new("boiler"), Surface: new("nauvis"), Position: &PositionInput{X: 1, Y: 1}},
	}); err != nil {
		t.Fatalf("entity + position + surface must be accepted, got %v", err)
	}
	lua := <-commands
	if !strings.Contains(lua, `local target={position={x=1,y=1},name=`+luaString("boiler")+`,surface=`+luaString("nauvis")+`}`) {
		t.Fatalf("named position target must carry the surface:\n%s", lua)
	}
}
