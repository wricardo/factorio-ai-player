package main

// Factorioform dynamic evaluation. HCL only supplies expression syntax; this
// file defines everything else a manifest can use: variable blocks (typed,
// validated), locals, count/for_each on resource and wire blocks, dynamic
// blocks (hcl ext/dynblock), the direction.* constants, a pure function table,
// and static-attribute references between resources.
//
// EvalFactorioform is the only entry point later layers need: it returns the
// fully expanded resource instances and wires, so nothing downstream ever sees
// HCL. Evaluation is deterministic: every value is known at plan time, there
// are no file/env/time/random functions, count instances are ordered by index
// and for_each instances by key.

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/dynblock"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// ffMaxInstances bounds count/for_each expansion of one block.
const ffMaxInstances = 100000

// ffDirectionNames are the built-in direction.* constants (Factorio 2.0's 16
// directions).
var ffDirectionNames = []string{
	"north", "northnortheast", "northeast", "eastnortheast",
	"east", "eastsoutheast", "southeast", "southsoutheast",
	"south", "southsouthwest", "southwest", "westsouthwest",
	"west", "westnorthwest", "northwest", "northnorthwest",
}

// ffEvalOptions carries caller-supplied variable values.
//
// Vars holds already-typed values (GraphQL variables via ffVarsFromJSON, or
// ffParseVarFile). RawVars holds CLI `--var k=v` strings: they are parsed as
// an HCL literal when the variable declares a non-string type, and used as the
// string itself otherwise. Naming a variable in both is an error, as is naming
// an undeclared variable. Both apply to the root module only.
//
// RootDir is the directory of the root module's files: names without a
// directory when empty. Files elsewhere are modules and must be referenced.
type ffEvalOptions struct {
	Vars    map[string]cty.Value
	RawVars map[string]string
	RootDir string
}

// ffExpanded is the result of evaluating a manifest.
type ffExpanded struct {
	Header    ffHeader
	Variables map[string]cty.Value // final variable values by name
	Locals    map[string]cty.Value // evaluated locals by name
	Outputs   map[string]cty.Value // root module outputs by name
	Resources []ffInstance         // declaration order, module instances inline; Address is absolute
	Wires     []*ffManifestWire    // From/To are absolute instance addresses
}

// ---------------------------------------------------------------------------
// Variable input helpers.

// ffVarsFromJSON converts GraphQL-style variables (decoded JSON) to cty values.
func ffVarsFromJSON(in map[string]any) (map[string]cty.Value, error) {
	out := make(map[string]cty.Value, len(in))
	for k, v := range in {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("variable %q: %w", k, err)
		}
		t, err := ctyjson.ImpliedType(raw)
		if err != nil {
			return nil, fmt.Errorf("variable %q: %w", k, err)
		}
		val, err := ctyjson.Unmarshal(raw, t)
		if err != nil {
			return nil, fmt.Errorf("variable %q: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}

// ffParseVarFile parses a *.ftfvars.hcl file: top-level `name = literal`
// attributes (the direction.* constants are available).
func ffParseVarFile(filename string, src []byte) (map[string]cty.Value, hcl.Diagnostics) {
	f, diags := hclsyntax.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	attrs, d := f.Body.JustAttributes()
	diags = append(diags, d...)
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := map[string]cty.Value{}
	for _, n := range names {
		v, d := attrs[n].Expr.Value(ffStaticContext())
		diags = append(diags, d...)
		if !d.HasErrors() {
			out[n] = v
		}
	}
	if diags.HasErrors() {
		return nil, diags
	}
	return out, diags
}

// ---------------------------------------------------------------------------
// Function table and context.

// ffFunctions is the complete function table. Pure functions only: no file,
// env, time, uuid or random functions exist, so plans are deterministic.
func ffFunctions() map[string]function.Function {
	return map[string]function.Function{
		"range":      stdlib.RangeFunc,
		"length":     ffLengthFunc,
		"concat":     stdlib.ConcatFunc,
		"flatten":    stdlib.FlattenFunc,
		"merge":      stdlib.MergeFunc,
		"keys":       stdlib.KeysFunc,
		"values":     stdlib.ValuesFunc,
		"lookup":     stdlib.LookupFunc,
		"min":        stdlib.MinFunc,
		"max":        stdlib.MaxFunc,
		"floor":      stdlib.FloorFunc,
		"ceil":       stdlib.CeilFunc,
		"abs":        stdlib.AbsoluteFunc,
		"format":     stdlib.FormatFunc,
		"join":       stdlib.JoinFunc,
		"coalesce":   stdlib.CoalesceFunc,
		"contains":   stdlib.ContainsFunc,
		"distinct":   stdlib.DistinctFunc,
		"reverse":    stdlib.ReverseListFunc,
		"slice":      stdlib.SliceFunc,
		"element":    stdlib.ElementFunc,
		"zipmap":     stdlib.ZipmapFunc,
		"setproduct": stdlib.SetProductFunc,
		"tostring":   stdlib.MakeToFunc(cty.String),
		"tonumber":   stdlib.MakeToFunc(cty.Number),
		"tobool":     stdlib.MakeToFunc(cty.Bool),
		"tolist":     stdlib.MakeToFunc(cty.List(cty.DynamicPseudoType)),
		"toset":      stdlib.MakeToFunc(cty.Set(cty.DynamicPseudoType)),
		"tomap":      stdlib.MakeToFunc(cty.Map(cty.DynamicPseudoType)),
		"offset":     ffOffsetFunc,
		"grid":       ffGridFunc,
	}
}

// length works on strings (characters), lists, sets, maps, tuples and objects.
var ffLengthFunc = function.New(&function.Spec{
	Params: []function.Parameter{{Name: "value", Type: cty.DynamicPseudoType}},
	Type:   function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		v := args[0]
		if v.IsNull() {
			return cty.NilVal, fmt.Errorf("argument must not be null")
		}
		t := v.Type()
		switch {
		case t == cty.String:
			return cty.NumberIntVal(int64(len([]rune(v.AsString())))), nil
		case t.IsListType() || t.IsSetType() || t.IsMapType() || t.IsTupleType() || t.IsObjectType():
			return cty.NumberIntVal(int64(v.LengthInt())), nil
		}
		return cty.NilVal, fmt.Errorf("argument must be a string, list, set, map, tuple or object")
	},
})

func ffPosParts(v cty.Value, what string) (map[string]cty.Value, cty.Value, cty.Value, error) {
	if v.IsNull() || !v.IsKnown() || !(v.Type().IsObjectType() || v.Type().IsMapType()) {
		return nil, cty.NilVal, cty.NilVal, fmt.Errorf("%s must be an object with numeric x and y", what)
	}
	m := v.AsValueMap()
	x, okx := m["x"]
	y, oky := m["y"]
	if !okx || !oky || x.IsNull() || y.IsNull() || x.Type() != cty.Number || y.Type() != cty.Number {
		return nil, cty.NilVal, cty.NilVal, fmt.Errorf("%s must be an object with numeric x and y", what)
	}
	return m, x, y, nil
}

// offset(pos, dx, dy) returns pos moved by (dx, dy); other attributes of pos
// (such as direction) are kept.
var ffOffsetFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "pos", Type: cty.DynamicPseudoType},
		{Name: "dx", Type: cty.Number},
		{Name: "dy", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		m, x, y, err := ffPosParts(args[0], "pos")
		if err != nil {
			return cty.NilVal, err
		}
		out := make(map[string]cty.Value, len(m))
		for k, v := range m {
			out[k] = v
		}
		out["x"] = x.Add(args[1])
		out["y"] = y.Add(args[2])
		return cty.ObjectVal(out), nil
	},
})

