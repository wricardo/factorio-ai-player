package main

import "testing"

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
