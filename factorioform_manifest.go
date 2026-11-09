package main

// Factorioform manifest layer: HCL native syntax (*.ftf) parsed into Go types
// with source-ranged diagnostics. No game access and no prototype lookups;
// those belong to the validation ticket. Variable/locals/count/for_each
// evaluation is plugged in through the *hcl.EvalContext argument and the
// retained expressions on ffManifest.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

const ffDynamicUnavailable = "dynamic evaluation not available"

var (
	ffNameRe       = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	ffIdentRe      = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	ffDirectionVal = map[string]int{"north": 0, "east": 4, "south": 8, "west": 12}
)

// ffHeader is the factorioform { } block.
type ffHeader struct {
	Version int
	Name    string
	Surface string
	Force   string
	Range   hcl.Range
}

// ffVariable is a variable "<name>" { } block.
type ffVariable struct {
	Name        string
	Type        cty.Type // cty.DynamicPseudoType when no type was declared
	HasType     bool
	Default     cty.Value
	HasDefault  bool
	Description string
	Range       hcl.Range
}

// ffLocal is one locals { name = expr } attribute, kept unevaluated for the
// dynamic-evaluation ticket.
type ffLocal struct {
	Name  string
	Expr  hcl.Expression
	Range hcl.Range
}

// ffConfigBlock is a decoded nested config block (inserter, splitter,
// container, logistic, circuit, constant_combinator, combinator and their
// children). Attribute values stay raw cty values; typing is the validation
// ticket's job.
type ffConfigBlock struct {
	Type       string
	Range      hcl.Range
	Attrs      map[string]cty.Value
	AttrRanges map[string]hcl.Range
	Blocks     []*ffConfigBlock
}

// ffBeltTile is one tile of a belt path.
type ffBeltTile struct {
	Position  ffPoint
	Direction int
	Range     hcl.Range
}

// ffBelt is the belt { entity, path } block.
type ffBelt struct {
	Entity string
	Tiles  []ffBeltTile
	Range  hcl.Range
}

// ffResourceSpec is one decoded resource instance. Optional attributes are
// pointers/nil when omitted: omitted means "unmanaged", never "cleared".
type ffResourceSpec struct {
	Entity   string
	Position ffPoint
	// Direction is nil when the manifest omits it: unmanaged, like Quality and
	// Recipe, so the diff never rotates or replaces an entity over it.
	Direction       *int
	Quality         *string
	Recipe          *string
	Modules         []string // nil = unmanaged, empty non-nil = no modules
	UndergroundType *string
	Configs         map[string]*ffConfigBlock // keyed by block type
	Belt            *ffBelt
}

// ffResourceDecl is a resource "<kind>" "<name>" block. Count and ForEach are
// kept unevaluated; when both are nil the block was decoded once into Spec.
// The dynamic-evaluation ticket calls ffDecodeResource(decl, ctx) per
// instance with count.index / each.* in ctx.
type ffResourceDecl struct {
	Kind    string
	Name    string
	Range   hcl.Range
	Body    hcl.Body // body minus count/for_each
	Count   hcl.Expression
	ForEach hcl.Expression
	Spec    *ffResourceSpec
}

// ffManifestWire is a wire { } block. From/To are resource addresses such as
// "inserter.feed", "assembler.green[0]" or "belt.bus.path[2]".
type ffManifestWire struct {
	Color         string
	From          string
	FromConnector string
	To            string
	ToConnector   string
	Range         hcl.Range
}

// ffManifest is the parsed static manifest.
type ffManifest struct {
	Header    ffHeader
	Variables []*ffVariable
	Locals    []*ffLocal
	Resources []*ffResourceDecl
	Wires     []*ffManifestWire
}

// ---------------------------------------------------------------------------
// Config block schema. One table drives decoding, unknown-attribute errors and
// the snake_case path -> GraphQL camelCase mapping used by the plan builder.

type ffAttrSpec struct {
	Name     string // snake_case manifest name
	GraphQL  string // camelCase GraphQL input field
	Required bool
	// Type is the value type the attribute must have; cty.NilType means the
	// attribute is passed through unchecked (legacy, being retired).
	Type cty.Type
}

type ffBlockSpec struct {
	Name    string
	GraphQL string // GraphQL field when nested (e.g. sections)
	Repeat  bool   // nested block may appear more than once
	Attrs   []ffAttrSpec
	Blocks  []*ffBlockSpec
}

// ffAttrs builds attribute specs from GraphQL camelCase names; a trailing "!"
// marks the attribute required.
func ffAttrs(camel ...string) []ffAttrSpec {
	out := make([]ffAttrSpec, 0, len(camel))
	for _, c := range camel {
		req := strings.HasSuffix(c, "!")
		c = strings.TrimSuffix(c, "!")
		out = append(out, ffAttrSpec{Name: ffSnake(c), GraphQL: c, Required: req, Type: ffAttrType(c)})
	}
	return out
}

// ffObject types. Nested values are closed: an undeclared key is an error, so a
// typo surfaces at plan time with the .ftf range instead of at apply.
var (
	ffTypeSignal = cty.Object(map[string]cty.Type{
		"type": cty.String, "name": cty.String, "quality": cty.String,
	})
	ffTypeNetworks = cty.Object(map[string]cty.Type{
		"red": cty.Bool, "green": cty.Bool,
	})
	ffTypeCondition = cty.Object(map[string]cty.Type{
		"comparator": cty.String, "constant": cty.Number,
		"firstSignal": ffTypeSignal, "secondSignal": ffTypeSignal,
	})
	ffTypeColor = cty.Object(map[string]cty.Type{
		"r": cty.Number, "g": cty.Number, "b": cty.Number, "a": cty.Number,
	})
)

