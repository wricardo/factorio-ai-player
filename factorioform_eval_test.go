package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

func ffEvalOne(src string, opts ffEvalOptions) (*ffExpanded, hcl.Diagnostics) {
	return EvalFactorioform(map[string][]byte{"m.ftf": []byte(src)}, opts)
}

func ffExpectDiag(t *testing.T, diags hcl.Diagnostics, summary, detail string) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == hcl.DiagError && d.Summary == summary && strings.Contains(d.Detail, detail) {
			return
		}
	}
	for _, d := range diags {
		t.Logf("got %q: %s", d.Summary, d.Detail)
	}
	t.Fatalf("missing diagnostic %q containing %q", summary, detail)
}

// ffDump renders an expansion deterministically for comparison.
func ffDirString(d *int) string {
	if d == nil {
		return "unmanaged"
	}
	return strconv.Itoa(*d)
}

func ffDump(x *ffExpanded) string {
	var b strings.Builder
	fmt.Fprintf(&b, "header %+v\n", x.Header)
	vn := make([]string, 0, len(x.Variables))
	for k := range x.Variables {
		vn = append(vn, k)
	}
	sort.Strings(vn)
	for _, k := range vn {
		fmt.Fprintf(&b, "var %s=%s\n", k, x.Variables[k].GoString())
	}
	for _, r := range x.Resources {
		s := r.Spec
		fmt.Fprintf(&b, "res %s entity=%s pos=%v dir=%s\n", r.Address, s.Entity, s.Position, ffDirString(s.Direction))
		if s.Quality != nil {
			fmt.Fprintf(&b, "  quality=%s\n", *s.Quality)
		}
		if s.Recipe != nil {
			fmt.Fprintf(&b, "  recipe=%s\n", *s.Recipe)
		}
		if s.Belt != nil {
			for _, t := range s.Belt.Tiles {
				fmt.Fprintf(&b, "  tile %v %d\n", t.Position, t.Direction)
			}
		}
		cn := make([]string, 0, len(s.Configs))
		for k := range s.Configs {
			cn = append(cn, k)
		}
		sort.Strings(cn)
		for _, k := range cn {
			var dumpCfg func(c *ffConfigBlock, ind string)
			dumpCfg = func(c *ffConfigBlock, ind string) {
				an := make([]string, 0, len(c.Attrs))
				for a := range c.Attrs {
					an = append(an, a)
				}
				sort.Strings(an)
				fmt.Fprintf(&b, "%s%s\n", ind, c.Type)
				for _, a := range an {
					fmt.Fprintf(&b, "%s  %s=%s\n", ind, a, c.Attrs[a].GoString())
				}
				for _, ch := range c.Blocks {
					dumpCfg(ch, ind+"  ")
				}
			}
			dumpCfg(s.Configs[k], "  ")
		}
	}
	for _, w := range x.Wires {
		fmt.Fprintf(&b, "wire %s %s.%s -> %s.%s\n", w.Color, w.From, w.FromConnector, w.To, w.ToConnector)
	}
	return b.String()
}

