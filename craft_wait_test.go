package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestWaitForCraftQueue(t *testing.T) {
	t.Run("already empty", func(t *testing.T) {
		calls := 0
		queue, complete, waited, err := waitForCraftQueue(context.Background(), 0, time.Second, func(context.Context) (float64, error) {
			calls++
			return 0, nil
		})
		if err != nil || !complete || queue != 0 || waited != 0 || calls != 0 {
			t.Fatalf("got queue=%v complete=%v waited=%v calls=%d err=%v", queue, complete, waited, calls, err)
		}
	})

	t.Run("drains queue", func(t *testing.T) {
		statuses := []float64{2, 0}
		queue, complete, waited, err := waitForCraftQueue(context.Background(), 3, 2*time.Second, func(context.Context) (float64, error) {
			status := statuses[0]
			statuses = statuses[1:]
			return status, nil
		})
		if err != nil || !complete || queue != 0 || waited <= 0 || len(statuses) != 0 {
			t.Fatalf("got queue=%v complete=%v waited=%v statuses=%v err=%v", queue, complete, waited, statuses, err)
		}
	})

	t.Run("timeout retains latest queue size", func(t *testing.T) {
		queue, complete, waited, err := waitForCraftQueue(context.Background(), 3, 300*time.Millisecond, func(context.Context) (float64, error) {
			return 2, nil
		})
		if err != nil || complete || queue != 2 || waited < 300*time.Millisecond {
			t.Fatalf("got queue=%v complete=%v waited=%v err=%v", queue, complete, waited, err)
		}
	})

	t.Run("cancellation reports active queue", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		queue, complete, _, err := waitForCraftQueue(ctx, 1, time.Second, func(context.Context) (float64, error) {
			t.Fatal("status called after cancellation")
			return 0, nil
		})
		if !errors.Is(err, context.Canceled) || complete || queue != 1 {
			t.Fatalf("got queue=%v complete=%v err=%v", queue, complete, err)
		}
	})
}

// fakeCraftRCON answers craft, queue polls, and game.print commands on separate
// connections, collecting every print command body.
func fakeCraftRCON(t *testing.T) (net.Listener, chan string) {
	t.Helper()
	const marker = "factorio-ai-player-response-end"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	prints := make(chan string, 8)
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
				if strings.Contains(body, "game.print(") {
					prints <- body
					_ = writePacket(conn, 2, 0, "")
					return
				}
				response := `{"queue_size":0}`
				if strings.Contains(body, "begin_crafting") {
					response = `{"started":1,"requested":1,"recipe":"iron-gear-wheel","queue_size":1}`
				}
				if err := writePacket(conn, 2, 0, response); err != nil {
					return
				}
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				_ = writePacket(conn, 3, 0, marker)
			}(conn)
		}
	}()
	return listener, prints
}

func TestCraftAnnouncesOnce(t *testing.T) {
	_ = newGraphQLSchema("", "") // mutationFields drives generic announce eligibility
	count := 1.0
	cases := []struct {
		name  string
		input CraftInput
		want  string
	}{
		{"queue only", CraftInput{Player: new("wricardo"), Recipe: "iron-gear-wheel", Count: &count, WaitTimeoutSeconds: 1}, "queued 1 of 1 iron-gear-wheel"},
		{"custom message", CraftInput{Player: new("wricardo"), Recipe: "iron-gear-wheel", Count: &count, WaitTimeoutSeconds: 1, Message: new("custom note")}, "custom note"},
		{"wait drains queue", CraftInput{Player: new("wricardo"), Recipe: "iron-gear-wheel", Count: &count, Wait: true, WaitTimeoutSeconds: 5}, "queued 1 of 1 iron-gear-wheel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listener, prints := fakeCraftRCON(t)
			r := &Resolver{addr: listener.Addr().String(), password: "password"}
			if _, err := craftMutation(context.Background(), r, tc.input); err != nil {
				t.Fatal(err)
			}
			var got []string
			for len(prints) > 0 {
				got = append(got, <-prints)
			}
			want := "/silent-command game.print(" + luaString(tc.want) + ")"
			if len(got) != 1 || got[0] != want {
				t.Fatalf("announcements: got %q, want [%q]", got, want)
			}
		})
	}
}
