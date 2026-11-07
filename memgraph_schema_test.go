package main

import (
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func TestBuildMemgraphSchemaGroupsAndSorts(t *testing.T) {
	snapshot := &MemgraphSnapshotInfo{ID: "snap-1", Status: "complete", Force: "player", Surfaces: []string{"nauvis"}, Nodes: 313, Edges: 358}
	got := buildMemgraphSchema([]memgraphSchemaRow{
		{kind: "nodeProperty", name: "Belt", property: "status", count: 230},
		{kind: "label", name: "Inserter", count: 14},
		{kind: "label", name: "Belt", count: 230},
		{kind: "relationshipProperty", name: "BELT_OUT", property: "kind", count: 225},
		{kind: "relationship", name: "BELT_OUT", count: 225},
		{kind: "nodeProperty", name: "Belt", property: "id", count: 230},
		{kind: "relationshipProperty", name: "BELT_OUT", property: "key", count: 225},
	}, snapshot)

	if got.Snapshot != snapshot {
		t.Fatalf("snapshot not preserved")
	}
	if len(got.Labels) != 2 || got.Labels[0].Name != "Belt" || got.Labels[1].Name != "Inserter" {
		t.Fatalf("labels not grouped and sorted by name: %+v", got.Labels)
	}
	if got.Labels[0].Nodes != 230 || got.Labels[1].Nodes != 14 {
		t.Fatalf("label counts wrong: %+v", got.Labels)
	}
	if len(got.Labels[0].Properties) != 2 || got.Labels[0].Properties[0].Name != "id" || got.Labels[0].Properties[1].Name != "status" {
		t.Fatalf("label properties not grouped and sorted: %+v", got.Labels[0].Properties)
	}
	if len(got.Labels[1].Properties) != 0 {
		t.Fatalf("label without properties got %+v", got.Labels[1].Properties)
	}
	if len(got.Relationships) != 1 || got.Relationships[0].Name != "BELT_OUT" || got.Relationships[0].Edges != 225 {
		t.Fatalf("relationship not grouped: %+v", got.Relationships)
	}
	if len(got.Relationships[0].Properties) != 2 || got.Relationships[0].Properties[0].Name != "key" || got.Relationships[0].Properties[1].Name != "kind" {
		t.Fatalf("relationship properties not grouped and sorted: %+v", got.Relationships[0].Properties)
	}
}

func TestBuildMemgraphSchemaEmpty(t *testing.T) {
	got := buildMemgraphSchema(nil, nil)
	if got.Labels == nil || got.Relationships == nil {
		t.Fatalf("empty schema must return empty lists, got labels=%v relationships=%v", got.Labels, got.Relationships)
	}
	if len(got.Labels) != 0 || len(got.Relationships) != 0 || got.Snapshot != nil {
		t.Fatalf("empty schema not empty: %+v", got)
	}
}

func TestGraphLoadedAtSortsInTimeOrder(t *testing.T) {
	// RFC3339Nano trims trailing zeros, so "…:52Z" would sort after "…:52.5Z".
	early := time.Date(2026, 11, 6, 3, 47, 52, 500000000, time.UTC).Format(graphLoadedAtLayout)
	late := time.Date(2026, 11, 6, 3, 47, 53, 0, time.UTC).Format(graphLoadedAtLayout)
	if !(early < late) {
		t.Fatalf("loadedAt strings not in time order: %q !< %q", early, late)
	}
	if _, err := time.Parse(time.RFC3339Nano, late); err != nil {
		t.Fatalf("fixed-width loadedAt does not parse as RFC3339Nano: %v", err)
	}
}

// Snapshot ticks are stored by the loader as numbers, but an older row may hold
// an integer, so the read accepts both and reports null when absent.
func TestMemgraphOptionalFloatPtr(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  *float64
	}{
		{"absent", nil, nil},
		{"float", float64(1147897), ptrFloat(1147897)},
		{"int64", int64(42), ptrFloat(42)},
	}
	for _, c := range cases {
		record := &neo4j.Record{Keys: []string{"tick"}, Values: []any{c.value}}
		got, err := memgraphOptionalFloatPtr(record, "tick")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	record := &neo4j.Record{Keys: []string{"tick"}, Values: []any{"1147897"}}
	if _, err := memgraphOptionalFloatPtr(record, "tick"); err == nil {
		t.Fatal("accepted a string tick")
	}
}

func ptrFloat(v float64) *float64 { return &v }
