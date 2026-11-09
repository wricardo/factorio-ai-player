package main

// Factorioform modules. A module is a directory of *.ftf files, instantiated
// by a `module` block of its caller, exactly like a Terraform local module:
//
//	module "iron" {
//	  source = "./modules/mining_line"   // relative directory, literal string
//	  origin = { x = -30, y = -10 }      // every other attribute is a child variable
//	  count / for_each                   // optional, same rules as resource blocks
//	}
//
// A module sees only its own variables, locals and resources; it hands values
// back through `output` blocks (module.<name>[key].<output>). Resource values
// carry their absolute instance address in an `address` attribute, so a
// resource can cross a module boundary (output or input) and still be a wire
// endpoint. Instances inside a module are prefixed in their address:
// module.iron.stone_furnace.smelter[0], module.ore["copper"].belt.line.
//
// Nothing here touches the filesystem: the caller supplies every file by name
// and a module's files are the supplied names whose directory is the
// directory its source resolves to (ffMissingModuleDirs tells a loader which
// directories still need reading).

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// ffMaxModuleDepth bounds module nesting (the root is depth 0).
const ffMaxModuleDepth = 8

// ffOutputDecl is an output "<name>" block.
type ffOutputDecl struct {
	Name  string
	Expr  hcl.Expression
	Range hcl.Range
}

// ffModuleCall is a module "<name>" block. Body holds the child variable
// inputs (everything but source, count and for_each).
type ffModuleCall struct {
	Name        string
	Range       hcl.Range
	Source      string
	SourceRange hcl.Range
	Dir         string // resolved directory
	Body        hcl.Body
	Count       hcl.Expression
	ForEach     hcl.Expression
	Def         *ffModuleDef
}

// ffModuleDef is one parsed module directory. It is immutable and shared by
// every instance of the module.
type ffModuleDef struct {
	Dir     string
	Root    bool
	Header  *hcl.Block
	Vars    []*ffVariableDecl
	Nodes   []*ffNode // declaration order
	Order   []*ffNode // dependency order
	Wires   []*ffWireDecl
	Outputs []*ffOutputDecl
	Calls   []*ffModuleCall
}

var ffModuleBlockSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
	{Type: "factorioform"},
	{Type: "variable", LabelNames: []string{"name"}},
	{Type: "locals"},
	{Type: "resource", LabelNames: []string{"kind", "name"}},
	{Type: "wire"},
	{Type: "module", LabelNames: []string{"name"}},
	{Type: "output", LabelNames: []string{"name"}},
}}

var ffModuleMetaSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{
	{Name: "source", Required: true}, {Name: "count"}, {Name: "for_each"},
}}

// ffCleanDir normalizes a directory so it compares equal to ffDirOf.
func ffCleanDir(d string) string {
	if d == "" {
		return "."
	}
	return path.Clean(filepath.ToSlash(d))
}

// ffDirOf is the directory of a manifest file name.
func ffDirOf(name string) string { return path.Dir(filepath.ToSlash(name)) }

// ---------------------------------------------------------------------------
// Loader.

type ffLoader struct {
	files   map[string][]byte
	names   []string
	parsed  map[string]*hcl.File
	defs    map[string]*ffModuleDef
	stack   []string
	missing map[string]bool
}

// newFFLoader parses every supplied file. Parse errors are returned.
func newFFLoader(files map[string][]byte) (*ffLoader, hcl.Diagnostics) {
	l := &ffLoader{files: files, parsed: map[string]*hcl.File{}, defs: map[string]*ffModuleDef{}, missing: map[string]bool{}}
	for n := range files {
		l.names = append(l.names, n)
	}
	sort.Strings(l.names)
	var diags hcl.Diagnostics
	for _, n := range l.names {
		f, d := hclsyntax.ParseConfig(files[n], n, hcl.InitialPos)
		diags = append(diags, d...)
		if f != nil {
			l.parsed[n] = f
		}
	}
	if !diags.HasErrors() && len(l.names) == 0 {
		diags = append(diags, &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "No manifest files", Detail: "at least one .ftf file is required"})
	}
	return l, diags
}

func (l *ffLoader) dirFiles(dir string) []string {
	var out []string
	for _, n := range l.names {
		if ffDirOf(n) == dir {
			out = append(out, n)
		}
	}
	return out
}

