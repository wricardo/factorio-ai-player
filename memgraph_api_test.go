package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TestQueryMemgraphSyncAfterValidation pins the order of the sync path: the
// statement is validated before an export starts, so a rejected statement
// never costs a multi-second export. The dead RCON address makes an early
// export fail loudly instead of reaching Memgraph.
func TestQueryMemgraphSyncAfterValidation(t *testing.T) {
	_, err := queryMemgraph(context.Background(), &Resolver{addr: "127.0.0.1:1", password: "x"},
		MemgraphQueryInput{Cypher: "MATCH (n) DELETE n", Sync: true, Limit: 1000, TimeoutMs: 1000})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want read-only guard error before any export, got %v", err)
	}
}

func TestValidateReadOnlyCypher(t *testing.T) {
	allowed := []string{
		"MATCH (n) RETURN n",
		"  optional match (a)-[:BELT_OUT*0..9]->(b) return b",
		"MATCH (n {name: 'create'}) RETURN n.set, n.delete",
		"MATCH (n:Set) RETURN n // DELETE everything",
		"MATCH (n) /* MERGE */ RETURN `drop` AS x",
		`MATCH (n) WHERE n.name = "it's \" CREATE" RETURN n`,
		"UNWIND $ids AS id MATCH (n {id: id}) RETURN n",
		"EXPLAIN MATCH (n) RETURN n",
		"MATCH (n) RETURN n;",
		"MATCH (n) RETURN n;  \n",
		"MATCH (n {name: 'a;b'}) RETURN n;",
	}
	for _, q := range allowed {
		if err := validateReadOnlyCypher(q); err != nil {
			t.Errorf("rejected %q: %v", q, err)
		}
	}
	rejected := []string{
		"",
		"   // only a comment",
		"CREATE (n)",
		"MATCH (n) DETACH DELETE n",
		"MATCH (n) SET n.x = 1 RETURN n",
		"MATCH (n) REMOVE n.x",
		"MATCH (n) CALL mg.something() YIELD x RETURN x",
		"MATCH (n) RETURN n; DROP GRAPH",
		"MATCH (n) RETURN n; MATCH (m) RETURN m",
		"MATCH (n) FOREACH (x IN [1] | CREATE ())",
		"SHOW CONFIG",
		"FREE MEMORY",
		"LOAD CSV FROM 'x' AS r RETURN r",
		"MATCH (n) RETURN 'unterminated",
		"MATCH (n) /* unterminated RETURN n",
		"MATCH (n) WITH n MERGE (m)",
	}
	for _, q := range rejected {
		if err := validateReadOnlyCypher(q); err == nil {
			t.Errorf("accepted %q", q)
		}
	}
}

func TestMemgraphReadErrorClassification(t *testing.T) {
	syntax := &neo4j.Neo4jError{Code: "Memgraph.ClientError.MemgraphError.MemgraphError", Msg: "mismatched input RETURN"}
	if got := memgraphReadError("Memgraph query", syntax).Error(); !strings.HasPrefix(got, "invalid Cypher:") || strings.Contains(got, "graph-up") {
		t.Fatalf("syntax error = %q, want invalid Cypher without stack hint", got)
	}
	for name, err := range map[string]error{
		"auth":         &neo4j.Neo4jError{Code: "Neo.ClientError.Security.Unauthorized", Msg: "bad"},
		"connectivity": &neo4j.ConnectivityError{Inner: errors.New("connection refused")},
	} {
		if got := memgraphReadError("Memgraph query", err).Error(); !strings.Contains(got, "graph-up") {
			t.Fatalf("%s error = %q, want stack hint", name, got)
		}
	}
}
