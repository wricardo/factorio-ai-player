package main

import (
	"strings"
	"testing"
)

func TestNearbyLuaOrdersAndPaginates(t *testing.T) {
	lua := nearbyLua("nauvis", 12, -4, 7, "electric-mining-drill", "mining-drill", "distance", 100, 50)
	for _, want := range []string{
		`local s=game.surfaces["\110\097\117\118\105\115"]`,
		"local cx,cy=12,-4",
		"radius=7",
		"table.sort(items,function(a,b) return a.d<b.d end)",
		"local offset=100; local limit=50",
		"local first=offset+1; local last=math.min(#items,offset+limit)",
		"truncated=#items>last,total=#items",
		"distance=math.sqrt(items[i].d)",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("nearby lua missing %q:\n%s", want, lua)
		}
	}
}

func TestNearbyLuaUnitNumberOrder(t *testing.T) {
	lua := nearbyLua("nauvis", 0, 0, 10, "", "", "unit-number", 0, 100)
	if !strings.Contains(lua, `if ordering=="unit-number" or ordering=="unit_number" then`) {
		t.Fatalf("unit-number ordering branch missing:\n%s", lua)
	}
	if !strings.Contains(lua, "(a.e.unit_number or 0)<(b.e.unit_number or 0)") {
		t.Fatalf("unit number comparison missing:\n%s", lua)
	}
}

func TestNearbyLuaFiltersAreOptional(t *testing.T) {
	lua := nearbyLua("nauvis", 0, 0, 5, "", "", "distance", 0, 100)
	if !strings.Contains(lua, "local name=\"\"; local kind=\"\"") {
		t.Fatalf("empty filters must stay empty locals:\n%s", lua)
	}
}
