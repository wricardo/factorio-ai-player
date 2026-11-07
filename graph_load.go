package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// graph load writes one snapshot directory into Memgraph. Nodes and edges are
// MERGEd by id (edges by key), so loading the same snapshot twice is a no-op.
// After every row is written, everything in the snapshot's scopes that does
// not carry this snapshot id is deleted: a snapshot replaces its scopes, it
// never patches them, because the world it describes may have been rebuilt.

//go:embed memgraph/schema.cypherl
var graphSchemaCypher string

type graphLoadOptions struct {
	Bolt            string
	User            string
	Password        string
	In              string
	Batch           int
	AllowIncomplete bool
}

var graphIdentifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// graphRelationshipTypes are the only relationship types a snapshot may carry.
var graphRelationshipTypes = map[string]bool{
	"BELT_OUT": true, "PICKS_FROM": true, "DROPS_TO": true, "DROPS_ON": true, "MINES": true, "AFFECTS": true,
	"RAIL_LINK": true, "FLUID_LINK": true, "POWER_WIRE": true, "CIRCUIT_WIRE": true,
	"POWERED_BY": true, "IN_LOGISTIC_NETWORK": true, "LOGISTIC_COVERS": true, "ON_CIRCUIT": true,
	"RUNS": true, "PRODUCES": true, "CONSUMES": true, "BURNS": true, "CONTAINS": true, "HOLDS": true,
	"IN_STRETCH": true, "FEEDS_STRETCH": true, "TAKES_FROM_STRETCH": true, "FLOWS_INTO": true,
	"ACCEPTS": true, "REQUIRES_PACK": true, "PREREQUISITE": true, "UNLOCKS": true,
}

// graphSchemaStatements reads memgraph/schema.cypherl, which holds one
// statement per line because Memgraph's --init-file parses it line by line.
func graphSchemaStatements() []string {
	var out []string
	for _, line := range strings.Split(graphSchemaCypher, "\n") {
		if s := strings.TrimSuffix(strings.TrimSpace(line), ";"); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func graphLoadFlags(fs *flag.FlagSet, opts *graphLoadOptions) {
	fs.StringVar(&opts.Bolt, "bolt", envOr("MEMGRAPH_BOLT", "bolt://127.0.0.1:7687"), "Memgraph Bolt URI (env MEMGRAPH_BOLT)")
	fs.StringVar(&opts.User, "user", os.Getenv("MEMGRAPH_USER"), "Memgraph user (env MEMGRAPH_USER; empty for no auth)")
	fs.StringVar(&opts.Password, "password", os.Getenv("MEMGRAPH_PASSWORD"), "Memgraph password (env MEMGRAPH_PASSWORD)")
	fs.IntVar(&opts.Batch, "batch", 2000, "rows per write transaction")
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func (o graphLoadOptions) driver() (neo4j.DriverWithContext, error) {
	auth := neo4j.NoAuth()
	if o.User != "" {
		auth = neo4j.BasicAuth(o.User, o.Password, "")
	}
	return neo4j.NewDriverWithContext(o.Bolt, auth)
}

func runGraphSchema(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("graph schema", flag.ContinueOnError)
	fs.SetOutput(out)
	var opts graphLoadOptions
	graphLoadFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ctx := context.Background()
	driver, err := opts.driver()
	if err != nil {
		return err
	}
	defer driver.Close(ctx)
	n, err := applyGraphSchema(ctx, driver)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "applied %d schema statements to %s\n", n, opts.Bolt)
	return nil
}

func applyGraphSchema(ctx context.Context, driver neo4j.DriverWithContext) (int, error) {
	if err := driver.VerifyConnectivity(ctx); err != nil {
		return 0, fmt.Errorf("connect Memgraph: %w", err)
	}
	session := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)
	statements := graphSchemaStatements()
	for _, s := range statements {
		// Memgraph rejects index and constraint DDL inside an explicit
		// transaction, so schema statements run as auto-commit queries.
		result, err := session.Run(ctx, s, nil)
		if err == nil {
			_, err = result.Consume(ctx)
		}
		if err != nil {
			return 0, fmt.Errorf("schema %q: %w", s, err)
		}
	}
	return len(statements), nil
}

func runGraphLoad(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("graph load", flag.ContinueOnError)
	fs.SetOutput(out)
	var opts graphLoadOptions
	graphLoadFlags(fs, &opts)
	fs.StringVar(&opts.In, "in", "", "snapshot directory (default the one named in .factorio/graph/LATEST)")
	fs.BoolVar(&opts.AllowIncomplete, "allow-incomplete", false, "load a snapshot whose reconciliation found a hole")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("graph load takes no positional arguments")
	}
	return loadGraph(opts, out)
}

