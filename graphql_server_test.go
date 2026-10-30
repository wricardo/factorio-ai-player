package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// fakeStatusRCON answers one GraphQL resolver command with the given JSON body.
func fakeStatusRCON(listener net.Listener, body string) <-chan error {
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		if _, _, _, err = readPacket(conn); err != nil {
			done <- err
			return
		}
		if err = writePacket(conn, 1, 2, ""); err != nil {
			done <- err
			return
		}
		for range 2 {
			if _, _, _, err = readPacket(conn); err != nil {
				done <- err
				return
			}
		}
		if err = writePacket(conn, 2, 0, body+"\n"); err != nil {
			done <- err
			return
		}
		done <- writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
	}()
	return done
}

func TestServeGraphQLServesQueries(t *testing.T) {
	rcon, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer rcon.Close()
	rconDone := fakeStatusRCON(rcon, `{"tick":123,"players":[]}`)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	served := make(chan error, 1)
	go func() {
		served <- serveGraphQL(ctx, &out, newGraphQLSchema(rcon.Addr().String(), "password"), listener)
	}()
	endpoint := "http://" + listener.Addr().String() + graphqlPath

	response, err := http.Post(endpoint, "application/json", strings.NewReader(`{"query":"{ status { tick } }"}`))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct {
		Data struct {
			Status struct {
				Tick float64 `json:"tick"`
			} `json:"status"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(result.Errors) > 0 || result.Data.Status.Tick != 123 {
		cancel()
		t.Fatalf("status query: code=%d errors=%v tick=%v", response.StatusCode, result.Errors, result.Data.Status.Tick)
	}

	mutation := endpoint + "?query=" + url.QueryEscape(`mutation { shutdown { response } }`)
	response, err = http.Get(mutation)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotAcceptable {
		cancel()
		t.Fatalf("GET mutation: code=%d, want %d", response.StatusCode, http.StatusNotAcceptable)
	}

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if err := <-rconDone; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), endpoint) {
		t.Fatalf("endpoint not announced: %q", out.String())
	}
}

func TestServeRequiresPassword(t *testing.T) {
	var out bytes.Buffer
	err := runWithCredentials([]string{"serve", "--listen", "127.0.0.1:0"}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "FACTORIO_RCON_PASSWORD") {
		t.Fatalf("serve must require RCON credentials, got %v", err)
	}
}

func TestServeRejectsPositionalArguments(t *testing.T) {
	err := runWithCredentials([]string{"serve", "extra"}, io.Discard, "password")
	if err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("serve must reject positional arguments, got %v", err)
	}
}
