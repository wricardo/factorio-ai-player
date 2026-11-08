package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// ffModHeader is the root module's factorioform block.
const ffModHeader = "factorioform {\n  version = 1\n  name    = \"t\"\n}\n"

// ffModChest is a module: a row of chests plus outputs.
const ffModChest = `
variable "origin" {
  type = object({ x = number, y = number })
}
variable "n" {
  type    = number
  default = 2
}
resource "chest" "c" {
  count    = var.n
  entity   = "wooden-chest"
  position = offset(var.origin, count.index, 0)
}
output "first" {
  value = resource.chest.c[0]
}
output "last" {
  value = resource.chest.c[var.n - 1]
}
output "n" {
  value = var.n
}
`

func ffEvalTree(files map[string]string) (*ffExpanded, hcl.Diagnostics) {
	b := map[string][]byte{}
	for k, v := range files {
		b[k] = []byte(v)
	}
	return EvalFactorioform(b, ffEvalOptions{})
}

func ffAddrs(x *ffExpanded) string {
	var a []string
	for _, r := range x.Resources {
		a = append(a, r.Address)
	}
	return strings.Join(a, ",")
}

func ffWireList(x *ffExpanded) string {
	var a []string
	for _, w := range x.Wires {
		a = append(a, w.Color+":"+w.From+"->"+w.To)
	}
	return strings.Join(a, "|")
}

func ffMustEval(t *testing.T, files map[string]string) *ffExpanded {
	t.Helper()
	x, d := ffEvalTree(files)
	if d.HasErrors() {
		for _, dd := range d {
			t.Logf("diag %s %q: %s", dd.Subject, dd.Summary, dd.Detail)
		}
		t.Fatal("unexpected diagnostics")
	}
	return x
}

func TestModuleCallPrefixesAddressesAndPassesOutputs(t *testing.T) {
	x := ffMustEval(t, map[string]string{
		"main.ftf": ffModHeader + `
resource "chest" "before" {
  entity   = "wooden-chest"
  position = { x = 0.5, y = 0.5 }
}
module "iron" {
  source = "./m"
  origin = { x = 10.5, y = 0.5 }
}
resource "chest" "after" {
  entity   = "wooden-chest"
  position = offset(module.iron.last.position, 0, 5)
}
`,
		"m/main.ftf": ffModChest,
	})
	// Module instances sit where their module block is declared.
	want := "chest.before,module.iron.chest.c[0],module.iron.chest.c[1],chest.after"
	if got := ffAddrs(x); got != want {
		t.Fatalf("addresses\n got %s\nwant %s", got, want)
	}
	if p := x.Resources[2].Spec.Position; p != (ffPoint{11.5, 0.5}) {
		t.Fatalf("module chest = %v", p)
	}
	if p := x.Resources[3].Spec.Position; p != (ffPoint{11.5, 5.5}) {
		t.Fatalf("output did not reach the caller: %v", p)
	}
}

func TestModuleInputs(t *testing.T) {
	call := func(body string) map[string]string {
		return map[string]string{"main.ftf": ffModHeader + "module \"m\" {\n  source = \"./m\"\n" + body + "}\n", "m/main.ftf": ffModChest}
	}
	// Defaults apply and typed conversion happens (string "3" -> number).
	x := ffMustEval(t, call("  origin = { x = 0.5, y = 0.5 }\n  n = \"3\"\n"))
	if len(x.Resources) != 3 {
		t.Fatalf("n=\"3\" gave %d resources", len(x.Resources))
	}
	x = ffMustEval(t, call("  origin = { x = 0.5, y = 0.5 }\n"))
	if len(x.Resources) != 2 {
		t.Fatalf("default n gave %d resources", len(x.Resources))
	}

	_, d := ffEvalTree(call(""))
	ffExpectDiag(t, d, "Missing required variable", `module "m" requires variable "origin"`)
	for _, dd := range d {
		if dd.Summary == "Missing required variable" && (dd.Subject == nil || dd.Subject.Filename != "main.ftf") {
			t.Fatalf("missing input must point at the module call: %v", dd.Subject)
		}
	}

	_, d = ffEvalTree(call("  origin = { x = 0.5, y = 0.5 }\n  nope = 1\n"))
	ffExpectDiag(t, d, "Unsupported argument", `declares no variable "nope"`)

	_, d = ffEvalTree(call("  origin = { x = 0.5, y = 0.5 }\n  n = \"many\"\n"))
	ffExpectDiag(t, d, "Invalid value for variable", `variable "n"`)

	// A child variable validation fails with the module instance named.
	files := call("  origin = { x = 0.5, y = 0.5 }\n  n = 9\n")
	files["m/main.ftf"] = strings.Replace(ffModChest, `variable "n" {`, "variable \"n\" {\n  validation {\n    condition     = var.n < 5\n    error_message = \"too many\"\n  }", 1)
	_, d = ffEvalTree(files)
	ffExpectDiag(t, d, "Invalid value for variable", "in module.m: variable \"n\": too many")
	for _, dd := range d {
		if dd.Subject == nil || dd.Subject.Filename != "m/main.ftf" {
			t.Fatalf("validation error must point into the module file: %v", dd.Subject)
		}
	}
}

