//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const integrationSurface = "factorio-ai-player-integration"

type integrationHarness struct {
	binary   string
	version  string
	dir      string
	addr     string
	password string
	cmd      *exec.Cmd
	logFile  *os.File
	wait     chan error

	mu      sync.Mutex
	stopped bool
}

func TestIntegration(t *testing.T) {
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })

	t.Run("SchemaContract", func(t *testing.T) {
		result := h.query(t, `{
		  __schema { mutationType { fields { name } } }
		}`)
		fields := jsonArray(t, jsonObject(t, jsonObject(t, result, "data"), "__schema"), "mutationType", "fields")
		got := make([]string, 0, len(fields))
		for _, field := range fields {
			got = append(got, jsonString(t, field.(map[string]any), "name"))
		}
		for _, required := range []string{"characterSpawn", "place", "remove", "setLampSettings", "setCircuitBehavior", "navigateStart", "save", "shutdown"} {
			if !slices.Contains(got, required) {
				t.Fatalf("required mutation %q absent from working-tree schema; mutations=%v", required, got)
			}
		}
	})

	t.Run("StatusTick", func(t *testing.T) {
		first := h.status(t)
		time.Sleep(100 * time.Millisecond)
		second := h.status(t)
		if second <= first {
			t.Fatalf("Factorio tick did not advance: first=%v second=%v", first, second)
		}
		t.Logf("real Factorio binary=%s version=%q rcon=%s tick=%v", h.binary, h.version, h.addr, second)
	})

	t.Run("WorldSnapshot", func(t *testing.T) {
		result := h.query(t, `{
		  status { players { name } }
		  characters { total }
		  entities(input:{surface:"nauvis",area:{leftTopX:-100,leftTopY:-100,rightBottomX:100,rightBottomY:100},limit:1000}) {
		    total
		    entities { name type }
		  }
		}`)
		data := jsonObject(t, result, "data")
		if players := jsonArray(t, data, "status", "players"); len(players) != 0 {
			t.Fatalf("fresh sandbox has connected players: %v", players)
		}
		if total := jsonNumber(t, jsonObject(t, data, "characters"), "total"); total != 0 {
			t.Fatalf("fresh sandbox character count=%v, want 0", total)
		}
		entities := jsonObject(t, data, "entities")
		if total := jsonNumber(t, entities, "total"); total <= 0 {
			t.Fatalf("fresh sandbox has no natural entities: %v", entities)
		}
		machineTypes := map[string]bool{
			"assembling-machine": true, "container": true, "electric-pole": true,
			"furnace": true, "inserter": true, "lamp": true, "transport-belt": true,
		}
		for _, raw := range jsonArray(t, entities, "entities") {
			entity := raw.(map[string]any)
			if machineTypes[jsonString(t, entity, "type")] {
				t.Fatalf("fresh sandbox unexpectedly contains machine: %v", entity)
			}
		}
	})

	h.createTestSurface(t)

	t.Run("CharacterSpawnInventory", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"wooden-chest": 1, "iron-plate": 1})

		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{character:%q,surface:%q,x:4,y:0,item:"wooden-chest"}) {
		    unitNumber position { x y }
		  }
		}`, character.id, integrationSurface))
		place := jsonObject(t, jsonObject(t, placed, "data"), "place")
		unit := jsonString(t, place, "unitNumber")
		position := jsonObject(t, place, "position")
		t.Cleanup(func() { h.removeEntity(t, character.id, "wooden-chest", unit) })

		moved := h.mutation(t, fmt.Sprintf(`mutation {
		  transfer(input:{character:%q,surface:%q,entity:"wooden-chest",unitNumber:%q,inventory:"chest",direction:"to-entity",item:"iron-plate",count:1}) { moved }
		}`, character.id, integrationSurface, unit))
		if got := jsonNumber(t, jsonObject(t, jsonObject(t, moved, "data"), "transfer"), "moved"); got != 1 {
			t.Fatalf("transfer moved=%v, want 1; chest position=%v", got, position)
		}

		inspection := h.query(t, fmt.Sprintf(`{
		  inspect(input:{character:%q,surface:%q,entity:"wooden-chest",unitNumber:%q}) {
		    inventories { chest { name count } }
		  }
		}`, character.id, integrationSurface, unit))
		chest := jsonArray(t, jsonObject(t, jsonObject(t, jsonObject(t, inspection, "data"), "inspect"), "inventories"), "chest")
		if !hasItem(chest, "iron-plate", 1) {
			t.Fatalf("transferred iron plate absent from chest: %v", chest)
		}

		returned := h.mutation(t, fmt.Sprintf(`mutation {
		  transfer(input:{character:%q,surface:%q,entity:"wooden-chest",unitNumber:%q,inventory:"chest",direction:"to-player",item:"iron-plate",count:1}) { moved }
		}`, character.id, integrationSurface, unit))
		if got := jsonNumber(t, jsonObject(t, jsonObject(t, returned, "data"), "transfer"), "moved"); got != 1 {
			t.Fatalf("return transfer moved=%v, want 1", got)
		}
	})

	t.Run("LampSettingsRoundTrip", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		before := h.entityCount(t, "small-lamp")
		h.grantItems(t, character.id, map[string]int{"small-lamp": 1})
		unit := h.placeLamp(t, character.id, 2, 0)
		t.Cleanup(func() {
			h.removeEntity(t, character.id, "small-lamp", unit)
			if after := h.entityCount(t, "small-lamp"); after != before {
				t.Errorf("small-lamp count after cleanup=%v, want %v", after, before)
			}
		})

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  setLampSettings(input:{character:%q,surface:%q,entity:"small-lamp",unitNumber:%q,color:{r:0.25,g:0.5,b:0.75,a:1},alwaysOn:true,useColors:false,colorMode:0}) {
		    tick lamp { color { r g b a } alwaysOn useColors colorMode }
		  }
		}`, character.id, integrationSurface, unit))
		settings := jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "setLampSettings"), "lamp")
		if !jsonBool(t, settings, "alwaysOn") || jsonBool(t, settings, "useColors") || jsonNumber(t, settings, "colorMode") != 0 {
			t.Fatalf("unexpected post-write lamp settings: %v", settings)
		}
		color := jsonObject(t, settings, "color")
		for key, want := range map[string]float64{"r": 0.25, "g": 0.5, "b": 0.75, "a": 1} {
			if got := jsonNumber(t, color, key); math.Abs(got-want) > 0.01 {
				t.Fatalf("lamp color %s=%v, want %v; color=%v", key, got, want, color)
			}
		}

		inspection := h.inspectSettings(t, character.id, unit)
		lamp := jsonObject(t, inspection, "lamp")
		if !jsonBool(t, lamp, "alwaysOn") || jsonBool(t, lamp, "useColors") {
			t.Fatalf("inspection did not preserve lamp settings: %v", lamp)
		}
	})

	t.Run("CircuitBehaviorCompatibility", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"small-lamp": 1})
		unit := h.placeLamp(t, character.id, -3, 0)
		t.Cleanup(func() { h.removeEntity(t, character.id, "small-lamp", unit) })

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  setCircuitBehavior(input:{character:%q,surface:%q,entity:"small-lamp",unitNumber:%q,useColors:true,colorMode:1}) {
		    circuit { useColors colorMode }
		  }
		}`, character.id, integrationSurface, unit))
		circuit := jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "setCircuitBehavior"), "circuit")
		if !jsonBool(t, circuit, "useColors") || jsonNumber(t, circuit, "colorMode") != 1 {
			t.Fatalf("unexpected post-write circuit behavior: %v", circuit)
		}
		readback := jsonObject(t, h.inspectSettings(t, character.id, unit), "circuit")
		if !jsonBool(t, readback, "useColors") || jsonNumber(t, readback, "colorMode") != 1 {
			t.Fatalf("inspection did not preserve circuit behavior: %v", readback)
		}
	})

	t.Run("NavigationMod", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		destinationX := character.x + 4
		result := h.mutation(t, fmt.Sprintf(`mutation {
		  navigateStart(input:{character:%q,x:%g,y:%g,tolerance:0.75,timeoutTicks:600,wait:true,waitTimeoutSeconds:15}) {
		    id character surface status reason position { x y }
		  }
		}`, character.id, destinationX, character.y))
		job := jsonObject(t, jsonObject(t, result, "data"), "navigateStart")
		if status := jsonString(t, job, "status"); status != "ARRIVED" {
			t.Fatalf("navigation status=%q, want ARRIVED; job=%v", status, job)
		}
		if surface := jsonString(t, job, "surface"); surface != integrationSurface {
			t.Fatalf("navigation surface=%q, want %q", surface, integrationSurface)
		}
	})

	t.Run("SaveShutdown", func(t *testing.T) {
		h.mutation(t, `mutation { save { response } }`)
		save := filepath.Join(h.dir, "data", "saves", "sandbox.zip")
		waitFor(t, 30*time.Second, func() bool {
			info, err := os.Stat(save)
			return err == nil && info.Size() > 0
		}, func() string { return "save not created at " + save + "\n" + h.logTail() })
		h.shutdown(t)
		if connection, err := net.DialTimeout("tcp", h.addr, 200*time.Millisecond); err == nil {
			connection.Close()
			t.Fatalf("RCON still accepts connections after shutdown: %s", h.addr)
		}
	})
}

type integrationCharacter struct {
	id   string
	x, y float64
}

func startIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	binary := os.Getenv("FACTORIO_BIN")
	if binary == "" {
		binary = "/Applications/factorio.app/Contents/MacOS/factorio"
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("real Factorio integration test requires FACTORIO_BIN: %v", err)
	}

	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := factorioSandboxScenario(binary)
	if err != nil {
		t.Skip(err)
	}
	mod := filepath.Join(repo, "mods", "factorio_ai_navigation")
	if _, err := os.Stat(filepath.Join(mod, "info.json")); err != nil {
		t.Skipf("navigation mod is absent: %v", err)
	}

	dir := t.TempDir()
	scenarioDest := filepath.Join(dir, "data", "scenarios", "sandbox")
	if err := os.MkdirAll(filepath.Dir(scenarioDest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(scenarioDest, os.DirFS(scenario)); err != nil {
		t.Fatalf("copy sandbox scenario: %v", err)
	}
	mods := filepath.Join(dir, "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mod, filepath.Join(mods, "factorio_ai_navigation_1.0.0")); err != nil {
		t.Fatalf("link navigation mod: %v", err)
	}

	config := filepath.Join(dir, "config.ini")
	configBody := fmt.Sprintf("[path]\nread-data=__PATH__system-read-data__\nwrite-data=%s\n", filepath.Join(dir, "data"))
	if err := os.WriteFile(config, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(passwordBytes)
	gamePort := freeUDPPort(t)
	rconPort := freeTCPPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", rconPort)
	logPath := filepath.Join(dir, "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	versionOutput, _ := exec.Command(binary, "--version").CombinedOutput()
	version := strings.TrimSpace(strings.SplitN(string(versionOutput), "\n", 2)[0])

	cmd := exec.Command(binary,
		"-c", config,
		"--mod-directory", mods,
		"--start-server-load-scenario", "sandbox",
		"--bind", fmt.Sprintf("127.0.0.1:%d", gamePort),
		"--rcon-bind", addr,
		"--rcon-password", password,
		"--server-settings", filepath.Join(repo, "server-settings.json"),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start Factorio: %v", err)
	}
	h := &integrationHarness{
		binary: binary, version: version, dir: dir, addr: addr, password: password,
		cmd: cmd, logFile: logFile, wait: make(chan error, 1),
	}
	go func() { h.wait <- cmd.Wait() }()

	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-h.wait:
			h.stopped = true
			logFile.Close()
			t.Fatalf("Factorio exited before RCON readiness: %v\n%s", err, h.logTail())
		default:
		}
		body, _ := os.ReadFile(logPath)
		if bytes.Contains(body, []byte("Starting RCON interface")) {
			break
		}
		if time.Now().After(deadline) {
			h.forceStop()
			t.Fatalf("Factorio RCON did not become ready within 60s\n%s", h.logTail())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return h
}

func factorioSandboxScenario(binary string) (string, error) {
	root := filepath.Dir(binary)
	candidates := []string{
		filepath.Join(root, "..", "data", "base", "scenarios", "sandbox"),
		filepath.Join(root, "..", "..", "data", "base", "scenarios", "sandbox"),
		filepath.Join(root, "..", "..", "..", "data", "base", "scenarios", "sandbox"),
	}
	if data := os.Getenv("FACTORIO_DATA"); data != "" {
		candidates = append([]string{filepath.Join(data, "base", "scenarios", "sandbox")}, candidates...)
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(filepath.Join(candidate, "control.lua")); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("sandbox scenario not found beside %s; set FACTORIO_DATA to Factorio's data directory", binary)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.LocalAddr().(*net.UDPAddr).Port
}

func (h *integrationHarness) query(t *testing.T, document string) map[string]any {
	t.Helper()
	return h.graphql(t, "query", document)
}

func (h *integrationHarness) mutation(t *testing.T, document string) map[string]any {
	t.Helper()
	return h.graphql(t, "mutation", document)
}

func (h *integrationHarness) graphql(t *testing.T, operation, document string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	err := runWithCredentials([]string{"--addr", h.addr, operation, "--format", "json", document}, &out, h.password)
	if err != nil {
		t.Fatalf("real Factorio GraphQL failed\noperation: %s\ndocument:\n%s\nendpoint: %s\ntick: %v\noutput: %s\nerror: %v", operation, document, h.addr, h.bestEffortTick(), out.String(), err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid GraphQL JSON for document\n%s\noutput: %q\nerror: %v", document, out.String(), err)
	}
	return result
}

func (h *integrationHarness) bestEffortTick() any {
	if h.isStopped() {
		return "server-stopped"
	}
	var out bytes.Buffer
	if err := runWithCredentials([]string{"--addr", h.addr, "query", "--format", "json", `{ status { tick } }`}, &out, h.password); err != nil {
		return "unavailable: " + err.Error()
	}
	var result map[string]any
	if json.Unmarshal(out.Bytes(), &result) != nil {
		return "unavailable"
	}
	data, _ := result["data"].(map[string]any)
	status, _ := data["status"].(map[string]any)
	return status["tick"]
}

func (h *integrationHarness) status(t *testing.T) float64 {
	t.Helper()
	result := h.query(t, `{ status { tick } }`)
	return jsonNumber(t, jsonObject(t, result, "data", "status"), "tick")
}

func (h *integrationHarness) lua(t *testing.T, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := executeContext(ctx, h.addr, h.password, "/silent-command "+script, false)
	if err != nil {
		t.Fatalf("real Factorio fixture Lua failed\nscript: %s\nendpoint: %s\ntick: %v\nerror: %v", script, h.addr, h.bestEffortTick(), err)
	}
	return result
}

func (h *integrationHarness) createTestSurface(t *testing.T) {
	t.Helper()
	h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
if not s then s=game.create_surface("`+integrationSurface+`",{width=64,height=64}) end
s.request_to_generate_chunks({0,0},2)
s.force_generate_chunk_requests()
for _,e in pairs(s.find_entities()) do if e.valid then e.destroy() end end
local tiles={}
for x=-16,15 do for y=-16,15 do tiles[#tiles+1]={name="lab-dark-1",position={x,y}} end end
s.set_tiles(tiles,true)
rcon.print("ok")`)
}

