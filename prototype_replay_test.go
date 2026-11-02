package main

import (
	"strings"
	"testing"
)

// prototypeQuery runs the shipped Lua against a prototype runtime that returns
// inserter pickup/drop offsets as two-element arrays, the engine shape that
// broke decoding with "cannot unmarshal array into ... inserterDrop".
func servePrototypeReplay(t *testing.T) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "prototype_engine.lua", nil, func(command string) bool {
		return strings.Contains(command, "prototypes.entity")
	})
}

func TestPrototypeInserterVectorsDecode(t *testing.T) {
	r := servePrototypeReplay(t)
	cases := map[string]struct {
		pickup Position
		drop   Position
	}{
		"inserter":             {pickup: Position{X: 0, Y: -1}, drop: Position{X: 0, Y: 1.2}},
		"burner-inserter":      {pickup: Position{X: 0, Y: -1}, drop: Position{X: 0, Y: 1.2}},
		"long-handed-inserter": {pickup: Position{X: 0, Y: -2}, drop: Position{X: 0, Y: 2.2}},
	}
	for name, want := range cases {
		prototype, err := prototypeQuery(r, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if prototype.Entity == nil {
			t.Fatalf("%s: no entity prototype", name)
		}
		pickup, drop := prototype.Entity.InserterPickup, prototype.Entity.InserterDrop
		if pickup == nil || drop == nil {
			t.Fatalf("%s: inserter offsets missing: %+v", name, prototype.Entity)
		}
		if *pickup != want.pickup || *drop != want.drop {
			t.Fatalf("%s: pickup=%+v drop=%+v, want %+v %+v", name, *pickup, *drop, want.pickup, want.drop)
		}
	}
}