// loadRoot loads the root module and every module it reaches, then rejects
// supplied files nothing refers to (a silently ignored module is a bug).
func (l *ffLoader) loadRoot(dir string) (*ffModuleDef, hcl.Diagnostics) {
	def, diags := l.load(dir, nil, "")
	if diags.HasErrors() {
		return nil, diags
	}
	for _, n := range l.names {
		if _, ok := l.defs[ffDirOf(n)]; !ok {
			r := hcl.Range{Filename: n, Start: hcl.InitialPos, End: hcl.InitialPos}
			diags = append(diags, ffErr(r, "Unreferenced manifest file",
				fmt.Sprintf("file %q is not in the root directory %q and no module block has a source that resolves to its directory", n, dir)))
		}
	}
	if diags.HasErrors() {
		return nil, diags
	}
	return def, diags
}

func (l *ffLoader) load(dir string, at *hcl.Range, source string) (*ffModuleDef, hcl.Diagnostics) {
	if def, ok := l.defs[dir]; ok {
		return def, nil
	}
	root := len(l.stack) == 0
	for i, s := range l.stack {
		if s == dir {
			chain := append(append([]string{}, l.stack[i:]...), dir)
			return nil, hcl.Diagnostics{ffErr(*at, "Module cycle", fmt.Sprintf("module %q includes itself: %s", dir, strings.Join(chain, " -> ")))}
		}
	}
	if len(l.stack) > ffMaxModuleDepth {
		return nil, hcl.Diagnostics{ffErr(*at, "Modules nested too deeply", fmt.Sprintf("modules may nest at most %d levels deep", ffMaxModuleDepth))}
	}
	names := l.dirFiles(dir)
	if len(names) == 0 {
		l.missing[dir] = true
		if root {
			return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "No manifest files", Detail: fmt.Sprintf("no .ftf files in directory %q", dir)}}
		}
		return nil, hcl.Diagnostics{ffErr(*at, "Module source not found", fmt.Sprintf("no .ftf files in %q (source %q)", dir, source))}
	}
	l.stack = append(l.stack, dir)
	def, diags := l.build(dir, names, root)
	for _, call := range def.Calls {
		sub, d := l.load(call.Dir, &call.SourceRange, call.Source)
		diags = append(diags, d...)
		call.Def = sub
	}
	l.stack = l.stack[:len(l.stack)-1]
	l.defs[dir] = def
	return def, diags
}