func (h *integrationHarness) spawnCharacter(t *testing.T) integrationCharacter {
	t.Helper()
	result := h.mutation(t, `mutation {
	  characterSpawn(input:{surface:"`+integrationSurface+`",x:0,y:0,force:"player"}) {
	    character position { x y }
	  }
	}`)
	spawn := jsonObject(t, jsonObject(t, result, "data"), "characterSpawn")
	position := jsonObject(t, spawn, "position")
	return integrationCharacter{id: jsonString(t, spawn, "character"), x: jsonNumber(t, position, "x"), y: jsonNumber(t, position, "y")}
}

func (h *integrationHarness) destroyCharacter(t *testing.T, id string) {
	t.Helper()
	if h.isStopped() {
		return
	}
	h.lua(t, `local e=game.get_entity_by_unit_number(`+id+`)
if e and e.valid then e.destroy() end
rcon.print("ok")`)
}

func (h *integrationHarness) grantItems(t *testing.T, character string, items map[string]int) {
	t.Helper()
	var inserts strings.Builder
	for name, count := range items {
		fmt.Fprintf(&inserts, "\nassert(inv.insert{name=%q,count=%d}==%d)", name, count, count)
	}
	h.lua(t, `local e=game.get_entity_by_unit_number(`+character+`)
assert(e and e.valid and e.type=="character")
local inv=e.get_inventory(defines.inventory.character_main)`+inserts.String()+`
rcon.print("ok")`)
}