// ffAttrTypes maps GraphQL input field names to the value type the manifest
// must supply. A field absent from the table keeps cty.NilType (unchecked).
var ffAttrTypes = map[string]cty.Type{
	// Strings and numbers.
	"inventory": cty.String, "filter": cty.String, "filterMode": cty.String,
	"inputPriority": cty.String, "outputPriority": cty.String, "colorMode": cty.String,
	"group": cty.String, "name": cty.String, "quality": cty.String,
	"stackSizeOverride": cty.Number, "resourceReadMode": cty.Number, "readItemsMode": cty.Number,
	"handReadMode": cty.Number, "contentsReadMode": cty.Number, "siloReadMode": cty.Number,
	"exclusiveModeOfOperation": cty.Number, "index": cty.Number, "count": cty.Number,
	"min": cty.Number, "max": cty.Number, "multiplier": cty.Number, "allowedSlots": cty.Number,
	"constant": cty.Number, "firstConstant": cty.Number, "secondConstant": cty.Number,
	"operation": cty.String, "comparator": cty.String, "compareType": cty.String,

	// Booleans.
	"pickupFromLeftLane": cty.Bool, "pickupFromRightLane": cty.Bool, "enabled": cty.Bool,
	"active": cty.Bool, "circuitEnableDisable": cty.Bool, "readContents": cty.Bool,
	"setRecipe": cty.Bool, "readIngredients": cty.Bool, "readWorking": cty.Bool,
	"readResources": cty.Bool, "readAmmo": cty.Bool, "readLogistics": cty.Bool,
	"readRobotStats": cty.Bool, "setFilters": cty.Bool, "readHandContents": cty.Bool,
	"setStackSize": cty.Bool, "connectToLogisticNetwork": cty.Bool, "useColors": cty.Bool,
	"readRecipeFinished": cty.Bool, "includeInCrafting": cty.Bool, "includeFuel": cty.Bool,
	"setFilter": cty.Bool, "splitterInputSide": cty.Bool, "splitterOutputSide": cty.Bool,
	"sendToTrain": cty.Bool, "readFromTrain": cty.Bool, "readStoppedTrain": cty.Bool,
	"setTrainsLimit": cty.Bool, "readTrainsCount": cty.Bool, "setPriority": cty.Bool,
	"circuitConditionEnabled": cty.Bool, "copyCountFromInput": cty.Bool,

	// Nested objects, keyed on their GraphQL field names.
	"circuitCondition": ffTypeCondition, "logisticCondition": ffTypeCondition,
	"inputLeftCondition": ffTypeCondition, "inputRightCondition": ffTypeCondition,
	"outputLeftCondition": ffTypeCondition, "outputRightCondition": ffTypeCondition,
	"redSignal": ffTypeSignal, "greenSignal": ffTypeSignal, "blueSignal": ffTypeSignal,
	"rgbSignal": ffTypeSignal, "stackControlSignal": ffTypeSignal, "recipeFinishedSignal": ffTypeSignal,
	"workingSignal": ffTypeSignal, "stoppedTrainSignal": ffTypeSignal, "trainsCountSignal": ffTypeSignal,
	"trainsLimitSignal": ffTypeSignal, "prioritySignal": ffTypeSignal, "signal": ffTypeSignal,
	"firstSignal": ffTypeSignal, "secondSignal": ffTypeSignal, "outputSignal": ffTypeSignal,
	"firstSignalNetworks": ffTypeNetworks, "secondSignalNetworks": ffTypeNetworks, "networks": ffTypeNetworks,
	"pickupVector": cty.Object(map[string]cty.Type{"x": cty.Number, "y": cty.Number}),
	"dropVector":   cty.Object(map[string]cty.Type{"x": cty.Number, "y": cty.Number}),

	// Lists. inserter.filters is validated by prototype filter count in the
	// normalizer (accepting bare names or name@quality), so it stays a string
	// list here rather than an InserterFilterInput object.
	"filters": cty.List(cty.String),
}

// ffAttrType returns the declared type of a GraphQL field, cty.NilType when the
// field has no entry yet.
func ffAttrType(graphql string) cty.Type {
	if t, ok := ffAttrTypes[graphql]; ok {
		return t
	}
	return cty.NilType
}

// ffConvertAttr converts a manifest value to its declared type with strict,
// closed semantics: no cross-type coercion (a string is never a number or a
// bool) and an object with an undeclared key is an error, so a typo cannot
// silently vanish. Null satisfies any type.
func ffConvertAttr(v cty.Value, t cty.Type, r hcl.Range, what string) (cty.Value, hcl.Diagnostics) {
	if t == cty.NilType || v.IsNull() || !v.IsKnown() {
		return v, nil
	}
	switch {
	case t == cty.String:
		if v.Type() != cty.String {
			return cty.NilVal, hcl.Diagnostics{ffErr(r, "Invalid "+what, fmt.Sprintf("%s must be a string, got %s", what, v.Type().FriendlyName()))}
		}
		return v, nil
	case t == cty.Number:
		if v.Type() != cty.Number {
			return cty.NilVal, hcl.Diagnostics{ffErr(r, "Invalid "+what, fmt.Sprintf("%s must be a number, got %s", what, v.Type().FriendlyName()))}
		}
		return v, nil
	case t == cty.Bool:
		if v.Type() != cty.Bool {
			return cty.NilVal, hcl.Diagnostics{ffErr(r, "Invalid "+what, fmt.Sprintf("%s must be true or false, got %s", what, v.Type().FriendlyName()))}
		}
		return v, nil
	case t.IsObjectType():
		if !v.Type().IsObjectType() && !v.Type().IsMapType() {
			return cty.NilVal, hcl.Diagnostics{ffErr(r, "Invalid "+what, fmt.Sprintf("%s must be an object, got %s", what, v.Type().FriendlyName()))}
		}
		return ffConvertAttrObject(v, t, r, what)
	}
	return v, nil
}