// build parses one module directory into a ffModuleDef: blocks, the static
// reference graph and its evaluation order. Module sources are resolved but
// not loaded.
func (l *ffLoader) build(dir string, names []string, root bool) (*ffModuleDef, hcl.Diagnostics) {
	files := make([]*hcl.File, len(names))
	for i, n := range names {
		files[i] = l.parsed[n]
	}
	content, diags := hcl.MergeFiles(files).Content(ffModuleBlockSchema)

	def := &ffModuleDef{Dir: dir, Root: root}
	var (
		headers  []*hcl.Block
		locals   []*ffLocal
		seenVar  = map[string]hcl.Range{}
		seenLoc  = map[string]hcl.Range{}
		declared = map[string]hcl.Range{}
		seenMod  = map[string]hcl.Range{}
		seenOut  = map[string]hcl.Range{}
	)
	for _, blk := range content.Blocks {
		switch blk.Type {
		case "factorioform":
			headers = append(headers, blk)
		case "variable":
			name := blk.Labels[0]
			if !hclsyntax.ValidIdentifier(name) {
				diags = append(diags, ffErr(blk.LabelRanges[0], "Invalid variable name", "variable names must be HCL identifiers"))
				continue
			}
			if prev, dup := seenVar[name]; dup {
				diags = append(diags, ffErr(blk.DefRange, "Duplicate variable", fmt.Sprintf("variable %q already declared at %s", name, prev)))
				continue
			}
			seenVar[name] = blk.DefRange
			v, d := ffDecodeVariable(blk, nil)
			diags = append(diags, d...)
			if v != nil {
				def.Vars = append(def.Vars, v)
			}
		case "locals":
			attrs, d := blk.Body.JustAttributes()
			diags = append(diags, d...)
			keys := make([]string, 0, len(attrs))
			for k := range attrs {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return attrs[keys[i]].Range.Start.Byte < attrs[keys[j]].Range.Start.Byte })
			for _, k := range keys {
				a := attrs[k]
				if prev, dup := seenLoc[k]; dup {
					diags = append(diags, ffErr(a.NameRange, "Duplicate local", fmt.Sprintf("local %q already declared at %s", k, prev)))
					continue
				}
				seenLoc[k] = a.NameRange
				loc := &ffLocal{Name: k, Expr: a.Expr, Range: a.NameRange}
				locals = append(locals, loc)
				def.Nodes = append(def.Nodes, &ffNode{key: "local." + k, kind: ffNodeLocal, rng: a.NameRange, local: loc})
			}
		case "resource":
			kind, name := blk.Labels[0], blk.Labels[1]
			if !ffIdentRe.MatchString(kind) {
				diags = append(diags, ffErr(blk.LabelRanges[0], "Invalid resource kind", fmt.Sprintf("kind %q must match [a-z][a-z0-9_]*", kind)))
				continue
			}
			if !ffIdentRe.MatchString(name) {
				diags = append(diags, ffErr(blk.LabelRanges[1], "Invalid resource name", fmt.Sprintf("name %q must match [a-z][a-z0-9_]*", name)))
				continue
			}
			key := kind + "." + name
			if prev, dup := declared[key]; dup {
				diags = append(diags, ffErr(blk.DefRange, "Duplicate resource", fmt.Sprintf("resource %s already declared at %s", key, prev)))
				continue
			}
			declared[key] = blk.DefRange
			meta, rest, d := blk.Body.PartialContent(metaSchema)
			diags = append(diags, d...)
			decl := &ffResourceDecl{Kind: kind, Name: name, Range: blk.DefRange, Body: rest}
			if a, ok := meta.Attributes["count"]; ok {
				decl.Count = a.Expr
			}
			if a, ok := meta.Attributes["for_each"]; ok {
				decl.ForEach = a.Expr
			}
			if decl.Count != nil && decl.ForEach != nil {
				diags = append(diags, ffErr(meta.Attributes["for_each"].NameRange, "Conflicting arguments", "count and for_each cannot both be set"))
			}
			def.Nodes = append(def.Nodes, &ffNode{key: "resource." + key, kind: ffNodeResource, rng: blk.DefRange, res: decl})
		case "wire":
			def.Wires = append(def.Wires, &ffWireDecl{rng: blk.DefRange, body: blk.Body})
		case "module":
			call, d := l.decodeModuleCall(dir, blk, seenMod)
			diags = append(diags, d...)
			if call != nil {
				def.Calls = append(def.Calls, call)
				def.Nodes = append(def.Nodes, &ffNode{key: "module." + call.Name, kind: ffNodeModule, rng: blk.DefRange, mod: call})
			}
		case "output":
			name := blk.Labels[0]
			if !hclsyntax.ValidIdentifier(name) {
				diags = append(diags, ffErr(blk.LabelRanges[0], "Invalid output name", "output names must be HCL identifiers"))
				continue
			}
			if prev, dup := seenOut[name]; dup {
				diags = append(diags, ffErr(blk.DefRange, "Duplicate output", fmt.Sprintf("output %q already declared at %s", name, prev)))
				continue
			}
			seenOut[name] = blk.DefRange
			oc, d := blk.Body.Content(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "value", Required: true}, {Name: "description"}}})
			diags = append(diags, d...)
			if a, ok := oc.Attributes["value"]; ok {
				def.Outputs = append(def.Outputs, &ffOutputDecl{Name: name, Expr: a.Expr, Range: blk.DefRange})
			}
		}
	}
	switch {
	case root && len(headers) == 0:
		r := hcl.Range{Filename: names[0], Start: hcl.InitialPos, End: hcl.InitialPos}
		diags = append(diags, ffErr(r, "Missing factorioform block", "exactly one factorioform block is required"))
	case root && len(headers) > 1:
		diags = append(diags, ffErr(headers[1].DefRange, "Duplicate factorioform block", "exactly one factorioform block is allowed"))
	case !root && len(headers) > 0:
		diags = append(diags, ffErr(headers[0].DefRange, "Unexpected factorioform block", "only the root module declares the factorioform block; a module inherits the manifest name, surface and force"))
	}
	if root && len(headers) > 0 {
		def.Header = headers[0]
	}
	if diags.HasErrors() {
		return def, diags
	}

	// Static reference graph over locals, resources and module calls.
	declVars := map[string]bool{}
	for _, v := range def.Vars {
		declVars[v.Name] = true
	}
	declLocals := map[string]bool{}
	for _, loc := range locals {
		declLocals[loc.Name] = true
	}
	declRes := map[string]bool{}
	for k := range declared {
		declRes["resource."+k] = true
	}
	declMods := map[string]bool{}
	for k := range seenMod {
		declMods[k] = true
	}
	refs := func(trs []hcl.Traversal) ([]ffDep, hcl.Diagnostics) {
		return ffRefs(trs, declVars, declLocals, declRes, declMods)
	}
	meta := func(exprs ...hcl.Expression) []hcl.Traversal {
		var trs []hcl.Traversal
		for _, e := range exprs {
			if e != nil {
				trs = append(trs, e.Variables()...)
			}
		}
		return trs
	}
	byKey := map[string]*ffNode{}
	for _, n := range def.Nodes {
		byKey[n.key] = n
		var trs []hcl.Traversal
		switch n.kind {
		case ffNodeLocal:
			trs = n.local.Expr.Variables()
		case ffNodeResource:
			trs = append(ffBodyTraversals(n.res.Body), meta(n.res.Count, n.res.ForEach)...)
		case ffNodeModule:
			trs = append(ffBodyTraversals(n.mod.Body), meta(n.mod.Count, n.mod.ForEach)...)
		}
		var d hcl.Diagnostics
		n.deps, d = refs(trs)
		diags = append(diags, d...)
	}
	for _, w := range def.Wires {
		_, d := refs(ffBodyTraversals(w.body))
		diags = append(diags, d...)
	}
	for _, o := range def.Outputs {
		_, d := refs(o.Expr.Variables())
		diags = append(diags, d...)
	}
	if diags.HasErrors() {
		return def, diags
	}
	order, d := ffTopoSort(def.Nodes, byKey)
	diags = append(diags, d...)
	def.Order = order
	return def, diags
}