func TestModuleCountAndForEach(t *testing.T) {
	x := ffMustEval(t, map[string]string{
		"main.ftf": ffModHeader + `
module "row" {
  count  = 2
  source = "./m"
  origin = { x = 0.5, y = 10 * count.index + 0.5 }
  n      = 1
}
module "col" {
  for_each = { a = 1, b = 2 }
  source   = "./m"
  origin   = { x = 20.5, y = 0.5 }
  n        = each.value
}
locals {
  firsts = [for r in module.row : r.first.position.y]
  lasts  = module.col["b"].last.address
}
`,
		"m/main.ftf": ffModChest,
	})
	want := `module.row[0].chest.c[0],module.row[1].chest.c[0],module.col["a"].chest.c[0],module.col["b"].chest.c[0],module.col["b"].chest.c[1]`
	if got := ffAddrs(x); got != want {
		t.Fatalf("addresses\n got %s\nwant %s", got, want)
	}
	if got := x.Locals["firsts"].GoString(); !strings.Contains(got, "0.5") || !strings.Contains(got, "10.5") {
		t.Fatalf("count module outputs = %s", got)
	}
	if got := x.Locals["lasts"].AsString(); got != `module.col["b"].chest.c[1]` {
		t.Fatalf("output address = %s", got)
	}
}

func TestModulesNest(t *testing.T) {
	x := ffMustEval(t, map[string]string{
		"main.ftf":     ffModHeader + "module \"a\" {\n  source = \"./a\"\n}\nlocals {\n  y = module.a.pos.y\n}\n",
		"a/main.ftf":   "module \"b\" {\n  source = \"./b\"\n  origin = { x = 1.5, y = 2.5 }\n}\noutput \"pos\" {\n  value = module.b.last.position\n}\n",
		"a/b/main.ftf": ffModChest,
	})
	if got := ffAddrs(x); got != "module.a.module.b.chest.c[0],module.a.module.b.chest.c[1]" {
		t.Fatalf("addresses = %s", got)
	}
	if got := x.Locals["y"]; !got.RawEquals(cty.NumberFloatVal(2.5)) {
		t.Fatalf("output through two levels = %#v", got)
	}
}

func TestModuleIsolation(t *testing.T) {
	root := ffModHeader + "variable \"outer\" {\n  default = 1\n}\nresource \"chest\" \"top\" {\n  entity = \"wooden-chest\"\n  position = { x = 0.5, y = 0.5 }\n}\nmodule \"m\" {\n  source = \"./m\"\n}\n"
	_, d := ffEvalTree(map[string]string{"main.ftf": root, "m/main.ftf": "locals {\n  a = var.outer\n}\n"})
	ffExpectDiag(t, d, "Reference to undeclared variable", "outer")
	_, d = ffEvalTree(map[string]string{"main.ftf": root, "m/main.ftf": "locals {\n  a = resource.chest.top.position\n}\n"})
	ffExpectDiag(t, d, "Reference to undeclared resource", "top")
	// The same names in two instances do not collide.
	x := ffMustEval(t, map[string]string{
		"main.ftf":   ffModHeader + "module \"p\" {\n  source = \"./m\"\n  origin = { x = 0.5, y = 0.5 }\n}\nmodule \"q\" {\n  source = \"./m\"\n  origin = { x = 0.5, y = 5.5 }\n}\n",
		"m/main.ftf": ffModChest,
	})
	if got := ffAddrs(x); got != "module.p.chest.c[0],module.p.chest.c[1],module.q.chest.c[0],module.q.chest.c[1]" {
		t.Fatalf("addresses = %s", got)
	}
}