// ffConvertAttrObject checks an object value against a closed object type and
// rewrites its keys from manifest snake_case to GraphQL camelCase, so the value
// compares equal to the live one and matches the write input.
func ffConvertAttrObject(v cty.Value, t cty.Type, r hcl.Range, what string) (cty.Value, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	in := v.AsValueMap()
	out := make(map[string]cty.Value, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := t.AttributeTypes()
	// Index the declared fields by their snake_case manifest spelling.
	bySnake := make(map[string]string, len(attrs))
	for name := range attrs {
		bySnake[ffSnake(name)] = name
	}
	for _, k := range keys {
		field, ok := bySnake[k]
		if !ok {
			if _, exact := attrs[k]; exact {
				field = k
			} else {
				known := make([]string, 0, len(bySnake))
				for n := range bySnake {
					known = append(known, n)
				}
				sort.Strings(known)
				diags = append(diags, ffErr(r, "Unsupported attribute", fmt.Sprintf("%s has no attribute %q (known: %s)", what, k, strings.Join(known, ", "))))
				continue
			}
		}
		cv, d := ffConvertAttr(in[k], attrs[field], r, what+"."+field)
		diags = append(diags, d...)
		if d.HasErrors() {
			continue
		}
		if !cv.IsNull() {
			out[field] = cv
		}
	}
	if diags.HasErrors() {
		return cty.NilVal, diags
	}
	return cty.ObjectVal(out), diags
}

// ffConfigSpecs are the resource-level config blocks, each allowed at most once.
var ffConfigSpecs = []*ffBlockSpec{
	{
		Name: "inserter",
		Attrs: append(ffAttrs("filters", "filterMode", "stackSizeOverride", "pickupFromLeftLane", "pickupFromRightLane"),
			// Offsets from the entity centre in map axes (GraphQL pickupVector/dropVector).
			ffAttrSpec{Name: "pickup_offset", GraphQL: "pickupVector", Type: ffAttrTypes["pickupVector"]},
			ffAttrSpec{Name: "drop_offset", GraphQL: "dropVector", Type: ffAttrTypes["dropVector"]}),
	},
	{
		Name:  "splitter",
		Attrs: ffAttrs("filter", "inputPriority", "outputPriority"),
	},
	{
		Name:  "container",
		Attrs: ffAttrs("inventory", "allowedSlots"),
	},
	{
		Name: "logistic",
		Blocks: []*ffBlockSpec{{
			Name: "section", GraphQL: "sections", Repeat: true,
			Blocks: []*ffBlockSpec{{
				Name: "slot", GraphQL: "slots", Repeat: true,
				Attrs: ffAttrs("index!", "name!", "quality", "min", "max"),
			}},
		}},
	},
	{
		Name: "circuit",
		Attrs: ffAttrs(
			"circuitEnableDisable", "circuitCondition", "logisticCondition", "readContents", "setRecipe",
			"readIngredients", "readWorking", "readResources", "resourceReadMode", "readAmmo", "readLogistics",
			"readRobotStats", "readItemsMode", "setFilters", "readHandContents", "handReadMode",
			"stackControlSignal", "setStackSize", "connectToLogisticNetwork", "useColors", "colorMode",
			"redSignal", "greenSignal", "blueSignal", "rgbSignal", "contentsReadMode", "readRecipeFinished",
			"recipeFinishedSignal", "workingSignal", "includeInCrafting", "includeFuel", "setFilter",
			"splitterInputSide", "splitterOutputSide", "inputLeftCondition", "inputRightCondition",
			"outputLeftCondition", "outputRightCondition", "sendToTrain", "readFromTrain", "readStoppedTrain",
			"setTrainsLimit", "readTrainsCount", "stoppedTrainSignal", "trainsCountSignal", "trainsLimitSignal",
			"setPriority", "prioritySignal", "siloReadMode", "circuitConditionEnabled", "exclusiveModeOfOperation",
		),
	},
	{
		Name:  "constant_combinator",
		Attrs: ffAttrs("enabled"),
		Blocks: []*ffBlockSpec{{
			Name: "section", GraphQL: "sections", Repeat: true,
			Attrs: ffAttrs("index", "active", "multiplier", "group"),
			Blocks: []*ffBlockSpec{{
				Name: "slot", GraphQL: "slots", Repeat: true,
				Attrs: ffAttrs("index!", "signal!", "count!"),
			}},
		}},
	},
	{
		Name: "combinator",
		Blocks: []*ffBlockSpec{
			{
				Name: "arithmetic", GraphQL: "arithmetic",
				Attrs: ffAttrs("firstSignal", "firstSignalNetworks", "firstConstant", "secondSignal",
					"secondSignalNetworks", "secondConstant", "operation!", "outputSignal!"),
			},
			{
				Name: "decider", GraphQL: "decider",
				Blocks: []*ffBlockSpec{
					{
						Name: "condition", GraphQL: "conditions", Repeat: true,
						Attrs: ffAttrs("firstSignal", "firstSignalNetworks", "secondSignal", "secondSignalNetworks",
							"constant", "comparator", "compareType"),
					},
					{
						Name: "output", GraphQL: "outputs", Repeat: true,
						Attrs: ffAttrs("signal!", "copyCountFromInput", "constant", "networks"),
					},
				},
			},
		},
	},
}

// ffResourceAttrGraphQL maps resource-level managed attributes to their
// GraphQL/lua field names.
var ffResourceAttrGraphQL = map[string]string{
	"recipe":           "recipe",
	"modules":          "modules",
	"underground_type": "undergroundType",
}

// ffGraphQLFields maps every config field path (HCL snake_case, dot-joined,
// e.g. "inserter.stack_size_override") to its GraphQL camelCase field
// ("stackSizeOverride"). Nested block paths map to the list field
// ("logistic.section" -> "sections").
var ffGraphQLFields = func() map[string]string {
	m := map[string]string{}
	for k, v := range ffResourceAttrGraphQL {
		m[k] = v
	}
	var walk func(prefix string, s *ffBlockSpec)
	walk = func(prefix string, s *ffBlockSpec) {
		path := prefix + s.Name
		if s.GraphQL != "" {
			m[path] = s.GraphQL
		}
		for _, a := range s.Attrs {
			m[path+"."+a.Name] = a.GraphQL
		}
		for _, b := range s.Blocks {
			walk(path+".", b)
		}
	}
	for _, s := range ffConfigSpecs {
		walk("", s)
	}
	return m
}()

// ffGraphQLField returns the GraphQL camelCase field for a manifest field path.
func ffGraphQLField(path string) (string, bool) {
	v, ok := ffGraphQLFields[path]
	return v, ok
}

// ---------------------------------------------------------------------------
// Evaluation helpers.

// ffStaticContext is the literal-only context used when no EvalContext is
// given: only the built-in direction constants resolve.
func ffStaticContext() *hcl.EvalContext {
	d := map[string]cty.Value{}
	for k, v := range ffDirectionVal {
		d[k] = cty.NumberIntVal(int64(v))
	}
	return &hcl.EvalContext{Variables: map[string]cty.Value{"direction": cty.ObjectVal(d)}}
}

// ffCheckStatic rejects everything but literals and direction.* when no
// evaluation context is available.
func ffCheckStatic(expr hcl.Expression) hcl.Diagnostics {
	node, ok := expr.(hclsyntax.Node)
	if !ok {
		return nil
	}
	var diags hcl.Diagnostics
	bad := func(r hcl.Range, what string) {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError, Summary: ffDynamicUnavailable,
			Detail: what + " requires dynamic evaluation, which is not available for this call.", Subject: r.Ptr(),
		})
	}
	hclsyntax.VisitAll(node, func(n hclsyntax.Node) hcl.Diagnostics {
		switch e := n.(type) {
		case *hclsyntax.ScopeTraversalExpr:
			if e.Traversal.RootName() != "direction" {
				bad(e.Range(), "reference to "+e.Traversal.RootName())
			}
		case *hclsyntax.FunctionCallExpr:
			bad(e.Range(), "function call "+e.Name)
		case *hclsyntax.ForExpr:
			bad(e.Range(), "for expression")
		}
		return nil
	})
	return diags
}

