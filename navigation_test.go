package main

import (
	"context"
	"errors"
	"net"
	"os/exec"
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

func TestWaitForNavigation(t *testing.T) {
	start := &NavigationJob{ID: "42", Status: "MOVING"}
	t.Run("terminal state", func(t *testing.T) {
		job, err := waitForNavigation(context.Background(), start, time.Second, func(context.Context, string) (*NavigationJob, error) {
			return &NavigationJob{ID: "42", Status: "ARRIVED"}, nil
		})
		if err != nil || job.Status != "ARRIVED" {
			t.Fatalf("got job %#v, error %v", job, err)
		}
	})
	t.Run("wall timeout preserves active job", func(t *testing.T) {
		job, err := waitForNavigation(context.Background(), start, 300*time.Millisecond, func(ctx context.Context, _ string) (*NavigationJob, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		if err != nil || job.ID != "42" || job.Status != "MOVING" {
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