// grid(origin, cols, rows, dx, dy) returns cols*rows positions {x,y}, row by
// row: (ox + c*dx, oy + r*dy).
var ffGridFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "origin", Type: cty.DynamicPseudoType},
		{Name: "cols", Type: cty.Number},
		{Name: "rows", Type: cty.Number},
		{Name: "dx", Type: cty.Number},
		{Name: "dy", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		_, ox, oy, err := ffPosParts(args[0], "origin")
		if err != nil {
			return cty.NilVal, err
		}
		dim := func(v cty.Value, name string) (int, error) {
			n, acc := v.AsBigFloat().Int64()
			if acc != big.Exact || n < 0 || n > ffMaxInstances {
				return 0, fmt.Errorf("%s must be an integer between 0 and %d", name, ffMaxInstances)
			}
			return int(n), nil
		}
		cols, err := dim(args[1], "cols")
		if err != nil {
			return cty.NilVal, err
		}
		rows, err := dim(args[2], "rows")
		if err != nil {
			return cty.NilVal, err
		}
		if cols*rows > ffMaxInstances {
			return cty.NilVal, fmt.Errorf("grid of %d positions exceeds the limit of %d", cols*rows, ffMaxInstances)
		}
		elemTy := cty.Object(map[string]cty.Type{"x": cty.Number, "y": cty.Number})
		if cols*rows == 0 {
			return cty.ListValEmpty(elemTy), nil
		}
		out := make([]cty.Value, 0, cols*rows)
		for r := range rows {
			y := oy.Add(args[4].Multiply(cty.NumberIntVal(int64(r))))
			for c := range cols {
				x := ox.Add(args[3].Multiply(cty.NumberIntVal(int64(c))))
				out = append(out, cty.ObjectVal(map[string]cty.Value{"x": x, "y": y}))
			}
		}
		return cty.ListVal(out), nil
	},
})

func ffDirectionObject() cty.Value {
	d := make(map[string]cty.Value, len(ffDirectionNames))
	for i, n := range ffDirectionNames {
		d[n] = cty.NumberIntVal(int64(i))
	}
	return cty.ObjectVal(d)
}

// ---------------------------------------------------------------------------
// Evaluator.

type ffNodeKind int

const (
	ffNodeLocal ffNodeKind = iota
	ffNodeResource
	ffNodeModule
)

type ffDep struct {
	key string
	rng hcl.Range
}

type ffNode struct {
	key   string // "local.<name>", "resource.<kind>.<name>" or "module.<name>"
	kind  ffNodeKind
	rng   hcl.Range
	deps  []ffDep
	local *ffLocal
	res   *ffResourceDecl
	mod   *ffModuleCall
}

