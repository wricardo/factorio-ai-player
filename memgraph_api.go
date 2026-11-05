package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// GraphQL surface for the Memgraph factory graph: exportMapSnapshotToMemgraph
// runs the same export + load as `graph sync`, queryMemgraph runs one
// read-only Cypher statement. The Bolt target is configuration
// (MEMGRAPH_BOLT/USER/PASSWORD), never caller input.

const (
	memgraphMaxRows       = 10000
	memgraphMinTimeoutMs  = 100
	memgraphMaxTimeoutMs  = 60000
	memgraphLoadBatchSize = 2000
)

// memgraphExportMu allows one export at a time: two concurrent loads would
// prune each other's rows.
var memgraphExportMu sync.Mutex

func memgraphConnection() graphLoadOptions {
	return graphLoadOptions{
		Bolt:     envOr("MEMGRAPH_BOLT", "bolt://127.0.0.1:7687"),
		User:     envOr("MEMGRAPH_USER", ""),
		Password: envOr("MEMGRAPH_PASSWORD", ""),
		Batch:    memgraphLoadBatchSize,
	}
}

func exportMapSnapshotToMemgraph(ctx context.Context, r *Resolver, in ExportMapSnapshotToMemgraphInput) (*MemgraphExportResult, error) {
	exportOpts := graphExportOptions{
		Force:            in.Force,
		Surfaces:         in.Surfaces,
		LeafMax:          in.LeafMax,
		Concurrency:      in.Concurrency,
		ChunkPage:        20000,
		IncludeLineLinks: in.IncludeLineLinks,
		AllowIncomplete:  in.AllowIncomplete,
		Keep:             5,
	}
	if err := validateGraphExportOptions(&exportOpts, in.Surfaces); err != nil {
		return nil, err
	}
	if !memgraphExportMu.TryLock() {
		return nil, errors.New("another Memgraph export is already running")
	}
	defer memgraphExportMu.Unlock()

	exportStarted := time.Now()
	dir, m, err := exportGraphContext(ctx, exportOpts, io.Discard, r.addr, r.password)
	if err != nil {
		return nil, err
	}
	exportMs := float64(time.Since(exportStarted)) / float64(time.Millisecond)

	loadOpts := memgraphConnection()
	loadOpts.In = dir
	loadOpts.AllowIncomplete = in.AllowIncomplete
	_, _, counts, took, err := loadGraphContext(ctx, loadOpts)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s was exported to %s but not loaded; the previous Memgraph data is unchanged: %w", m.Snapshot, dir, err)
	}

	result := &MemgraphExportResult{
		Snapshot:       m.Snapshot,
		Directory:      dir,
		Status:         m.Status,
		Force:          m.Force,
		Nodes:          counts.nodes,
		ExternalNodes:  m.External,
		Edges:          counts.edges,
		PrunedNodes:    counts.prunedNodes,
		PrunedEdges:    counts.prunedEdges,
		PrunedNetworks: counts.prunedNetworks,
		ExportMs:       exportMs,
		LoadMs:         float64(took) / float64(time.Millisecond),
		Surfaces:       make([]*MemgraphSurfaceReport, 0, len(m.Surfaces)),
		LabelCounts:    memgraphCounts(m.LabelCounts),
		EdgeTypes:      memgraphCounts(m.EdgeTypes),
	}
	for _, s := range m.Surfaces {
		result.Surfaces = append(result.Surfaces, &MemgraphSurfaceReport{
			Surface: s.Surface, Status: s.Status, Explanation: s.Explanation,
			TickBefore: s.TickBefore, TickAfter: s.TickAfter,
			StaticExported: s.Static, MobileExported: s.Mobile, Leaves: len(s.Leaves),
		})
	}
	return result, nil
}