func (h *integrationHarness) placeLamp(t *testing.T, character string, x, y float64) string {
	t.Helper()
	result := h.mutation(t, fmt.Sprintf(`mutation {
	  place(input:{character:%q,surface:%q,x:%g,y:%g,item:"small-lamp"}) { unitNumber }
	}`, character, integrationSurface, x, y))
	return jsonString(t, jsonObject(t, jsonObject(t, result, "data"), "place"), "unitNumber")
}

func (h *integrationHarness) removeEntity(t *testing.T, character, entity, unit string) {
	t.Helper()
	if h.isStopped() {
		return
	}
	h.mutation(t, fmt.Sprintf(`mutation {
	  remove(input:{character:%q,entity:%q,unitNumber:%q}) { entity }
	}`, character, entity, unit))
}

func (h *integrationHarness) entityCount(t *testing.T, name string) float64 {
	t.Helper()
	result := h.query(t, fmt.Sprintf(`{
	  entities(input:{surface:%q,area:{leftTopX:-16,leftTopY:-16,rightBottomX:16,rightBottomY:16},name:%q,limit:1000}) { total }
	}`, integrationSurface, name))
	return jsonNumber(t, jsonObject(t, jsonObject(t, result, "data"), "entities"), "total")
}

func (h *integrationHarness) inspectSettings(t *testing.T, character, unit string) map[string]any {
	t.Helper()
	result := h.query(t, fmt.Sprintf(`{
	  inspect(input:{character:%q,surface:%q,entity:"small-lamp",unitNumber:%q}) {
	    settings {
	      lamp { color { r g b a } alwaysOn useColors colorMode }
	      circuit { enabled useColors colorMode }
	    }
	  }
	}`, character, integrationSurface, unit))
	return jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "inspect"), "settings")
}

