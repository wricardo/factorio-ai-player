package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

func TestGraphQLConnectionEntityEndpointsAcceptMissingTilePositions(t *testing.T) {
	anchor := `{"from":{"x":1.5,"y":0.5,"direction":4,"existing":false,"side":"east"},"to":{"x":4.5,"y":0.5,"direction":12,"existing":true,"side":"west"}}`
	plan := `{"tick":100,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":1.5,"y":0.5},"to":{"x":4.5,"y":0.5},"bend":"HORIZONTAL_FIRST","possible":true,"needed":2,"steps":[{"position":{"x":1.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":2.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":3.5,"y":0.5},"direction":4,"existing":false},{"position":{"x":4.5,"y":0.5},"direction":4,"existing":false}],"conflicts":[]}`
	applied := `{"tick":101,"surface":"nauvis","force":"player","kind":"BELT","item":"transport-belt","from":{"x":1.5,"y":0.5},"to":{"x":4.5,"y":0.5},"bend":"HORIZONTAL_FIRST","possible":true,"placed":4,"skipped":0,"failed":0,"entities":[],"conflicts":[],"errors":[]}`
	resolver, recorder := serveScriptedRCON(t, []scriptRule{
		{"surface.create_entity", applied},
		{"local expected_type=", plan},
		{"local from_ref,to_ref=", anchor},
	})
	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema(resolver.addr, resolver.password)))
	defer server.Close()
	post := func(document string) map[string]any {
		t.Helper()
		body, err := json.Marshal(map[string]string{"query": document})
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(server.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result struct {
			Data   map[string]any             `json:"data"`
			Errors []struct{ Message string } `json:"errors"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || len(result.Errors) != 0 {
			t.Fatalf("GraphQL response: status=%d errors=%v", response.StatusCode, result.Errors)
		}
		return result.Data
	}
	query := `{ connectionPlan(input:{kind:BELT,fromEntity:{entity:{unitNumber:"42"},side:"east"},toEntity:{entity:{unitNumber:"43"},lane:"left"}}) { possible } }`
	queryResult, ok := post(query)["connectionPlan"].(map[string]any)
	if !ok || queryResult["possible"] != true {
		t.Fatalf("connectionPlan result=%v, want possible=true", queryResult)
	}
	mutation := `mutation { connectEntities(input:{actor:{character:"7"},kind:BELT,fromEntity:{entity:{unitNumber:"42"},side:"east"},toEntity:{entity:{unitNumber:"43"},lane:"left"}}) { possible placed } }`
	result, ok := post(mutation)["connectEntities"].(map[string]any)
	if !ok || result["possible"] != true || result["placed"] != float64(4) {
		t.Fatalf("connectEntities result=%v, want possible=true and placed=4", result)
	}
	if recorder.seen("local from_ref,to_ref=") != 2 || recorder.seen("surface.create_entity") != 1 {
		t.Fatalf("unexpected RCON stages: %+v", recorder.commands)
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
