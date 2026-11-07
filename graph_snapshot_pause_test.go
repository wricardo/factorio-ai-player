package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// snapshotPauseRCON models engine pause ownership while using the real RCON
// transport and export lifecycle. Lua ownership itself is exercised below.
type snapshotPauseRCON struct {
	mu              sync.Mutex
	paused          bool
	original        bool
	holders         map[string]bool
	cancelled       map[string]bool
	acquires        int
	releases        int
	reads           int
	readUnpaused    bool
	acquireResponse string
	dropAcquire     bool
	readError       bool
	releaseError    bool
	onAcquire       func()
	onRead          func()
}

func serveSnapshotPauseRCON(t *testing.T, state *snapshotPauseRCON) *Resolver {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	state.holders = map[string]bool{}
	state.cancelled = map[string]bool{}
	t.Cleanup(func() { _ = listener.Close() })
	r := &Resolver{addr: listener.Addr().String(), password: "password"}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				if err := writePacket(conn, 1, 2, ""); err != nil {
					return
				}
				_, _, command, err := readPacket(conn)
				if err != nil {
					return
				}
				response, drop := state.command(command, r)
				if drop {
					return
				}
				_ = writePacket(conn, 2, 0, response+"\n")
				_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
			}()
		}
	}()
	return r
}

func (s *snapshotPauseRCON) command(command string, r *Resolver) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(command, "local token=") {
		token := strings.SplitN(strings.SplitN(command, "local token=", 2)[1], "\n", 2)[0]
		if strings.Contains(command, "state.holders[token]=true") {
			s.acquires++
			if s.cancelled[token] {
				delete(s.cancelled, token)
				return `{"acquired":false}`, false
			}
			if len(s.holders) == 0 {
				s.original = s.paused
			}
			s.holders[token] = true
			s.paused = true
			if s.onAcquire != nil {
				s.onAcquire()
			}
			if s.acquireResponse != "" {
				return s.acquireResponse, s.dropAcquire
			}
			return `{"acquired":true}`, s.dropAcquire
		}
		s.releases++
		if strings.Contains(command, "if true then state.cancelled[token]=true end") {
			s.cancelled[token] = true
		}
		if s.holders[token] {
			delete(s.holders, token)
			if len(s.holders) == 0 {
				s.paused = s.original
			}
		}
		if s.releaseError {
			return `{"error":"resume failed"}`, false
		}
		return `{"released":true}`, false
	}
	s.reads++
	if !s.paused {
		s.readUnpaused = true
	}
	if s.onRead != nil {
		s.onRead()
	}
	if s.readError {
		return `{"error":"snapshot read failed"}`, false
	}
	switch command {
	case "/silent-command " + graphCensusLua("player", []string{"nauvis"}):
		return `{"tick":42,"force":"player","surfaces":[{"surface":"nauvis","index":1,"total":0,"static":0,"mobile":0}]}`, false
	case "/silent-command " + graphChunksLua("nauvis", "player", 0, 20000):
		return `{"tick":42,"next":0,"done":true,"chunks":[]}`, false
	case "/silent-command " + graphMobileLua(r, "nauvis", "player"):
		return `{"tick":42,"nodes":[],"edges":[],"refs":[]}`, false
	case "/silent-command " + graphCatalogLua("player", []string{"nauvis"}, nil, nil, nil):
		return `{"recipes":{},"resources":{},"machines":{},"infinity":[]}`, false
	default:
		return `{"error":"unexpected snapshot read"}`, false
	}
}

func snapshotPauseOptions(t *testing.T, pause bool) graphExportOptions {
	t.Helper()
	return graphExportOptions{Force: "player", Surfaces: []string{"nauvis"}, Pause: pause,
		Out: filepath.Join(t.TempDir(), "snapshot"), LeafMax: 2000, Concurrency: 1, ChunkPage: 20000, Keep: 1}
}

