package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/zclconf/go-cty/cty"
)

// Factorioform plan: files + variables -> eval -> validate -> live read ->
// state get -> diff (ffRunPipeline, shared with factorioformRefresh) and the
// serializer from diff rows to ordered, executable GraphQL steps (ffBuildPlan).

const ffPlanFormatVersion = 1

// ---------------------------------------------------------------------------
// Shared pipeline.

// ffPipelineInput is what every Factorioform operation starts from. Vars holds
// typed values (GraphQL variables via ffVarsFromJSON, ffParseVarFile) and
// RawVars holds CLI `--var k=v` strings, exactly as ffEvalOptions.
type ffPipelineInput struct {
	Files   map[string][]byte
	RootDir string
	Vars    map[string]cty.Value
	RawVars map[string]string
}

// ffPipelineResult is everything downstream steps (plan, refresh) need.
type ffPipelineResult struct {
	Expanded   *ffExpanded
	Normalized *ffNormalized
	// State is the manifest's recorded state, nil when the manifest has none.
	State        *FactorioformManifestState
	StateEntries []ffStateEntry
	Live         *ffLiveSnapshot
	Diff         *ffDiffResult
}

// ffRunPipeline evaluates the manifest files, validates them against engine
// prototypes, reads the live area the manifest covers plus the entities in
// state, reads the manifest's state and diffs the three. It never writes.
//
// The returned diagnostics are manifest problems (parse, evaluation and
// validation errors with file:line:col); err is an engine or transport failure.
// Exactly one of the two is non-empty on failure.
func ffRunPipeline(ctx context.Context, r *Resolver, in ffPipelineInput) (*ffPipelineResult, hcl.Diagnostics, error) {
	if len(in.Files) == 0 {
		return nil, nil, errors.New("at least one manifest file is required")
	}
	exp, diags := EvalFactorioform(in.Files, ffEvalOptions{Vars: in.Vars, RawVars: in.RawVars, RootDir: in.RootDir})
	if diags.HasErrors() {
		return nil, diags, nil
	}
	norm, diags, err := ffValidate(ctx, r, exp.Resources, exp.Wires)
	if err != nil {
		return nil, nil, err
	}
	if diags.HasErrors() {
		return nil, diags, nil
	}
	res := &ffPipelineResult{Expanded: exp, Normalized: norm}
	name := exp.Header.Name
	state, err := ffStateGet(r, name)
	if err != nil {
		return nil, nil, err
	}
	if state != nil && (state.Surface != exp.Header.Surface || state.Force != exp.Header.Force) {
		return nil, nil, fmt.Errorf("manifest %q state is bound to surface %q force %q but the manifest declares surface %q force %q; factorioformForget it first",
			name, state.Surface, state.Force, exp.Header.Surface, exp.Header.Force)
	}
	res.State = state
	var units []int64
	if state != nil {
		for _, s := range state.Resources {
			u, err := strconv.ParseUint(s.UnitNumber, 10, 64)
			if err != nil || u == 0 {
				return nil, nil, fmt.Errorf("state address %s has invalid unit number %q", s.Address, s.UnitNumber)
			}
			res.StateEntries = append(res.StateEntries, ffStateEntry{
				Address: s.Address, Unit: u, Entity: s.Entity, X: s.Position.X, Y: s.Position.Y,
				Direction: s.Direction, Quality: s.Quality,
			})
			units = append(units, int64(u))
		}
	}
	sort.Slice(res.StateEntries, func(i, j int) bool { return res.StateEntries[i].Address < res.StateEntries[j].Address })
	sort.Slice(units, func(i, j int) bool { return units[i] < units[j] })

	live, err := ffReadLive(ctx, r, ffLiveQuery{
		Surface: exp.Header.Surface, Force: exp.Header.Force,
		Area: ffManifestArea(norm), StateUnits: units,
		ContainerInventories: ffContainerInventories(norm),
	})
	if err != nil {
		return nil, nil, err
	}
	res.Live = live
	res.Diff = ffDiff(ffDiffInput{Desired: norm, State: res.StateEntries, Live: live.Entities})
	return res, nil, nil
}