type ffWireDecl struct {
	rng  hcl.Range
	body hcl.Body
}

// ffEvaluator evaluates one module instance (the root, or one count/for_each
// instance of a module call). prefix is the instance path every address of
// this instance starts with: "" for the root, `module.iron.` and so on.
type ffEvaluator struct {
	tree      *ffTree
	prefix    string
	ctx       *hcl.EvalContext
	vars      map[string]cty.Value
	locals    map[string]cty.Value
	resources map[string]map[string]cty.Value // kind -> name -> reference value
	modules   map[string]cty.Value            // module call -> outputs
}

type ffVariableDecl struct {
	ffVariable
	validations []ffValidation
}

type ffValidation struct {
	condition hcl.Expression
	message   hcl.Expression
	rng       hcl.Range
}

// EvalFactorioform parses, merges and fully evaluates *.ftf files: variables,
// locals, count/for_each, dynamic blocks, resource references and modules.
// The root module is the files whose directory is opts.RootDir; every other
// file must be reachable through a module block. The result contains only
// plain Go data. On any error the returned value is nil.
func EvalFactorioform(files map[string][]byte, opts ffEvalOptions) (*ffExpanded, hcl.Diagnostics) {
	l, diags := newFFLoader(files)
	if diags.HasErrors() {
		return nil, ffDedupe(diags)
	}
	root, d := l.loadRoot(ffCleanDir(opts.RootDir))
	diags = append(diags, d...)
	if diags.HasErrors() {
		return nil, ffDedupe(diags)
	}

	ev := newFFEvaluator(&ffTree{instances: map[string]*ffInstance{}}, "")
	diags = append(diags, ev.resolveVariables(root.Vars, ffVarInputs{typed: opts.Vars, raw: opts.RawVars})...)
	if diags.HasErrors() {
		return nil, ffDedupe(diags)
	}
	res, outputs, d := ev.run(root)
	diags = append(diags, d...)
	if diags.HasErrors() {
		return nil, ffDedupe(diags)
	}

	out := &ffExpanded{Variables: ev.vars, Locals: ev.locals, Wires: res.wires, Outputs: map[string]cty.Value{}}
	for _, in := range res.inst {
		out.Resources = append(out.Resources, *in)
	}
	if !outputs.IsNull() {
		for k, v := range outputs.AsValueMap() {
			out.Outputs[k] = v
		}
	}
	// Header may use variables and locals.
	h, d := decodeFFHeader(root.Header, ev.ctx)
	diags = append(diags, d...)
	if h != nil {
		out.Header = *h
	}
	if diags.HasErrors() {
		return nil, ffDedupe(diags)
	}
	return out, ffDedupe(diags)
}

var metaSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "count"}, {Name: "for_each"}}}

// ffDedupe drops repeated identical diagnostics (a bad attribute in a
// count/for_each block fails once per instance).
func ffDedupe(in hcl.Diagnostics) hcl.Diagnostics {
	seen := map[string]bool{}
	var out hcl.Diagnostics
	for _, d := range in {
		k := fmt.Sprintf("%d|%s|%s|%v", d.Severity, d.Summary, d.Detail, d.Subject)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	return out
}

// ---------------------------------------------------------------------------
// Variables.

// ffDecodeVariable is the single variable decoder for both the static parser
// and the evaluator: type constraint, description, default conversion and
// validation blocks. ctx supplies constants for description/default; nil means
// literal-only (direction.* plus the function table).
func ffDecodeVariable(blk *hcl.Block, ctx *hcl.EvalContext) (*ffVariableDecl, hcl.Diagnostics) {
	content, diags := blk.Body.Content(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "type"}, {Name: "default"}, {Name: "description"}},
		Blocks:     []hcl.BlockHeaderSchema{{Type: "validation"}},
	})
	v := &ffVariableDecl{ffVariable: ffVariable{Name: blk.Labels[0], Type: cty.DynamicPseudoType, Range: blk.DefRange}}
	if a, ok := content.Attributes["type"]; ok {
		t, d := typeexpr.TypeConstraint(a.Expr)
		diags = append(diags, d...)
		if !d.HasErrors() {
			v.Type, v.HasType = t, true
		}
	}
	empty := ctx
	if empty == nil {
		empty = &hcl.EvalContext{Variables: map[string]cty.Value{"direction": ffDirectionObject()}, Functions: ffFunctions()}
	}
	if a, ok := content.Attributes["description"]; ok {
		val, d := ffEval(a.Expr, empty)
		diags = append(diags, d...)
		if !d.HasErrors() {
			s, d := ffString(val, a.Expr.Range(), "description")
			diags = append(diags, d...)
			v.Description = s
		}
	}
	if a, ok := content.Attributes["default"]; ok {
		val, d := ffEval(a.Expr, empty)
		diags = append(diags, d...)
		if !d.HasErrors() {
			if v.HasType {
				c, err := convert.Convert(val, v.Type)
				if err != nil {
					diags = append(diags, ffErr(a.Expr.Range(), "Invalid default value", err.Error()))
				} else {
					val = c
				}
			}
			v.Default, v.HasDefault = val, true
		}
	}
	for _, vb := range content.Blocks {
		vc, d := vb.Body.Content(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{
			{Name: "condition", Required: true}, {Name: "error_message", Required: true},
		}})
		diags = append(diags, d...)
		if d.HasErrors() {
			continue
		}
		v.validations = append(v.validations, ffValidation{
			condition: vc.Attributes["condition"].Expr,
			message:   vc.Attributes["error_message"].Expr,
			rng:       vc.Attributes["condition"].Expr.Range(),
		})
	}
	return v, diags
}