func TestModuleStructureErrors(t *testing.T) {
	m := func(src string) map[string]string {
		return map[string]string{"main.ftf": ffModHeader + "module \"m\" {\n  source = \"" + src + "\"\n  origin = { x = 0.5, y = 0.5 }\n}\n", "m/main.ftf": ffModChest}
	}
	_, d := ffEvalTree(m("./nowhere"))
	ffExpectDiag(t, d, "Module source not found", `"nowhere"`)
	for _, src := range []string{"hashicorp/foo/aws", "git::https://example.com/x.git", "/abs/path", "m"} {
		_, d = ffEvalTree(m(src))
		ffExpectDiag(t, d, "Unsupported module source", src)
	}
	// A file nothing refers to is an error, not silently ignored.
	files := m("./m")
	files["stray/x.ftf"] = "locals {\n  a = 1\n}\n"
	_, d = ffEvalTree(files)
	ffExpectDiag(t, d, "Unreferenced manifest file", "stray/x.ftf")
	// The factorioform block belongs to the root only.
	files = m("./m")
	files["m/main.ftf"] += "\n" + ffModHeader
	_, d = ffEvalTree(files)
	ffExpectDiag(t, d, "Unexpected factorioform block", "only the root module")
	// Meta-arguments conflict like on resources.
	files = m("./m")
	files["main.ftf"] = strings.Replace(files["main.ftf"], "source =", "count = 1\n  for_each = toset([\"a\"])\n  source =", 1)
	_, d = ffEvalTree(files)
	ffExpectDiag(t, d, "Conflicting arguments", "count and for_each")
	// Module names are identifiers and unique.
	files = m("./m")
	files["main.ftf"] += "module \"m\" {\n  source = \"./m\"\n  origin = { x = 0.5, y = 0.5 }\n}\n"
	_, d = ffEvalTree(files)
	ffExpectDiag(t, d, "Duplicate module", `"m"`)
	_, d = ffEvalTree(map[string]string{"main.ftf": ffModHeader + "locals {\n  a = module.ghost.x\n}\n"})
	ffExpectDiag(t, d, "Reference to undeclared module", "ghost")
	// Module references take part in cycle detection.
	_, d = ffEvalTree(map[string]string{
		"main.ftf":   ffModHeader + "module \"a\" {\n  source = \"./m\"\n  origin = module.b.first.position\n}\nmodule \"b\" {\n  source = \"./m\"\n  origin = module.a.first.position\n}\n",
		"m/main.ftf": ffModChest,
	})
	ffExpectDiag(t, d, "Reference cycle", "module.a")
}

func TestModuleCycleAndDepth(t *testing.T) {
	_, d := ffEvalTree(map[string]string{
		"main.ftf":   ffModHeader + "module \"a\" {\n  source = \"./a\"\n}\n",
		"a/main.ftf": "module \"again\" {\n  source = \"../a\"\n}\n",
	})
	ffExpectDiag(t, d, "Module cycle", "a -> a")
	_, d = ffEvalTree(map[string]string{
		"main.ftf":     ffModHeader + "module \"a\" {\n  source = \"./a\"\n}\n",
		"a/main.ftf":   "module \"b\" {\n  source = \"./b\"\n}\n",
		"a/b/main.ftf": "module \"a\" {\n  source = \"../..\"\n}\n",
	})
	ffExpectDiag(t, d, "Module cycle", "->")

	files := map[string]string{"main.ftf": ffModHeader + "module \"n\" {\n  source = \"./d\"\n}\n"}
	dir := "d"
	for i := range ffMaxModuleDepth + 2 {
		files[dir+"/main.ftf"] = fmt.Sprintf("module \"n\" {\n  source = \"./d%d\"\n}\n", i)
		dir += fmt.Sprintf("/d%d", i)
	}
	files[dir+"/main.ftf"] = "locals {\n  a = 1\n}\n"
	_, d = ffEvalTree(files)
	ffExpectDiag(t, d, "Modules nested too deeply", fmt.Sprint(ffMaxModuleDepth))
	// Just inside the limit is fine.
	files = map[string]string{"main.ftf": ffModHeader + "module \"n\" {\n  source = \"./d\"\n}\n"}
	dir = "d"
	for i := range ffMaxModuleDepth - 1 {
		files[dir+"/main.ftf"] = fmt.Sprintf("module \"n\" {\n  source = \"./d%d\"\n}\n", i)
		dir += fmt.Sprintf("/d%d", i)
	}
	files[dir+"/main.ftf"] = "locals {\n  a = 1\n}\n"
	ffMustEval(t, files)
}

