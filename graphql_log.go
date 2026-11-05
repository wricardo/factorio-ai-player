package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gqlcli "github.com/wricardo/gqlcli/pkg"
)

// graphqlLogEnv names the file that receives one JSON object per GraphQL
// operation. Unset logs to defaultGraphQLLogPath; set to an empty value
// disables logging.
const graphqlLogEnv = "FACTORIO_GRAPHQL_LOG"

// defaultGraphQLLogPath lives beside the other ignored local state, so logging
// works from any depth without a configuration step.
const defaultGraphQLLogPath = ".factorio/graphql-log.jsonl"

// graphQLOperationLog is one line of the GraphQL operation log. Response keeps
// the whole GraphQL envelope, so a validation failure and a partially resolved
// operation are recorded with the same errors the caller saw.
type graphQLOperationLog struct {
	Time          time.Time              `json:"time"`
	Source        string                 `json:"source"`
	Command       string                 `json:"command"`
	OperationName string                 `json:"operationName,omitempty"`
	Document      string                 `json:"document"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
	DurationMS    float64                `json:"durationMs"`
	HTTPStatus    int                    `json:"httpStatus,omitempty"`
	Response      json.RawMessage        `json:"response,omitempty"`
	Error         string                 `json:"error,omitempty"`
}

var (
	graphqlLogMu     sync.Mutex
	graphqlLogWarned bool
)

// graphqlLogTarget resolves the log destination per operation so a test or a
// long-lived server can redirect it without holding a handle open.
func graphqlLogTarget() (string, bool) {
	path, set := os.LookupEnv(graphqlLogEnv)
	if set {
		path = strings.TrimSpace(path)
		return path, path != ""
	}
	return defaultGraphQLLogPath, true
}

func appendGraphQLOperationLog(entry graphQLOperationLog) {
	path, enabled := graphqlLogTarget()
	if !enabled {
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		warnGraphQLLog(err)
		return
	}
	line = append(line, '\n')

	graphqlLogMu.Lock()
	defer graphqlLogMu.Unlock()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			warnGraphQLLog(err)
			return
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		warnGraphQLLog(err)
		return
	}
	if _, err := file.Write(line); err != nil {
		warnGraphQLLog(err)
	}
	if err := file.Close(); err != nil {
		warnGraphQLLog(err)
	}
}

// warnGraphQLLog reports the first failure only: a bad log path must not turn
// every operation into a failure or an unbounded stderr stream.
func warnGraphQLLog(err error) {
	if graphqlLogWarned {
		return
	}
	graphqlLogWarned = true
	fmt.Fprintf(os.Stderr, "warning: GraphQL operation log disabled: %v\n", err)
}

// recordGraphQLOperation appends one operation record. started is the instant
// the operation was accepted, not the instant execution began, so validation
// failures are timed and recorded too.
func recordGraphQLOperation(source, command, document string, variables map[string]interface{}, operationName string, started time.Time, response json.RawMessage, httpStatus int, err error) {
	entry := graphQLOperationLog{
		Time:          time.Now(),
		Source:        source,
		Command:       command,
		OperationName: operationName,
		Document:      document,
		Variables:     variables,
		DurationMS:    float64(time.Since(started).Microseconds()) / 1000,
		HTTPStatus:    httpStatus,
	}
	if len(response) > 0 {
		entry.Response = response
	}
	if err != nil {
		entry.Error = err.Error()
	}
	appendGraphQLOperationLog(entry)
}

// graphQLExecutor is the in-process execution surface used by the CLI
// commands. Routing every command through it keeps operation logging in one
// place, including documents gqlgen rejects before a resolver runs.
type graphQLExecutor interface {
	Execute(ctx context.Context, query string, variables map[string]interface{}) (json.RawMessage, error)
}

// loggedGraphQLExecutor logs each call against the underlying inline executor.
type loggedGraphQLExecutor struct {
	inner   *gqlcli.InlineExecutor
	source  string
	command string
}

func logGraphQLOperations(exec *gqlcli.InlineExecutor, source, command string) *loggedGraphQLExecutor {
	return &loggedGraphQLExecutor{inner: exec, source: source, command: command}
}

func (e *loggedGraphQLExecutor) Execute(ctx context.Context, query string, variables map[string]interface{}) (json.RawMessage, error) {
	started := time.Now()
	raw, err := e.inner.Execute(ctx, query, variables)
	recordGraphQLOperation(e.source, e.command, query, variables, "", started, raw, 0, err)
	return raw, err
}

// scriptOperationExecutor adapts graphQLExecutor to the OperationExecutor the
// JavaScript runner needs, so each gql.query/gql.mutation inside a script is
// logged by the wrapped executor.
type scriptOperationExecutor struct {
	exec graphQLExecutor
}

func (s scriptOperationExecutor) Execute(ctx context.Context, _ gqlcli.ExecutionMode, opts gqlcli.QueryOptions) (map[string]interface{}, error) {
	return s.run(ctx, opts.Query, opts.Variables)
}

func (s scriptOperationExecutor) ExecuteMutation(ctx context.Context, _ gqlcli.ExecutionMode, opts gqlcli.MutationOptions) (map[string]interface{}, error) {
	variables := opts.Variables
	if opts.Input != nil {
		if variables == nil {
			variables = make(map[string]interface{})
		}
		variables["input"] = opts.Input
	}
	return s.run(ctx, opts.Mutation, variables)
}

func (s scriptOperationExecutor) run(ctx context.Context, operation string, variables map[string]interface{}) (map[string]interface{}, error) {
	raw, err := s.exec.Execute(ctx, operation, variables)
	if err != nil {
		return nil, err
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse inline GraphQL response: %w", err)
	}
	return envelope, nil
}

// graphQLDocumentKind labels a document by its first executable token so HTTP
// requests, which arrive without a command name, are still distinguishable.
func graphQLDocumentKind(document string) string {
	s := strings.TrimLeft(document, " \t\r\n,")
	for strings.HasPrefix(s, "#") {
		end := strings.IndexByte(s, '\n')
		if end < 0 {
			return ""
		}
		s = strings.TrimLeft(s[end+1:], " \t\r\n,")
	}
	if strings.HasPrefix(s, "{") {
		return "query"
	}
	for _, kind := range []string{"query", "mutation", "subscription"} {
		if !strings.HasPrefix(s, kind) {
			continue
		}
		rest := s[len(kind):]
		if rest == "" || strings.ContainsRune(" \t\r\n({$@", rune(rest[0])) {
			return kind
		}
	}
	return ""
}

// graphQLHTTPRequest is the subset of a GraphQL-over-HTTP body the log needs.
type graphQLHTTPRequest struct {
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables"`
	OperationName string                 `json:"operationName"`
}