// ffStrictMismatch reports why val does not already match typ, without relying
// on cty's unsafe conversions: "" means the value is acceptable. Undeclared
// object attributes are always errors, so a typo cannot be silently dropped.
// Primitive type identity is only enforced for external values: HCL-authored
// arguments keep HCL's conversion rules (allowCoerce).
func ffStrictMismatch(val cty.Value, typ cty.Type, allowCoerce bool) string {
	if val.IsNull() || !val.IsKnown() || typ == cty.DynamicPseudoType {
		return ""
	}
	switch {
	case typ == cty.String, typ == cty.Number, typ == cty.Bool:
		if val.Type() != typ && !allowCoerce {
			return fmt.Sprintf("a %s is not a %s", val.Type().FriendlyName(), typ.FriendlyName())
		}
		return ""
	case typ.IsListType() || typ.IsSetType():
		if !val.CanIterateElements() {
			return fmt.Sprintf("%s is not a %s", val.Type().FriendlyName(), typ.FriendlyName())
		}
		for it := val.ElementIterator(); it.Next(); {
			_, e := it.Element()
			if why := ffStrictMismatch(e, typ.ElementType(), allowCoerce); why != "" {
				return why
			}
		}
		return ""
	case typ.IsMapType():
		if !val.CanIterateElements() {
			return fmt.Sprintf("%s is not a map", val.Type().FriendlyName())
		}
		for it := val.ElementIterator(); it.Next(); {
			_, e := it.Element()
			if why := ffStrictMismatch(e, typ.ElementType(), allowCoerce); why != "" {
				return why
			}
		}
		return ""
	case typ.IsObjectType():
		if !val.Type().IsObjectType() && !val.Type().IsMapType() {
			return fmt.Sprintf("%s is not an object", val.Type().FriendlyName())
		}
		attrs := typ.AttributeTypes()
		got := val.AsValueMap()
		var unknown []string
		for k := range got {
			if _, ok := attrs[k]; !ok {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Sprintf("%s has no attribute %s", typeexpr.TypeString(typ), strings.Join(unknown, ", "))
		}
		for name, at := range attrs {
			if g, ok := got[name]; ok {
				if why := ffStrictMismatch(g, at, allowCoerce); why != "" {
					return fmt.Sprintf("attribute %q: %s", name, why)
				}
			}
		}
		return ""
	case typ.IsTupleType():
		if !val.CanIterateElements() {
			return fmt.Sprintf("%s is not a tuple", val.Type().FriendlyName())
		}
		return ""
	}
	return ""
}

func (ev *ffEvaluator) resolveVariables(vars []*ffVariableDecl, in ffVarInputs) hcl.Diagnostics {
	var diags hcl.Diagnostics
	declared := map[string]*ffVariableDecl{}
	for _, v := range vars {
		declared[v.Name] = v
	}
	// The root takes caller-supplied values by name, so an unknown name is an
	// error here; a module call checks its attributes where it has their ranges.
	if in.call == nil {
		var bad []string
		for n := range in.typed {
			if declared[n] == nil {
				bad = append(bad, n)
			}
		}
		for n := range in.raw {
			if declared[n] == nil {
				bad = append(bad, n)
			}
		}
		sort.Strings(bad)
		for _, n := range bad {
			diags = append(diags, &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Undeclared variable",
				Detail: fmt.Sprintf("a value was supplied for variable %q, but the manifest declares no such variable", n)})
		}
	}
	// at is where a problem with a variable's value is reported: the module
	// call attribute that wrote it, the call itself when it wrote nothing, or
	// the variable block in the root.
	at := func(v *ffVariableDecl) hcl.Range {
		if r, ok := in.ranges[v.Name]; ok {
			return r
		}
		if in.call != nil {
			return in.call.Range
		}
		return v.Range
	}

	values := map[string]cty.Value{}
	for _, v := range vars {
		val, supplied := in.typed[v.Name]
		raw, hasRaw := in.raw[v.Name]
		if supplied && hasRaw {
			diags = append(diags, ffErr(v.Range, "Conflicting variable values", fmt.Sprintf("variable %q was supplied twice", v.Name)))
			continue
		}
		if hasRaw {
			supplied = true
			if v.HasType && v.Type != cty.String {
				expr, d := hclsyntax.ParseExpression([]byte(raw), "--var "+v.Name, hcl.InitialPos)
				if d.HasErrors() {
					diags = append(diags, ffErr(v.Range, "Invalid variable value", fmt.Sprintf("variable %q: %q is not an HCL literal of type %s", v.Name, raw, typeexpr.TypeString(v.Type))))
					continue
				}
				pv, d := expr.Value(ffStaticContext())
				if d.HasErrors() {
					diags = append(diags, ffErr(v.Range, "Invalid variable value", fmt.Sprintf("variable %q: %q is not a literal value", v.Name, raw)))
					continue
				}
				val = pv
			} else {
				val = cty.StringVal(raw)
			}
		}
		if !supplied {
			if !v.HasDefault {
				if in.call != nil {
					diags = append(diags, ffErr(in.call.Range, "Missing required variable",
						fmt.Sprintf("module %q requires variable %q; set %s = ... in the module block", in.call.Name, v.Name, v.Name)))
				} else {
					diags = append(diags, ffErr(v.Range, "Missing required variable",
						fmt.Sprintf("variable %q has no default; supply a value (GraphQL variables, --var %s=..., or --var-file)", v.Name, v.Name)))
				}
				continue
			}
			val = v.Default
		} else if v.HasType {
			c, err := convert.Convert(val, v.Type)
			if err != nil {
				diags = append(diags, ffErr(at(v), "Invalid value for variable",
					fmt.Sprintf("variable %q: %s (want %s)", v.Name, ffConvertMsg(err), typeexpr.TypeString(v.Type))))
				continue
			}
			// convert.Convert is permissive (a string satisfies a number) and drops
			// undeclared object attributes. Values that arrive through the GraphQL
			// boundary must match the declared type exactly; HCL-authored values
			// (defaults, module arguments) and --var keep HCL's conversion rules,
			// because module inputs are written by hand and CLI arguments are
			// always strings.
			if !hasRaw {
				if why := ffStrictMismatch(val, v.Type, in.call != nil); why != "" {
					diags = append(diags, ffErr(at(v), "Invalid value for variable",
						fmt.Sprintf("variable %q: %s (want %s)", v.Name, why, typeexpr.TypeString(v.Type))))
					continue
				}
			}
			val = c
		}
		if !val.IsWhollyKnown() {
			diags = append(diags, ffErr(at(v), "Unknown variable value", fmt.Sprintf("variable %q must be fully known", v.Name)))
			continue
		}
		values[v.Name] = val
	}
	if diags.HasErrors() {
		return diags
	}
	ev.vars = values
	ev.ctx.Variables["var"] = cty.ObjectVal(values)

	for _, v := range vars {
		for _, vd := range v.validations {
			cond, d := ffEval(vd.condition, ev.ctx)
			diags = append(diags, d...)
			if d.HasErrors() {
				continue
			}
			if cond.IsNull() || cond.Type() != cty.Bool {
				diags = append(diags, ffErr(vd.rng, "Invalid validation condition", "condition must be a bool"))
				continue
			}
			if cond.True() {
				continue
			}
			msg := "validation failed"
			if mv, d := ffEval(vd.message, ev.ctx); !d.HasErrors() && !mv.IsNull() && mv.Type() == cty.String {
				msg = mv.AsString()
			}
			diags = append(diags, ffErr(vd.rng, "Invalid value for variable", fmt.Sprintf("variable %q: %s", v.Name, msg)))
		}
	}
	return diags
}