const ffEpicExample = `
factorioform {
  version = 1
  name    = "green-circuits"
}

variable "origin" {
  type    = object({ x = number, y = number })
  default = { x = 20.5, y = 0.5 }
}

variable "recipe" {
  type    = string
  default = "electronic-circuit"
  validation {
    condition     = length(var.recipe) > 3
    error_message = "recipe too short"
  }
}

locals {
  circuits = ["c1", "c2", "c3"]
  chests   = { in = 0, out = 2 }
  slots    = ["iron-plate", "copper-plate"]
}

resource "assembler" "green" {
  count     = length(local.circuits)
  entity    = "assembling-machine-2"
  position  = { x = var.origin.x + 4 * count.index, y = var.origin.y }
  direction = direction.north
  quality   = "normal"
  recipe    = var.recipe
  modules   = ["speed-module", "speed-module"]
}

resource "chest" "box" {
  for_each = local.chests
  entity   = "wooden-chest"
  position = offset(var.origin, -10 + each.value, 5)
}

resource "inserter" "feed" {
  entity    = "fast-inserter"
  position  = offset(resource.assembler.green[0].position, -2, 0)
  direction = direction.east
  inserter {
    filter_mode = "whitelist"
    filters     = ["copper-cable"]
  }
}

resource "belt" "coal_bus" {
  belt {
    entity = "transport-belt"
    path   = [for y in range(7, 3, -1) : { x = 0.5, y = y + 0.5, direction = direction.north }]
  }
}

resource "logistic" "req" {
  entity   = "requester-chest"
  position = { x = 1.5, y = 1.5 }
  logistic {
    section {
      dynamic "slot" {
        for_each = local.slots
        content {
          index = slot.key + 1
          name  = slot.value
          min   = 10
        }
      }
    }
  }
}

wire {
  color          = "red"
  from           = resource.inserter.feed
  from_connector = "circuit_red"
  to             = resource.assembler.green[1]
  to_connector   = "combinator_output_red"
}

wire {
  count = 2
  color = "green"
  from  = resource.assembler.green[count.index]
  to    = resource.chest.box["out"]
}

wire {
  for_each = toset(["a"])
  color    = "red"
  from     = resource.belt.coal_bus.path[0]
  to       = resource.belt.coal_bus.path[each.key == "a" ? 3 : 0]
}
`

func TestEvalFactorioformEpicExample(t *testing.T) {
	x, diags := ffEvalOne(ffEpicExample, ffEvalOptions{})
	if diags.HasErrors() {
		t.Fatalf("diags: %v", diags)
	}
	var addrs []string
	for _, r := range x.Resources {
		addrs = append(addrs, r.Address)
	}
	want := []string{
		"assembler.green[0]", "assembler.green[1]", "assembler.green[2]",
		`chest.box["in"]`, `chest.box["out"]`, "inserter.feed", "belt.coal_bus", "logistic.req",
	}
	if strings.Join(addrs, ",") != strings.Join(want, ",") {
		t.Fatalf("addresses = %v", addrs)
	}
	for i, wantX := range []float64{20.5, 24.5, 28.5} {
		if p := x.Resources[i].Spec.Position; p != (ffPoint{wantX, 0.5}) || *x.Resources[i].Spec.Recipe != "electronic-circuit" {
			t.Fatalf("assembler %d = %+v", i, x.Resources[i].Spec)
		}
	}
	if p := x.Resources[3].Spec.Position; p != (ffPoint{10.5, 5.5}) {
		t.Fatalf("chest in = %v", p)
	}
	if p := x.Resources[4].Spec.Position; p != (ffPoint{12.5, 5.5}) {
		t.Fatalf("chest out = %v", p)
	}
	ins := x.Resources[5].Spec
	if ins.Position != (ffPoint{18.5, 0.5}) || ins.Direction == nil || *ins.Direction != 4 {
		t.Fatalf("inserter = %+v", ins)
	}
	tiles := x.Resources[6].Spec.Belt.Tiles
	if len(tiles) != 4 || tiles[0].Position != (ffPoint{0.5, 7.5}) || tiles[3].Position != (ffPoint{0.5, 4.5}) {
		t.Fatalf("tiles = %+v", tiles)
	}
	sec := x.Resources[7].Spec.Configs["logistic"].Blocks
	if len(sec) != 1 || len(sec[0].Blocks) != 2 || sec[0].Blocks[1].Attrs["name"].AsString() != "copper-plate" {
		t.Fatalf("dynamic slots = %+v", sec)
	}
	var wires []string
	for _, w := range x.Wires {
		wires = append(wires, w.Color+":"+w.From+"->"+w.To)
	}
	wantW := []string{
		"red:inserter.feed->assembler.green[1]",
		`green:assembler.green[0]->chest.box["out"]`,
		`green:assembler.green[1]->chest.box["out"]`,
		"red:belt.coal_bus.path[0]->belt.coal_bus.path[3]",
	}
	if strings.Join(wires, "|") != strings.Join(wantW, "|") {
		t.Fatalf("wires = %v", wires)
	}
	if x.Wires[0].FromConnector != "circuit_red" {
		t.Fatalf("wire = %+v", x.Wires[0])
	}
}