func TestGraphSnapshotPauseLifecycle(t *testing.T) {
	for _, test := range []struct {
		name          string
		initialPaused bool
		pause         bool
		readError     bool
		cancel        bool
	}{
		{name: "success", pause: true},
		{name: "read error", pause: true, readError: true},
		{name: "cancellation", pause: true, cancel: true},
		{name: "already paused", pause: true, initialPaused: true},
		{name: "opt out"},
		{name: "paused opt out", initialPaused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state := &snapshotPauseRCON{paused: test.initialPaused, readError: test.readError}
			if test.cancel {
				state.onRead = cancel
			}
			r := serveSnapshotPauseRCON(t, state)
			_, _, err := exportGraphContext(ctx, snapshotPauseOptions(t, test.pause), io.Discard, r.addr, r.password)
			if wantError := test.readError || test.cancel; (err != nil) != wantError {
				t.Fatalf("error=%v, want error=%t", err, wantError)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.paused != test.initialPaused {
				t.Errorf("paused=%t, want original %t", state.paused, test.initialPaused)
			}
			wantMutations := 0
			if test.pause {
				wantMutations = 1
			}
			if state.acquires != wantMutations || state.releases != wantMutations {
				t.Errorf("acquires=%d releases=%d, want %d each", state.acquires, state.releases, wantMutations)
			}
			if test.pause && state.readUnpaused {
				t.Error("snapshot read ran while simulation was unpaused")
			}
			if len(state.holders) != 0 || len(state.cancelled) != 0 {
				t.Errorf("lease records leaked: holders=%v cancelled=%v", state.holders, state.cancelled)
			}
		})
	}
}

func TestGraphSnapshotPauseAmbiguousAcquisition(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		drop     bool
		cancel   bool
	}{
		{name: "malformed response", response: "not json"},
		{name: "invalid acquired type", response: `{"acquired":"yes"}`},
		{name: "lost response", drop: true},
		{name: "cancelled acquisition", drop: true, cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state := &snapshotPauseRCON{acquireResponse: test.response, dropAcquire: test.drop}
			if test.cancel {
				state.onAcquire = cancel
			}
			r := serveSnapshotPauseRCON(t, state)
			_, _, err := exportGraphContext(ctx, snapshotPauseOptions(t, true), io.Discard, r.addr, r.password)
			if err == nil {
				t.Fatal("ambiguous acquisition must fail export")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.paused || state.acquires != 1 || state.releases != 1 || state.reads != 0 {
				t.Fatalf("paused=%t acquires=%d releases=%d reads=%d", state.paused, state.acquires, state.releases, state.reads)
			}
		})
	}
}

func TestGraphSnapshotPauseRestoreErrorJoined(t *testing.T) {
	for _, readError := range []bool{false, true} {
		t.Run(fmt.Sprintf("readError=%t", readError), func(t *testing.T) {
			state := &snapshotPauseRCON{readError: readError, releaseError: true}
			r := serveSnapshotPauseRCON(t, state)
			_, _, err := exportGraphContext(context.Background(), snapshotPauseOptions(t, true), io.Discard, r.addr, r.password)
			if err == nil || !strings.Contains(err.Error(), "restore graph snapshot pause state") || !strings.Contains(err.Error(), "resume failed") {
				t.Fatalf("expected restore error, got %v", err)
			}
			if readError && !strings.Contains(err.Error(), "snapshot read failed") {
				t.Fatalf("read error lost: %v", err)
			}
		})
	}
}

func TestGraphSnapshotPauseOverlappingExports(t *testing.T) {
	state := &snapshotPauseRCON{}
	r := serveSnapshotPauseRCON(t, state)
	ctx := context.Background()
	// A holder owned by another export must outlive this export's cleanup.
	if _, _, err := executeLuaContext(ctx, r.addr, r.password, graphSnapshotAcquireLua("other-process")); err != nil {
		t.Fatal(err)
	}
	x := &graphExporter{r: r, opts: snapshotPauseOptions(t, true), log: io.Discard}
	if _, _, _, err := x.snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	stillPaused := state.paused && len(state.holders) == 1
	state.mu.Unlock()
	if !stillPaused {
		t.Error("finishing one export resumed another export's snapshot")
	}
	if err := graphSnapshotPauseCall(ctx, r, graphSnapshotReleaseLua("other-process", false)); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.paused || len(state.holders) != 0 {
		t.Error("last export did not restore original running state")
	}
}

