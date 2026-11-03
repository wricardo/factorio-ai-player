package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// serveLuaRepeated answers every RCON exchange until the listener closes and
// records each command, which lets a polling test observe more than one read.
func serveLuaRepeated(t *testing.T, response string) (*Resolver, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	commands := make(chan string, 64)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if _, _, _, err = readPacket(conn); err != nil {
				conn.Close()
				continue
			}
			if err = writePacket(conn, 1, 2, ""); err != nil {
				conn.Close()
				continue
			}
			if _, _, body, err := readPacket(conn); err == nil {
				select {
				case commands <- body:
				default:
				}
			}
			if err = writePacket(conn, 2, 0, response+"\n"); err != nil {
				conn.Close()
				continue
			}
			_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
			conn.Close()
		}
	}()
	return &Resolver{addr: listener.Addr().String(), password: "password"}, commands
}

// A satisfied first poll must return immediately with the observed facts and a
// single read that only compares the game tick.
func TestWaitUntilTicksSatisfied(t *testing.T) {
	r, commands := serveLuaRepeated(t, `{"tick":500,"satisfied":true,"count":50,"detail":"waited 50 of 50 ticks"}`)
	result, err := waitUntil(context.Background(), r, WaitUntilInput{
		TimeoutSeconds: 5, PollSeconds: 0.05,
		Condition: &WaitConditionInput{Ticks: new(50.0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Satisfied || result.TimedOut || result.Polls != 1 || result.Condition != WaitConditionKindTicks {
		t.Fatalf("result: %+v", result)
	}
	if result.Last == nil || result.Last.Count == nil || *result.Last.Count != 50 || result.Last.Detail == "" {
		t.Fatalf("last observation: %+v", result.Last)
	}
	baseline := <-commands
	if !strings.Contains(baseline, "local tick=game.tick") || strings.Contains(baseline, "create_entity") {
		t.Fatalf("baseline read must only sample the tick: %s", baseline)
	}
	lua := <-commands
	for _, want := range []string{"local baseline=500", "local target=550", "local want=50", "tick>=target"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("ticks poll Lua missing %q\n%s", want, lua)
		}
	}
	if strings.Contains(lua, "create_entity") || strings.Contains(lua, "inv.") {
		t.Fatalf("ticks poll must not touch the world: %s", lua)
	}
}

// Inventory polling must use the actor's main inventory and the requested
// quality, and decode the observed count.
func TestWaitUntilInventoryPoll(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":10,"satisfied":true,"count":50,"detail":"iron-plate 50 of 50"}`)
	result, err := waitUntil(context.Background(), r, WaitUntilInput{
		TimeoutSeconds: 5, PollSeconds: 0.05,
		Condition: &WaitConditionInput{Inventory: &WaitInventoryInput{
			Actor: &ActorInput{Character: new("77")},
			Item:  "iron-plate", Quality: "uncommon", Count: 50,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Satisfied || result.Condition != WaitConditionKindInventory {
		t.Fatalf("result: %+v", result)
	}
	if result.Last.Count == nil || *result.Last.Count != 50 {
		t.Fatalf("observed count: %+v", result.Last)
	}
	lua := <-commands
	for _, want := range []string{
		`local unit=77`,
		`local item=` + luaString("iron-plate"),
		`local quality=` + luaString("uncommon"),
		`inv.get_item_count{name=item,quality=quality}`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("inventory poll Lua missing %q\n%s", want, lua)
		}
	}
}

// A condition that never holds must stop at the deadline and report the number
// of polls instead of waiting forever.
func TestWaitUntilTimeout(t *testing.T) {
	r, commands := serveLuaRepeated(t, `{"tick":1,"satisfied":false,"count":0,"detail":"iron-plate 0 of 9"}`)
	start := time.Now()
	result, err := waitUntil(context.Background(), r, WaitUntilInput{
		TimeoutSeconds: 1, PollSeconds: 0.1,
		Condition: &WaitConditionInput{Inventory: &WaitInventoryInput{
			Actor: &ActorInput{Character: new("5")}, Item: "iron-plate", Quality: "normal", Count: 9,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.Satisfied || result.Polls < 2 {
		t.Fatalf("timeout result: %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout overshot: %v", elapsed)
	}
	if got := len(commands); got < 2 {
		t.Fatalf("polls observed=%d, want at least 2", got)
	}
	if result.Last.Detail != "iron-plate 0 of 9" {
		t.Fatalf("last detail: %+v", result.Last)
	}
}

// Machine status, research, fluid, production and craft-queue conditions must
// each emit their own engine read and decode the matching observation field.
func TestWaitUntilConditionLua(t *testing.T) {
	cases := []struct {
		name      string
		condition *WaitConditionInput
		response  string
		wants     []string
		check     func(*testing.T, *WaitResult)
	}{
		{
			name:      "craft queue",
			condition: &WaitConditionInput{CraftQueueEmpty: &ActorInput{Character: new("12")}},
			response:  `{"tick":7,"satisfied":true,"queueSize":0,"detail":"crafting queue 0"}`,
			wants:     []string{"local unit=12", "c.crafting_queue", "queueSize=size"},
			check: func(t *testing.T, r *WaitResult) {
				if r.Condition != WaitConditionKindCraftQueueEmpty || r.Last.QueueSize == nil || *r.Last.QueueSize != 0 {
					t.Fatalf("queue observation: %+v", r)
				}
			},
		},
		{
			name: "machine status",
			condition: &WaitConditionInput{MachineStatus: &WaitMachineStatusInput{
				Target:   &EntityRefInput{Surface: new("nauvis"), UnitNumber: new("88")},
				Statuses: []string{"no_recipe", "no_fuel"},
			}},
			response: `{"tick":8,"satisfied":true,"status":"no_fuel","detail":"status no_fuel"}`,
			wants: []string{
				`local target_unit=88`,
				`local allowed={[` + luaString("no_recipe") + `]=true,[` + luaString("no_fuel") + `]=true}`,
				"defines.entity_status",
				"target.status",
			},
			check: func(t *testing.T, r *WaitResult) {
				if r.Last.Status == nil || *r.Last.Status != "no_fuel" {
					t.Fatalf("status observation: %+v", r.Last)
				}
			},
		},
		{
			name:      "research",
			condition: &WaitConditionInput{Research: &WaitResearchInput{Force: "player", Technology: "automation"}},
			response:  `{"tick":9,"satisfied":true,"researched":true,"detail":"automation researched=true"}`,
			wants:     []string{`local technology=` + luaString("automation"), "tech.researched"},
			check: func(t *testing.T, r *WaitResult) {
				if r.Last.Researched == nil || !*r.Last.Researched {
					t.Fatalf("research observation: %+v", r.Last)
				}
			},
		},
		{
			name: "fluid",
			condition: &WaitConditionInput{Fluid: &WaitFluidInput{
				Target:  &EntityRefInput{Surface: new("nauvis"), Entity: new("storage-tank"), Position: &PositionInput{X: 4.5, Y: 5.5}},
				Fluid:   new("crude-oil"),
				AtLeast: 100,
			}},
			response: `{"tick":10,"satisfied":true,"count":120,"detail":"fluid 120 of 100"}`,
			wants: []string{
				"target.get_fluid_contents()",
				`local wanted=` + luaString("crude-oil"),
				"total>=target_amount",
			},
			check: func(t *testing.T, r *WaitResult) {
				if r.Last.Count == nil || *r.Last.Count != 120 {
					t.Fatalf("fluid observation: %+v", r.Last)
				}
			},
		},
		{
			name: "production",
			condition: &WaitConditionInput{Production: &WaitProductionInput{
				Surface: "nauvis", Force: "player", Item: "iron-plate", Count: 30, Window: ProductionWindowTenMinutes,
			}},
			response: `{"tick":11,"satisfied":true,"count":31,"detail":"iron-plate 31 of 30"}`,
			wants: []string{
				"get_item_production_statistics",
				"defines.flow_precision_index.ten_minutes",
				`local item=` + luaString("iron-plate"),
			},
			check: func(t *testing.T, r *WaitResult) {
				if r.Condition != WaitConditionKindProduction || r.Last.Count == nil || *r.Last.Count != 31 {
					t.Fatalf("production observation: %+v", r)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, commands := serveLuaOnce(t, tc.response)
			result, err := waitUntil(context.Background(), r, WaitUntilInput{
				TimeoutSeconds: 5, PollSeconds: 0.05, Condition: tc.condition,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Satisfied {
				t.Fatalf("result: %+v", result)
			}
			tc.check(t, result)
			lua := <-commands
			for _, want := range tc.wants {
				if !strings.Contains(lua, want) {
					t.Fatalf("poll Lua missing %q\n%s", want, lua)
				}
			}
		})
	}
}

// Invalid inputs must fail before any RCON poll.
func TestWaitUntilValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	both := &WaitConditionInput{Ticks: new(5.0), Research: &WaitResearchInput{Force: "player", Technology: "automation"}}
	none := &WaitConditionInput{}
	cases := []struct {
		name  string
		input WaitUntilInput
		want  string
	}{
		{"timeout low", WaitUntilInput{TimeoutSeconds: 0.5, PollSeconds: 0.25, Condition: &WaitConditionInput{Ticks: new(1.0)}}, "timeoutSeconds must be at least 1"},
		{"poll low", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.01, Condition: &WaitConditionInput{Ticks: new(1.0)}}, "pollSeconds must be between"},
		{"poll high", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 6, Condition: &WaitConditionInput{Ticks: new(1.0)}}, "pollSeconds must be between"},
		{"no condition", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: none}, "exactly one field"},
		{"two conditions", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: both}, "exactly one field"},
		{"nil condition", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25}, "condition is required"},
		{"zero ticks", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{Ticks: new(0.0)}}, "ticks must be finite and greater than 0"},
		{"empty item", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{Inventory: &WaitInventoryInput{Actor: &ActorInput{Character: new("1")}, Count: 1}}}, "item must not be empty"},
		{"zero count", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{Inventory: &WaitInventoryInput{Actor: &ActorInput{Character: new("1")}, Item: "iron-plate", Quality: "normal"}}}, "count must be finite and greater than 0"},
		{"empty status list", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{MachineStatus: &WaitMachineStatusInput{Target: &EntityRefInput{UnitNumber: new("2")}}}}, "statuses must contain between"},
		{"blank status", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{MachineStatus: &WaitMachineStatusInput{Target: &EntityRefInput{UnitNumber: new("2")}, Statuses: []string{" "}}}}, "status 1 must not be empty"},
		{"entity without address", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{MachineStatus: &WaitMachineStatusInput{Target: &EntityRefInput{}, Statuses: []string{"no_fuel"}}}}, "entity name or unitNumber is required"},
		{"unit and position", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{Fluid: &WaitFluidInput{Target: &EntityRefInput{UnitNumber: new("3"), Position: &PositionInput{X: 1, Y: 2}}, AtLeast: 1}}}, "not both"},
		{"empty technology", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{Research: &WaitResearchInput{Force: "player"}}}, "technology must not be empty"},
		{"bad window", WaitUntilInput{TimeoutSeconds: 5, PollSeconds: 0.25, Condition: &WaitConditionInput{Production: &WaitProductionInput{Surface: "nauvis", Force: "player", Item: "iron-plate", Count: 1, Window: ProductionWindow("WEEKLY")}}}, "unsupported production window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := waitUntil(context.Background(), r, tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// Cancelling the request must stop the poll loop instead of waiting out the
// timeout.
func TestWaitUntilContextCanceled(t *testing.T) {
	r, _ := serveLuaRepeated(t, `{"tick":1,"satisfied":false,"count":0,"detail":"waiting"}`)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		_, err := waitUntil(ctx, r, WaitUntilInput{
			TimeoutSeconds: 30, PollSeconds: 0.2,
			Condition: &WaitConditionInput{Ticks: new(1e9)},
		})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("want context cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitUntil did not stop after cancel")
	}
}

// Every wait is capped at 10 seconds; larger requests are clamped rather than
// rejected so an agent cannot block the CLI for minutes.
func TestClampWaitSeconds(t *testing.T) {
	if got := clampWaitSeconds(3600); got != 10*time.Second {
		t.Fatalf("clampWaitSeconds(3600) = %v, want 10s", got)
	}
	if got := clampWaitSeconds(7); got != 7*time.Second {
		t.Fatalf("clampWaitSeconds(7) = %v, want 7s", got)
	}
}

func TestWaitTickTarget(t *testing.T) {
	if got := waitTickTarget(100, 50); got != 150 {
		t.Fatalf("waitTickTarget(100, 50) = %v, want 150", got)
	}
	if got := waitTickTarget(100.5, 2); got != 103 {
		t.Fatalf("waitTickTarget(100.5, 2) = %v, want 103", got)
	}
}