func memgraphCounts(counts map[string]int) []*MemgraphCount {
	out := make([]*MemgraphCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, &MemgraphCount{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// memgraphSyncInput mirrors the schema defaults of
// ExportMapSnapshotToMemgraphInput. gqlgen applies those defaults only on the
// GraphQL path, so the sync option builds the same input explicitly.
func memgraphSyncInput() ExportMapSnapshotToMemgraphInput {
	return ExportMapSnapshotToMemgraphInput{Force: "player", LeafMax: 2000, Concurrency: 4}
}

func queryMemgraph(ctx context.Context, r *Resolver, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
	if in.Limit < 1 || in.Limit > memgraphMaxRows {
		return nil, fmt.Errorf("limit must be between 1 and %d", memgraphMaxRows)
	}
	if in.TimeoutMs < memgraphMinTimeoutMs || in.TimeoutMs > memgraphMaxTimeoutMs {
		return nil, fmt.Errorf("timeoutMs must be between %d and %d", memgraphMinTimeoutMs, memgraphMaxTimeoutMs)
	}
	if err := validateReadOnlyCypher(in.Cypher); err != nil {
		return nil, err
	}
	params, err := memgraphParams(in.Params)
	if err != nil {
		return nil, err
	}
	// The sync runs after the cheap validation so a rejected statement never
	// costs an export, and before the statement deadline so timeoutMs covers
	// only the Bolt read.
	if in.Sync {
		if _, err := exportMapSnapshotToMemgraph(ctx, r, memgraphSyncInput()); err != nil {
			return nil, fmt.Errorf("sync before queryMemgraph: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutMs)*time.Millisecond)
	defer cancel()
	driver, err := memgraphConnection().driver()
	if err != nil {
		return nil, err
	}
	defer driver.Close(context.Background())
	session := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(context.Background())

	started := time.Now()
	value, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		cursor, err := tx.Run(ctx, in.Cypher, params)
		if err != nil {
			return nil, err
		}
		result := &MemgraphQueryResult{Rows: [][]any{}}
		keys, err := cursor.Keys()
		if err != nil {
			return nil, err
		}
		result.Columns = keys
		for cursor.Next(ctx) {
			if len(result.Rows) == in.Limit {
				result.Truncated = true
				break
			}
			values := cursor.Record().Values
			row := make([]any, len(values))
			for i, v := range values {
				row[i] = memgraphValue(v)
			}
			result.Rows = append(result.Rows, row)
		}
		if err := cursor.Err(); err != nil {
			return nil, err
		}
		if _, err := cursor.Consume(ctx); err != nil {
			return nil, err
		}
		return result, nil
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("Memgraph query exceeded timeoutMs=%d", in.TimeoutMs)
		}
		return nil, fmt.Errorf("Memgraph query failed (is the stack up? `just graph-up`): %w", err)
	}
	result := value.(*MemgraphQueryResult)
	result.DurationMs = float64(time.Since(started)) / float64(time.Millisecond)
	return result, nil
}

// memgraphParams accepts the Any scalar as a JSON object. Integral numbers
// become int64 so Cypher integer arithmetic and index lookups work.
func memgraphParams(v any) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("params must be a JSON object")
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = memgraphParamValue(val)
	}
	return out, nil
}

func memgraphParamValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = memgraphParamValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = memgraphParamValue(e)
		}
		return out
	default:
		return v
	}
}

// memgraphValue renders a Bolt value as JSON-safe data. Graph structures keep
// their labels/type and properties; temporal and spatial values fall back to
// their string form.
func memgraphValue(v any) any {
	switch x := v.(type) {
	case nil, bool, int64, float64, string:
		return x
	case int:
		return int64(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = memgraphValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = memgraphValue(e)
		}
		return out
	case neo4j.Node:
		return map[string]any{"id": x.ElementId, "labels": x.Labels, "properties": memgraphValue(x.Props)}
	case neo4j.Relationship:
		return map[string]any{"id": x.ElementId, "type": x.Type, "startId": x.StartElementId, "endId": x.EndElementId,
			"properties": memgraphValue(x.Props)}
	case neo4j.Path:
		nodes := make([]any, len(x.Nodes))
		for i, n := range x.Nodes {
			nodes[i] = memgraphValue(n)
		}
		rels := make([]any, len(x.Relationships))
		for i, rel := range x.Relationships {
			rels[i] = memgraphValue(rel)
		}
		return map[string]any{"nodes": nodes, "relationships": rels}
	default:
		return fmt.Sprint(x)
	}
}