// ffContainerInventories returns the sorted, distinct inventory keys whose
// container bar the manifest manages (container.<inv>.bar).
func ffContainerInventories(n *ffNormalized) []string {
	if n == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, r := range n.Resources {
		for k := range r.Config {
			if rest, ok := strings.CutPrefix(k, "container."); ok {
				if inv, found := strings.CutSuffix(rest, ".bar"); found {
					seen[inv] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ffManifestArea is the union of desired footprints plus a 2 tile margin, empty
// when the manifest has no resources.
func ffManifestArea(n *ffNormalized) ffBox {
	if n == nil || len(n.Resources) == 0 {
		return ffBox{}
	}
	box := n.Resources[0].Footprint
	for _, r := range n.Resources[1:] {
		box.X1 = math.Min(box.X1, r.Footprint.X1)
		box.Y1 = math.Min(box.Y1, r.Footprint.Y1)
		box.X2 = math.Max(box.X2, r.Footprint.X2)
		box.Y2 = math.Max(box.Y2, r.Footprint.Y2)
	}
	const margin = 2
	return ffBox{X1: math.Floor(box.X1) - margin, Y1: math.Floor(box.Y1) - margin, X2: math.Ceil(box.X2) + margin, Y2: math.Ceil(box.Y2) + margin}
}

// ---------------------------------------------------------------------------
// Diagnostics.

// ffDiagLines renders error diagnostics as "file:line:col: summary: detail".
func ffDiagLines(diags hcl.Diagnostics) []string {
	var out []string
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		line := d.Summary
		if d.Detail != "" {
			line += ": " + d.Detail
		}
		if d.Subject != nil {
			line = fmt.Sprintf("%s:%d:%d: %s", d.Subject.Filename, d.Subject.Start.Line, d.Subject.Start.Column, line)
		}
		out = append(out, line)
	}
	return out
}

// ffDiagError is the GraphQL error for manifest diagnostics: the message lists
// every diagnostic and extensions.diagnostics carries them structured.
func ffDiagError(diags hcl.Diagnostics) error {
	var structured []map[string]any
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		e := map[string]any{"summary": d.Summary, "detail": d.Detail}
		if d.Subject != nil {
			e["file"], e["line"], e["column"] = d.Subject.Filename, d.Subject.Start.Line, d.Subject.Start.Column
		}
		structured = append(structured, e)
	}
	return &gqlerror.Error{
		Message:    "factorioform manifest has errors:\n" + strings.Join(ffDiagLines(diags), "\n"),
		Extensions: map[string]any{"code": "MANIFEST_DIAGNOSTICS", "diagnostics": structured},
	}
}

// ---------------------------------------------------------------------------
// Entry points.

// ffPlanRequest is the typed input of ffPlan.
type ffPlanRequest struct {
	Files   map[string][]byte
	RootDir string
	Vars    map[string]cty.Value
	RawVars map[string]string
	Actor   *ActorInput
}

// ffPlanRequestFromInput converts the GraphQL input.
func ffPlanRequestFromInput(in FactorioformPlanInput) (ffPlanRequest, error) {
	req := ffPlanRequest{Files: map[string][]byte{}, Actor: in.Actor}
	if in.RootDir != nil {
		req.RootDir = *in.RootDir
	}
	for _, f := range in.Files {
		if f == nil || f.Name == "" {
			return req, errors.New("every file needs a name")
		}
		if _, dup := req.Files[f.Name]; dup {
			return req, fmt.Errorf("duplicate file name %q", f.Name)
		}
		req.Files[f.Name] = []byte(f.Content)
	}
	if len(req.Files) == 0 {
		return req, errors.New("files must not be empty")
	}
	if in.Variables != nil {
		m, ok := in.Variables.(map[string]any)
		if !ok {
			return req, errors.New("variables must be a JSON object")
		}
		vars, err := ffVarsFromJSON(m)
		if err != nil {
			return req, err
		}
		req.Vars = vars
	}
	if a := in.Actor; a != nil {
		if (a.Player == nil) == (a.Character == nil) {
			return req, errors.New("actor must name exactly one of player or character")
		}
	}
	return req, nil
}

// ffPlan runs the pipeline and serializes the plan. diags are manifest errors.
func ffPlan(ctx context.Context, r *Resolver, req ffPlanRequest) (*FactorioformPlan, hcl.Diagnostics, error) {
	res, diags, err := ffRunPipeline(ctx, r, ffPipelineInput{Files: req.Files, Vars: req.Vars, RawVars: req.RawVars, RootDir: req.RootDir})
	if err != nil || diags.HasErrors() {
		return nil, diags, err
	}
	plan, err := ffBuildPlan(res, req.Actor)
	if err == nil {
		err = ffAttachRawMaterials(ctx, r, res.Expanded.Header.Force, plan)
	}
	if err != nil {
		return nil, nil, err
	}
	return plan, nil, nil
}

func factorioformPlan(ctx context.Context, r *Resolver, in FactorioformPlanInput) (*FactorioformPlan, error) {
	req, err := ffPlanRequestFromInput(in)
	if err != nil {
		return nil, err
	}
	plan, diags, err := ffPlan(ctx, r, req)
	if err != nil {
		return nil, err
	}
	if diags.HasErrors() {
		return nil, ffDiagError(diags)
	}
	return plan, nil
}

// ---------------------------------------------------------------------------
// Plan builder.

func ffStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ffUnitStr(u int64) *string {
	if u == 0 {
		return nil
	}
	s := strconv.FormatInt(u, 10)
	return &s
}

// ffBuildPlan serializes a pipeline result. actor may be nil.
func ffBuildPlan(res *ffPipelineResult, actor *ActorInput) (*FactorioformPlan, error) {
	h := res.Expanded.Header
	d := res.Diff
	p := &ffPlanner{
		surface: h.Surface, force: h.Force,
		res: map[string]*ffResource{}, rows: map[string]*ffDiffRow{},
		materials: map[[2]string]int{},
	}
	if actor != nil {
		p.actor = map[string]any{}
		if actor.Player != nil {
			p.actor["player"] = *actor.Player
		} else if actor.Character != nil {
			p.actor["character"] = *actor.Character
		}
	}
	for i := range res.Normalized.Resources {
		r := &res.Normalized.Resources[i]
		p.res[r.Address] = r
	}
	for i := range d.Rows {
		p.rows[d.Rows[i].Address] = &d.Rows[i]
	}
	if err := p.build(d); err != nil {
		return nil, err
	}

	plan := &FactorioformPlan{
		FormatVersion: ffPlanFormatVersion, Manifest: h.Name, Tick: res.Live.Tick,
		Surface: h.Surface, Force: h.Force, Status: d.Status,
		Summary: &FactorioformPlanSummary{
			Create: d.Summary.Create, Update: d.Summary.Update, Replace: d.Summary.Replace, Delete: d.Summary.Delete,
			Drift: d.Summary.Drift, Adopt: d.Summary.Adopt, Conflict: d.Summary.Conflict, Pending: d.Summary.Pending, Noop: d.Summary.Noop,
		},
		Diffs: []*FactorioformPlanDiff{}, Steps: p.steps, Materials: []*FactorioformPlanMaterial{}, RawMaterials: []*FactorioformPlanRawMaterial{},
	}
	if plan.Steps == nil {
		plan.Steps = []*FactorioformPlanStep{}
	}
	for i := range d.Rows {
		plan.Diffs = append(plan.Diffs, ffPlanDiff(&d.Rows[i]))
	}
	keys := make([][2]string, 0, len(p.materials))
	for k := range p.materials {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		plan.Materials = append(plan.Materials, &FactorioformPlanMaterial{Name: k[0], Quality: k[1], Count: p.materials[k]})
	}
	plan.PlanID = ffPlanID(res, d)
	return plan, nil
}

func ffPlanDiff(r *ffDiffRow) *FactorioformPlanDiff {
	out := &FactorioformPlanDiff{
		Action: r.Action, Address: r.Address, Entity: ffStr(r.Entity), Position: &Position{X: r.Position.X, Y: r.Position.Y},
		Direction: r.Direction, Quality: ffStr(r.Quality), UnitNumber: ffUnitStr(r.UnitNumber),
		Changes: []*FactorioformPlanChange{}, Reason: ffStr(r.Reason), Blockers: []*FactorioformPlanBlocker{},
	}
	for _, c := range r.Changes {
		out.Changes = append(out.Changes, &FactorioformPlanChange{Path: c.Path, Before: c.Before, After: c.After})
	}
	for _, b := range r.Blockers {
		out.Blockers = append(out.Blockers, &FactorioformPlanBlocker{
			UnitNumber: strconv.FormatInt(b.UnitNumber, 10), Entity: b.Entity, Position: &Position{X: b.Position.X, Y: b.Position.Y},
		})
	}
	if w := r.Wire; w != nil {
		out.Wire = &FactorioformPlanWire{
			Color: w.Color, FromAddress: w.FromAddress, FromUnit: ffUnitStr(w.FromUnit), FromConnector: w.FromConnector,
			ToAddress: w.ToAddress, ToUnit: ffUnitStr(w.ToUnit), ToConnector: w.ToConnector,
		}
	}
	return out
}

// ffPlanID hashes the normalized desired state, the recorded state and the
// diff rows (which are the live snapshot as far as the manifest is concerned).
// The live tick is excluded on purpose: an idle world must hash identically
// between two plans, while any change that alters a row changes the id.
func ffPlanID(res *ffPipelineResult, d *ffDiffResult) string {
	type desired struct {
		Address   string
		Entity    string
		Position  ffPoint
		Direction *int
		Quality   string
		Footprint ffBox
		Config    map[string]any
	}
	doc := struct {
		Manifest, Surface, Force string
		Resources                []desired
		Wires                    []ffDesiredWire
		State                    []ffStateEntry
		Rows                     []ffDiffRow
	}{Manifest: res.Expanded.Header.Name, Surface: res.Expanded.Header.Surface, Force: res.Expanded.Header.Force,
		Wires: res.Normalized.Wires, State: res.StateEntries, Rows: d.Rows}
	for _, r := range res.Normalized.Resources {
		doc.Resources = append(doc.Resources, desired{r.Address, r.Entity, r.Position, r.Direction, r.Quality, r.Footprint, r.Config})
	}
	raw, _ := json.Marshal(doc)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Steps.

type ffPlanner struct {
	surface, force string
	actor          map[string]any // nil: steps require an actor
	res            map[string]*ffResource
	rows           map[string]*ffDiffRow
	steps          []*FactorioformPlanStep
	materials      map[[2]string]int
}

// Selection sets: enough to see success, count and the per-entry failures.
const (
	ffSelBatchErrors = `errors { index code message }`
	ffSelPlaceMany   = `placed failed ` + ffSelBatchErrors + ` entities { index name unitNumber position { x y } direction }`
	ffSelPlaceBelts  = `possible stopReason placed skipped failed conflicts { position { x y } kind blockers terrain } ` + ffSelBatchErrors
	ffSelPlace       = `name unitNumber position { x y } direction`
	ffSelRemoveMany  = `removed failed ` + ffSelBatchErrors + ` results { index name unitNumber }`
	ffSelSettings    = `name unitNumber position { x y }`
	ffSelAction      = `name unitNumber`
	ffSelWire        = `connected color`
)

var ffOperationInputs = map[string]string{
	"placeMany": "PlaceManyInput", "placeBelts": "BeltRunInput", "place": "PlaceInput", "removeMany": "RemoveManyInput",
	"setRecipe": "SetRecipeInput", "transfer": "TransferInput", "setInserterSettings": "InserterSettingsInput",
	"setSplitterSettings": "SplitterSettingsInput", "setContainerBar": "ContainerBarInput",
	"setLogisticRequests": "LogisticRequestsInput", "setCircuitBehavior": "CircuitBehaviorInput",
	"setConstantCombinator": "SetConstantCombinatorInput", "setCombinatorParameters": "SetCombinatorParametersInput",
	"setDirection": "SetDirectionInput", "connectWire": "WireInput", "disconnectWire": "WireInput",
}

// add appends one step; input must not contain the actor.
func (p *ffPlanner) add(kind, op, selection string, addrs []string, input map[string]any) {
	if p.actor != nil {
		input["actor"] = p.actor
	}
	name := strings.ToUpper(op[:1]) + op[1:]
	doc := fmt.Sprintf("mutation Factorioform%s($input: %s!) {\n  %s(input: $input) {\n    %s\n  }\n}", name, ffOperationInputs[op], op, selection)
	p.steps = append(p.steps, &FactorioformPlanStep{
		Order: len(p.steps) + 1, Kind: kind, Addresses: addrs, Operation: op, Document: doc,
		Variables: map[string]any{"input": input}, RequiresActor: p.actor == nil,
	})
}

func ffPos(pt ffPoint) map[string]any { return map[string]any{"x": pt.X, "y": pt.Y} }

// ref addresses an entity by unit number when known, else by name and center.
func (p *ffPlanner) ref(unit int64, entity string, pos ffPoint) map[string]any {
	if unit != 0 {
		return map[string]any{"surface": p.surface, "unitNumber": strconv.FormatInt(unit, 10)}
	}
	return map[string]any{"surface": p.surface, "entity": entity, "position": ffPos(pos)}
}

func (p *ffPlanner) need(item, quality string, n int) {
	if item == "" || n <= 0 {
		return
	}
	if quality == "" {
		quality = "normal"
	}
	p.materials[[2]string{item, quality}] += n
}

var ffBeltAddrRe = regexp.MustCompile(`^(.*)\.path\[(\d+)\]$`)

var ffDirVector = map[int][2]float64{0: {0, -1}, 4: {1, 0}, 8: {0, 1}, 12: {-1, 0}}

func (p *ffPlanner) build(d *ffDiffResult) error {
	// 1. Disconnect wires.
	for _, row := range d.Rows {
		if row.Wire != nil && row.Action == ffActionDelete {
			p.wireStep("disconnect-wire", "disconnectWire", row)
		}
	}
	// 2. Deletes (state entries that left the manifest).
	var del, rep []*ffDiffRow
	for i := range d.Rows {
		row := &d.Rows[i]
		switch {
		case row.Wire != nil:
		case row.Action == ffActionDelete:
			del = append(del, row)
		case row.Action == ffActionReplace:
			rep = append(rep, row)
		}
	}
	p.removeStep("delete", del)
	// 3. Replace removals.
	p.removeStep("replace-remove", rep)

	// 4. Creates: CREATE, DRIFT and the re-creation half of REPLACE.
	var creates []*ffDiffRow
	for i := range d.Rows {
		row := &d.Rows[i]
		if row.Wire != nil {
			continue
		}
		if row.Action == ffActionCreate || row.Action == ffActionDrift || row.Action == ffActionReplace {
			if p.res[row.Address] == nil {
				return fmt.Errorf("diff row %s has no desired resource", row.Address)
			}
			creates = append(creates, row)
		}
	}
	p.createSteps(creates)

	// 5. Per-entity configuration.
	for i := range d.Rows {
		row := &d.Rows[i]
		if row.Wire != nil {
			continue
		}
		switch row.Action {
		case ffActionCreate, ffActionDrift, ffActionReplace, ffActionUpdate:
			p.configSteps(row, p.res[row.Address])
		}
	}

	// 6. Connect wires.
	for _, row := range d.Rows {
		if row.Wire != nil && row.Action == ffActionCreate {
			p.wireStep("connect-wire", "connectWire", row)
		}
	}
	return nil
}

func (p *ffPlanner) removeStep(kind string, rows []*ffDiffRow) {
	if len(rows) == 0 {
		return
	}
	var addrs []string
	var targets []any
	for _, row := range rows {
		addrs = append(addrs, row.Address)
		targets = append(targets, p.ref(row.UnitNumber, row.Entity, row.Position))
	}
	p.add(kind, "removeMany", ffSelRemoveMany, addrs, map[string]any{"targets": targets, "mode": "MINE"})
}

type ffPlacementGroup struct {
	item, quality string
	addrs         []string
	placements    []any
}

func (p *ffPlanner) createSteps(rows []*ffDiffRow) {
	groups := map[[2]string]*ffPlacementGroup{}
	var undergrounds []*ffDiffRow
	type beltTile struct {
		row    *ffDiffRow
		res    *ffResource
		prefix string
		index  int
	}
	var belts []beltTile
	for _, row := range rows {
		r := p.res[row.Address]
		quality := r.Quality
		if quality == "" {
			quality = "normal"
		}
		p.need(r.PlaceItem, quality, 1)
		switch r.Type {
		case "transport-belt":
			prefix, idx := row.Address, 0
			if m := ffBeltAddrRe.FindStringSubmatch(row.Address); m != nil {
				prefix = m[1]
				idx, _ = strconv.Atoi(m[2])
			}
			belts = append(belts, beltTile{row, r, prefix, idx})
		case "underground-belt":
			undergrounds = append(undergrounds, row)
		default:
			k := [2]string{r.PlaceItem, quality}
			g := groups[k]
			if g == nil {
				g = &ffPlacementGroup{item: k[0], quality: k[1]}
				groups[k] = g
			}
			g.addrs = append(g.addrs, row.Address)
			pl := map[string]any{"position": ffPos(r.Position)}
			// Omitted direction leaves the engine default; PlacementInput.direction
			// is a non-null Int defaulting to 0, so send nothing rather than null.
			if r.Direction != nil {
				pl["direction"] = *r.Direction
			}
			g.placements = append(g.placements, pl)
		}
	}
	keys := make([][2]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		g := groups[k]
		p.add("create", "placeMany", ffSelPlaceMany, g.addrs, map[string]any{
			"surface": p.surface, "item": g.item, "quality": g.quality, "placements": g.placements,
		})
	}
	for _, row := range undergrounds {
		r := p.res[row.Address]
		quality := r.Quality
		if quality == "" {
			quality = "normal"
		}
		in := map[string]any{
			"surface": p.surface, "position": ffPos(r.Position), "item": r.PlaceItem, "quality": quality,
		}
		// PlaceInput.direction is nullable; omit it when the manifest does not say.
		if r.Direction != nil {
			in["direction"] = *r.Direction
		}
		half := "INPUT"
		if v, ok := r.Config["underground_type"].(string); ok && strings.EqualFold(v, "output") {
			half = "OUTPUT"
		}
		in["undergroundType"] = half
		p.add("create-underground", "place", ffSelPlace, []string{row.Address}, in)
	}

	sort.SliceStable(belts, func(i, j int) bool {
		if belts[i].prefix != belts[j].prefix {
			return belts[i].prefix < belts[j].prefix
		}
		return belts[i].index < belts[j].index
	})
	for i := 0; i < len(belts); {
		start := belts[i]
		j := i + 1
		startDir := 0
		if start.res.Direction != nil {
			startDir = *start.res.Direction
		}
		vec, cardinal := ffDirVector[startDir]
		for cardinal && j < len(belts) {
			prev, cur := belts[j-1], belts[j]
			curDir := 0
			if cur.res.Direction != nil {
				curDir = *cur.res.Direction
			}
			if cur.prefix != start.prefix || cur.index != prev.index+1 || curDir != startDir ||
				cur.res.PlaceItem != start.res.PlaceItem ||
				math.Abs(cur.res.Position.X-(prev.res.Position.X+vec[0])) > ffEpsilon ||
				math.Abs(cur.res.Position.Y-(prev.res.Position.Y+vec[1])) > ffEpsilon {
				break
			}
			j++
		}
		last := belts[j-1]
		var addrs []string
		for _, t := range belts[i:j] {
			addrs = append(addrs, t.row.Address)
		}
		in := map[string]any{
			"surface": p.surface, "force": p.force, "item": start.res.PlaceItem, "routing": "DIRECT",
			// The manifest dictates the layout, so neighbouring belt runs of the same
			// plan (corners, side-loads) are intended, not accidents to refuse.
			"allowBeltAdjacency": true,
			"from":               ffPos(start.res.Position), "to": ffPos(last.res.Position),
		}
		if j-i == 1 {
			in["direction"] = startDir
		}
		p.add("create-belts", "placeBelts", ffSelPlaceBelts, addrs, in)
		i = j
	}
}

// ffPathSet is the config field paths to write for a row and their desired and
// previous values.
type ffPathSet struct {
	paths  []string
	before map[string]any
}

func (s *ffPathSet) has(prefix string) bool {
	for _, p := range s.paths {
		if p == prefix || strings.HasPrefix(p, prefix+".") {
			return true
		}
	}
	return false
}

func (p *ffPlanner) configSteps(row *ffDiffRow, r *ffResource) {
	created := row.Action != ffActionUpdate
	set := &ffPathSet{before: map[string]any{}}
	directionChanged := false
	if created {
		for k := range r.Config {
			if k != "underground_type" {
				set.paths = append(set.paths, k)
			}
		}
		sort.Strings(set.paths)
	} else {
		for _, c := range row.Changes {
			if c.Path == "direction" {
				directionChanged = true
				continue
			}
			set.paths = append(set.paths, c.Path)
			set.before[c.Path] = c.Before
		}
	}
	if directionChanged {
		// A rotation rotates the engine's pickup/drop points with the entity, so
		// the map-frame offsets change; write the desired ones after setDirection.
		for _, path := range []string{"inserter.pickup_offset", "inserter.drop_offset"} {
			if _, ok := r.Config[path]; ok && !slices.Contains(set.paths, path) {
				set.paths = append(set.paths, path)
			}
		}
	}
	addrs := []string{row.Address}
	var unit int64
	if !created {
		unit = row.UnitNumber
	}
	target := func() map[string]any { return p.ref(unit, r.Entity, r.Position) }
	val := func(path string) any { return r.Config[path] }

	if directionChanged && r.Direction != nil {
		p.add("config", "setDirection", ffSelAction, addrs, map[string]any{"target": target(), "direction": *r.Direction})
	}
	if set.has("recipe") {
		in := map[string]any{"target": target(), "recipe": val("recipe")}
		if !created {
			in["drain"] = "TO_ACTOR"
		}
		p.add("config", "setRecipe", ffSelAction, addrs, in)
	}
	if set.has("modules") {
		want := ffModuleCounts(val("modules"))
		have := map[string]int{}
		if !created {
			have = ffModuleCounts(set.before["modules"])
		}
		var extra, missing []string
		for k, n := range have {
			if n > want[k] {
				extra = append(extra, k)
			}
		}
		for k, n := range want {
			if n > have[k] {
				missing = append(missing, k)
			}
		}
		sort.Strings(extra)
		sort.Strings(missing)
		move := func(dir, key string, n int) {
			name, quality, _ := strings.Cut(key, ":")
			p.add("config", "transfer", `name unitNumber count inventory`, addrs, map[string]any{
				"target": target(), "inventory": "MODULES", "direction": dir, "item": name, "quality": quality, "count": n,
			})
		}
		for _, k := range extra {
			move("TO_ACTOR", k, have[k]-want[k])
		}
		for _, k := range missing {
			n := want[k] - have[k]
			move("TO_ENTITY", k, n)
			name, quality, _ := strings.Cut(k, ":")
			p.need(name, quality, n)
		}
	}
	if set.has("inserter") {
		in := map[string]any{"target": target()}
		for _, path := range set.paths {
			if !strings.HasPrefix(path, "inserter.") {
				continue
			}
			if path == "inserter.filters" {
				in["filters"] = ffInserterFilters(val(path))
			} else {
				in[ffConfigField(path)] = val(path)
			}
		}
		p.add("config", "setInserterSettings", ffSelSettings, addrs, in)
	}
	if set.has("splitter") {
		p.splitterSteps(set, r, target, addrs, created, val)
	}
	if inv, _, ok := ffContainerBarPath(set.paths); ok {
		in := map[string]any{"target": target(), "allowedSlots": val("container." + inv + ".bar")}
		if inv != "" {
			in["inventory"] = inv
		}
		p.add("config", "setContainerBar", ffSelSettings, addrs, in)
	}
	if set.has("logistic") {
		p.add("config", "setLogisticRequests", ffSelSettings, addrs, map[string]any{"target": target(), "sections": val("logistic.sections")})
	}
	if set.has("circuit") {
		in := map[string]any{"target": target()}
		for _, path := range set.paths {
			if strings.HasPrefix(path, "circuit.") {
				in[ffConfigField(path)] = val(path)
			}
		}
		p.add("config", "setCircuitBehavior", ffSelSettings, addrs, in)
	}
	if set.has("constant_combinator") {
		in := map[string]any{"target": target()}
		for _, path := range set.paths {
			if strings.HasPrefix(path, "constant_combinator.") {
				in[ffConfigField(path)] = val(path)
			}
		}
		p.add("config", "setConstantCombinator", ffSelSettings, addrs, in)
	}
	if set.has("combinator") {
		params := map[string]any{}
		for _, kind := range []string{"arithmetic", "decider"} {
			if v, ok := r.Config["combinator."+kind]; ok && set.has("combinator."+kind) {
				params[kind] = v
			}
		}
		p.add("config", "setCombinatorParameters", ffSelAction, addrs, map[string]any{"target": target(), "parameters": params})
	}
}

func (p *ffPlanner) splitterSteps(set *ffPathSet, r *ffResource, target func() map[string]any, addrs []string, created bool, val func(string) any) {
	filter, _ := val("splitter.filter").(string)
	if set.has("splitter.filter") && filter == "" && !created {
		// Clearing a filter also resets both priorities to none, so every desired
		// priority is re-applied afterwards, changed or not.
		p.add("config", "setSplitterSettings", ffSelSettings, addrs, map[string]any{"target": target(), "clear": true})
		in := map[string]any{"target": target()}
		for _, path := range []string{"splitter.input_priority", "splitter.output_priority"} {
			if v, ok := r.Config[path].(string); ok && v != "none" {
				in[ffConfigField(path)] = v
			}
		}
		if len(in) > 1 {
			p.add("config", "setSplitterSettings", ffSelSettings, addrs, in)
		}
		return
	}
	in := map[string]any{"target": target()}
	for _, path := range set.paths {
		switch path {
		case "splitter.filter":
			if filter != "" {
				in["filter"] = ffItemFilter(filter)
			}
		case "splitter.input_priority", "splitter.output_priority":
			in[ffConfigField(path)] = val(path)
		}
	}
	if len(in) > 1 {
		p.add("config", "setSplitterSettings", ffSelSettings, addrs, in)
	}
}

// ffContainerBarPath returns the inventory and bar path of the managed
// container bar, if the path set contains one. Keys are container.<inv>.bar,
// so a manifest that leaves inventory out reads the chest bar.
func ffContainerBarPath(paths []string) (string, string, bool) {
	for _, p := range paths {
		if rest, ok := strings.CutPrefix(p, "container."); ok {
			inv, found := strings.CutSuffix(rest, ".bar")
			if found {
				return inv, p, true
			}
		}
	}
	return "", "", false
}

// ffConfigField is the GraphQL input field of a config path. Attribute paths
// map through the schema table; nested block paths (constant_combinator.sections)
// already carry the GraphQL name as their last element.
func ffConfigField(path string) string {
	if f, ok := ffGraphQLField(path); ok {
		return f
	}
	return path[strings.LastIndex(path, ".")+1:]
}

// ffItemFilter converts "name" or "name@quality" to ItemFilterInput.
func ffItemFilter(s string) map[string]any {
	name, quality := ffSplitQuality(s)
	out := map[string]any{"name": name}
	if quality != "normal" {
		out["quality"] = quality
	}
	return out
}

// ffInserterFilters converts ordered slot names ("" = empty) to one-based
// InserterFilterInput entries; slots not listed are cleared by the write.
func ffInserterFilters(v any) []any {
	out := []any{}
	for i, e := range ffSlice(v) {
		s, _ := e.(string)
		if s == "" {
			continue
		}
		f := ffItemFilter(s)
		f["index"] = i + 1
		out = append(out, f)
	}
	return out
}

func (p *ffPlanner) wireEndpoint(addr string, unit int64, connector string, row ffDiffRow) map[string]any {
	ep := map[string]any{}
	if connector != "" {
		ep["connector"] = connector
	}
	r := p.res[addr]
	if other := p.rows[addr]; unit != 0 && (other == nil || other.Action == ffActionNoop || other.Action == ffActionUpdate || other.Action == ffActionAdopt || other.Action == ffActionDelete) || r == nil {
		entity := ""
		if r != nil {
			entity = r.Entity
		}
		ep["target"] = p.ref(unit, entity, ffPoint{})
		return ep
	}
	ep["target"] = p.ref(0, r.Entity, r.Position)
	return ep
}

func (p *ffPlanner) wireStep(kind, op string, row ffDiffRow) {
	w := row.Wire
	in := map[string]any{
		"from":  p.wireEndpoint(w.FromAddress, w.FromUnit, w.FromConnector, row),
		"to":    p.wireEndpoint(w.ToAddress, w.ToUnit, w.ToConnector, row),
		"color": strings.ToUpper(w.Color),
	}
	p.add(kind, op, ffSelWire, []string{row.Address}, in)
}