func TestGraphSnapshotPauseReleasedBeforeDiskFailure(t *testing.T) {
	state := &snapshotPauseRCON{}
	r := serveSnapshotPauseRCON(t, state)
	opts := snapshotPauseOptions(t, true)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Out = filepath.Join(file, "snapshot")
	_, _, err := exportGraphContext(context.Background(), opts, io.Discard, r.addr, r.password)
	if err == nil {
		t.Fatal("expected disk failure")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.paused || state.releases != 1 {
		t.Fatalf("disk failure left pause lease: paused=%t releases=%d", state.paused, state.releases)
	}
}

func TestGraphSnapshotPauseFlags(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{{want: true}, {args: []string{"--pause=false"}}} {
		fs := flag.NewFlagSet("export", flag.ContinueOnError)
		var opts graphExportOptions
		var surfaces stringList
		graphExportFlags(fs, &opts, &surfaces)
		if err := fs.Parse(test.args); err != nil {
			t.Fatal(err)
		}
		if opts.Pause != test.want {
			t.Errorf("args=%v pause=%t, want %t", test.args, opts.Pause, test.want)
		}
	}
}

func TestGraphSnapshotPauseLeaseLua(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	for _, initialPaused := range []bool{false, true} {
		script := fmt.Sprintf(`storage={}
game={tick_paused=%t}
helpers={table_to_json=function(value) return value end}
rcon={print=function(value) result=value end}
local function run(command) assert(load(command))() end
`, initialPaused)
		script += "run(" + luaString(graphSnapshotAcquireLua("one")) + ")\nassert(result.acquired and game.tick_paused)\n"
		script += "run(" + luaString(graphSnapshotAcquireLua("two")) + ")\nassert(result.acquired and game.tick_paused)\n"
		script += "run(" + luaString(graphSnapshotReleaseLua("one", false)) + ")\nassert(result.released and game.tick_paused)\n"
		script += "run(" + luaString(graphSnapshotReleaseLua("two", false)) + ")\n"
		script += fmt.Sprintf("assert(game.tick_paused==%t)\n", initialPaused)
		// Cleanup can overtake an acquire after cancellation on another socket.
		script += "run(" + luaString(graphSnapshotReleaseLua("late", true)) + ")\n"
		script += "run(" + luaString(graphSnapshotAcquireLua("late")) + ")\nassert(not result.acquired)\n"
		script += fmt.Sprintf("assert(game.tick_paused==%t)\n", initialPaused)
		script += "assert(next(storage.factorio_ai_player_graph_snapshot_pause.holders)==nil)\n"
		script += "assert(next(storage.factorio_ai_player_graph_snapshot_pause.cancelled)==nil)\n"
		// An interrupted export can leave a lease holder behind; sync unpauses
		// and drops the lease so the save is not paused and later exports are
		// not fooled by the stale holder.
		script += "run(" + luaString(graphSnapshotAcquireLua("stale")) + ")\nassert(game.tick_paused)\n"
		script += "run(" + luaString(graphSnapshotUnpauseLua) + ")\nassert(not game.tick_paused and result.paused==false)\n"
		script += "assert(storage.factorio_ai_player_graph_snapshot_pause==nil)\n"
		script += "run(" + luaString(graphSnapshotAcquireLua("after")) + ")\nassert(result.acquired)\n"
		script += "run(" + luaString(graphSnapshotReleaseLua("after", false)) + ")\nassert(not game.tick_paused)\n"
		cmd := exec.Command(lua, "-")
		cmd.Stdin = strings.NewReader(script)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("initialPaused=%t: %v\n%s", initialPaused, err, output)
		}
	}
}