func ffEval(expr hcl.Expression, ctx *hcl.EvalContext) (cty.Value, hcl.Diagnostics) {
	if ctx == nil {
		if d := ffCheckStatic(expr); d.HasErrors() {
			return cty.DynamicVal, d
		}
		ctx = ffStaticContext()
	}
	v, d := expr.Value(ctx)
	if !d.HasErrors() && !v.IsWhollyKnown() {
		d = append(d, ffErr(expr.Range(), "Unknown value", "the value must be fully known"))
	}
	return v, d
}

func ffErr(r hcl.Range, summary, detail string) *hcl.Diagnostic {
	return &hcl.Diagnostic{Severity: hcl.DiagError, Summary: summary, Detail: detail, Subject: r.Ptr()}
}

func ffString(v cty.Value, r hcl.Range, what string) (string, hcl.Diagnostics) {
	if v.IsNull() {
		return "", hcl.Diagnostics{ffErr(r, "Invalid "+what, what+" must not be null")}
	}
	c, err := convert.Convert(v, cty.String)
	if err != nil {
		return "", hcl.Diagnostics{ffErr(r, "Invalid "+what, what+" must be a string")}
	}
	return c.AsString(), nil
}

func ffFloat(v cty.Value, r hcl.Range, what string) (float64, hcl.Diagnostics) {
	if v.IsNull() {
		return 0, hcl.Diagnostics{ffErr(r, "Invalid "+what, what+" must not be null")}
	}
	c, err := convert.Convert(v, cty.Number)
	if err != nil {
		return 0, hcl.Diagnostics{ffErr(r, "Invalid "+what, what+" must be a number")}
	}
	f, _ := c.AsBigFloat().Float64()
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, hcl.Diagnostics{ffErr(r, "Invalid "+what, what+" must be finite")}
	}
	return f, nil
}

func ffDirection(v cty.Value, r hcl.Range) (int, hcl.Diagnostics) {
	f, d := ffFloat(v, r, "direction")
	if d.HasErrors() {
		return 0, d
	}
	if f != math.Floor(f) || f < 0 || f > 15 {
		return 0, hcl.Diagnostics{ffErr(r, "Invalid direction", "direction must be an integer 0-15 (0 north, 4 east, 8 south, 12 west)")}
	}
	return int(f), nil
}

// ffObjectFields returns the attribute map of an object/map value.
func ffObjectFields(v cty.Value, r hcl.Range, what string) (map[string]cty.Value, hcl.Diagnostics) {
	if v.IsNull() || !(v.Type().IsObjectType() || v.Type().IsMapType()) {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid "+what, what+" must be an object")}
	}
	return v.AsValueMap(), nil
}

