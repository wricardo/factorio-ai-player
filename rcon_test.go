package main

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// Factorio may execute the completion marker before the gameplay command when
// a client is connected. Both orders must return the entire command response.
func TestExecuteResponseOrdering(t *testing.T) {
	for _, markerFirst := range []bool{false, true} {
		name := "command-first"
		if markerFirst {
			name = "marker-first"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverErr := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverErr <- err
					return
				}
				defer conn.Close()
				_, _, _, err = readPacket(conn)
				if err == nil {
					err = writePacket(conn, 1, 2, "")
				}
				if err == nil {
					_, _, _, err = readPacket(conn)
				}
				if err == nil {
					_, _, _, err = readPacket(conn)
				}
				if err == nil && markerFirst {
					err = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
				}
				if err == nil {
					err = writePacket(conn, 2, 0, `{"tick":`)
				}
				if err == nil {
					err = writePacket(conn, 2, 0, "123}\n")
				}
				if err == nil && markerFirst {
					_, _, _, err = readPacket(conn)
				}
				if err == nil {
					id := int32(3)
					if markerFirst {
						id = 4
					}
					err = writePacket(conn, id, 0, "factorio-ai-player-response-end\n")
				}
				serverErr <- err
			}()
			response, err := execute(listener.Addr().String(), "password", "/silent-command example", false)
			if err != nil {
				t.Fatal(err)
			}
			if response != "{\"tick\":123}\n" {
				t.Fatalf("incomplete response: %q", response)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A command that prints nothing is still acknowledged with one empty response
// packet; the client must return an empty response promptly instead of waiting
// for the connection deadline.
func TestExecuteEmptyOutputReturnsPromptly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _, _ = readPacket(conn)
		_ = writePacket(conn, 1, 2, "")
		_, _, _, _ = readPacket(conn)
		_, _, _, _ = readPacket(conn)
		_ = writePacket(conn, 2, 0, "")
		_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
		<-done
	}()
	start := time.Now()
	response, err := execute(listener.Addr().String(), "password", "/silent-command example", false)
	if err != nil || response != "" || time.Since(start) > 3*time.Second {
		t.Fatalf("response %q, error %v after %v", response, err, time.Since(start))
	}
}

// Factorio can answer the completion marker while a slow command is still
// running, so a marker that arrives first must not be read as an empty reply.
func TestExecuteSlowCommandAfterMarker(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		_, _, _, err = readPacket(conn)
		if err == nil {
			err = writePacket(conn, 1, 2, "")
		}
		if err == nil {
			_, _, _, err = readPacket(conn)
		}
		if err == nil {
			_, _, _, err = readPacket(conn)
		}
		if err == nil {
			err = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
		}
		if err == nil {
			time.Sleep(1500 * time.Millisecond)
			err = writePacket(conn, 2, 0, `{"tick":123}`)
		}
		if err == nil {
			_, _, _, err = readPacket(conn)
		}
		if err == nil {
			err = writePacket(conn, 4, 0, "factorio-ai-player-response-end\n")
		}
		serverErr <- err
	}()
	response, err := execute(listener.Addr().String(), "password", "/silent-command slow", false)
	if err != nil {
		t.Fatal(err)
	}
	if response != `{"tick":123}` {
		t.Fatalf("incomplete response: %q", response)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

// Factorio closes the connection without replying once /quit succeeds.
func TestRCONClosedAfterQuit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_, _, _, _ = readPacket(conn)
		_ = writePacket(conn, 1, 2, "")
		_, _, _, _ = readPacket(conn)
		conn.Close()
	}()
	var out bytes.Buffer
	if err := executeGameOperation([]string{"shutdown"}, &out, listener.Addr().String(), "password"); err != nil {
		t.Fatal(err)
	}
}