func TestEvalFactorioformDeterministic(t *testing.T) {
	first := ""
	for i := range 5 {
		x, diags := ffEvalOne(ffEpicExample, ffEvalOptions{})
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		d := ffDump(x)
		if i == 0 {
			first = d
		} else if d != first {
			t.Fatalf("run %d differs", i)
		}
	}
}

const ffMiniHeader = "factorioform {\n  version = 1\n  name = \"t\"\n}\n"

func ffMiniRes(name, extra string) string {
	return "resource \"chest\" \"" + name + "\" {\n  entity = \"wooden-chest\"\n  position = { x = 0.5, y = 0.5 }\n" + extra + "}\n"
}

func TestEvalFactorioformStrictVariableTypes(t *testing.T) {
	// A typed string is not a number, even though cty can convert it. GraphQL
	// callers and var files must match the declared type exactly.
	num := ffMiniHeader + "variable \"n\" {\n  type = number\n}\n"
	if _, d := ffEvalOne(num, ffEvalOptions{Vars: map[string]cty.Value{"n": cty.StringVal("5")}}); true {
		ffExpectDiag(t, d, "Invalid value for variable", "is not a number")
	}
	if _, d := ffEvalOne(num, ffEvalOptions{Vars: map[string]cty.Value{"n": cty.NumberIntVal(5)}}); d.HasErrors() {
		t.Fatalf("number should be accepted: %v", d)
	}
	// --var stays permissive: CLI arguments arrive as strings.
	if _, d := ffEvalOne(num, ffEvalOptions{RawVars: map[string]string{"n": "5"}}); d.HasErrors() {
		t.Fatalf("--var must still coerce: %v", d)
	}

	// An object with an undeclared attribute is rejected rather than silently
	// dropped by convert.Convert.
	pos := ffMiniHeader + "variable \"p\" {\n  type = object({ x = number, y = number })\n}\n"
	bad := cty.ObjectVal(map[string]cty.Value{"x": cty.NumberIntVal(1), "y": cty.NumberIntVal(2), "zzz": cty.True})
	if _, d := ffEvalOne(pos, ffEvalOptions{Vars: map[string]cty.Value{"p": bad}}); true {
		ffExpectDiag(t, d, "Invalid value for variable", "has no attribute zzz")
	}
	// A nested attribute of the wrong type is reported with its path.
	badType := cty.ObjectVal(map[string]cty.Value{"x": cty.StringVal("1"), "y": cty.NumberIntVal(2)})
	if _, d := ffEvalOne(pos, ffEvalOptions{Vars: map[string]cty.Value{"p": badType}}); true {
		ffExpectDiag(t, d, "Invalid value for variable", `attribute "x"`)
	}

	// HCL-authored module arguments keep HCL conversion for scalars ("3" is a
	// number) but still may not invent object attributes.
	mod := map[string][]byte{
		"main.ftf":   []byte(ffMiniHeader + "module \"m\" {\n  source = \"./m\"\n  n = \"3\"\n  p = { x = 1, y = 2, zzz = 3 }\n}\n"),
		"m/main.ftf": []byte("variable \"n\" {\n  type = number\n}\nvariable \"p\" {\n  type = object({ x = number, y = number })\n}\n"),
	}
	if _, d := EvalFactorioform(mod, ffEvalOptions{RootDir: "."}); true {
		ffExpectDiag(t, d, "Invalid value for variable", "has no attribute zzz")
	}
}

