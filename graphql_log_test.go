package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readGraphQLOperationLog parses every JSONL record written to path.
func readGraphQLOperationLog(t *testing.T, path string) []graphQLOperationLog {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read operation log: %v", err)
	}
	var entries []graphQLOperationLog
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry graphQLOperationLog
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode operation log line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func responseErrors(t *testing.T, entry graphQLOperationLog) []interface{} {
	t.Helper()
	var envelope struct {
		Errors []interface{} `json:"errors"`
	}
	if err := json.Unmarshal(entry.Response, &envelope); err != nil {
		t.Fatalf("decode logged response %q: %v", entry.Response, err)
	}
	return envelope.Errors
}

func TestCLILogsGraphQLOperationsIncludingValidationFailures(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "graphql-log.jsonl")
	t.Setenv(graphqlLogEnv, logPath)

	var out bytes.Buffer
	if err := runWithCredentials([]string{"query", "{ __typename }"}, &out, ""); err != nil {
		t.Fatalf("successful query: %v", err)
	}
	if err := runWithCredentials([]string{"mutation", "mutation { __typename }"}, &out, ""); err != nil {
		t.Fatalf("successful mutation: %v", err)
	}
	err := runWithCredentials([]string{"query", "{ unknownField }"}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "unknownField") {
		t.Fatalf("invalid query error = %v, want unknownField validation failure", err)
	}

	entries := readGraphQLOperationLog(t, logPath)
	if len(entries) != 3 {
		t.Fatalf("log entries = %d, want 3: %+v", len(entries), entries)
	}
	for i, want := range []struct{ command, document string }{
		{"query", "{ __typename }"},
		{"mutation", "mutation { __typename }"},
		{"query", "{ unknownField }"},
	} {
		entry := entries[i]
		if entry.Source != "cli" || entry.Command != want.command || entry.Document != want.document {
			t.Fatalf("entry %d = source %q command %q document %q, want cli/%s/%q", i, entry.Source, entry.Command, entry.Document, want.command, want.document)
		}
		if entry.Time.IsZero() || entry.Response == nil {
			t.Fatalf("entry %d lacks time or response: %+v", i, entry)
		}
	}
	if errs := responseErrors(t, entries[2]); len(errs) == 0 {
		t.Fatalf("validation failure recorded without errors: %s", entries[2].Response)
	}
}

func TestServeLogsGraphQLRequestsIncludingValidationFailures(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "graphql-log.jsonl")
	t.Setenv(graphqlLogEnv, logPath)

	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema("127.0.0.1:1", "password")))
	defer server.Close()

	post := func(document string) {
		body, err := json.Marshal(graphQLHTTPRequest{Query: document})
		if err != nil {
			t.Fatal(err)
		}
		reply, err := http.Post(server.URL+graphqlPath, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer reply.Body.Close()
		if _, err := bytes.NewBuffer(nil).ReadFrom(reply.Body); err != nil {
			t.Fatal(err)
		}
	}
	post("{ __typename }")
	post("{ unknownField }")

	entries := readGraphQLOperationLog(t, logPath)
	if len(entries) != 2 {
		t.Fatalf("log entries = %d, want 2: %+v", len(entries), entries)
	}
	first, second := entries[0], entries[1]
	if first.Source != "serve" || first.Command != "query" || first.Document != "{ __typename }" || first.HTTPStatus != http.StatusOK {
		t.Fatalf("successful request entry = %+v", first)
	}
	if second.Document != "{ unknownField }" || second.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("validation failure entry = %+v", second)
	}
	if errs := responseErrors(t, second); len(errs) == 0 {
		t.Fatalf("validation failure recorded without errors: %s", second.Response)
	}
}

func TestScriptLogsEveryGraphQLCall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "graphql-log.jsonl")
	t.Setenv(graphqlLogEnv, logPath)

	var out bytes.Buffer
	source := `async function run(gql) {
		await gql.query("{ __typename }")
		return gql.mutation("mutation { __typename }")
	}`
	if err := runWithCredentials([]string{"script", "--source", source}, &out, ""); err != nil {
		t.Fatalf("script: %v", err)
	}

	entries := readGraphQLOperationLog(t, logPath)
	if len(entries) != 2 {
		t.Fatalf("log entries = %d, want 2: %+v", len(entries), entries)
	}
	if entries[0].Command != "script" || entries[0].Document != "{ __typename }" {
		t.Fatalf("query entry = %+v", entries[0])
	}
	if entries[1].Command != "script" || !strings.HasPrefix(entries[1].Document, "mutation") {
		t.Fatalf("mutation entry = %+v", entries[1])
	}
}

func TestGraphQLDocumentKind(t *testing.T) {
	cases := map[string]string{
		"{ __typename }":                       "query",
		"query Q { __typename }":               "query",
		"mutation M { shutdown { response } }": "mutation",
		"# comment\nmutation { x }":            "mutation",
		"":                                     "",
	}
	for document, want := range cases {
		if got := graphQLDocumentKind(document); got != want {
			t.Errorf("graphQLDocumentKind(%q) = %q, want %q", document, got, want)
		}
	}
}