// loggedGraphQLHandler records every request the HTTP endpoint accepts,
// including documents gqlgen rejects during parsing or validation: the
// buffered response carries the same errors the client received.
func loggedGraphQLHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		request, readErr := readGraphQLHTTPRequest(r)
		if readErr != nil {
			var tooLarge *http.MaxBytesError
			status := http.StatusBadRequest
			if errors.As(readErr, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, readErr.Error(), status)
			recordGraphQLOperation("serve", "", request.Query, request.Variables, request.OperationName, started, nil, status, readErr)
			return
		}
		recorder := &graphQLResponseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		recordGraphQLOperation("serve", graphQLDocumentKind(request.Query), request.Query, request.Variables, request.OperationName, started, recorder.body.Bytes(), recorder.status, nil)
	})
}

// readGraphQLHTTPRequest extracts the operation from a GET query string or a
// JSON POST body and restores the body so the GraphQL transport still reads it.
func readGraphQLHTTPRequest(r *http.Request) (graphQLHTTPRequest, error) {
	if r.Method == http.MethodGet {
		query := r.URL.Query()
		request := graphQLHTTPRequest{Query: query.Get("query"), OperationName: query.Get("operationName")}
		if raw := query.Get("variables"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &request.Variables); err != nil {
				return request, fmt.Errorf("invalid variables query parameter: %w", err)
			}
		}
		return request, nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return graphQLHTTPRequest{}, err
	}
	var request graphQLHTTPRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return graphQLHTTPRequest{}, fmt.Errorf("invalid GraphQL request body: %w", err)
	}
	return request, nil
}

// graphQLResponseRecorder buffers the response body while still forwarding it
// to the client, so the log holds exactly what the client received.
type graphQLResponseRecorder struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (r *graphQLResponseRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *graphQLResponseRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	r.body.Write(data)
	return r.ResponseWriter.Write(data)
}

// Flush forwards streaming writes; the buffer still receives every chunk.
func (r *graphQLResponseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *graphQLResponseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
