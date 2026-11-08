package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// runGraphQLInProcess executes one query or mutation against the schema
// without opening a socket. args are [--addr HOST:PORT] KIND, then the
// document and optional --variables JSON / --format NAME in any order; KIND is "query" or "mutation". The raw
// JSON response is written to out, and a response carrying GraphQL errors is
// returned as an error holding every message, so a rejected document fails
// before any RCON call exactly as it does over HTTP.
func runGraphQLInProcess(args []string, out io.Writer, password string) error {
	addr := "127.0.0.1:0"
	if len(args) >= 2 && args[0] == "--addr" {
		addr, args = args[1], args[2:]
	}
	if len(args) < 2 {
		return errors.New("operation kind and document required")
	}
	kind := args[0]
	if kind != "query" && kind != "mutation" {
		return fmt.Errorf("unknown operation kind %q", kind)
	}
	var request graphQLHTTPRequest
	for rest := args[1:]; len(rest) > 0; {
		if !strings.HasPrefix(rest[0], "--") {
			if request.Query != "" {
				return fmt.Errorf("unexpected extra argument %q", rest[0])
			}
			request.Query, rest = rest[0], rest[1:]
			continue
		}
		if len(rest) < 2 {
			return fmt.Errorf("flag %s needs a value", rest[0])
		}
		switch rest[0] {
		case "--variables":
			if err := json.Unmarshal([]byte(rest[1]), &request.Variables); err != nil {
				return fmt.Errorf("invalid variables JSON: %w", err)
			}
		case "--format":
		default:
			return fmt.Errorf("unknown flag %s", rest[0])
		}
		rest = rest[2:]
	}
	if request.Query == "" {
		return errors.New("operation document required")
	}
	if got := graphQLDocumentKind(request.Query); got != kind {
		return fmt.Errorf("%s command requires a %s document, got %s", kind, kind, got)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req := httptest.NewRequest(http.MethodPost, graphqlPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newGraphQLHandler(newGraphQLSchema(addr, password)).ServeHTTP(recorder, req)
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		return fmt.Errorf("GraphQL HTTP %d: %w", recorder.Code, err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			messages[i] = e.Message
		}
		return fmt.Errorf("GraphQL: %s", strings.Join(messages, "; "))
	}
	_, err = out.Write(recorder.Body.Bytes())
	return err
}
