package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// assertLuaParses loads a generated chunk without running it, so a broken
// concatenation between the mod fast path, the shared prelude and a body is
// caught here instead of on a live server.
func assertLuaParses(t *testing.T, name, chunk string) {
	t.Helper()
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	chunk = strings.TrimPrefix(chunk, "/silent-command ")
	if chunk == "" {
		t.Fatalf("%s chunk is empty", name)
	}
	path := filepath.Join(t.TempDir(), name+".lua")
	if err := os.WriteFile(path, []byte(chunk), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(lua, "-e", "assert(loadfile("+strconv.Quote(path)+"))")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s chunk does not parse: %v\n%s\n%s", name, err, output, chunk)
	}
}

// Every topology query must emit Lua that parses: the fast path, the prelude and
// the body are concatenated at runtime, so a missing newline or separator would
// otherwise only show up against a real server.
func TestTopologyChunksParse(t *testing.T) {
	x, y := 0.5, 0.5

	t.Run("beltRun", func(t *testing.T) {
		r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","nodes":{},"edges":{},"boundaryEdges":{},"sources":{},"sinks":{},"inserters":{}}`)
		_, _ = beltRunQuery(r, "nauvis", &x, &y, nil, 10, true)
		assertLuaParses(t, "belt-run", <-commands)
	})
	t.Run("beltStretch", func(t *testing.T) {
		r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","nodes":{},"ends":{},"left":{},"right":{},"unknown":{},"inserters":{}}`)
		_, _ = beltStretchQuery(r, "nauvis", &x, &y, nil, 10)
		assertLuaParses(t, "belt-stretch", <-commands)
	})
	t.Run("beltRuns", func(t *testing.T) {
		r, commands := serveLuaOnce(t, `{"beltRuns":{}}`)
		obj := &InspectionLogistics{
			Surface: "nauvis",
			Inserters: []*ConnectedInserter{{
				Pickup: &LogisticsEndpoint{Surface: "nauvis", Name: "transport-belt", Type: "transport-belt", Position: &Position{X: 0.5, Y: 0.5}},
			}},
		}
		_, _ = inspectionBeltRuns(r, obj, 10, false)
		assertLuaParses(t, "belt-runs", <-commands)
	})
	t.Run("factoryGraph", func(t *testing.T) {
		r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","area":{},"nodes":{},"machines":{},"belts":{},"chests":{},"inserters":{},"fluidEntities":{},"edges":{},"boundaryEdges":{}}`)
		_, _ = factoryGraphQuery(r, &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8}, "player", "nauvis", true, true)
		assertLuaParses(t, "factory-graph", <-commands)
	})
	t.Run("adjacency", func(t *testing.T) {
		r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","area":{},"nodes":{},"pairs":{}}`)
		_, _ = adjacencyQuery(r, &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8}, "player", "nauvis", 0.5, 100, []string{"boiler", "pipe"}, true)
		assertLuaParses(t, "adjacency", <-commands)
	})
	t.Run("beltComponents", func(t *testing.T) {
		r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","area":{},"pageInfo":{},"components":{}}`)
		_, _ = beltComponentsQuery(r, BeltComponentsInput{
			Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8},
			MaxEntities: 100, Limit: 10, IncludeLineLinks: true,
		})
		assertLuaParses(t, "belt-components", <-commands)
	})
	t.Run("inspectionLogistics", func(t *testing.T) {
		r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","inserters":{}}`)
		_, _ = inspectionLogistics(r, &EntityInspection{
			Surface: "nauvis", Force: "player", Name: new("inserter"), Position: &Position{X: 1.5, Y: 2.5},
		})
		assertLuaParses(t, "inspection-logistics", <-commands)
	})
}