func latestGraphSnapshot() (string, error) {
	data, err := os.ReadFile(filepath.Join(graphDefaultDir, graphLatestFile))
	if err != nil {
		return "", fmt.Errorf("no --in given and no latest snapshot recorded: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func readGraphManifest(dir string) (*graphManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, graphSnapshotManifest))
	if err != nil {
		return nil, err
	}
	var m graphManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if m.FormatVersion != graphFormatVersion {
		return nil, fmt.Errorf("snapshot format %d, loader expects %d", m.FormatVersion, graphFormatVersion)
	}
	if m.Snapshot == "" || len(m.Surfaces) == 0 {
		return nil, errors.New("manifest has no snapshot id or surfaces")
	}
	return &m, nil
}

type graphLoadCounts struct {
	nodes, edges                   int
	prunedNodes, prunedEdges       int
	prunedNetworks, prunedSnapshot int
}

func loadGraph(opts graphLoadOptions, out io.Writer) error {
	m, dir, counts, took, err := loadGraphContext(context.Background(), opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "loaded %s (%s) from %s: %d nodes, %d edges; pruned %d stale nodes, %d networks, %d edges in %.1fs\n",
		m.Snapshot, m.Status, dir, counts.nodes, counts.edges, counts.prunedNodes, counts.prunedNetworks, counts.prunedEdges,
		took.Seconds())
	return nil
}

// loadGraphContext writes one snapshot into Memgraph and returns the manifest,
// the directory it was read from, the write/prune counts and the write time.
func loadGraphContext(ctx context.Context, opts graphLoadOptions) (*graphManifest, string, graphLoadCounts, time.Duration, error) {
	var none graphLoadCounts
	if opts.Batch < 1 || opts.Batch > 50000 {
		return nil, "", none, 0, errors.New("--batch must be between 1 and 50000")
	}
	dir := opts.In
	if dir == "" {
		var err error
		if dir, err = latestGraphSnapshot(); err != nil {
			return nil, "", none, 0, err
		}
	}
	m, err := readGraphManifest(dir)
	if err != nil {
		return nil, "", none, 0, err
	}
	if m.Status == graphStatusIncomplete && !opts.AllowIncomplete {
		return nil, "", none, 0, fmt.Errorf("snapshot %s is incomplete; pass --allow-incomplete to load it anyway", m.Snapshot)
	}
	driver, err := opts.driver()
	if err != nil {
		return nil, "", none, 0, err
	}
	defer driver.Close(ctx)
	if _, err := applyGraphSchema(ctx, driver); err != nil {
		return nil, "", none, 0, err
	}
	started := time.Now()
	counts, err := writeGraphSnapshotToMemgraph(ctx, driver, dir, m, opts.Batch)
	if err != nil {
		return nil, "", none, 0, err
	}
	return m, dir, counts, time.Since(started), nil
}

func graphRunWrite(ctx context.Context, session neo4j.SessionWithContext, query string, params map[string]any) (int64, error) {
	v, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, query, params)
		if err != nil {
			return nil, err
		}
		var n int64
		if result.Next(ctx) {
			if c, ok := result.Record().Values[0].(int64); ok {
				n = c
			}
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		_, err = result.Consume(ctx)
		return n, err
	})
	if err != nil {
		return 0, err
	}
	return v.(int64), nil
}

