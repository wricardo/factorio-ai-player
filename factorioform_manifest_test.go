package main

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
)

const ffTestHeader = `factorioform {
  version = 1
  name    = "green-circuits"
}
`

func ffParseOne(src string, ctx *hcl.EvalContext) (*ffManifest, hcl.Diagnostics) {
	return ParseFactorioform(map[string][]byte{"m.ftf": []byte(src)}, ctx)
}

func TestParseFactorioformLiteralExample(t *testing.T) {
	src := ffTestHeader + `
resource "assembler" "green" {
  entity    = "assembling-machine-2"
  position  = { x = 20.5, y = 0.5 }
  direction = direction.north
  quality   = "normal"
  recipe    = "electronic-circuit"
  modules   = ["speed-module", "speed-module"]
}

resource "inserter" "feed" {
  entity    = "fast-inserter"
  position  = { x = 18.5, y = 0.5 }
  direction = direction.east
  inserter {
    filter_mode         = "whitelist"
    filters             = ["copper-cable"]
    stack_size_override = 4
  }
  circuit {
    set_filters    = true
    hand_read_mode = 1
  }
}

resource "splitter" "sort" {
  entity   = "splitter"
  position = { x = 10, y = 0.5 }
  splitter {
    input_priority  = "left"
    output_priority = "right"
    filter          = "iron-ore"
  }
}

resource "belt" "coal_bus" {
  belt {
    entity = "transport-belt"
    path = [
      { x = 0.5, y = 6.5, direction = direction.north },
      { x = 0.5, y = 5.5, direction = direction.north },
    ]
  }
}

resource "logistic" "req" {
  entity   = "requester-chest"
  position = { x = 1.5, y = 1.5 }
  logistic {
    section {
      slot {
        index = 1
        name  = "iron-plate"
        min   = 10
      }
    }
  }
}

wire {
  color          = "red"
  from           = resource.inserter.feed
  from_connector = "circuit_red"
  to             = resource.assembler.green
  to_connector   = "combinator_output_red"
}
`
	m, diags := ffParseOne(src, nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if m.Header.Name != "green-circuits" || m.Header.Version != 1 || m.Header.Surface != "nauvis" || m.Header.Force != "player" {
		t.Fatalf("header = %+v", m.Header)
	}
	if len(m.Resources) != 5 || len(m.Wires) != 1 {
		t.Fatalf("resources=%d wires=%d", len(m.Resources), len(m.Wires))
	}
	a := m.Resources[0].Spec
	if a.Entity != "assembling-machine-2" || a.Position != (ffPoint{20.5, 0.5}) || a.Direction != 0 ||
		*a.Recipe != "electronic-circuit" || len(a.Modules) != 2 {
		t.Fatalf("assembler = %+v", a)
	}
	ins := m.Resources[1].Spec
	if ins.Direction != 4 || ins.Configs["inserter"].Attrs["filter_mode"].AsString() != "whitelist" || ins.Configs["circuit"] == nil {
		t.Fatalf("inserter = %+v", ins)
	}
	if got := len(m.Resources[3].Spec.Belt.Tiles); got != 2 {
		t.Fatalf("belt tiles = %d", got)
	}
	if lg := m.Resources[4].Spec.Configs["logistic"]; len(lg.Blocks) != 1 || len(lg.Blocks[0].Blocks) != 1 {
		t.Fatalf("logistic = %+v", lg)
	}
	if w := m.Wires[0]; w.From != "inserter.feed" || w.To != "assembler.green" || w.Color != "red" {
		t.Fatalf("wire = %+v", w)
	}
	if f, ok := ffGraphQLField("inserter.stack_size_override"); !ok || f != "stackSizeOverride" {
		t.Fatalf("graphql field = %q %v", f, ok)
	}
	if f, ok := ffGraphQLField("logistic.section.slot"); !ok || f != "slots" {
		t.Fatalf("graphql field = %q %v", f, ok)
	}
}

func TestParseFactorioformErrors(t *testing.T) {
	res := func(body string) string {
		return ffTestHeader + "resource \"chest\" \"c\" {\n  entity = \"wooden-chest\"\n  position = { x = 0.5, y = 0.5 }\n" + body + "}\n"
	}
	tests := []struct {
		name    string
		src     string
		summary string
		line    int
	}{
		{"unknown attribute", res("  bogus = 1\n"), "Unsupported argument", 8},
		{"unknown block", res("  bogus {\n  }\n"), "Unsupported block type", 8},
		{"duplicate resource", ffTestHeader + "resource \"chest\" \"c\" {\n  entity = \"a\"\n  position = { x = 0.5, y = 0.5 }\n}\nresource \"chest\" \"c\" {\n  entity = \"a\"\n  position = { x = 0.5, y = 0.5 }\n}\n", "Duplicate resource", 9},
		{"bad direction", res("  direction = 16\n"), "Invalid direction", 8},
		{"non-contiguous belt", ffTestHeader + "resource \"belt\" \"b\" {\n  belt {\n    entity = \"transport-belt\"\n    path = [\n      { x = 0.5, y = 0.5, direction = 0 },\n      { x = 0.5, y = 2.5, direction = 0 },\n    ]\n  }\n}\n", "Non-contiguous belt path", 8},
		{"two config blocks", res("  container {\n  }\n  container {\n  }\n"), "Duplicate configuration block", 10},
		{"unknown inserter attribute", ffTestHeader + "resource \"inserter\" \"i\" {\n  entity = \"inserter\"\n  position = { x = 0.5, y = 0.5 }\n  inserter {\n    pickup_vector = { x = 0, y = -1 }\n  }\n}\n", "Unsupported argument", 9},
		{"variable reference", res("  recipe = var.recipe\n"), ffDynamicUnavailable, 8},
		{"entity and belt", res("  belt {\n    entity = \"x\"\n    path = []\n  }\n"), "Conflicting arguments", 8},
		{"missing header", "resource \"chest\" \"c\" {\n  entity = \"a\"\n  position = { x = 0.5, y = 0.5 }\n}\n", "Missing factorioform block", 1},
		{"bad version", strings.Replace(ffTestHeader, "version = 1", "version = 2", 1), "Unsupported version", 2},
		{"bad name", strings.Replace(ffTestHeader, "green-circuits", "Bad Name", 1), "Invalid manifest name", 3},
		{"bad kind", ffTestHeader + "resource \"Chest\" \"c\" {\n}\n", "Invalid resource kind", 5},
		{"bad wire color", ffTestHeader + "resource \"chest\" \"c\" {\n  entity = \"a\"\n  position = { x = 0.5, y = 0.5 }\n}\nwire {\n  color = \"blue\"\n  from = resource.chest.c\n  to = resource.chest.c\n}\n", "Invalid wire color", 10},
		{"duplicate header", ffTestHeader + ffTestHeader, "Duplicate factorioform block", 5},
		{"belt off center", ffTestHeader + "resource \"belt\" \"b\" {\n  belt {\n    entity = \"transport-belt\"\n    path = [{ x = 1, y = 0.5, direction = 0 }]\n  }\n}\n", "Invalid belt tile", 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := ffParseOne(tc.src, nil)
			for _, d := range diags {
				if d.Severity == hcl.DiagError && d.Summary == tc.summary && d.Subject != nil && d.Subject.Start.Line == tc.line {
					return
				}
			}
			for _, d := range diags {
				t.Logf("got %q at %v", d.Summary, d.Subject)
			}
			t.Fatalf("missing diagnostic %q at line %d", tc.summary, tc.line)
		})
	}
}

func TestParseFactorioformMergesFiles(t *testing.T) {
	m, diags := ParseFactorioform(map[string][]byte{
		"a.ftf": []byte(ffTestHeader),
		"b.ftf": []byte("resource \"chest\" \"c\" {\n  entity = \"wooden-chest\"\n  position = { x = 0.5, y = 0.5 }\n}\n"),
	}, nil)
	if diags.HasErrors() || len(m.Resources) != 1 {
		t.Fatalf("diags=%v m=%+v", diags, m)
	}
}

func TestParseFactorioformCountNeedsContext(t *testing.T) {
	src := ffTestHeader + "resource \"chest\" \"c\" {\n  count = 2\n  entity = \"a\"\n  position = { x = 0.5, y = 0.5 }\n}\n"
	if _, diags := ffParseOne(src, nil); !diags.HasErrors() || diags[0].Summary != ffDynamicUnavailable {
		t.Fatalf("diags = %v", diags)
	}
	m, diags := ffParseOne(src, ffStaticContext())
	if diags.HasErrors() || m.Resources[0].Spec != nil || m.Resources[0].Count == nil {
		t.Fatalf("diags=%v", diags)
	}
}
