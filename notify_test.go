package main

import (
	"net"
	"strings"
	"testing"
)

func TestMutationSummary(t *testing.T) {
	placed, item := "stone-furnace", "coal"
	entity, moved := "stone-furnace", 5.0
	tests := []struct {
		name   string
		result any
		want   string
	}{
		{"place", &ActionResult{Name: &placed, Position: &Position{X: 3, Y: 4}}, "placed stone-furnace at (3, 4)"},
		{"fuel", &ActionResult{Name: &entity, Item: &item, Inserted: &moved}, "inserted 5 coal into stone-furnace"},
		{"transfer", &ActionResult{Entity: &entity, Item: &item, TransferDirection: new(TransferDirectionToEntity), Moved: &moved}, "moved 5 coal into stone-furnace"},
		{"clear-area", &ClearAreaResult{Surface: "nauvis", TreesRemoved: 2, RocksRemoved: 1}, "cleared nauvis: 2 trees and 1 rocks removed"},
		{"remove", &RemoveResult{Entity: "stone-furnace", Position: &Position{X: 3, Y: 4}}, "removed stone-furnace at (3, 4)"},
		{"harvest-area", &HarvestAreaResult{Surface: "nauvis", TreesMined: 2, RocksMined: 1, OutOfReach: 3, Failed: 4}, "harvested nauvis: 2 trees and 1 rocks mined, 3 out of reach, 4 failed"},
	}
	for _, tt := range tests {
		if got := mutationSummary(tt.name, tt.result); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestAnnouncePrintsMessage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 1)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		if _, _, _, err = readPacket(conn); err != nil {
			serverErr <- err
			return
		}
		if err = writePacket(conn, 1, 2, ""); err != nil {
			serverErr <- err
			return
		}
		_, _, body, err := readPacket(conn)
		if err != nil {
			serverErr <- err
			return
		}
		commands <- body
		if _, _, _, err = readPacket(conn); err != nil {
			serverErr <- err
			return
		}
		if err = writePacket(conn, 2, 0, `{"tick":42,"message":"hello world"}`+"\n"); err != nil {
			serverErr <- err
			return
		}
		err = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
		serverErr <- err
	}()
	r := &Resolver{addr: listener.Addr().String(), password: "password"}
	result, err := announce(r, "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "hello world" || result.Tick != 42 {
		t.Fatalf("result: %+v", result)
	}
	got := <-commands
	if !strings.Contains(got, "game.print("+luaString("hello world")+")") || !strings.Contains(got, "rcon.print") {
		t.Fatalf("announce command missing print and response: %q", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestAnnounceRejectsEmptyMessage(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	if _, err := announce(r, " \n\t"); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("expected empty message error, got %v", err)
	}
}
