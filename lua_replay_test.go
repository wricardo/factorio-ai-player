package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// serveLuaFixture accepts an RCON command, runs the received Lua inside a
// stubbed Factorio runtime (fixture), and replies with whatever the Lua prints.
// It lets tests drive the shipped Lua instead of a canned response. runs
// decides which received command the fixture should execute; other commands
// (game.print announcements, the completion marker) get an empty reply.
func serveLuaFixture(t *testing.T, fixture string, env []string, runs func(command string) bool) *Resolver {
	t.Helper()
	luaPath, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("Lua interpreter not installed")
	}
	script, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				if err := writePacket(conn, 1, 2, ""); err != nil {
					return
				}
				_, _, body, err := readPacket(conn)
				if err != nil {
					return
				}
				command := strings.TrimPrefix(body, "/silent-command ")
				if !runs(command) {
					_ = writePacket(conn, 2, 0, "")
					return
				}
				response, err := runLua(luaPath, script, env, command)
				if err != nil {
					t.Errorf("replay Lua: %v", err)
					return
				}
				if err := writePacket(conn, 2, 0, response+"\n"); err != nil {
					return
				}
				// The completion marker is what ends a multi-packet reply.
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end")
			}(conn)
		}
	}()
	return &Resolver{addr: listener.Addr().String(), password: "password"}
}

func runLua(luaPath string, fixture []byte, env []string, command string) (string, error) {
	file, err := os.CreateTemp("", "factorio-lua-replay-*.lua")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString(string(fixture) + "\n" + command + "\n"); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	cmd := exec.Command(luaPath, path)
	cmd.Env = append(os.Environ(), env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
