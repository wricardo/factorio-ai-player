package main

import (
	"sort"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestEveryGraphQLResponseListIsNormalized(t *testing.T) {
	schema := newGraphQLSchema("", "").Schema()
	missing := map[string][]string{}
	for typeName, definition := range schema.Types {
		if len(typeName) >= 2 && typeName[:2] == "__" {
			continue
		}
		if definition.Kind != ast.Object {
			continue
		}
		for _, field := range definition.Fields {
			if field.Type == nil || field.Type.Elem == nil || isListField(field.Name) {
				continue
			}
			missing[field.Name] = append(missing[field.Name], typeName+"."+field.Name)
		}
	}
	if len(missing) == 0 {
		return
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Errorf("list field %q missing from isListField: %v", name, missing[name])
	}
}

func TestListNormalizationKeepsObjectValuedEmptyTables(t *testing.T) {
	raw := map[string]any{
		"inventories": map[string]any{},
		"items":       map[string]any{},
	}
	normalized := normalizeGraphQL(raw, "", "inspect").(map[string]any)
	if _, ok := normalized["inventories"].(map[string]any); !ok {
		t.Fatalf("inventories changed from object: %#v", normalized["inventories"])
	}
	if items, ok := normalized["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("items did not normalize to empty list: %#v", normalized["items"])
	}
}
