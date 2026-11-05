package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// topologyModSource reads the mod library that the console fast path calls.
func topologyModSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("mods/factorio_ai_navigation/topology.lua")
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

// topologyModRegion extracts one marked region. The markers keep the mod file and
// the Go fallback provably identical instead of drifting apart silently.
func topologyModRegion(t *testing.T, source, name string) string {
	t.Helper()
	begin := "-- BEGIN " + name + "\n"
	end := "\n-- END " + name + "\n"
	start := strings.Index(source, begin)
	if start < 0 {
		t.Fatalf("missing %s", begin)
	}
	start += len(begin)
	stop := strings.Index(source[start:], end)
	if stop < 0 {
		t.Fatalf("missing %s", end)
	}
	return source[start : start+stop]
}

// normalizeLua drops trailing whitespace and surrounding blank lines so the
// comparison ignores formatting-only differences.
func normalizeLua(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// The inline fallback is what an old save without the mod interface runs, so both
// copies of every topology chunk must stay byte-identical.
func TestTopologyModPreludeParity(t *testing.T) {
	source := topologyModSource(t)
	regions := map[string]string{
		"factorio_ai_topology.prelude":              transportLineLua + topologyCommonLua,
		"factorio_ai_topology.belt_run":             beltRunCallLua,
		"factorio_ai_topology.belt_stretch":         beltStretchCallLua,
		"factorio_ai_topology.belt_runs":            beltRunsCallLua,
		"factorio_ai_topology.factory_graph":        factoryGraphCallLua,
		"factorio_ai_topology.adjacency":            adjacencyLua,
		"factorio_ai_topology.belt_components":      beltComponentsCallLua,
		"factorio_ai_topology.inspection_logistics": inspectionLogisticsCallLua,
		"factorio_ai_topology.graph_prelude":        graphPreludeLua,
		"factorio_ai_topology.graph_leaf":           graphLeafCallLua,
		"factorio_ai_topology.graph_mobile":         graphMobileCallLua,
	}
	for name, want := range regions {
		got := topologyModRegion(t, source, name)
		if normalizeLua(got) != normalizeLua(want) {
			t.Fatalf("%s drifted from the Go fallback:\n--- mod ---\n%s\n--- go ---\n%s", name, normalizeLua(got), normalizeLua(want))
		}
	}
}

// The remote interface must expose every operation the console calls, with the
// documented protocol version.
func TestTopologyModInterface(t *testing.T) {
	source := topologyModSource(t)
	for _, need := range []string{
		`remote.add_interface("factorio_ai_topology"`,
		fmt.Sprintf("version = function() return %d end", topologyModProtocol),
		"belt_run = guarded(belt_run_op)",
		"belt_stretch = guarded(belt_stretch_op)",
		"belt_runs = guarded(belt_runs_op)",
		"factory_graph = guarded(factory_graph_op)",
		"adjacency = guarded(adjacency_op)",
		"belt_components = guarded(belt_components_op)",
		"inspection_logistics = guarded(inspection_logistics_op)",
		"graph_leaf = guarded(graph_leaf_op)",
		"graph_mobile = guarded(graph_mobile_op)",
	} {
		if !strings.Contains(source, need) {
			t.Fatalf("topology.lua missing %q", need)
		}
	}
	if !strings.Contains(mustReadFile(t, "mods/factorio_ai_navigation/control.lua"), `require("topology")`) {
		t.Fatal("control.lua must load the topology module")
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

// The mod file must load and answer every operation under the stub harness.
func TestTopologyModHarness(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	cmd := exec.Command(lua, "mods/factorio_ai_navigation/topology_test.lua")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("topology harness: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "topology harness ok") {
		t.Fatalf("unexpected harness output: %s", output)
	}
}

// pinTopologyInterface forces one capability answer for the rest of the test and
// clears the memoized probe, so stub-RCON tests stay at one command per query.
func pinTopologyInterface(t *testing.T, available bool) {
	t.Helper()
	previous := topologyInterfaceOverride
	value := available
	topologyInterfaceOverride = &value
	resetTopologyInterfaceCache()
	t.Cleanup(func() {
		topologyInterfaceOverride = previous
		resetTopologyInterfaceCache()
	})
}

func beltRunLocals() string {
	return "local surface_name,seed_x,seed_y,limit,include_line_links=\"nauvis\",0.5,0.5,10,false\n"
}

// Exactly one path is compiled: with the interface present the chunk is only the
// remote call, and without it the chunk carries the whole inline implementation.
func TestTopologyChunkChoosesOnePath(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	args := []string{luaString("nauvis"), "0.5", "0.5", "10", "false"}

	pinTopologyInterface(t, true)
	fast := topologyChunk(r, "belt_run", args, beltRunLocals(), beltRunCallLua)
	for _, absent := range []string{"local transport_line_names", "local belt_types", "belt_run(seed,limit,include_line_links)", "local function endpoint"} {
		if strings.Contains(fast, absent) {
			t.Fatalf("fast chunk must not compile the inline prelude or body, found %q:\n%s", absent, fast)
		}
	}
	if !strings.Contains(fast, "remote.call("+luaString(topologyModInterface)+","+luaString("belt_run")) {
		t.Fatalf("fast chunk must call the mod operation:\n%s", fast)
	}
	if strings.Contains(fast, "BEGIN factorio_ai_topology.prelude") {
		t.Fatalf("fast chunk must not copy the mod prelude marker:\n%s", fast)
	}
	if len(fast) > 1200 {
		t.Fatalf("fast chunk is %d bytes; it must stay small enough to compile instantly", len(fast))
	}

	pinTopologyInterface(t, false)
	fallback := topologyChunk(r, "belt_run", args, beltRunLocals(), beltRunCallLua)
	for _, present := range []string{"local transport_line_names", "local belt_types", "local function endpoint", "belt_run(seed,limit,include_line_links)"} {
		if !strings.Contains(fallback, present) {
			t.Fatalf("fallback chunk missing %q", present)
		}
	}
	if strings.Contains(fallback, "remote.call(") {
		t.Fatalf("fallback chunk must not call the mod:\n%s", fallback)
	}
}

// The capability probe must run once per process, not once per query.
func TestTopologyProbeRunsOnce(t *testing.T) {
	previous := topologyInterfaceOverride
	topologyInterfaceOverride = nil
	resetTopologyInterfaceCache()
	t.Cleanup(func() {
		topologyInterfaceOverride = previous
		resetTopologyInterfaceCache()
	})

	r, recorder := serveScriptedRCON(t, []scriptRule{{"remote.interfaces", `{"topology":1}`}})
	args := []string{luaString("nauvis"), "0.5", "0.5", "10", "false"}
	for i := 0; i < 5; i++ {
		chunk := topologyChunk(r, "belt_run", args, beltRunLocals(), beltRunCallLua)
		if !strings.Contains(chunk, "remote.call(") {
			t.Fatalf("query %d did not take the fast path", i)
		}
	}
	if len(recorder.commands) != 1 {
		t.Fatalf("capability probe ran %d times, want 1:\n%v", len(recorder.commands), recorder.commands)
	}
	if !strings.Contains(recorder.commands[0], "remote.interfaces") {
		t.Fatalf("the single probe command looks wrong:\n%s", recorder.commands[0])
	}
}

// A probe failure must fall back to the inline implementation, not break queries.
func TestTopologyProbeFailureFallsBack(t *testing.T) {
	previous := topologyInterfaceOverride
	topologyInterfaceOverride = nil
	resetTopologyInterfaceCache()
	t.Cleanup(func() {
		topologyInterfaceOverride = previous
		resetTopologyInterfaceCache()
	})
	r, _ := serveScriptedRCON(t, []scriptRule{{"remote.interfaces", `{"error":"lua disabled"}`}})
	chunk := topologyChunk(r, "belt_run", nil, beltRunLocals(), beltRunCallLua)
	if !strings.Contains(chunk, "local transport_line_names") {
		t.Fatalf("a failed probe must use the inline fallback:\n%s", chunk)
	}
}