func graphNodeQuery(labels []string) (string, error) {
	if len(labels) == 0 || (labels[0] != "Entity" && labels[0] != "Network" && labels[0] != beltStretchLabel &&
		labels[0] != itemLabel && labels[0] != recipeLabel && labels[0] != technologyLabel) {
		return "", fmt.Errorf("node labels %v must start with Entity, Network, %s, %s, %s or %s", labels, beltStretchLabel, itemLabel, recipeLabel, technologyLabel)
	}
	for _, l := range labels {
		if !graphIdentifier.MatchString(l) {
			return "", fmt.Errorf("invalid label %q", l)
		}
	}
	q := "UNWIND $rows AS r MERGE (n:" + labels[0] + " {id: r.id}) SET n = r.props"
	if labels[0] == "Entity" {
		// A node changes labels when its entity is replaced in place or turns
		// from external into owned; clear the managed ones before setting.
		q += " REMOVE n:" + strings.Join(graphManagedLabels(), ":")
	}
	if len(labels) > 1 {
		q += " SET n:" + strings.Join(labels[1:], ":")
	}
	return q + " RETURN count(*)", nil
}

func graphEdgeQuery(typ, fromLabel, toLabel string) (string, error) {
	if !graphRelationshipTypes[typ] {
		return "", fmt.Errorf("unknown relationship type %q", typ)
	}
	for _, l := range []string{fromLabel, toLabel} {
		if l != "Entity" && l != "Network" && l != beltStretchLabel && l != itemLabel && l != recipeLabel && l != technologyLabel {
			return "", fmt.Errorf("invalid endpoint label %q", l)
		}
	}
	return "UNWIND $rows AS r MATCH (a:" + fromLabel + " {id: r.from}) MATCH (b:" + toLabel + " {id: r.to}) " +
		"MERGE (a)-[e:" + typ + " {key: r.key}]->(b) SET e = r.props SET e.key = r.key RETURN count(*)", nil
}

// graphBatcher groups rows by their Cypher statement and flushes a group when
// it reaches the batch size.
type graphBatcher struct {
	ctx     context.Context
	session neo4j.SessionWithContext
	size    int
	pending map[string][]map[string]any
	order   []string
	written int
	// strict makes a short write an error: every edge endpoint must exist.
	strict bool
}

func (g *graphBatcher) add(query string, row map[string]any) error {
	if _, ok := g.pending[query]; !ok {
		g.order = append(g.order, query)
	}
	g.pending[query] = append(g.pending[query], row)
	if len(g.pending[query]) >= g.size {
		return g.flush(query)
	}
	return nil
}

func (g *graphBatcher) flush(query string) error {
	rows := g.pending[query]
	if len(rows) == 0 {
		return nil
	}
	n, err := graphRunWrite(g.ctx, g.session, query, map[string]any{"rows": rows})
	if err != nil {
		return err
	}
	if g.strict && int(n) != len(rows) {
		return fmt.Errorf("wrote %d of %d rows: an edge endpoint is missing from the snapshot (%s)", n, len(rows), query)
	}
	g.written += int(n)
	g.pending[query] = nil
	return nil
}

func (g *graphBatcher) flushAll() error {
	for _, q := range g.order {
		if err := g.flush(q); err != nil {
			return err
		}
	}
	return nil
}

func eachGraphLine(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return fmt.Errorf("%s:%d: %w", filepath.Base(path), line, err)
		}
	}
	return sc.Err()
}

// graphSnapshotProps is the Snapshot node written by a load: the manifest
// summary plus the load state. The capture tick is omitted for a manifest
// written before the field existed, so a read reports null instead of a false
// tick 0.
func graphSnapshotProps(m *graphManifest, dir string) map[string]any {
	surfaces := make([]any, 0, len(m.Surfaces))
	scopes := make([]any, 0, len(m.Surfaces))
	statuses := make([]any, 0, len(m.Surfaces))
	for _, s := range m.Surfaces {
		surfaces = append(surfaces, s.Surface)
		scopes = append(scopes, s.Scope)
		statuses = append(statuses, s.Surface+":"+s.Status)
	}
	props := map[string]any{"id": m.Snapshot, "force": m.Force, "status": m.Status, "createdAt": m.CreatedAt.Format(time.RFC3339Nano),
		"surfaces": surfaces, "scopes": scopes, "surfaceStatus": statuses, "nodes": m.Nodes, "edges": m.Edges, "loaded": false, "dir": dir}
	if m.Tick > 0 {
		props["tick"] = m.Tick
	}
	return props
}