func (h *integrationHarness) shutdown(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.mutation(t, `mutation { shutdown { response } }`)
	select {
	case err := <-h.wait:
		h.mu.Lock()
		h.stopped = true
		h.mu.Unlock()
		if h.logFile != nil {
			_ = h.logFile.Close()
		}
		if err != nil && !strings.Contains(err.Error(), "exit status 255") {
			t.Fatalf("Factorio shutdown exit: %v\n%s", err, h.logTail())
		}
	case <-time.After(30 * time.Second):
		h.forceStop()
		t.Fatalf("Factorio did not exit within 30s after shutdown\n%s", h.logTail())
	}
}

func (h *integrationHarness) close(t *testing.T) {
	t.Helper()
	if !h.isStopped() {
		h.shutdown(t)
	}
}

func (h *integrationHarness) isStopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}

func (h *integrationHarness) forceStop() {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.stopped = true
	h.mu.Unlock()
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(os.Interrupt)
		select {
		case <-h.wait:
		case <-time.After(5 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.wait
		}
	}
	if h.logFile != nil {
		_ = h.logFile.Close()
	}
}

func (h *integrationHarness) logTail() string {
	body, err := os.ReadFile(filepath.Join(h.dir, "server.log"))
	if err != nil {
		return err.Error()
	}
	if len(body) > 8000 {
		body = body[len(body)-8000:]
	}
	return string(body)
}

