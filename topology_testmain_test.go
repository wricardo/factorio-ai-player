//go:build !integration

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The unit suite answers every query with a single scripted RCON response, so it
// pins the topology interface to the inline fallback: the capability probe would
// otherwise consume that response. Tests that exercise the compiled-mod lane set
// the override themselves, and the integration suite always runs the real probe
// against a live server.
func TestMain(m *testing.M) {
	fallback := false
	topologyInterfaceOverride = &fallback
	// Keep the suite's GraphQL operation log out of the developer's live log.
	logDir, err := os.MkdirTemp("", "factorio-ai-player-test-log-")
	if err == nil {
		_ = os.Setenv(graphqlLogEnv, filepath.Join(logDir, "graphql-log.jsonl"))
	}
	code := m.Run()
	if logDir != "" {
		_ = os.RemoveAll(logDir)
	}
	os.Exit(code)
}
