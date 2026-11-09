package main

// Schema-drift guard for the typed config attributes. ffConfigSpecs drives what
// a manifest may write; the GraphQL input types are what actually accepts those
// writes. This test fails when the two disagree, so adding a setting to the
// schema forces a manifest-side decision instead of silently shipping a field no
// manifest can reach (or one whose name no longer matches).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/zclconf/go-cty/cty"
)

// ffConfigBlockInput maps a manifest config block to the GraphQL input type its
// attributes are written into.
var ffConfigBlockInput = map[string]string{
	"inserter":            "InserterSettingsInput",
	"splitter":            "SplitterSettingsInput",
	"container":           "ContainerBarInput",
	"circuit":             "CircuitBehaviorInput",
	"constant_combinator": "SetConstantCombinatorInput",
}

// ffParseSchemaInputs parses every *.graphqls in the package directory and
// indexes input object definitions by name.
func ffParseSchemaInputs(t *testing.T) map[string]*ast.Definition {
	t.Helper()
	paths, err := filepath.Glob("*.graphqls")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no schema files: %v", err)
	}
	sort.Strings(paths)
	out := map[string]*ast.Definition{}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		doc, err := parser.ParseSchema(&ast.Source{Name: p, Input: string(src)})
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		for _, d := range doc.Definitions {
			if d.Kind == ast.InputObject {
				out[d.Name] = d
			}
		}
	}
	return out
}

// ffInputFields returns the field names of an input type, minus the actor/target
// plumbing every mutation input carries and the notification message.
func ffInputFields(def *ast.Definition) map[string]bool {
	out := map[string]bool{}
	for _, f := range def.Fields {
		switch f.Name {
		case "actor", "target", "message", "clear", "clearConditions":
			continue
		}
		out[f.Name] = true
	}
	return out
}

// ffSchemaOnlyFields are GraphQL input fields a manifest intentionally cannot
// set: read-only or rejected settings, and fields supplied by a nested block
// rather than an attribute.
var ffSchemaOnlyFields = map[string]map[string]bool{
	"CircuitBehaviorInput": {"enabled": true}, // read-only in Factorio 2.0, always rejected
	"SetConstantCombinatorInput": {
		"sections":      true, // supplied by the nested section/slot blocks
		"clearSections": true, // omitted blocks are unmanaged, never cleared
	},
}

func TestFFConfigSpecsMatchGraphQLInputs(t *testing.T) {
	inputs := ffParseSchemaInputs(t)
	for _, spec := range ffConfigSpecs {
		inputType, ok := ffConfigBlockInput[spec.Name]
		if !ok {
			// Nested-only blocks (logistic) are covered by their parent.
			if len(spec.Attrs) > 0 {
				t.Errorf("block %q has attributes but no input type in ffConfigBlockInput", spec.Name)
			}
			continue
		}
		def := inputs[inputType]
		if def == nil {
			t.Fatalf("input type %s not found in schema", inputType)
		}
		fields := ffInputFields(def)
		declared := map[string]bool{}
		for _, a := range spec.Attrs {
			declared[a.GraphQL] = true
			if !fields[a.GraphQL] {
				t.Errorf("%s: attribute %q (%s) is not a field of %s; add it to the schema or drop it from ffConfigSpecs",
					spec.Name, a.Name, a.GraphQL, inputType)
			}
		}
		for _, b := range spec.Blocks {
			if b.GraphQL != "" {
				declared[b.GraphQL] = true
			}
		}
		for f := range fields {
			if !declared[f] && !ffSchemaOnlyFields[inputType][f] {
				t.Errorf("%s: %s field %q has no manifest attribute in ffConfigSpecs (manifest cannot set it)",
					spec.Name, inputType, f)
			}
		}
	}
}

func TestFFAttrTypeTableCoversEveryAttr(t *testing.T) {
	// Every attribute that reaches a write must also declare a value type, so a
	// new setting cannot slip through unchecked.
	var walk func(s *ffBlockSpec, path string)
	walk = func(s *ffBlockSpec, path string) {
		for _, a := range s.Attrs {
			if path+a.Name == "container.inventory" {
				continue // qualifier of allowed_slots, not a written field
			}
			if a.Type == cty.NilType {
				t.Errorf("%s%s (%s) has no type in ffAttrTypes", path, a.Name, a.GraphQL)
			}
		}
		for _, b := range s.Blocks {
			walk(b, path+s.Name+".")
		}
	}
	for _, s := range ffConfigSpecs {
		walk(s, "")
	}
}

// ffSnakeCaseRe is used to assert the nested-key rewrite is a real mapping
// rather than an identity.
var ffSnakeCaseRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestFFNestedObjectKeysAreCamelCase(t *testing.T) {
	// The declared object types must name their fields in GraphQL camelCase,
	// because ffConvertAttrObject rewrites manifest keys onto them.
	for graphql, typ := range ffAttrTypes {
		if typ == cty.NilType || !typ.IsObjectType() {
			continue
		}
		for field := range typ.AttributeTypes() {
			if ffSnakeCaseRe.MatchString(field) && strings.Contains(field, "_") {
				t.Errorf("%s: declared field %q is snake_case; object types carry GraphQL camelCase names", graphql, field)
			}
		}
	}
}