func ffPointOf(v cty.Value, r hcl.Range, what string, allowed ...string) (ffPoint, map[string]cty.Value, hcl.Diagnostics) {
	m, d := ffObjectFields(v, r, what)
	if d.HasErrors() {
		return ffPoint{}, nil, d
	}
	allow := map[string]bool{"x": true, "y": true}
	for _, a := range allowed {
		allow[a] = true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allow[k] {
			d = append(d, ffErr(r, "Unsupported attribute", fmt.Sprintf("%s has no attribute %q", what, k)))
		}
	}
	var p ffPoint
	for _, c := range []struct {
		k string
		p *float64
	}{{"x", &p.X}, {"y", &p.Y}} {
		val, ok := m[c.k]
		if !ok {
			d = append(d, ffErr(r, "Missing attribute", fmt.Sprintf("%s requires %q", what, c.k)))
			continue
		}
		f, fd := ffFloat(val, r, what+"."+c.k)
		d = append(d, fd...)
		*c.p = f
	}
	return p, m, d
}

func ffStringList(v cty.Value, r hcl.Range, what string) ([]string, hcl.Diagnostics) {
	if v.IsNull() || !(v.Type().IsListType() || v.Type().IsTupleType() || v.Type().IsSetType()) {
		return nil, hcl.Diagnostics{ffErr(r, "Invalid "+what, what+" must be a list of strings")}
	}
	out := []string{}
	var diags hcl.Diagnostics
	for it := v.ElementIterator(); it.Next(); {
		_, e := it.Element()
		s, d := ffString(e, r, what+" element")
		diags = append(diags, d...)
		out = append(out, s)
	}
	return out, diags
}

// ---------------------------------------------------------------------------
// Parsing.

// ParseFactorioform parses and merges *.ftf files (Terraform-style merge) and
// decodes every structural element. ctx nil means literals only (plus the
// direction.* constants): any other reference, function call or for
// expression yields "dynamic evaluation not available". Resources that use
// count or for_each are returned undecoded (Spec == nil) with their
// unevaluated expressions for the dynamic-evaluation ticket; with ctx nil they
// additionally report the dynamic-evaluation diagnostic.
func ParseFactorioform(files map[string][]byte, ctx *hcl.EvalContext) (*ffManifest, hcl.Diagnostics) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var (
		diags  hcl.Diagnostics
		parsed []*hcl.File
	)
	for _, n := range names {
		f, d := hclsyntax.ParseConfig(files[n], n, hcl.InitialPos)
		diags = append(diags, d...)
		if f != nil {
			parsed = append(parsed, f)
		}
	}
	if diags.HasErrors() {
		return nil, diags
	}
	if len(parsed) == 0 {
		return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "No manifest files", Detail: "at least one .ftf file is required"}}
	}
	body := hcl.MergeFiles(parsed)
	content, d := body.Content(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "factorioform"},
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "locals"},
		{Type: "resource", LabelNames: []string{"kind", "name"}},
		{Type: "wire"},
	}})
	diags = append(diags, d...)

	m := &ffManifest{}
	var headers []*hcl.Block
	seenVar := map[string]hcl.Range{}
	seenLocal := map[string]hcl.Range{}
	seenRes := map[string]hcl.Range{}
	for _, blk := range content.Blocks {
		switch blk.Type {
		case "factorioform":
			headers = append(headers, blk)
		case "variable":
			if !hclsyntax.ValidIdentifier(blk.Labels[0]) {
				diags = append(diags, ffErr(blk.LabelRanges[0], "Invalid variable name", "variable names must be HCL identifiers"))
				continue
			}
			if prev, dup := seenVar[blk.Labels[0]]; dup {
				diags = append(diags, ffErr(blk.DefRange, "Duplicate variable", fmt.Sprintf("variable %q already declared at %s", blk.Labels[0], prev)))
				continue
			}
			seenVar[blk.Labels[0]] = blk.DefRange
			vd, d := ffDecodeVariable(blk, ctx)
			diags = append(diags, d...)
			if vd != nil {
				v := vd.ffVariable
				m.Variables = append(m.Variables, &v)
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
				if prev, dup := seenLocal[k]; dup {
					diags = append(diags, ffErr(a.NameRange, "Duplicate local", fmt.Sprintf("local %q already declared at %s", k, prev)))
					continue
				}
				seenLocal[k] = a.NameRange
				m.Locals = append(m.Locals, &ffLocal{Name: k, Expr: a.Expr, Range: a.NameRange})
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
			if prev, dup := seenRes[key]; dup {
				diags = append(diags, ffErr(blk.DefRange, "Duplicate resource", fmt.Sprintf("resource %s already declared at %s", key, prev)))
				continue
			}
			seenRes[key] = blk.DefRange
			r, d := decodeFFResourceDecl(blk, ctx)
			diags = append(diags, d...)
			if r != nil {
				m.Resources = append(m.Resources, r)
			}
		case "wire":
			w, d := decodeFFWire(blk, ctx)
			diags = append(diags, d...)
			if w != nil {
				m.Wires = append(m.Wires, w)
			}
		}
	}

	switch {
	case len(headers) == 0:
		r := hcl.Range{Filename: names[0], Start: hcl.InitialPos, End: hcl.InitialPos}
		diags = append(diags, ffErr(r, "Missing factorioform block", "exactly one factorioform block is required"))
	case len(headers) > 1:
		diags = append(diags, ffErr(headers[1].DefRange, "Duplicate factorioform block", "exactly one factorioform block is allowed"))
	default:
		h, d := decodeFFHeader(headers[0], ctx)
		diags = append(diags, d...)
		if h != nil {
			m.Header = *h
		}
	}

	// Wire endpoints must reference declared resource blocks.
	for _, w := range m.Wires {
		for _, addr := range []string{w.From, w.To} {
			base := addr
			if i := strings.IndexAny(base, "[."); i >= 0 {
				// kind.name prefix is the first two dot segments before any index.
				parts := strings.SplitN(addr, ".", 3)
				base = parts[0] + "." + strings.SplitN(parts[1], "[", 2)[0]
			}
			if _, ok := seenRes[base]; !ok {
				diags = append(diags, ffErr(w.Range, "Unknown wire endpoint", fmt.Sprintf("wire references undeclared resource %s", base)))
			}
		}
	}

	if diags.HasErrors() {
		return nil, diags
	}
	return m, diags
}

