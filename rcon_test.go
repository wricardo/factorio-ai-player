package main

import (
	"net"
	"testing"
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
