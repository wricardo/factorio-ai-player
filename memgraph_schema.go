package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// memgraphSchemaTimeout bounds the whole schema read, including both Bolt
// statements. The schema is small compared with the graph itself, so a slow
// read means the stack is unhealthy rather than that the graph is huge.
const memgraphSchemaTimeout = 30 * time.Second

const memgraphSchemaQuery = `MATCH (n) UNWIND labels(n) AS label
RETURN 'label' AS kind, label AS name, null AS property, count(*) AS total
UNION ALL
MATCH ()-[r]->()
RETURN 'relationship' AS kind, type(r) AS name, null AS property, count(*) AS total
UNION ALL
MATCH (n) UNWIND labels(n) AS label WITH label, n UNWIND keys(n) AS property
RETURN 'nodeProperty' AS kind, label AS name, property, count(*) AS total
UNION ALL
MATCH ()-[r]->() WITH r, type(r) AS name UNWIND keys(r) AS property
RETURN 'relationshipProperty' AS kind, name, property, count(*) AS total`

const memgraphSnapshotQuery = `MATCH (s:Snapshot {loaded: true})
RETURN s.id AS id, s.createdAt AS createdAt, s.loadedAt AS loadedAt,
       s.status AS status, s.force AS force, s.surfaces AS surfaces,
       s.nodes AS nodes, s.edges AS edges, s.tick AS tick
ORDER BY s.loadedAt DESC, s.id DESC LIMIT 1`

// graphLoadedAtLayout is fixed-width UTC with nanoseconds, so loadedAt strings
// sort in time order. RFC3339Nano trims trailing zeros and would misorder.
const graphLoadedAtLayout = "2006-01-02T15:04:05.000000000Z"

// memgraphSchemaRow is one flattened row of the schema query: a label, a
// relationship type, or one of their property keys.
type memgraphSchemaRow struct {
	kind     string
	name     string
	property string
	count    int
}

// memgraphSchema returns the labels, relationship types, property keys and
// latest loaded snapshot from the Memgraph factory graph. It is the built-in
// counterpart of running the same four Cypher aggregates through queryMemgraph.
func memgraphSchema(ctx context.Context) (*MemgraphSchema, error) {
	ctx, cancel := context.WithTimeout(ctx, memgraphSchemaTimeout)
	defer cancel()

	driver, err := memgraphConnection().driver()
	if err != nil {
		return nil, err
	}
	defer driver.Close(context.Background())
	session := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(context.Background())

	value, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		return readMemgraphSchema(ctx, tx)
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("Memgraph schema query exceeded %s", memgraphSchemaTimeout)
		}
		return nil, memgraphReadError("Memgraph schema query", err)
	}
	return value.(*MemgraphSchema), nil
}

func readMemgraphSchema(ctx context.Context, tx neo4j.ManagedTransaction) (*MemgraphSchema, error) {
	var rows []memgraphSchemaRow
	if err := memgraphSchemaRows(ctx, tx, memgraphSchemaQuery, func(record *neo4j.Record) error {
		kind, err := memgraphString(record, "kind")
		if err != nil {
			return err
		}
		name, err := memgraphString(record, "name")
		if err != nil {
			return err
		}
		property, err := memgraphOptionalString(record, "property")
		if err != nil {
			return err
		}
		total, err := memgraphInt(record, "total")
		if err != nil {
			return err
		}
		rows = append(rows, memgraphSchemaRow{kind: kind, name: name, property: property, count: total})
		return nil
	}); err != nil {
		return nil, err
	}

	snapshot, err := readMemgraphSnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	return buildMemgraphSchema(rows, snapshot), nil
}