func (l *ffLoader) decodeModuleCall(dir string, blk *hcl.Block, seen map[string]hcl.Range) (*ffModuleCall, hcl.Diagnostics) {
	name := blk.Labels[0]
	if !ffIdentRe.MatchString(name) {
		return nil, hcl.Diagnostics{ffErr(blk.LabelRanges[0], "Invalid module name", fmt.Sprintf("name %q must match [a-z][a-z0-9_]*", name))}
	}
	if prev, dup := seen[name]; dup {
		return nil, hcl.Diagnostics{ffErr(blk.DefRange, "Duplicate module", fmt.Sprintf("module %q already declared at %s", name, prev))}
	}
	seen[name] = blk.DefRange
	meta, rest, diags := blk.Body.PartialContent(ffModuleMetaSchema)
	call := &ffModuleCall{Name: name, Range: blk.DefRange, Body: rest}
	if a, ok := meta.Attributes["count"]; ok {
		call.Count = a.Expr
	}
	if a, ok := meta.Attributes["for_each"]; ok {
		call.ForEach = a.Expr
	}
	if call.Count != nil && call.ForEach != nil {
		diags = append(diags, ffErr(meta.Attributes["for_each"].NameRange, "Conflicting arguments", "count and for_each cannot both be set"))
	}
	src, ok := meta.Attributes["source"]
	if !ok {
		return nil, diags
	}
	call.SourceRange = src.Expr.Range()
	v, d := src.Expr.Value(nil)
	if d.HasErrors() || v.IsNull() || v.Type() != cty.String {
		return nil, append(diags, ffErr(call.SourceRange, "Invalid module source", "source must be a literal string"))
	}
	call.Source = v.AsString()
	if !strings.HasPrefix(call.Source, "./") && !strings.HasPrefix(call.Source, "../") {
		return nil, append(diags, ffErr(call.SourceRange, "Unsupported module source",
			fmt.Sprintf("source %q is not supported; only relative local directories (\"./dir\" or \"../dir\") are", call.Source)))
	}
	call.Dir = path.Join(dir, call.Source)
	return call, diags
}