func TestModuleInstanceLimits(t *testing.T) {
	_, d := ffEvalTree(map[string]string{
		"main.ftf":   ffModHeader + "module \"a\" {\n  count  = 3\n  source = \"./a\"\n}\n",
		"a/main.ftf": "module \"b\" {\n  count  = 40000\n  source = \"./b\"\n}\n",
		"a/b/b.ftf":  "locals {\n  x = 1\n}\n",
	})
	ffExpectDiag(t, d, "Too many instances", fmt.Sprint(ffMaxInstances))

	key := strings.Repeat("k", 300)
	_, d = ffEvalTree(map[string]string{"main.ftf": ffModHeader + "resource \"chest\" \"c\" {\n  for_each = toset([\"" + key + "\"])\n  entity = \"wooden-chest\"\n  position = { x = 0.5, y = 0.5 }\n}\n"})
	ffExpectDiag(t, d, "Address too long", "256")
}

func TestModuleWires(t *testing.T) {
	// Wires inside a module, with dynamic indices and belt tiles, are
	// prefixed; a wire may join resources exposed by two modules, and a
	// resource passed in as an input is a valid endpoint in the child.
	x := ffMustEval(t, map[string]string{
		"main.ftf": ffModHeader + `
resource "chest" "pole" {
  entity   = "wooden-chest"
  position = { x = 0.5, y = 0.5 }
}
module "a" {
  source = "./w"
  origin = { x = 5.5, y = 0.5 }
  anchor = resource.chest.pole
}
module "b" {
  source = "./w"
  origin = { x = 5.5, y = 10.5 }
  anchor = resource.chest.pole
}
wire {
  color = "red"
  from  = module.a.head
  to    = module.b.head
}
wire {
  color = "green"
  from  = module.a.belt_tile
  to    = module.b.belt_tile
}
`,
		"w/main.ftf": `
variable "origin" {
  type = object({ x = number, y = number })
}
variable "anchor" {
  type = any
}
resource "chest" "c" {
  count    = 2
  entity   = "wooden-chest"
  position = offset(var.origin, count.index, 0)
}
resource "belt" "line" {
  belt {
    entity = "transport-belt"
    path   = [for i in range(3) : { x = var.origin.x, y = var.origin.y + 2 + i, direction = direction.south }]
  }
}
wire {
  count = 2
  color = "red"
  from  = resource.chest.c[count.index]
  to    = var.anchor
}
output "head" {
  value = resource.chest.c[0]
}
output "belt_tile" {
  value = resource.belt.line.path[1]
}
`,
	})
	want := strings.Join([]string{
		"red:module.a.chest.c[0]->chest.pole",
		"red:module.a.chest.c[1]->chest.pole",
		"red:module.b.chest.c[0]->chest.pole",
		"red:module.b.chest.c[1]->chest.pole",
		"red:module.a.chest.c[0]->module.b.chest.c[0]",
		"green:module.a.belt.line.path[1]->module.b.belt.line.path[1]",
	}, "|")
	if got := ffWireList(x); got != want {
		t.Fatalf("wires\n got %s\nwant %s", got, want)
	}
}

