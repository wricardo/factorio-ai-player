package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNavigationController(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	out, err := exec.Command(lua, "mods/factorio_ai_navigation/navigation_test.lua").CombinedOutput()
	if err != nil {
		t.Fatalf("navigation controller: %v\n%s", err, out)
	}
}

func TestActionController(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	out, err := exec.Command(lua, "mods/factorio_ai_navigation/actions_test.lua").CombinedOutput()
	if err != nil {
		t.Fatalf("action controller: %v\n%s", err, out)
	}
}

func TestUnknownEngineStatusPreservesRawAndStaysNonterminal(t *testing.T) {
	status := engineStatus("future_engine_state")
	if status.Category != StatusCategoryOther || status.RawName == nil || *status.RawName != "future_engine_state" || status.Terminal {
		t.Fatalf("status = %+v", status)
	}
}

func TestWaitForNavigation(t *testing.T) {
	start := &NavigationJob{ID: "42", Status: engineStatus("MOVING")}
	t.Run("terminal state", func(t *testing.T) {
		job, err := waitForNavigation(context.Background(), start, time.Second, func(context.Context, string) (*NavigationJob, error) {
			return &NavigationJob{ID: "42", Status: engineStatus("ARRIVED")}, nil
		})
		if err != nil || statusRawName(job.Status) != "ARRIVED" {
			t.Fatalf("got job %#v, error %v", job, err)
		}
	})
	t.Run("wall timeout preserves active job", func(t *testing.T) {
		job, err := waitForNavigation(context.Background(), start, 300*time.Millisecond, func(ctx context.Context, _ string) (*NavigationJob, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		if err != nil || job.ID != "42" || statusRawName(job.Status) != "MOVING" {
			t.Fatalf("got job %#v, error %v", job, err)
		}
	})
	t.Run("cancelled request reports job", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := waitForNavigation(ctx, start, time.Second, nil)
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "42") {
			t.Fatalf("got error %v", err)
		}
	})
}

func TestRCONContextCancelsBlockedRead(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			<-done
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = executeContext(ctx, listener.Addr().String(), "password", "command", false)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("blocked RCON read: error %v after %v", err, time.Since(start))
	}
}

// runLuaScript executes a script with the system Lua interpreter and returns its
// trimmed stdout. It is the harness for Lua fragments that only need stubs.
func runLuaScript(t *testing.T, script string) string {
	t.Helper()
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	path := filepath.Join(t.TempDir(), "script.lua")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(lua, path).CombinedOutput()
	if err != nil {
		t.Fatalf("lua script failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// runLuaScriptError is runLuaScript for scripts that are expected to raise.
func runLuaScriptError(t *testing.T, script string) (string, error) {
	t.Helper()
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	path := filepath.Join(t.TempDir(), "script.lua")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(lua, path).CombinedOutput()
	return string(out), err
}

func TestNavigationTargetRef(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ref     *EntityRefInput
		want    []string
		absent  []string
		wantErr string
	}{
		{
			name:   "name and position",
			ref:    &EntityRefInput{Entity: new("wooden-chest"), Position: &PositionInput{X: 10.5, Y: -3}},
			want:   []string{"name=", "x=10.5", "y=-3"},
			absent: []string{"unit="},
		},
		{
			name: "unit alone",
			ref:  &EntityRefInput{UnitNumber: new("42")},
			want: []string{"{unit=42}"},
		},
		{
			name: "unit with name and position",
			ref:  &EntityRefInput{UnitNumber: new("42"), Entity: new("wooden-chest"), Position: &PositionInput{X: 1, Y: 2}},
			want: []string{"name=", "x=1", "y=2", "unit=42"},
		},
		{name: "name without position", ref: &EntityRefInput{Entity: new("wooden-chest")}, wantErr: "both"},
		{name: "position without name", ref: &EntityRefInput{Position: &PositionInput{X: 1, Y: 2}}, wantErr: "both"},
		{name: "empty reference", ref: &EntityRefInput{}, wantErr: "targetEntity"},
		{name: "zero unit", ref: &EntityRefInput{UnitNumber: new("0")}, wantErr: "unitNumber"},
		{name: "own unit", ref: &EntityRefInput{UnitNumber: new("1")}, wantErr: "differ"},
		{name: "nan position", ref: &EntityRefInput{Entity: new("wooden-chest"), Position: &PositionInput{X: 0, Y: math.NaN()}}, wantErr: "finite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lua, err := navigationTargetRef(tc.ref, 1)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q error, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(lua, want) {
					t.Fatalf("%q missing from %s", want, lua)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(lua, absent) {
					t.Fatalf("%q must not appear in %s", absent, lua)
				}
			}
		})
	}
}

func TestNavigationJobProgressFields(t *testing.T) {
	sample := `{"tick":100,"id":"7","character":"1","surface":"nauvis","destination":{"x":3,"y":0},"position":{"x":1.5,"y":0},"status":"MOVING","tolerance":0.5,"timeoutTicks":600,"replans":2,"waypointsRemaining":4,"distanceRemaining":1.25,"startedTick":10,"updatedTick":99}`
	var wire navigationJobWire
	if err := json.Unmarshal([]byte(sample), &wire); err != nil {
		t.Fatalf("decode navigation job: %v", err)
	}
	job := wire.model()
	if job.Tolerance != 0.5 || job.TimeoutTicks != 600 || job.Replans != 2 {
		t.Fatalf("unexpected job settings: %+v", job)
	}
	if job.WaypointsRemaining != 4 || job.DistanceRemaining != 1.25 {
		t.Fatalf("unexpected job progress: %+v", job)
	}
}

func TestNavigationActiveRequiresActor(t *testing.T) {
	if _, err := navigationActive(context.Background(), &Resolver{}, &ActorInput{}); err == nil {
		t.Fatal("expected an actor validation error")
	}
}

func TestNavigationStatusRequiresID(t *testing.T) {
	if _, err := navigationStatus(&Resolver{}, "  "); err == nil {
		t.Fatal("expected an empty id error")
	}
}
