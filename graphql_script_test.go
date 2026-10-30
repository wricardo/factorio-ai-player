package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptRunsInlineQueryAndMutation(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "query",
			source: `function run(gql) { return gql.query("{ __typename }") }`,
			want:   "Query",
		},
		{
			name:   "mutation",
			source: `async function run(gql) { return gql.mutation("mutation { __typename }") }`,
			want:   "Mutation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runWithCredentials([]string{"script", "--source", tt.source}, &out, "")
			if err != nil {
				t.Fatalf("script failed: %v", err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("output missing %q: %s", tt.want, out.String())
			}
		})
	}
}

func TestScriptRunsFileWithArg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.js")
	source := `function run(gql, input) {
  return { count: input.count, typename: gql.query("{ __typename }").data.__typename }
}`
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runWithCredentials([]string{"script", "--file", path, "--arg", `{"count":3}`}, &out, "")
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	for _, want := range []string{`"count": 3`, `"typename": "Query"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
}

func TestScriptOpUsesSavedFunctionAndDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	config := `{
  "scripts": {
    "status": {
      "function": "main",
      "source": "function main(gql, input) { return input }",
      "defaults": {"surface": "nauvis", "limit": 1}
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, ".gqlcli.json"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runWithCredentials([]string{"script", "--op", "status", "--arg", `{"limit":5}`}, &out, "")
	if err != nil {
		t.Fatalf("script failed: %v", err)
	}
	for _, want := range []string{`"surface": "nauvis"`, `"limit": 5`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q: %s", want, out.String())
		}
	}
}

func TestScriptRejectsMultipleSources(t *testing.T) {
	var out bytes.Buffer
	err := runWithCredentials([]string{"script", "--source", "function run() {}", "--file", "workflow.js"}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "only one of --file, --source, or --op") {
		t.Fatalf("expected source conflict error, got %v", err)
	}
}