func decodeFFHeader(blk *hcl.Block, ctx *hcl.EvalContext) (*ffHeader, hcl.Diagnostics) {
	content, diags := blk.Body.Content(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "version", Required: true}, {Name: "name", Required: true}, {Name: "surface"}, {Name: "force"},
	}})
	h := &ffHeader{Surface: "nauvis", Force: "player", Range: blk.DefRange}
	for _, name := range []string{"version", "name", "surface", "force"} {
		attr, ok := content.Attributes[name]
		if !ok {
			continue
		}
		v, d := ffEval(attr.Expr, ctx)
		diags = append(diags, d...)
		if d.HasErrors() {
			continue
		}
		r := attr.Expr.Range()
		if name == "version" {
			f, d := ffFloat(v, r, "version")
			diags = append(diags, d...)
			if !d.HasErrors() && f != 1 {
				diags = append(diags, ffErr(r, "Unsupported version", "factorioform version must be 1"))
			}
			h.Version = int(f)
			continue
		}
		s, d := ffString(v, r, name)
		diags = append(diags, d...)
		if d.HasErrors() {
			continue
		}
		switch name {
		case "name":
			if !ffNameRe.MatchString(s) {
				diags = append(diags, ffErr(r, "Invalid manifest name", "name must be 1-64 characters of [a-z0-9_-]"))
			}
			h.Name = s
		case "surface":
			if s == "" {
				diags = append(diags, ffErr(r, "Invalid surface", "surface must not be empty"))
			}
			h.Surface = s
		case "force":
			if s == "" {
				diags = append(diags, ffErr(r, "Invalid force", "force must not be empty"))
			}
			h.Force = s
		}
	}
	return h, diags
}

func decodeFFResourceDecl(blk *hcl.Block, ctx *hcl.EvalContext) (*ffResourceDecl, hcl.Diagnostics) {
	meta, rest, diags := blk.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "count"}, {Name: "for_each"},
	}})
	decl := &ffResourceDecl{Kind: blk.Labels[0], Name: blk.Labels[1], Range: blk.DefRange, Body: rest}
	c, hasCount := meta.Attributes["count"]
	f, hasEach := meta.Attributes["for_each"]
	if hasCount {
		decl.Count = c.Expr
	}
	if hasEach {
		decl.ForEach = f.Expr
	}
	if hasCount && hasEach {
		diags = append(diags, ffErr(f.NameRange, "Conflicting arguments", "count and for_each cannot both be set"))
	}
	if hasCount || hasEach {
		if ctx == nil {
			expr := decl.Count
			if hasEach {
				expr = decl.ForEach
			}
			diags = append(diags, ffErr(expr.Range(), ffDynamicUnavailable, "count and for_each require dynamic evaluation, which is not available for this call."))
		}
		return decl, diags
	}
	spec, d := ffDecodeResource(decl, ctx)
	diags = append(diags, d...)
	decl.Spec = spec
	return decl, diags
}

var ffResourceAttrSchema = []hcl.AttributeSchema{
	{Name: "entity"}, {Name: "position"}, {Name: "direction"}, {Name: "quality"},
	{Name: "recipe"}, {Name: "modules"}, {Name: "underground_type"},
}

func ffResourceBlockSchema() []hcl.BlockHeaderSchema {
	out := make([]hcl.BlockHeaderSchema, 0, len(ffConfigSpecs)+1)
	for _, s := range ffConfigSpecs {
		out = append(out, hcl.BlockHeaderSchema{Type: s.Name})
	}
	return append(out, hcl.BlockHeaderSchema{Type: "belt"})
}