// readMemgraphSnapshot returns the latest completed snapshot, or nil before the
// first successful load. AgeSeconds is measured from loadedAt to the read.
func readMemgraphSnapshot(ctx context.Context, tx neo4j.ManagedTransaction) (*MemgraphSnapshotInfo, error) {
	var snapshot *MemgraphSnapshotInfo
	if err := memgraphSchemaRows(ctx, tx, memgraphSnapshotQuery, func(record *neo4j.Record) error {
		snapshot = &MemgraphSnapshotInfo{}
		var err error
		if snapshot.ID, err = memgraphString(record, "id"); err != nil {
			return err
		}
		if snapshot.CreatedAt, err = memgraphOptionalStringPtr(record, "createdAt"); err != nil {
			return err
		}
		if snapshot.LoadedAt, err = memgraphOptionalStringPtr(record, "loadedAt"); err != nil {
			return err
		}
		if snapshot.Status, err = memgraphString(record, "status"); err != nil {
			return err
		}
		if snapshot.Force, err = memgraphString(record, "force"); err != nil {
			return err
		}
		surfaces, err := memgraphStringSlice(record, "surfaces")
		if err != nil {
			return err
		}
		snapshot.Surfaces = surfaces
		if snapshot.Nodes, err = memgraphInt(record, "nodes"); err != nil {
			return err
		}
		if snapshot.Edges, err = memgraphInt(record, "edges"); err != nil {
			return err
		}
		if snapshot.Tick, err = memgraphOptionalFloatPtr(record, "tick"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if snapshot != nil && snapshot.LoadedAt != nil {
		loaded, err := time.Parse(time.RFC3339Nano, *snapshot.LoadedAt)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s loadedAt %q: %w", snapshot.ID, *snapshot.LoadedAt, err)
		}
		age := time.Since(loaded).Seconds()
		snapshot.AgeSeconds = &age
	}
	return snapshot, nil
}

// buildMemgraphSchema folds the flattened rows into the nested GraphQL shape.
// It is separate from Bolt so ordering and grouping can be tested without a
// live Memgraph.
func buildMemgraphSchema(rows []memgraphSchemaRow, snapshot *MemgraphSnapshotInfo) *MemgraphSchema {
	schema := &MemgraphSchema{
		Labels:        []*MemgraphSchemaLabel{},
		Relationships: []*MemgraphSchemaRelationship{},
		Snapshot:      snapshot,
	}
	labels := map[string]*MemgraphSchemaLabel{}
	relationships := map[string]*MemgraphSchemaRelationship{}

	for _, row := range rows {
		switch row.kind {
		case "label":
			label := &MemgraphSchemaLabel{Name: row.name, Nodes: row.count, Properties: []*MemgraphSchemaProperty{}}
			labels[row.name] = label
			schema.Labels = append(schema.Labels, label)
		case "relationship":
			relationship := &MemgraphSchemaRelationship{Name: row.name, Edges: row.count, Properties: []*MemgraphSchemaProperty{}}
			relationships[row.name] = relationship
			schema.Relationships = append(schema.Relationships, relationship)
		}
	}
	for _, row := range rows {
		switch row.kind {
		case "nodeProperty":
			if label := labels[row.name]; label != nil {
				label.Properties = append(label.Properties, &MemgraphSchemaProperty{Name: row.property, Count: row.count})
			}
		case "relationshipProperty":
			if relationship := relationships[row.name]; relationship != nil {
				relationship.Properties = append(relationship.Properties, &MemgraphSchemaProperty{Name: row.property, Count: row.count})
			}
		}
	}

	sort.Slice(schema.Labels, func(i, j int) bool { return schema.Labels[i].Name < schema.Labels[j].Name })
	sort.Slice(schema.Relationships, func(i, j int) bool { return schema.Relationships[i].Name < schema.Relationships[j].Name })
	for _, label := range schema.Labels {
		sortMemgraphProperties(label.Properties)
	}
	for _, relationship := range schema.Relationships {
		sortMemgraphProperties(relationship.Properties)
	}
	return schema
}

func sortMemgraphProperties(properties []*MemgraphSchemaProperty) {
	sort.Slice(properties, func(i, j int) bool { return properties[i].Name < properties[j].Name })
}

func memgraphSchemaRows(ctx context.Context, tx neo4j.ManagedTransaction, cypher string, handle func(*neo4j.Record) error) error {
	result, err := tx.Run(ctx, cypher, nil)
	if err != nil {
		return err
	}
	for result.Next(ctx) {
		if err := handle(result.Record()); err != nil {
			return err
		}
	}
	if err := result.Err(); err != nil {
		return err
	}
	_, err = result.Consume(ctx)
	return err
}

func memgraphString(record *neo4j.Record, key string) (string, error) {
	value, err := memgraphOptionalString(record, key)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("Memgraph schema row is missing %q", key)
	}
	return value, nil
}

func memgraphOptionalString(record *neo4j.Record, key string) (string, error) {
	value, ok := record.Get(key)
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("Memgraph schema field %q is %T, want string", key, value)
	}
	return text, nil
}

func memgraphOptionalStringPtr(record *neo4j.Record, key string) (*string, error) {
	value, ok := record.Get(key)
	if !ok || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("Memgraph schema field %q is %T, want string", key, value)
	}
	return &text, nil
}

func memgraphInt(record *neo4j.Record, key string) (int, error) {
	value, ok := record.Get(key)
	if !ok || value == nil {
		return 0, fmt.Errorf("Memgraph schema row is missing %q", key)
	}
	switch number := value.(type) {
	case int64:
		return int(number), nil
	case int:
		return number, nil
	default:
		return 0, fmt.Errorf("Memgraph schema field %q is %T, want integer", key, value)
	}
}

// memgraphOptionalFloatPtr reads a numeric property that may be stored as a
// float or, for values written by older loaders, an integer.
func memgraphOptionalFloatPtr(record *neo4j.Record, key string) (*float64, error) {
	value, ok := record.Get(key)
	if !ok || value == nil {
		return nil, nil
	}
	switch number := value.(type) {
	case float64:
		return &number, nil
	case int64:
		f := float64(number)
		return &f, nil
	case int:
		f := float64(number)
		return &f, nil
	default:
		return nil, fmt.Errorf("Memgraph schema field %q is %T, want number", key, value)
	}
}

func memgraphStringSlice(record *neo4j.Record, key string) ([]string, error) {
	value, ok := record.Get(key)
	if !ok || value == nil {
		return []string{}, nil
	}
	switch values := value.(type) {
	case []string:
		return values, nil
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("Memgraph schema field %q contains %T, want string", key, item)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("Memgraph schema field %q is %T, want string list", key, value)
	}
}