func waitFor(t *testing.T, timeout time.Duration, ready func() bool, failure func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(failure())
}

func jsonObject(t *testing.T, object map[string]any, path ...string) map[string]any {
	t.Helper()
	var current any = object
	for _, key := range path {
		parent, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("GraphQL path %v has non-object parent %T: %v", path, current, current)
		}
		current, ok = parent[key]
		if !ok {
			t.Fatalf("GraphQL path %v missing key %q in %v", path, key, parent)
		}
	}
	result, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL path %v is %T, want object: %v", path, current, current)
	}
	return result
}

func jsonArray(t *testing.T, object map[string]any, path ...string) []any {
	t.Helper()
	var current any = object
	for _, key := range path {
		parent, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("GraphQL path %v has non-object parent %T: %v", path, current, current)
		}
		current, ok = parent[key]
		if !ok {
			t.Fatalf("GraphQL path %v missing key %q in %v", path, key, parent)
		}
	}
	result, ok := current.([]any)
	if !ok {
		t.Fatalf("GraphQL path %v is %T, want array: %v", path, current, current)
	}
	return result
}

func jsonString(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok {
		t.Fatalf("GraphQL field %q is %T, want string: %v", key, object[key], object)
	}
	return value
}

func jsonNumber(t *testing.T, object map[string]any, key string) float64 {
	t.Helper()
	value, ok := object[key].(float64)
	if !ok {
		t.Fatalf("GraphQL field %q is %T, want number: %v", key, object[key], object)
	}
	return value
}

func jsonBool(t *testing.T, object map[string]any, key string) bool {
	t.Helper()
	value, ok := object[key].(bool)
	if !ok {
		t.Fatalf("GraphQL field %q is %T, want bool: %v", key, object[key], object)
	}
	return value
}

func hasItem(items []any, name string, count float64) bool {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["name"] == name && item["count"] == count {
			return true
		}
	}
	return false
}