// ffDecodeResource decodes one resource instance from decl.Body. The dynamic
// evaluation ticket calls it per count/for_each instance with count.index or
// each.* present in ctx.
func ffDecodeResource(decl *ffResourceDecl, ctx *hcl.EvalContext) (*ffResourceSpec, hcl.Diagnostics) {
	content, diags := decl.Body.Content(&hcl.BodySchema{Attributes: ffResourceAttrSchema, Blocks: ffResourceBlockSchema()})
	spec := &ffResourceSpec{Configs: map[string]*ffConfigBlock{}}

	var beltBlk *hcl.Block
	seen := map[string]*hcl.Block{}
	specByName := map[string]*ffBlockSpec{}
	for _, s := range ffConfigSpecs {
		specByName[s.Name] = s
	}
	for _, blk := range content.Blocks {
		if prev, dup := seen[blk.Type]; dup {
			diags = append(diags, ffErr(blk.DefRange, "Duplicate configuration block", fmt.Sprintf("block %q already declared at %s; each config block may appear at most once", blk.Type, prev.DefRange)))
			continue
		}
		seen[blk.Type] = blk
		if blk.Type == "belt" {
			beltBlk = blk
			continue
		}
		cfg, d := decodeFFConfigBlock(blk, specByName[blk.Type], ctx)
		diags = append(diags, d...)
		if cfg != nil {
			spec.Configs[blk.Type] = cfg
		}
	}

	attr := func(name string) (cty.Value, *hcl.Attribute, bool) {
		a, ok := content.Attributes[name]
		if !ok {
			return cty.NilVal, nil, false
		}
		v, d := ffEval(a.Expr, ctx)
		diags = append(diags, d...)
		if d.HasErrors() {
			return cty.NilVal, a, false
		}
		return v, a, true
	}

	_, hasEntity := content.Attributes["entity"]
	switch {
	case hasEntity && beltBlk != nil:
		diags = append(diags, ffErr(beltBlk.DefRange, "Conflicting arguments", "exactly one of the entity attribute or the belt block is allowed"))
	case !hasEntity && beltBlk == nil:
		diags = append(diags, ffErr(decl.Range, "Missing entity", "resource requires exactly one of the entity attribute or a belt block"))
	}

	if beltBlk != nil {
		for name, a := range content.Attributes {
			diags = append(diags, ffErr(a.NameRange, "Unsupported argument", fmt.Sprintf("attribute %q is not allowed on a belt resource; configure tiles inside the belt block", name)))
		}
		for _, blk := range content.Blocks {
			if blk.Type != "belt" {
				diags = append(diags, ffErr(blk.DefRange, "Unsupported block type", fmt.Sprintf("block %q is not allowed on a belt resource", blk.Type)))
			}
		}
		b, d := decodeFFBelt(beltBlk, ctx)
		diags = append(diags, d...)
		spec.Belt = b
		return spec, diags
	}

	if v, a, ok := attr("entity"); ok {
		s, d := ffString(v, a.Expr.Range(), "entity")
		diags = append(diags, d...)
		if !d.HasErrors() && s == "" {
			diags = append(diags, ffErr(a.Expr.Range(), "Invalid entity", "entity must not be empty"))
		}
		spec.Entity = s
	}
	if v, a, ok := attr("position"); ok {
		p, _, d := ffPointOf(v, a.Expr.Range(), "position")
		diags = append(diags, d...)
		spec.Position = p
	} else if _, present := content.Attributes["position"]; !present {
		diags = append(diags, ffErr(decl.Range, "Missing position", "entity resources require position (entity center, like place/placeMany)"))
	}
	if v, a, ok := attr("direction"); ok {
		n, d := ffDirection(v, a.Expr.Range())
		diags = append(diags, d...)
		if !d.HasErrors() {
			spec.Direction = &n
		}
	}
	strAttr := func(name string) *string {
		v, a, ok := attr(name)
		if !ok {
			return nil
		}
		s, d := ffString(v, a.Expr.Range(), name)
		diags = append(diags, d...)
		if d.HasErrors() {
			return nil
		}
		return &s
	}
	spec.Quality = strAttr("quality")
	spec.Recipe = strAttr("recipe")
	spec.UndergroundType = strAttr("underground_type")
	if spec.UndergroundType != nil && *spec.UndergroundType != "input" && *spec.UndergroundType != "output" {
		diags = append(diags, ffErr(content.Attributes["underground_type"].Expr.Range(), "Invalid underground_type", `underground_type must be "input" or "output"`))
	}
	if v, a, ok := attr("modules"); ok {
		mods, d := ffStringList(v, a.Expr.Range(), "modules")
		diags = append(diags, d...)
		if !d.HasErrors() {
			spec.Modules = mods
		}
	}
	return spec, diags
}

func decodeFFConfigBlock(blk *hcl.Block, spec *ffBlockSpec, ctx *hcl.EvalContext) (*ffConfigBlock, hcl.Diagnostics) {
	schema := &hcl.BodySchema{}
	for _, a := range spec.Attrs {
		schema.Attributes = append(schema.Attributes, hcl.AttributeSchema{Name: a.Name})
	}
	nested := map[string]*ffBlockSpec{}
	for _, b := range spec.Blocks {
		schema.Blocks = append(schema.Blocks, hcl.BlockHeaderSchema{Type: b.Name})
		nested[b.Name] = b
	}
	content, diags := blk.Body.Content(schema)
	cfg := &ffConfigBlock{
		Type: blk.Type, Range: blk.DefRange,
		Attrs: map[string]cty.Value{}, AttrRanges: map[string]hcl.Range{},
	}
	for _, as := range spec.Attrs {
		a, ok := content.Attributes[as.Name]
		if !ok {
			if as.Required {
				diags = append(diags, ffErr(blk.DefRange, "Missing required argument", fmt.Sprintf("block %q requires attribute %q", blk.Type, as.Name)))
			}
			continue
		}
		v, d := ffEval(a.Expr, ctx)
		diags = append(diags, d...)
		if d.HasErrors() {
			continue
		}
		if as.Type != cty.NilType {
			cv, cd := ffConvertAttr(v, as.Type, a.Expr.Range(), blk.Type+"."+as.Name)
			diags = append(diags, cd...)
			if cd.HasErrors() {
				continue
			}
			v = cv
		}
		cfg.Attrs[as.Name] = v
		cfg.AttrRanges[as.Name] = a.Expr.Range()
	}
	seen := map[string]*hcl.Block{}
	for _, nb := range content.Blocks {
		ns := nested[nb.Type]
		if prev, dup := seen[nb.Type]; dup && !ns.Repeat {
			diags = append(diags, ffErr(nb.DefRange, "Duplicate configuration block", fmt.Sprintf("block %q already declared at %s", nb.Type, prev.DefRange)))
			continue
		}
		seen[nb.Type] = nb
		child, d := decodeFFConfigBlock(nb, ns, ctx)
		diags = append(diags, d...)
		if child != nil {
			cfg.Blocks = append(cfg.Blocks, child)
		}
	}
	return cfg, diags
}

