package main

import (
	"strings"
	"testing"
)

// The map must decode rows and a legend that maps every character back to a
// prototype, and the Lua must downsample deterministically with a stable
// priority order.
func TestMapAreaDecodeAndLua(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":42,"surface":"nauvis","force":"player","area":{"leftTopX":-4,"leftTopY":-2,"rightBottomX":0,"rightBottomY":1},"cellSize":1,"columns":4,"rows":["AB.","~..","..."],"legend":[{"char":".","name":"empty","type":"empty","count":4},{"char":"~","name":"water","type":"tile","count":1},{"char":"A","name":"wooden-chest","type":"container","unitNumber":77,"count":1},{"char":"B","name":"stone-furnace","type":"furnace","count":1}],"truncated":false}`)
	result, err := mapAreaQuery(r, MapAreaInput{
		Surface: "nauvis", Force: "player",
		Area:    &FactoryAreaInput{LeftTopX: -4, LeftTopY: -2, RightBottomX: 0, RightBottomY: 1},
		MaxRows: 32, MaxColumns: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tick != 42 || result.Columns != 4 || result.CellSize != 1 || result.Truncated {
		t.Fatalf("map header: %+v", result)
	}
	if len(result.Rows) != 3 || result.Rows[0] != "AB." || result.Rows[1] != "~.." || result.Rows[2] != "..." {
		t.Fatalf("rows: %v", result.Rows)
	}
	if len(result.Legend) != 4 {
		t.Fatalf("legend: %+v", result.Legend)
	}
	chest := result.Legend[2]
	if chest.Char != "A" || chest.Name != "wooden-chest" || chest.Type != "container" || chest.Count != 1 {
		t.Fatalf("chest legend: %+v", chest)
	}
	if chest.UnitNumber == nil || *chest.UnitNumber != "77" {
		t.Fatalf("legend unit number: %+v", chest.UnitNumber)
	}
	lua := <-commands
	for _, want := range []string{
		`local layer=` + luaString("ALL"),
		`local cell_size=1`,
		`local columns,rows=4,3`,
		`surface.find_entities_filtered{area=area}`,
		`local function priority(e)`,
		`if t=="character" then return 100 end`,
		`local function paint(entity)`,
		`ghost_prototype`,
		`local x0=math.floor(px-fw/2+0.001)`,
		`for _,candidate in ipairs(candidates) do paint(candidate.entity) end`,
		`local ALPHABET=`,
		`tile.prototype.fluid`,
		`table.concat(parts)`,
		`truncated=cell_size>1`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("map Lua missing %q\n%s", want, lua)
		}
	}
}

// Types missing from the explicit priority table must keep a default rank,
// enemy-force entities and ghosts must be drawable, and the alphabet must never
// reuse the water character.
func TestMapAreaCoversEveryTypeAndAlphabet(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":4,"rightBottomY":4},"cellSize":1,"columns":4,"rows":["....","....","....","...."],"legend":[],"truncated":false}`)
	if _, err := mapAreaQuery(r, MapAreaInput{
		Surface: "nauvis", Force: "player",
		Area:    &FactoryAreaInput{RightBottomX: 4, RightBottomY: 4},
		MaxRows: 8, MaxColumns: 8,
	}); err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		`-- Every other type keeps a default rank instead of disappearing`,
		`return 45`,
		`if ignored_entities[t] then return 0 end`,
		`if force.is_enemy(e.force) then return 65 end`,
		`if layer=="FACTORY" then return not natural[e.type] end`,
		`if e.type=="entity-ghost" then return true end`,
		`return force.is_enemy(e.force)`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("map Lua missing %q\n%s", want, lua)
		}
	}
	const prefix = `local ALPHABET="`
	start := strings.Index(lua, prefix)
	if start < 0 {
		t.Fatalf("alphabet missing:\n%s", lua)
	}
	rest := lua[start+len(prefix):]
	end := strings.Index(rest, `"`)
	alphabet := rest[:end]
	if len(alphabet) != 87 {
		t.Fatalf("alphabet length=%d, want 87: %q", len(alphabet), alphabet)
	}
	for _, reserved := range []string{"~", ".", "?"} {
		if strings.Contains(alphabet, reserved) {
			t.Fatalf("alphabet must not reuse %q: %q", reserved, alphabet)
		}
	}
}

// A large area must be downsampled into at most maxRows by maxColumns cells
// while keeping square cells.
func TestMapAreaDownsampleGeometry(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":256,"rightBottomY":256},"cellSize":32,"columns":8,"rows":[],"legend":[],"truncated":true}`)
	result, err := mapAreaQuery(r, MapAreaInput{
		Surface: "nauvis", Force: "player",
		Area:    &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 256, RightBottomY: 256},
		MaxRows: 8, MaxColumns: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CellSize != 32 || result.Columns != 8 || !result.Truncated {
		t.Fatalf("downsampled map: %+v", result)
	}
	if result.Rows == nil || len(result.Rows) != 0 {
		t.Fatalf("empty rows must decode as an empty list: %#v", result.Rows)
	}
	lua := <-commands
	if !strings.Contains(lua, "local cell_size=32") || !strings.Contains(lua, "local columns,rows=8,8") {
		t.Fatalf("downsample geometry missing:\n%s", lua)
	}
}

// Invalid dimensions must fail before any RCON dial.
func TestMapAreaValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 8, RightBottomY: 8}
	cases := []struct {
		name  string
		input MapAreaInput
		want  string
	}{
		{"empty surface", MapAreaInput{Force: "player", Area: area, MaxRows: 8, MaxColumns: 8}, "surface and force are required"},
		{"empty force", MapAreaInput{Surface: "nauvis", Area: area, MaxRows: 8, MaxColumns: 8}, "surface and force are required"},
		{"missing area", MapAreaInput{Surface: "nauvis", Force: "player", MaxRows: 8, MaxColumns: 8}, "area is required"},
		{"rows low", MapAreaInput{Surface: "nauvis", Force: "player", Area: area, MaxRows: 0, MaxColumns: 8}, "maxRows must be between 1 and 256"},
		{"rows high", MapAreaInput{Surface: "nauvis", Force: "player", Area: area, MaxRows: maxMapDimension + 1, MaxColumns: 8}, "maxRows must be between 1 and 256"},
		{"columns low", MapAreaInput{Surface: "nauvis", Force: "player", Area: area, MaxRows: 8, MaxColumns: 0}, "maxColumns must be between 1 and 256"},
		{"columns high", MapAreaInput{Surface: "nauvis", Force: "player", Area: area, MaxRows: 8, MaxColumns: maxMapDimension + 1}, "maxColumns must be between 1 and 256"},
		{"oversized area", MapAreaInput{Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: maxAreaTiles + 1, RightBottomY: 8}, MaxRows: 8, MaxColumns: 8}, "no greater than 1024"},
		{"unsupported layer", MapAreaInput{Surface: "nauvis", Force: "player", Area: area, MaxRows: 8, MaxColumns: 8, Layer: MapLayer("FLUIDS")}, "unsupported map layer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mapAreaQuery(r, tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