// memgraphStartKeywords are the only leading keywords a statement may have.
var memgraphStartKeywords = map[string]bool{
	"MATCH": true, "OPTIONAL": true, "WITH": true, "UNWIND": true, "RETURN": true, "EXPLAIN": true, "PROFILE": true,
}

// memgraphForbiddenKeywords reject write clauses and CALL (procedures can
// write); schema and administrative statements are already excluded because
// they cannot start with an allowed keyword. They are matched as whole words
// outside string literals, comments and quoted identifiers, and are ignored
// after "." or ":" so a property or label may share a keyword's spelling.
var memgraphForbiddenKeywords = map[string]bool{
	"CREATE": true, "MERGE": true, "DELETE": true, "DETACH": true, "SET": true, "REMOVE": true, "DROP": true,
	"FOREACH": true, "CALL": true, "LOAD": true,
}

type cypherWord struct {
	text   string
	member bool // preceded by "." or ":", so a property key or label
}

// cypherWords returns the bare words of a statement with literals, comments
// and backtick identifiers removed.
func cypherWords(q string) ([]cypherWord, error) {
	var words []cypherWord
	var prev byte
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '/' && i+1 < len(q) && q[i+1] == '/':
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated comment in Cypher")
			}
			i += end + 4
		case c == '\'' || c == '"' || c == '`':
			i++
			closed := false
			for i < len(q) {
				if q[i] == '\\' && c != '`' {
					i += 2
					continue
				}
				if q[i] == c {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated string or identifier in Cypher")
			}
			prev = 'x'
		case isCypherWordByte(c):
			start := i
			for i < len(q) && isCypherWordByte(q[i]) {
				i++
			}
			words = append(words, cypherWord{text: strings.ToUpper(q[start:i]), member: prev == '.' || prev == ':'})
			prev = 'x'
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		default:
			prev = c
			i++
		}
	}
	return words, nil
}

func isCypherWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// validateReadOnlyCypher is a lexical guard, not a permission system: a Bolt
// read transaction and, for shared endpoints, a read-only Memgraph user are
// the real boundary.
func validateReadOnlyCypher(q string) error {
	if strings.TrimSpace(q) == "" {
		return errors.New("cypher must not be empty")
	}
	words, err := cypherWords(q)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return errors.New("cypher contains no statement")
	}
	if !memgraphStartKeywords[words[0].text] {
		return fmt.Errorf("cypher must start with MATCH, OPTIONAL MATCH, WITH, UNWIND, RETURN, EXPLAIN or PROFILE, not %s", words[0].text)
	}
	for _, w := range words {
		if !w.member && memgraphForbiddenKeywords[w.text] {
			return fmt.Errorf("queryMemgraph is read-only: %s is not allowed", w.text)
		}
	}
	if strings.Contains(stripCypherLiterals(q), ";") {
		return errors.New("queryMemgraph runs exactly one statement; remove the semicolon")
	}
	return nil
}

// stripCypherLiterals blanks string literals, comments and quoted identifiers
// so structural characters can be searched without matching inside them.
func stripCypherLiterals(q string) string {
	var b strings.Builder
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '/' && i+1 < len(q) && q[i+1] == '/':
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 4
		case c == '\'' || c == '"' || c == '`':
			i++
			for i < len(q) && q[i] != c {
				if q[i] == '\\' && c != '`' {
					i++
				}
				i++
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