// ffMissingModuleDirs lists the module directories the supplied files refer to
// but do not contain, so a caller that reads from disk knows what to load
// next. Call it until it returns nothing. Manifest errors are not its concern
// and yield an empty result.
func ffMissingModuleDirs(files map[string][]byte, rootDir string) []string {
	l, diags := newFFLoader(files)
	if diags.HasErrors() {
		return nil
	}
	root := ffCleanDir(rootDir)
	l.loadRoot(root)
	var out []string
	for d := range l.missing {
		if d != root {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Evaluation of module calls.

// ffTree is state shared by every module instance of one evaluation.
type ffTree struct {
	instances map[string]*ffInstance // absolute address -> resource instance
	total     int                    // resource and module instances expanded so far
}

func (t *ffTree) add(n int, r hcl.Range) hcl.Diagnostics {
	t.total += n
	if t.total > ffMaxInstances {
		return hcl.Diagnostics{ffErr(r, "Too many instances", fmt.Sprintf("the manifest expands to more than %d resource and module instances", ffMaxInstances))}
	}
	return nil
}

// ffNodeResult is what a node contributes to the flattened result: its
// resource instances and the wires declared by module instances inside it.
type ffNodeResult struct {
	inst  []*ffInstance
	wires []*ffManifestWire
}

// ffVarInputs are the values supplied to a module's variables. The root takes
// typed and raw (CLI) values; a module call takes typed values with the range
// of each attribute that wrote them.
type ffVarInputs struct {
	typed  map[string]cty.Value
	raw    map[string]string
	ranges map[string]hcl.Range
	call   *ffModuleCall // nil for the root module
}

// ffModuleExtra marks a diagnostic already attributed to a module instance.
type ffModuleExtra struct{ Address string }

// ffAnnotate attributes error diagnostics to the innermost module instance
// that raised them.
func ffAnnotate(diags hcl.Diagnostics, addr string) hcl.Diagnostics {
	for _, d := range diags {
		if d.Severity != hcl.DiagError || d.Extra != nil {
			continue
		}
		d.Extra = ffModuleExtra{Address: addr}
		if d.Detail == "" {
			d.Detail = "in " + addr
		} else {
			d.Detail = "in " + addr + ": " + d.Detail
		}
	}
	return diags
}

func newFFEvaluator(tree *ffTree, prefix string) *ffEvaluator {
	return &ffEvaluator{
		tree: tree, prefix: prefix,
		vars:      map[string]cty.Value{},
		locals:    map[string]cty.Value{},
		resources: map[string]map[string]cty.Value{},
		modules:   map[string]cty.Value{},
		ctx: &hcl.EvalContext{
			Variables: map[string]cty.Value{
				"direction": ffDirectionObject(),
				"var":       cty.EmptyObjectVal,
				"local":     cty.EmptyObjectVal,
				"resource":  cty.EmptyObjectVal,
				"module":    cty.EmptyObjectVal,
			},
			Functions: ffFunctions(),
		},
	}
}

// run evaluates one module instance whose variables are already resolved:
// locals, resources and module calls in dependency order, then wires, then
// outputs. Resources and wires come back flattened in declaration order, with
// module instances in place of their module block.
func (ev *ffEvaluator) run(def *ffModuleDef) (*ffNodeResult, cty.Value, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	// A node whose dependency failed is skipped silently: its own errors would
	// only be cascades.
	failed := map[string]bool{}
	results := map[string]*ffNodeResult{}
	for _, n := range def.Order {
		skip := false
		for _, dep := range n.deps {
			if failed[dep.key] {
				skip = true
			}
		}
		if skip {
			failed[n.key] = true
			continue
		}
		var d hcl.Diagnostics
		switch n.kind {
		case ffNodeLocal:
			d = ev.evalLocal(n.local)
		case ffNodeResource:
			var inst []*ffInstance
			inst, d = ev.expandResource(n.res)
			results[n.key] = &ffNodeResult{inst: inst}
		case ffNodeModule:
			var r *ffNodeResult
			r, d = ev.expandModule(n.mod)
			results[n.key] = r
		}
		diags = append(diags, d...)
		if d.HasErrors() {
			failed[n.key] = true
		}
	}
	if len(failed) > 0 {
		return nil, cty.NilVal, diags
	}
	res := &ffNodeResult{}
	for _, n := range def.Nodes {
		if r := results[n.key]; r != nil {
			res.inst = append(res.inst, r.inst...)
			res.wires = append(res.wires, r.wires...)
		}
	}
	for _, w := range def.Wires {
		out, d := ev.expandWire(w)
		diags = append(diags, d...)
		res.wires = append(res.wires, out...)
	}
	outs := map[string]cty.Value{}
	for _, o := range def.Outputs {
		v, d := ffEval(o.Expr, ev.ctx)
		diags = append(diags, d...)
		if !d.HasErrors() {
			outs[o.Name] = v
		}
	}
	if diags.HasErrors() {
		return nil, cty.NilVal, diags
	}
	ov := cty.EmptyObjectVal
	if len(outs) > 0 {
		ov = cty.ObjectVal(outs)
	}
	return res, ov, diags
}

// expandModule instantiates a module call once, count times or per for_each
// key. The result flattens every instance's resources and wires; the call's
// value in the evaluation context is the module's outputs (an object, a tuple
// for count, an object keyed by for_each key).
func (ev *ffEvaluator) expandModule(call *ffModuleCall) (*ffNodeResult, hcl.Diagnostics) {
	keys, mode, diags := ffInstanceKeys(call.Count, call.ForEach, call.Range, ev.ctx)
	if diags.HasErrors() {
		return nil, diags
	}
	attrs, d := call.Body.JustAttributes()
	diags = append(diags, d...)
	names := make([]string, 0, len(attrs))
	declared := map[string]bool{}
	for _, v := range call.Def.Vars {
		declared[v.Name] = true
	}
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !declared[n] {
			diags = append(diags, ffErr(attrs[n].NameRange, "Unsupported argument", fmt.Sprintf("module %q declares no variable %q", call.Name, n)))
		}
	}
	if diags.HasErrors() {
		return nil, diags
	}

	res := &ffNodeResult{}
	vals := make([]cty.Value, 0, len(keys))
	fields := map[string]cty.Value{}
	for _, k := range keys {
		ictx := ev.ctx
		if mode != 0 {
			ictx = ffInstanceCtx(ev.ctx, k, mode == 2)
		}
		in := ffVarInputs{typed: map[string]cty.Value{}, ranges: map[string]hcl.Range{}, call: call}
		for _, n := range names {
			v, d := ffEval(attrs[n].Expr, ictx)
			diags = append(diags, d...)
			if !d.HasErrors() {
				in.typed[n] = v
				in.ranges[n] = attrs[n].Expr.Range()
			}
		}
		if diags.HasErrors() {
			break
		}
		addr := ev.prefix + "module." + call.Name + ffKeySuffix(k, mode)
		if d := ev.tree.add(1, call.Range); d.HasErrors() {
			diags = append(diags, d...)
			break
		}
		child := newFFEvaluator(ev.tree, addr+".")
		d := child.resolveVariables(call.Def.Vars, in)
		var (
			r    *ffNodeResult
			outs cty.Value
		)
		if !d.HasErrors() {
			var d2 hcl.Diagnostics
			r, outs, d2 = child.run(call.Def)
			d = append(d, d2...)
		}
		d = ffAnnotate(d, addr)
		diags = append(diags, d...)
		// One failing instance is reported; the rest would repeat it.
		if d.HasErrors() {
			break
		}
		res.inst = append(res.inst, r.inst...)
		res.wires = append(res.wires, r.wires...)
		vals = append(vals, outs)
		if mode == 2 {
			fields[k.key] = outs
		}
	}
	if diags.HasErrors() {
		return nil, diags
	}

	var ref cty.Value
	switch mode {
	case 0:
		ref = vals[0]
	case 1:
		ref = cty.EmptyTupleVal
		if len(vals) > 0 {
			ref = cty.TupleVal(vals)
		}
	case 2:
		ref = cty.EmptyObjectVal
		if len(fields) > 0 {
			ref = cty.ObjectVal(fields)
		}
	}
	ev.modules[call.Name] = ref
	ev.ctx.Variables["module"] = cty.ObjectVal(ev.modules)
	return res, diags
}