// graphSnapshotNodesKept bounds the :Snapshot metadata nodes in Memgraph to the
// same count of snapshot directories kept on disk by graph export.
const graphSnapshotNodesKept = 5

func writeGraphSnapshotToMemgraph(ctx context.Context, driver neo4j.DriverWithContext, dir string, m *graphManifest, batch int) (graphLoadCounts, error) {
	var counts graphLoadCounts
	session := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	scopes := make([]any, 0, len(m.Surfaces))
	for _, s := range m.Surfaces {
		scopes = append(scopes, s.Scope)
	}
	snapshotProps := graphSnapshotProps(m, dir)
	if _, err := graphRunWrite(ctx, session, "MERGE (s:Snapshot {id: $id}) SET s = $props RETURN count(*)",
		map[string]any{"id": m.Snapshot, "props": snapshotProps}); err != nil {
		return counts, fmt.Errorf("record snapshot: %w", err)
	}

	nodes := &graphBatcher{ctx: ctx, session: session, size: batch, pending: map[string][]map[string]any{}}
	err := eachGraphLine(filepath.Join(dir, graphSnapshotNodes), func(line []byte) error {
		var n graphNodeOut
		if err := json.Unmarshal(line, &n); err != nil {
			return err
		}
		if n.Props["snapshot"] != m.Snapshot {
			return fmt.Errorf("node %s belongs to snapshot %v, manifest says %s", n.ID, n.Props["snapshot"], m.Snapshot)
		}
		q, err := graphNodeQuery(n.Labels)
		if err != nil {
			return err
		}
		return nodes.add(q, map[string]any{"id": n.ID, "props": graphBoltValue(n.Props)})
	})
	if err == nil {
		err = nodes.flushAll()
	}
	if err != nil {
		return counts, fmt.Errorf("load nodes: %w", err)
	}
	counts.nodes = nodes.written

	edges := &graphBatcher{ctx: ctx, session: session, size: batch, pending: map[string][]map[string]any{}, strict: true}
	err = eachGraphLine(filepath.Join(dir, graphSnapshotEdges), func(line []byte) error {
		var e graphEdgeOut
		if err := json.Unmarshal(line, &e); err != nil {
			return err
		}
		q, err := graphEdgeQuery(e.Type, e.FromLabel, e.ToLabel)
		if err != nil {
			return err
		}
		return edges.add(q, map[string]any{"from": e.From, "to": e.To, "key": e.Key, "props": graphBoltValue(e.Props)})
	})
	if err == nil {
		err = edges.flushAll()
	}
	if err != nil {
		return counts, fmt.Errorf("load edges: %w", err)
	}
	counts.edges = edges.written

	params := map[string]any{"scopes": scopes, "snapshot": m.Snapshot}
	// Edges are pruned by their own snapshot stamp whatever their source label is:
	// stretch-to-stretch FLOWS_INTO and recipe/item edges do not start at an
	// :Entity, and a pruned-node DETACH DELETE never reaches them while both
	// endpoints persist, so a vanished sideload would otherwise live on forever.
	prune := []struct {
		target *int
		query  string
	}{
		{&counts.prunedEdges, "MATCH ()-[r]->() WHERE r.scope IN $scopes AND r.snapshot <> $snapshot WITH collect(r) AS rs " +
			"FOREACH (r IN rs | DELETE r) RETURN size(rs)"},
		{&counts.prunedNodes, "MATCH (n:Entity) WHERE n.scope IN $scopes AND n.snapshot <> $snapshot WITH collect(n) AS ns " +
			"FOREACH (n IN ns | DETACH DELETE n) RETURN size(ns)"},
		{&counts.prunedNetworks, "MATCH (n) WHERE (n:Network OR n:" + beltStretchLabel + " OR n:" + itemLabel + " OR n:" + recipeLabel + " OR n:" + technologyLabel + ") AND n.scope IN $scopes AND n.snapshot <> $snapshot WITH collect(n) AS ns " +
			"FOREACH (n IN ns | DETACH DELETE n) RETURN size(ns)"},
	}
	for _, p := range prune {
		n, err := graphRunWrite(ctx, session, p.query, params)
		if err != nil {
			return counts, fmt.Errorf("prune: %w", err)
		}
		*p.target = int(n)
	}
	at := time.Now().UTC().Format(graphLoadedAtLayout)
	if _, err := graphRunWrite(ctx, session, "MATCH (s:Snapshot) WHERE s.id <> $id AND s.loaded = true SET s.loaded = false RETURN count(*)",
		map[string]any{"id": m.Snapshot}); err != nil {
		return counts, err
	}
	if _, err := graphRunWrite(ctx, session, "MATCH (s:Snapshot {id: $id}) SET s.loaded = true, s.loadedAt = $at RETURN count(*)",
		map[string]any{"id": m.Snapshot, "at": at}); err != nil {
		return counts, err
	}
	// Snapshot ids are timestamps, so ordering by id keeps the newest. The
	// loaded one is never deleted, matching the on-disk retention.
	if _, err := graphRunWrite(ctx, session, "MATCH (s:Snapshot) WITH s ORDER BY s.id DESC SKIP $keep "+
		"WHERE s.loaded IS NULL OR s.loaded = false WITH collect(s) AS old FOREACH (s IN old | DETACH DELETE s) RETURN size(old)",
		map[string]any{"keep": graphSnapshotNodesKept}); err != nil {
		return counts, fmt.Errorf("prune snapshots: %w", err)
	}
	return counts, nil
}