func TestWireEndpointMustBeAResource(t *testing.T) {
	w := func(from string) map[string]string {
		return map[string]string{"main.ftf": ffModHeader + ffMiniRes("a", "") + "locals {\n  p = { x = 1 }\n  s = \"chest.a\"\n}\nwire {\n  color = \"red\"\n  from  = resource.chest.a\n  to    = " + from + "\n}\n"}
	}
	for _, bad := range []string{"local.p", "local.s", "resource.chest.a.position"} {
		_, d := ffEvalTree(w(bad))
		ffExpectDiag(t, d, "Invalid wire endpoint", "single resource")
	}
	// An address that names no instance is refused, even when forged.
	_, d := ffEvalTree(w(`{ address = "chest.ghost" }`))
	ffExpectDiag(t, d, "Invalid wire endpoint", "declared resource instance: chest.ghost")
	_, d = ffEvalTree(w(`{ address = "chest.a.path[0]" }`))
	ffExpectDiag(t, d, "Invalid wire endpoint", "not a belt resource")
	files := w("resource.belt.b.path[9]")
	files["main.ftf"] += "resource \"belt\" \"b\" {\n  belt {\n    entity = \"transport-belt\"\n    path   = [{ x = 0.5, y = 3.5, direction = direction.north }]\n  }\n}\n"
	_, d = ffEvalTree(files)
	ffExpectDiag(t, d, "Invalid index", "")
	_, d = ffEvalTree(w(`{ address = "belt.b.path[9]" }`))
	ffExpectDiag(t, d, "Invalid wire endpoint", "declared resource instance")
}

func TestMissingModuleDirs(t *testing.T) {
	files := map[string][]byte{
		"proj/main.ftf": []byte(ffModHeader + "module \"a\" {\n  source = \"./mods/a\"\n}\nmodule \"b\" {\n  source = \"../shared/b\"\n}\n"),
	}
	got := ffMissingModuleDirs(files, "proj")
	if strings.Join(got, ",") != "proj/mods/a,shared/b" {
		t.Fatalf("missing = %v", got)
	}
	files["proj/mods/a/a.ftf"] = []byte("module \"c\" {\n  source = \"./c\"\n}\n")
	files["shared/b/b.ftf"] = []byte("locals {\n  x = 1\n}\n")
	got = ffMissingModuleDirs(files, "proj")
	if strings.Join(got, ",") != "proj/mods/a/c" {
		t.Fatalf("missing after loading = %v", got)
	}
	files["proj/mods/a/c/c.ftf"] = []byte("locals {\n  x = 1\n}\n")
	if got = ffMissingModuleDirs(files, "proj"); len(got) != 0 {
		t.Fatalf("missing when complete = %v", got)
	}
	x, d := EvalFactorioform(files, ffEvalOptions{RootDir: "proj/"})
	if d.HasErrors() || len(x.Resources) != 0 {
		t.Fatalf("eval of a resource-less tree: %v", d)
	}
}

func TestModuleOutputsAndRootOutputs(t *testing.T) {
	x := ffMustEval(t, map[string]string{
		"main.ftf":   ffModHeader + "module \"m\" {\n  source = \"./m\"\n  origin = { x = 0.5, y = 0.5 }\n}\noutput \"total\" {\n  value = module.m.n * 2\n}\n",
		"m/main.ftf": ffModChest,
	})
	if got := x.Outputs["total"]; !got.RawEquals(cty.NumberIntVal(4)) {
		t.Fatalf("root output = %#v", got)
	}
	_, d := ffEvalTree(map[string]string{"main.ftf": ffModHeader + "output \"x\" {\n  value = 1\n}\noutput \"x\" {\n  value = 2\n}\n"})
	ffExpectDiag(t, d, "Duplicate output", `"x"`)
	_, d = ffEvalTree(map[string]string{"main.ftf": ffModHeader + "output \"x\" {\n  value = var.nope\n}\n"})
	ffExpectDiag(t, d, "Reference to undeclared variable", "nope")
}

// Evaluating the same tree repeatedly yields identical output.
func TestModuleEvaluationDeterministic(t *testing.T) {
	files := map[string]string{
		"main.ftf":   ffModHeader + "module \"col\" {\n  for_each = { a = 1, b = 2, c = 3 }\n  source   = \"./m\"\n  origin   = { x = 0.5, y = 0.5 }\n  n        = each.value\n}\n",
		"m/main.ftf": ffModChest,
	}
	var first string
	for i := range 5 {
		d := ffDump(ffMustEval(t, files))
		if i == 0 {
			first = d
		} else if d != first {
			t.Fatalf("run %d differs", i)
		}
	}
}