func ffConvertMsg(err error) string {
	return strings.TrimSpace(err.Error())
}

// ---------------------------------------------------------------------------
// Dependency graph.

func ffBodyTraversals(b hcl.Body) []hcl.Traversal {
	node, ok := b.(hclsyntax.Node)
	if !ok {
		return nil
	}
	var out []hcl.Traversal
	hclsyntax.VisitAll(node, func(n hclsyntax.Node) hcl.Diagnostics {
		if s, ok := n.(*hclsyntax.ScopeTraversalExpr); ok {
			out = append(out, s.Traversal)
		}
		return nil
	})
	return out
}

// ffRefs validates var/local/resource/module references and returns the node
// dependencies (locals, resources and module calls). Unknown roots
// (for-expression and dynamic iterator variables, count, each, direction) are
// ignored.
func ffRefs(trs []hcl.Traversal, vars, locals, res, mods map[string]bool) ([]ffDep, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	var deps []ffDep
	seen := map[string]bool{}
	add := func(key string, r hcl.Range) {
		if !seen[key] {
			seen[key] = true
			deps = append(deps, ffDep{key: key, rng: r})
		}
	}
	attr := func(tr hcl.Traversal, i int) (string, bool) {
		if len(tr) <= i {
			return "", false
		}
		a, ok := tr[i].(hcl.TraverseAttr)
		return a.Name, ok
	}
	for _, tr := range trs {
		r := tr.SourceRange()
		switch tr.RootName() {
		case "var":
			if n, ok := attr(tr, 1); ok && !vars[n] {
				diags = append(diags, ffErr(r, "Reference to undeclared variable", fmt.Sprintf("variable %q is not declared; add a variable %q block", n, n)))
			}
		case "local":
			n, ok := attr(tr, 1)
			if !ok {
				continue
			}
			if !locals[n] {
				diags = append(diags, ffErr(r, "Reference to undeclared local", fmt.Sprintf("local %q is not declared in a locals block", n)))
				continue
			}
			add("local."+n, r)
		case "resource":
			k, ok1 := attr(tr, 1)
			n, ok2 := attr(tr, 2)
			if !ok1 || !ok2 {
				diags = append(diags, ffErr(r, "Invalid resource reference", "resource references look like resource.<kind>.<name>"))
				continue
			}
			key := "resource." + k + "." + n
			if !res[key] {
				diags = append(diags, ffErr(r, "Reference to undeclared resource", fmt.Sprintf("no resource %q %q is declared", k, n)))
				continue
			}
			add(key, r)
		case "module":
			n, ok := attr(tr, 1)
			if !ok {
				diags = append(diags, ffErr(r, "Invalid module reference", "module references look like module.<name>.<output>"))
				continue
			}
			if !mods[n] {
				diags = append(diags, ffErr(r, "Reference to undeclared module", fmt.Sprintf("no module %q is declared", n)))
				continue
			}
			add("module."+n, r)
		}
	}
	return deps, diags
}

