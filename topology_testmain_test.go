//go:build !integration

package main

import (
	"os"
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
	code := m.Run()
	os.Exit(code)
}