// graphBoltValue normalizes decoded JSON for Bolt: encoding/json decodes every
// number as float64, so identifier and code properties are turned back into
// integers and queries can match them as integers.
func graphBoltValue(props map[string]any) map[string]any {
	out := make(map[string]any, len(props))
	for k, v := range props {
		if f, ok := v.(float64); ok && graphIntegerProps[k] && f == float64(int64(f)) {
			out[k] = int64(f)
			continue
		}
		out[k] = v
	}
	return out
}

// graphIntegerProps are numeric properties that are identifiers or codes, not
// measurements, so queries can match them as integers.
var graphIntegerProps = map[string]bool{
	"unit": true, "direction": true, "electricNetwork": true, "logisticNetwork": true, "network": true,
	"train": true, "ports": true, "tick": true,
	"fuelCount": true, "inputCount": true, "outputCount": true, "itemsCount": true, "leftCount": true, "rightCount": true,
	"ammoCount": true, "robotsCount": true,
	// Production edges count items: 50 stays 50, not 50.0.
	"count": true,
}

func runGraphSync(args []string, out io.Writer, addr, password string) error {
	fs := flag.NewFlagSet("graph sync", flag.ContinueOnError)
	fs.SetOutput(out)
	var exportOpts graphExportOptions
	var surfaces stringList
	var loadOpts graphLoadOptions
	graphExportFlags(fs, &exportOpts, &surfaces)
	graphLoadFlags(fs, &loadOpts)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("graph sync takes no positional arguments")
	}
	if err := validateGraphExportOptions(&exportOpts, surfaces); err != nil {
		return err
	}
	dir, err := exportGraph(exportOpts, out, addr, password)
	// sync is the refresh entry point for a live factory: it always leaves the
	// simulation running. Run the unpause even when the export failed, because
	// the export release restores a previously paused state and a killed
	// earlier export can leave a stale lease behind.
	unpauseErr := unpauseGraphSnapshot(context.Background(), &Resolver{addr: addr, password: password})
	if unpauseErr != nil {
		unpauseErr = fmt.Errorf("unpause after graph export: %w", unpauseErr)
	}
	if err != nil {
		return errors.Join(err, unpauseErr)
	}
	if unpauseErr != nil {
		return unpauseErr
	}
	loadOpts.In = dir
	loadOpts.AllowIncomplete = exportOpts.AllowIncomplete
	return loadGraph(loadOpts, out)
}