func ffTopoSort(nodes []*ffNode, byKey map[string]*ffNode) ([]*ffNode, hcl.Diagnostics) {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var (
		order []*ffNode
		stack []*ffNode
		diags hcl.Diagnostics
		visit func(n *ffNode)
	)
	visit = func(n *ffNode) {
		color[n.key] = grey
		stack = append(stack, n)
		for _, dep := range n.deps {
			t := byKey[dep.key]
			switch color[dep.key] {
			case white:
				visit(t)
			case grey:
				start := 0
				for i, s := range stack {
					if s.key == dep.key {
						start = i
					}
				}
				var chain []string
				for _, s := range stack[start:] {
					chain = append(chain, fmt.Sprintf("%s (declared at %s)", s.key, s.rng))
				}
				chain = append(chain, t.key)
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError, Summary: "Reference cycle",
					Detail:  fmt.Sprintf("%s refers back to %s (declared at %s): %s", n.key, t.key, t.rng, strings.Join(chain, " -> ")),
					Subject: dep.rng.Ptr(), Context: t.rng.Ptr(),
				})
			}
		}
		stack = stack[:len(stack)-1]
		color[n.key] = black
		order = append(order, n)
	}
	for _, n := range nodes {
		if color[n.key] == white {
			visit(n)
		}
	}
	return order, diags
}

// ---------------------------------------------------------------------------
// Locals and resources.

func (ev *ffEvaluator) evalLocal(l *ffLocal) hcl.Diagnostics {
	v, d := ffEval(l.Expr, ev.ctx)
	if d.HasErrors() {
		return d
	}
	ev.locals[l.Name] = v
	ev.ctx.Variables["local"] = cty.ObjectVal(ev.locals)
	return d
}

// ffKey is one count/for_each iteration.
type ffKey struct {
	index  int
	key    string
	value  cty.Value
	hasKey bool
}

func ffEachCount(expr hcl.Expression, ctx *hcl.EvalContext) ([]ffKey, hcl.Diagnostics) {
	v, d := ffEval(expr, ctx)
	if d.HasErrors() {
		return nil, d
	}
	r := expr.Range()
	if v.IsNull() {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid count", "count must not be null")}
	}
	nv, err := convert.Convert(v, cty.Number)
	if err != nil {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid count", "count must be a number")}
	}
	n, acc := nv.AsBigFloat().Int64()
	if acc != big.Exact || n < 0 {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid count", "count must be a whole number >= 0")}
	}
	if n > ffMaxInstances {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid count", fmt.Sprintf("count %d exceeds the limit of %d instances", n, ffMaxInstances))}
	}
	keys := make([]ffKey, n)
	for i := range keys {
		keys[i] = ffKey{index: i}
	}
	return keys, d
}

func ffEachForEach(expr hcl.Expression, ctx *hcl.EvalContext) ([]ffKey, hcl.Diagnostics) {
	v, d := ffEval(expr, ctx)
	if d.HasErrors() {
		return nil, d
	}
	r := expr.Range()
	if v.IsNull() {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid for_each", "for_each must not be null")}
	}
	t := v.Type()
	var keys []ffKey
	switch {
	case t.IsMapType() || t.IsObjectType():
		for it := v.ElementIterator(); it.Next(); {
			k, ev := it.Element()
			keys = append(keys, ffKey{key: k.AsString(), value: ev, hasKey: true})
		}
	case t.IsSetType() && t.ElementType() == cty.String:
		for it := v.ElementIterator(); it.Next(); {
			k, _ := it.Element()
			keys = append(keys, ffKey{key: k.AsString(), value: k, hasKey: true})
		}
	default:
		return nil, hcl.Diagnostics{ffErr(r, "Invalid for_each", "for_each must be a map, an object or a set of strings (use toset(...) for a list of strings)")}
	}
	if len(keys) > ffMaxInstances {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid for_each", fmt.Sprintf("for_each has %d elements; the limit is %d", len(keys), ffMaxInstances))}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].key < keys[j].key })
	return keys, d
}

func ffInstanceCtx(parent *hcl.EvalContext, k ffKey, forEach bool) *hcl.EvalContext {
	c := parent.NewChild()
	if forEach {
		c.Variables = map[string]cty.Value{"each": cty.ObjectVal(map[string]cty.Value{"key": cty.StringVal(k.key), "value": k.value})}
	} else {
		c.Variables = map[string]cty.Value{"count": cty.ObjectVal(map[string]cty.Value{"index": cty.NumberIntVal(int64(k.index))})}
	}
	return c
}

func ffPointValue(p ffPoint) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{"x": cty.NumberFloatVal(p.X), "y": cty.NumberFloatVal(p.Y)})
}