func TestEvalFactorioformVariables(t *testing.T) {
	decl := ffMiniHeader + `variable "n" {
  type = number
  validation {
    condition     = var.n < 5
    error_message = "n must be below 5"
  }
}
`
	if _, d := ffEvalOne(decl, ffEvalOptions{}); true {
		ffExpectDiag(t, d, "Missing required variable", `"n"`)
	}
	if _, d := ffEvalOne(decl, ffEvalOptions{Vars: map[string]cty.Value{"n": cty.StringVal("abc")}}); true {
		ffExpectDiag(t, d, "Invalid value for variable", `"n"`)
	}
	if _, d := ffEvalOne(decl, ffEvalOptions{Vars: map[string]cty.Value{"n": cty.NumberIntVal(9)}}); true {
		ffExpectDiag(t, d, "Invalid value for variable", "n must be below 5")
	}
	if _, d := ffEvalOne(decl, ffEvalOptions{RawVars: map[string]string{"n": "3"}}); d.HasErrors() {
		t.Fatalf("raw var: %v", d)
	}
	if _, d := ffEvalOne(decl, ffEvalOptions{RawVars: map[string]string{"n": "x"}}); !d.HasErrors() {
		t.Fatal("expected raw literal error")
	}
	if _, d := ffEvalOne(decl, ffEvalOptions{Vars: map[string]cty.Value{"n": cty.NumberIntVal(1), "zzz": cty.True}}); true {
		ffExpectDiag(t, d, "Undeclared variable", "zzz")
	}
	vars, err := ffVarsFromJSON(map[string]any{"n": 2})
	if err != nil {
		t.Fatal(err)
	}
	if x, d := ffEvalOne(decl, ffEvalOptions{Vars: vars}); d.HasErrors() || x.Variables["n"].AsBigFloat().String() != "2" {
		t.Fatalf("json var: %v", d)
	}
	fv, d := ffParseVarFile("x.ftfvars.hcl", []byte("n = 4\n"))
	if d.HasErrors() || len(fv) != 1 {
		t.Fatalf("var file: %v", d)
	}
}

func TestEvalFactorioformMetaErrors(t *testing.T) {
	_, d := ffEvalOne(ffMiniHeader+ffMiniRes("c", "  count = 1\n  for_each = toset([\"a\"])\n"), ffEvalOptions{})
	ffExpectDiag(t, d, "Conflicting arguments", "count and for_each")
	_, d = ffEvalOne(ffMiniHeader+ffMiniRes("c", "  count = -1\n"), ffEvalOptions{})
	ffExpectDiag(t, d, "Invalid count", ">= 0")
	_, d = ffEvalOne(ffMiniHeader+ffMiniRes("c", "  for_each = [\"a\"]\n"), ffEvalOptions{})
	ffExpectDiag(t, d, "Invalid for_each", "toset")
	x, d := ffEvalOne(ffMiniHeader+ffMiniRes("c", "  count = 0\n"), ffEvalOptions{})
	if d.HasErrors() || len(x.Resources) != 0 {
		t.Fatalf("count 0: %v", d)
	}
}

func TestEvalFactorioformCycles(t *testing.T) {
	_, d := ffEvalOne(ffMiniHeader+"locals {\n  a = local.b\n  b = local.a\n}\n", ffEvalOptions{})
	ffExpectDiag(t, d, "Reference cycle", "local.a")
	for _, dd := range d {
		if dd.Summary == "Reference cycle" && (dd.Subject == nil || dd.Context == nil) {
			t.Fatal("cycle diagnostic must carry both ranges")
		}
	}
	src := ffMiniHeader +
		"resource \"chest\" \"a\" {\n  entity = \"x\"\n  position = resource.chest.b.position\n}\n" +
		"resource \"chest\" \"b\" {\n  entity = \"x\"\n  position = resource.chest.a.position\n}\n"
	_, d = ffEvalOne(src, ffEvalOptions{})
	ffExpectDiag(t, d, "Reference cycle", "resource.chest.a")
	// Mixed local/resource cycle.
	src = ffMiniHeader + "locals {\n  p = resource.chest.a.position\n}\n" +
		"resource \"chest\" \"a\" {\n  entity = \"x\"\n  position = local.p\n}\n"
	_, d = ffEvalOne(src, ffEvalOptions{})
	ffExpectDiag(t, d, "Reference cycle", "local.p")
}

