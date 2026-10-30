package main

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestEntityTarget(t *testing.T) {
	for _, tc := range []struct {
		name, unit, entity string
		x, y               float64
		want               uint64
	}{
		{"by name", "", "wooden-chest", 3, 4, 0},
		{"by unit", "439", "", math.NaN(), math.NaN(), 439},
		{"by unit with name", "439", "wooden-chest", math.NaN(), math.NaN(), 439},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := entityTarget(tc.entity, tc.unit, tc.x, tc.y)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, unit, entity string
		x, y               float64
	}{
		{"neither", "", "", math.NaN(), math.NaN()},
		{"coordinates without name", "", "", 3, 4},
		{"half coordinates", "", "wooden-chest", 3, math.NaN()},
		{"infinite coordinates", "", "wooden-chest", math.Inf(1), 4},
		{"zero unit", "0", "", math.NaN(), math.NaN()},
		{"text unit", "abc", "", math.NaN(), math.NaN()},
		{"both modes", "439", "wooden-chest", 3, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := entityTarget(tc.entity, tc.unit, tc.x, tc.y); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestUnitNumberAddressingLua(t *testing.T) {
	var out bytes.Buffer
	for _, tc := range []struct {
		name  string
		build func([]string) (string, error)
		args  []string
	}{
		{"transfer", func(a []string) (string, error) { return productionLua("transfer", a, &out) }, []string{"--player=wricardo", "--unitNumber=439", "--inventory=chest", "--direction=to-player", "--item=iron-plate", "--count=5"}},
		{"inspect", func(a []string) (string, error) { return productionLua("inspect", a, &out) }, []string{"--player=wricardo", "--unit-number=439"}},
		{"fuel", func(a []string) (string, error) { return actionLua("fuel", a, &out) }, []string{"--player=wricardo", "--unitNumber=439", "--item=coal", "--count=2"}},
		{"remove", func(a []string) (string, error) { return removeLua(a, &out) }, []string{"--player=wricardo", "--unitNumber=439"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lua, err := tc.build(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"local target_unit=439", "resolve_entity(target_unit,target_name,target_position,surface_name,p)", "e=game.get_entity_by_unit_number(unit)", "find_entities_filtered{name=name}", "find_entities_filtered{name=name,position=pos"} {
				if !strings.Contains(lua, want) {
					t.Errorf("Lua missing %q:\n%s", want, lua)
				}
			}
		})
	}
}

func TestEntityAddressingValidation(t *testing.T) {
	var out bytes.Buffer
	if _, err := productionLua("transfer", []string{"--player=wricardo", "--inventory=chest", "--direction=to-player", "--item=iron-plate", "--count=5"}, &out); err == nil {
		t.Fatal("transfer without entity name or unit number should be rejected")
	}
	if _, err := productionLua("inspect", []string{"--player=wricardo", "--unitNumber=439", "--x=3", "--y=4"}, &out); err == nil {
		t.Fatal("inspect with both unitNumber and coordinates should be rejected")
	}
	if _, err := actionLua("fuel", []string{"--player=wricardo", "--entity=stone-furnace", "--item=coal"}, &out); err == nil {
		t.Fatal("fuel by name without coordinates should be rejected")
	}
	if _, err := actionLua("place", []string{"--player=wricardo", "--item=wooden-chest"}, &out); err == nil {
		t.Fatal("place without coordinates should be rejected")
	}
	if _, err := removeLua([]string{"--player=wricardo", "--unitNumber=0"}, &out); err == nil {
		t.Fatal("remove with unit number zero should be rejected")
	}
}

func TestPlayerRestoreCharacterLua(t *testing.T) {
	var out bytes.Buffer
	lua, err := playerRestoreCharacterLua([]string{"--player=wricardo"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"game.get_player(" + luaString("wricardo") + ")", "if p.character then", "p.create_character()", "character=c.unit_number", "surface=c.surface.name", "position=c.position"} {
		if !strings.Contains(lua, want) {
			t.Errorf("restore Lua missing %q:\n%s", want, lua)
		}
	}
	for _, args := range [][]string{{}, {"--player="}, {"--player=wricardo", "extra"}} {
		if _, err := playerRestoreCharacterLua(args, &out); err == nil {
			t.Errorf("restore args %v should be rejected", args)
		}
	}
}

func TestPlayerInspectionReportsCharacter(t *testing.T) {
	var out bytes.Buffer
	lua, err := playerInspectLua("player", []string{"--player=wricardo"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lua, "subject.character=c.unit_number") {
		t.Fatalf("player inspection must expose the character unit number:\n%s", lua)
	}
}

func TestUnitNumberAddressingOtherOperations(t *testing.T) {
	var out bytes.Buffer
	lua, err := rotateLua([]string{"--character=267", "--unitNumber=307", "--turns=2"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local target_unit=307", "e.rotate{}", "p.can_reach_entity(e)"} {
		if !strings.Contains(lua, want) {
			t.Errorf("rotate Lua missing %q:\n%s", want, lua)
		}
	}
	lua, err = machineRecipeLua([]string{"--character=267", "--unitNumber=500", "--entity=assembling-machine-1", "--recipe=iron-gear-wheel"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lua, "local target_unit=500") || !strings.Contains(lua, `if e.type~="assembling-machine"`) {
		t.Errorf("setRecipe Lua must accept a unit number:\n%s", lua)
	}
	lua, err = beltTakeLua([]string{"--character=267", "--unitNumber=440", "--item=iron-plate", "--count=2"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local target_unit=440", "target_name=" + luaString("transport-belt"), `if e.type~="transport-belt"`} {
		if !strings.Contains(lua, want) {
			t.Errorf("beltTake Lua missing %q:\n%s", want, lua)
		}
	}
	for _, tc := range []struct {
		name string
		call func() (string, error)
	}{
		{"rotate both addresses", func() (string, error) {
			return rotateLua([]string{"--character=267", "--unitNumber=307", "--x=1", "--y=2"}, &out)
		}},
		{"beltTake no address", func() (string, error) {
			return beltTakeLua([]string{"--character=267", "--item=iron-plate"}, &out)
		}},
		{"setRecipe no address", func() (string, error) {
			return machineRecipeLua([]string{"--character=267", "--recipe=iron-gear-wheel"}, &out)
		}},
	} {
		if _, err := tc.call(); err == nil {
			t.Errorf("%s should be rejected", tc.name)
		}
	}
}