func decodeFFBelt(blk *hcl.Block, ctx *hcl.EvalContext) (*ffBelt, hcl.Diagnostics) {
	content, diags := blk.Body.Content(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "entity", Required: true}, {Name: "path", Required: true},
	}})
	b := &ffBelt{Range: blk.DefRange}
	if a, ok := content.Attributes["entity"]; ok {
		v, d := ffEval(a.Expr, ctx)
		diags = append(diags, d...)
		if !d.HasErrors() {
			s, d := ffString(v, a.Expr.Range(), "entity")
			diags = append(diags, d...)
			if !d.HasErrors() && s == "" {
				diags = append(diags, ffErr(a.Expr.Range(), "Invalid entity", "entity must not be empty"))
			}
			b.Entity = s
		}
	}
	a, ok := content.Attributes["path"]
	if !ok {
		return b, diags
	}
	v, d := ffEval(a.Expr, ctx)
	diags = append(diags, d...)
	if d.HasErrors() {
		return b, diags
	}
	r := a.Expr.Range()
	if v.IsNull() || !(v.Type().IsTupleType() || v.Type().IsListType()) {
		return b, append(diags, ffErr(r, "Invalid path", "belt path must be a list of {x, y, direction} tiles"))
	}
	if v.LengthInt() == 0 {
		return b, append(diags, ffErr(r, "Invalid path", "belt path must contain at least one tile"))
	}
	i := 0
	for it := v.ElementIterator(); it.Next(); i++ {
		_, e := it.Element()
		what := fmt.Sprintf("path[%d]", i)
		p, m, d := ffPointOf(e, r, what, "direction")
		diags = append(diags, d...)
		if d.HasErrors() {
			continue
		}
		for _, c := range []struct {
			n string
			f float64
		}{{"x", p.X}, {"y", p.Y}} {
			if c.f-math.Floor(c.f) != 0.5 {
				diags = append(diags, ffErr(r, "Invalid belt tile", fmt.Sprintf("%s.%s=%v must be on a .5 tile center", what, c.n, c.f)))
			}
		}
		dv, ok := m["direction"]
		if !ok {
			diags = append(diags, ffErr(r, "Missing attribute", what+" requires \"direction\""))
			continue
		}
		dir, d := ffDirection(dv, r)
		diags = append(diags, d...)
		if d.HasErrors() {
			continue
		}
		if n := len(b.Tiles); n > 0 {
			prev := b.Tiles[n-1].Position
			if math.Abs(p.X-prev.X)+math.Abs(p.Y-prev.Y) != 1 {
				diags = append(diags, ffErr(r, "Non-contiguous belt path", fmt.Sprintf("%s (%v,%v) is not adjacent to the previous tile (%v,%v)", what, p.X, p.Y, prev.X, prev.Y)))
			}
		}
		b.Tiles = append(b.Tiles, ffBeltTile{Position: p, Direction: dir, Range: r})
	}
	return b, diags
}

func decodeFFWire(blk *hcl.Block, ctx *hcl.EvalContext) (*ffManifestWire, hcl.Diagnostics) {
	content, diags := blk.Body.Content(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "color", Required: true}, {Name: "from", Required: true}, {Name: "to", Required: true},
		{Name: "from_connector"}, {Name: "to_connector"},
	}})
	w := &ffManifestWire{Range: blk.DefRange}
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
		addr, d := ffResourceAddress(a.Expr)
		diags = append(diags, d...)
		*e.dst = addr
	}
	return w, diags
}

// ffResourceAddress converts a resource.<kind>.<name>[key][.path[i]] traversal
// into an address string.
func ffResourceAddress(expr hcl.Expression) (string, hcl.Diagnostics) {
	trav, d := hcl.AbsTraversalForExpr(expr)
	if d.HasErrors() || trav.RootName() != "resource" || len(trav) < 3 {
		return "", hcl.Diagnostics{ffErr(expr.Range(), "Invalid wire endpoint", "wire endpoints must be resource references such as resource.inserter.feed")}
	}
	var b strings.Builder
	for i, step := range trav[1:] {
		switch s := step.(type) {
		case hcl.TraverseAttr:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s.Name)
		case hcl.TraverseIndex:
			if b.Len() == 0 {
				return "", hcl.Diagnostics{ffErr(expr.Range(), "Invalid wire endpoint", "wire endpoints must be resource references such as resource.inserter.feed")}
			}
			switch s.Key.Type() {
			case cty.String:
				fmt.Fprintf(&b, "[%q]", s.Key.AsString())
			case cty.Number:
				f, _ := s.Key.AsBigFloat().Float64()
				fmt.Fprintf(&b, "[%d]", int(f))
			default:
				return "", hcl.Diagnostics{ffErr(expr.Range(), "Invalid wire endpoint", "index must be a string or number")}
			}
		default:
			return "", hcl.Diagnostics{ffErr(expr.Range(), "Invalid wire endpoint", "unsupported traversal step")}
		}
	}
	addr := b.String()
	if strings.Count(strings.SplitN(addr, "[", 2)[0], ".") < 1 {
		return "", hcl.Diagnostics{ffErr(expr.Range(), "Invalid wire endpoint", "wire endpoints must be resource references such as resource.inserter.feed")}
	}
	return addr, nil
}