// ffStaticAttrs is the ONLY view of a resource other blocks can reference:
// entity, position, direction, quality (belts: entity and path tiles), and
// address, the instance's absolute address. address is what lets a resource
// cross a module boundary and still be a wire endpoint; belt path tiles carry
// "<belt address>.path[i]".
func ffStaticAttrs(spec *ffResourceSpec, addr string) cty.Value {
	if spec.Belt != nil {
		tiles := make([]cty.Value, len(spec.Belt.Tiles))
		for i, t := range spec.Belt.Tiles {
			tiles[i] = cty.ObjectVal(map[string]cty.Value{
				"x": cty.NumberFloatVal(t.Position.X), "y": cty.NumberFloatVal(t.Position.Y),
				"position":  ffPointValue(t.Position),
				"direction": cty.NumberIntVal(int64(t.Direction)),
				"address":   cty.StringVal(fmt.Sprintf("%s.path[%d]", addr, i)),
			})
		}
		path := cty.EmptyTupleVal
		if len(tiles) > 0 {
			path = cty.TupleVal(tiles)
		}
		return cty.ObjectVal(map[string]cty.Value{"entity": cty.StringVal(spec.Belt.Entity), "path": path, "address": cty.StringVal(addr)})
	}
	quality := cty.NullVal(cty.String)
	if spec.Quality != nil {
		quality = cty.StringVal(*spec.Quality)
	}
	direction := cty.NullVal(cty.Number)
	if spec.Direction != nil {
		direction = cty.NumberIntVal(int64(*spec.Direction))
	}
	return cty.ObjectVal(map[string]cty.Value{
		"entity":    cty.StringVal(spec.Entity),
		"position":  ffPointValue(spec.Position),
		"direction": direction,
		"quality":   quality,
		"address":   cty.StringVal(addr),
	})
}

// ffKeySuffix is the "[i]" / `["key"]` part of an instance address.
func ffKeySuffix(k ffKey, mode int) string {
	switch mode {
	case 1:
		return fmt.Sprintf("[%d]", k.index)
	case 2:
		return fmt.Sprintf("[%q]", k.key)
	}
	return ""
}

// ffAddress is the absolute address of an instance; prefix is the module
// instance path ("" in the root module, otherwise `module.iron.`).
func ffAddress(prefix, kind, name string, k ffKey, mode int) string {
	return prefix + kind + "." + name + ffKeySuffix(k, mode)
}

// ffInstanceKeys expands count / for_each. mode is 0 for a single instance, 1
// for count and 2 for for_each.
func ffInstanceKeys(count, forEach hcl.Expression, rng hcl.Range, ctx *hcl.EvalContext) ([]ffKey, int, hcl.Diagnostics) {
	switch {
	case count != nil && forEach != nil:
		return nil, 0, hcl.Diagnostics{ffErr(rng, "Conflicting arguments", "count and for_each cannot both be set")}
	case count != nil:
		keys, d := ffEachCount(count, ctx)
		return keys, 1, d
	case forEach != nil:
		keys, d := ffEachForEach(forEach, ctx)
		return keys, 2, d
	}
	return []ffKey{{index: -1}}, 0, nil
}