func TestEvalFactorioformReferenceErrors(t *testing.T) {
	_, d := ffEvalOne(ffMiniHeader+ffMiniRes("a", "")+"wire {\n  color = \"red\"\n  from = resource.chest.a\n  to = resource.chest.nope\n}\n", ffEvalOptions{})
	ffExpectDiag(t, d, "Reference to undeclared resource", "nope")
	_, d = ffEvalOne(ffMiniHeader+"locals {\n  a = var.missing\n}\n", ffEvalOptions{})
	ffExpectDiag(t, d, "Reference to undeclared variable", "missing")
	_, d = ffEvalOne(ffMiniHeader+ffMiniRes("a", "")+"wire {\n  color = \"red\"\n  from = resource.chest.a\n  to = resource.chest.a[3]\n}\n", ffEvalOptions{})
	ffExpectDiag(t, d, "Invalid index", "numeric index")
	// Only static attributes are visible.
	_, d = ffEvalOne(ffMiniHeader+ffMiniRes("a", "")+"locals {\n  u = resource.chest.a.unit_number\n}\n", ffEvalOptions{})
	if !d.HasErrors() {
		t.Fatal("unit_number must not be referenceable")
	}
}

func TestEvalFactorioformFunctions(t *testing.T) {
	_, d := ffEvalOne(ffMiniHeader+"locals {\n  a = nosuchfn(1)\n}\n", ffEvalOptions{})
	ffExpectDiag(t, d, "Call to unknown function", "nosuchfn")
	for _, fn := range []string{"file", "fileexists", "timestamp", "uuid", "env", "getenv", "bcrypt", "plantimestamp"} {
		if _, ok := ffFunctions()[fn]; ok {
			t.Fatalf("non-deterministic/impure function %q must not exist", fn)
		}
		_, d := ffEvalOne(ffMiniHeader+"locals {\n  a = "+fn+"(\"x\")\n}\n", ffEvalOptions{})
		ffExpectDiag(t, d, "Call to unknown function", fn)
	}
	src := ffMiniHeader + `locals {
  g   = grid({ x = 0.5, y = 1.5 }, 2, 2, 3, 2)
  n   = length(local.g)
  all = [min(3, 1), max(3, 1), floor(1.5), ceil(1.2), abs(-2), length(tolist(toset(["a", "a"]))), join("-", ["a", "b"]), format("%02d", 5)]
}
resource "chest" "c" {
  count    = local.n
  entity   = "wooden-chest"
  position = local.g[count.index]
}
`
	x, d := ffEvalOne(src, ffEvalOptions{})
	if d.HasErrors() {
		t.Fatal(d)
	}
	want := []ffPoint{{0.5, 1.5}, {3.5, 1.5}, {0.5, 3.5}, {3.5, 3.5}}
	if len(x.Resources) != 4 {
		t.Fatalf("resources = %d", len(x.Resources))
	}
	for i, w := range want {
		if x.Resources[i].Spec.Position != w {
			t.Fatalf("grid[%d] = %v", i, x.Resources[i].Spec.Position)
		}
	}
	if got := x.Locals["all"].LengthInt(); got != 8 {
		t.Fatalf("all = %v", x.Locals["all"])
	}
}

func TestEvalFactorioformDirections(t *testing.T) {
	src := ffMiniHeader + ffMiniRes("c", "  direction = direction.northwest\n")
	x, d := ffEvalOne(src, ffEvalOptions{})
	if d.HasErrors() || x.Resources[0].Spec.Direction == nil || *x.Resources[0].Spec.Direction != 14 {
		t.Fatalf("direction: %v", d)
	}
}