func (ev *ffEvaluator) expandResource(decl *ffResourceDecl) ([]*ffInstance, hcl.Diagnostics) {
	keys, mode, diags := ffInstanceKeys(decl.Count, decl.ForEach, decl.Range, ev.ctx)
	if diags.HasErrors() {
		return nil, diags
	}
	if d := ev.tree.add(len(keys), decl.Range); d.HasErrors() {
		return nil, d
	}

	var out []*ffInstance
	vals := make([]cty.Value, 0, len(keys))
	fields := map[string]cty.Value{}
	for _, k := range keys {
		ictx := ev.ctx
		if mode != 0 {
			ictx = ffInstanceCtx(ev.ctx, k, mode == 2)
		}
		addr := ffAddress(ev.prefix, decl.Kind, decl.Name, k, mode)
		if len(addr) > maxFactorioformAddressCharacters {
			diags = append(diags, ffErr(decl.Range, "Address too long", fmt.Sprintf("instance address %s exceeds %d characters", addr, maxFactorioformAddressCharacters)))
			continue
		}
		inst := *decl
		inst.Body = dynblock.Expand(decl.Body, ictx)
		spec, d := ffDecodeResource(&inst, ictx)
		diags = append(diags, d...)
		if d.HasErrors() || spec == nil {
			continue
		}
		in := &ffInstance{Address: addr, Spec: spec, Range: decl.Range}
		attrs := ffStaticAttrs(spec, addr)
		if mode == 2 {
			fields[k.key] = attrs
		}
		out = append(out, in)
		vals = append(vals, attrs)
		ev.tree.instances[addr] = in
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
	if ev.resources[decl.Kind] == nil {
		ev.resources[decl.Kind] = map[string]cty.Value{}
	}
	ev.resources[decl.Kind][decl.Name] = ref
	all := make(map[string]cty.Value, len(ev.resources))
	for kind, m := range ev.resources {
		all[kind] = cty.ObjectVal(m)
	}
	ev.ctx.Variables["resource"] = cty.ObjectVal(all)
	return out, diags
}

// ---------------------------------------------------------------------------
// Wires.

func (ev *ffEvaluator) expandWire(w *ffWireDecl) ([]*ffManifestWire, hcl.Diagnostics) {
	meta, rest, diags := w.body.PartialContent(metaSchema)
	var (
		keys []ffKey
		mode int
	)
	c, hasCount := meta.Attributes["count"]
	f, hasEach := meta.Attributes["for_each"]
	switch {
	case hasCount && hasEach:
		return nil, append(diags, ffErr(f.NameRange, "Conflicting arguments", "count and for_each cannot both be set"))
	case hasCount:
		mode = 1
		var d hcl.Diagnostics
		keys, d = ffEachCount(c.Expr, ev.ctx)
		diags = append(diags, d...)
	case hasEach:
		mode = 2
		var d hcl.Diagnostics
		keys, d = ffEachForEach(f.Expr, ev.ctx)
		diags = append(diags, d...)
	default:
		keys = []ffKey{{index: -1}}
	}
	if diags.HasErrors() {
		return nil, diags
	}
	var out []*ffManifestWire
	for _, k := range keys {
		ictx := ev.ctx
		if mode != 0 {
			ictx = ffInstanceCtx(ev.ctx, k, mode == 2)
		}
		mw, d := ev.decodeWire(w.rng, rest, ictx)
		diags = append(diags, d...)
		if mw != nil && !d.HasErrors() {
			out = append(out, mw)
		}
	}
	return out, diags
}

func (ev *ffEvaluator) decodeWire(rng hcl.Range, body hcl.Body, ctx *hcl.EvalContext) (*ffManifestWire, hcl.Diagnostics) {
	content, diags := body.Content(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "color", Required: true}, {Name: "from", Required: true}, {Name: "to", Required: true},
		{Name: "from_connector"}, {Name: "to_connector"},
	}})
	w := &ffManifestWire{Range: rng}
	str := func(name string) (string, bool) {
		a, ok := content.Attributes[name]
		if !ok {
			return "", false
		}
		v, d := ffEval(a.Expr, ctx)
		diags = append(diags, d...)
		if d.HasErrors() {
			return "", false
		}
		s, d := ffString(v, a.Expr.Range(), name)
		diags = append(diags, d...)
		return s, !d.HasErrors()
	}
	if c, ok := str("color"); ok {
		if c != "red" && c != "green" {
			diags = append(diags, ffErr(content.Attributes["color"].Expr.Range(), "Invalid wire color", `color must be "red" or "green"`))
		}
		w.Color = c
	}
	w.FromConnector, _ = str("from_connector")
	w.ToConnector, _ = str("to_connector")
	for _, e := range []struct {
		name string
		dst  *string
	}{{"from", &w.From}, {"to", &w.To}} {
		a, ok := content.Attributes[e.name]
		if !ok {
			continue
		}
		addr, d := ev.endpointAddress(a.Expr, ctx)
		diags = append(diags, d...)
		*e.dst = addr
	}
	return w, diags
}

// endpointAddress evaluates a wire endpoint. An endpoint is any value that
// carries the absolute address of a resource instance (or of one belt tile,
// "<belt>.path[i]"): a resource reference in this module, or one that crossed
// a module boundary as an output or an input.
func (ev *ffEvaluator) endpointAddress(expr hcl.Expression, ctx *hcl.EvalContext) (string, hcl.Diagnostics) {
	bad := func(detail string) (string, hcl.Diagnostics) {
		return "", hcl.Diagnostics{ffErr(expr.Range(), "Invalid wire endpoint", detail)}
	}
	v, d := ffEval(expr, ctx)
	if d.HasErrors() {
		return "", d
	}
	const hint = "wire endpoints must be a single resource such as resource.inserter.feed, or one exposed through a module output or input"
	if v.IsNull() || !v.Type().IsObjectType() || !v.Type().HasAttribute("address") {
		return bad(hint)
	}
	av := v.GetAttr("address")
	if av.IsNull() || av.Type() != cty.String {
		return bad(hint)
	}
	addr := av.AsString()
	if _, ok := ev.tree.instances[addr]; ok {
		return addr, nil
	}
	if m := ffBeltAddrRe.FindStringSubmatch(addr); m != nil {
		if in, ok := ev.tree.instances[m[1]]; ok {
			if in.Spec.Belt == nil {
				return bad(fmt.Sprintf("%s is not a belt resource; .path[i] only applies to belts", m[1]))
			}
			if n, err := strconv.Atoi(m[2]); err == nil && n >= 0 && n < len(in.Spec.Belt.Tiles) {
				return addr, nil
			}
			return bad(fmt.Sprintf("%s has %d tiles; %s is out of range", m[1], len(in.Spec.Belt.Tiles), addr))
		}
	}
	return bad(fmt.Sprintf("reference does not match a declared resource instance: %s", addr))
}
