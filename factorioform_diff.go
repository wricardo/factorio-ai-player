package main

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Diff actions (the epic's action set).
const (
	ffActionNoop     = "NOOP"
	ffActionCreate   = "CREATE"
	ffActionUpdate   = "UPDATE"
	ffActionReplace  = "REPLACE"
	ffActionDelete   = "DELETE"
	ffActionDrift    = "DRIFT"
	ffActionAdopt    = "ADOPT"
	ffActionConflict = "CONFLICT"
	ffActionPending  = "PENDING"
)

// Overall plan status.
const (
	ffStatusConverged = "CONVERGED"
	ffStatusChanges   = "CHANGES"
	ffStatusDrift     = "DRIFT_DETECTED"
	ffStatusConflict  = "CONFLICT"
)

const ffEpsilon = 1e-6

// ffDiffChange is one config field-path change (or structural attribute for
// REPLACE rows: entity, quality, position, direction, underground_type).
type ffDiffChange struct {
	Path   string `json:"path"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// ffDiffBlocker names a live entity that blocks a CONFLICT row.
type ffDiffBlocker struct {
	UnitNumber int64   `json:"unitNumber"`
	Entity     string  `json:"entity"`
	Position   ffPoint `json:"position"`
}

// ffDiffWireRef identifies the endpoints of a wire row. Units are 0 when the
// endpoint is not (yet) a live entity.
type ffDiffWireRef struct {
	Color         string `json:"color"`
	FromAddress   string `json:"fromAddress"`
	FromUnit      int64  `json:"fromUnit"`
	FromConnector string `json:"fromConnector"`
	ToAddress     string `json:"toAddress"`
	ToUnit        int64  `json:"toUnit"`
	ToConnector   string `json:"toConnector"`
}

// ffDiffRow is one reconciliation row. Resource rows carry the desired
// entity/position/direction/quality (state values for DELETE). Wire rows have
// Wire set and the address "wire:<from>-><to>:<color>".
type ffDiffRow struct {
	Action     string          `json:"action"`
	Address    string          `json:"address"`
	Entity     string          `json:"entity,omitempty"`
	Position   ffPoint         `json:"position"`
	Direction  int             `json:"direction"`
	Quality    string          `json:"quality,omitempty"`
	UnitNumber int64           `json:"unitNumber,omitempty"`
	Changes    []ffDiffChange  `json:"changes,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Blockers   []ffDiffBlocker `json:"blockers,omitempty"`
	Wire       *ffDiffWireRef  `json:"wire,omitempty"`
}

// ffDiffSummary counts rows per action. A DRIFT row is counted in both Drift
// and Create because the drifted resource is recreated.
type ffDiffSummary struct {
	Create   int `json:"create"`
	Update   int `json:"update"`
	Replace  int `json:"replace"`
	Delete   int `json:"delete"`
	Drift    int `json:"drift"`
	Adopt    int `json:"adopt"`
	Conflict int `json:"conflict"`
	Pending  int `json:"pending"`
	Noop     int `json:"noop"`
}

// ffPrunedState is a state entry for a removed address whose live entity is
// already gone; refresh drops it without a plan row.
type ffPrunedState struct {
	Address string `json:"address"`
	Unit    int64  `json:"unit"`
	Reason  string `json:"reason"`
}

// ffDiffResult is the output of ffDiff, deterministically ordered.
type ffDiffResult struct {
	Status  string          `json:"status"`
	Summary ffDiffSummary   `json:"summary"`
	Rows    []ffDiffRow     `json:"diffs"`
	Pruned  []ffPrunedState `json:"pruned,omitempty"`
}

// ffDiffInput feeds ffDiff. Rotatable reports whether an entity (name, type)
// can be turned in place with setDirection; nil uses ffDefaultRotatable.
type ffDiffInput struct {
	Desired   *ffNormalized
	State     []ffStateEntry
	Live      []*ffLive
	Rotatable func(entity, typ string) bool
}

var ffRotatableTypes = map[string]bool{
	"inserter": true, "transport-belt": true, "underground-belt": true, "splitter": true,
	"loader": true, "loader-1x1": true, "lane-splitter": true, "linked-belt": true,
	"mining-drill": true, "offshore-pump": true, "pump": true, "burner-inserter": true,
	"rail-signal": true, "rail-chain-signal": true,
}

// ffDefaultRotatable lists prototype types that setDirection can turn in place.
func ffDefaultRotatable(_, typ string) bool { return ffRotatableTypes[typ] }

// ffLiveEntity is the prototype name of a live record, resolving ghosts.
func ffLiveEntity(l *ffLive) string {
	if l.GhostName != "" {
		return l.GhostName
	}
	return l.Entity
}

func ffPointEq(a, b ffPoint) bool {
	return math.Abs(a.X-b.X) <= ffEpsilon && math.Abs(a.Y-b.Y) <= ffEpsilon
}

func ffDirEq(a, b int) bool { return ((a%16)+16)%16 == ((b%16)+16)%16 }

func ffLiveQuality(l *ffLive) string {
	if l.Quality == "" {
		return "normal"
	}
	return l.Quality
}

// ffDiff reconciles desired resources/wires, recorded state and live entities.
func ffDiff(in ffDiffInput) *ffDiffResult {
	rotatable := in.Rotatable
	if rotatable == nil {
		rotatable = ffDefaultRotatable
	}
	var desired ffNormalized
	if in.Desired != nil {
		desired = *in.Desired
	}
	res := append([]ffResource(nil), desired.Resources...)
	sort.Slice(res, func(i, j int) bool { return res[i].Address < res[j].Address })

	live := append([]*ffLive(nil), in.Live...)
	sort.SliceStable(live, func(i, j int) bool {
		a, b := live[i], live[j]
		if a.UnitNumber != b.UnitNumber {
			return a.UnitNumber < b.UnitNumber
		}
		if a.Position.X != b.Position.X {
			return a.Position.X < b.Position.X
		}
		if a.Position.Y != b.Position.Y {
			return a.Position.Y < b.Position.Y
		}
		return ffLiveEntity(live[i]) < ffLiveEntity(live[j])
	})
	byUnit := map[int64]*ffLive{}
	for _, l := range live {
		if l.UnitNumber != 0 && l.GhostName == "" {
			byUnit[l.UnitNumber] = l
		}
	}
	state := map[string]ffStateEntry{}
	stateUnits := map[int64]string{}
	for _, s := range in.State {
		state[s.Address] = s
		stateUnits[int64(s.Unit)] = s.Address
	}
	wanted := map[string]bool{}
	for _, r := range res {
		wanted[r.Address] = true
	}

	out := &ffDiffResult{}
	rows := map[string]*ffDiffRow{} // resource address -> row
	unitOf := map[string]int64{}    // address -> live unit when the entity exists now
	claimed := map[int64]bool{}     // live units owned by a resource row
	var order []string

	add := func(row ffDiffRow) *ffDiffRow {
		r := row
		rows[r.Address] = &r
		order = append(order, r.Address)
		return &r
	}
	base := func(r ffResource, action string) ffDiffRow {
		return ffDiffRow{Action: action, Address: r.Address, Entity: r.Entity, Position: r.Position, Direction: r.Direction, Quality: r.Quality}
	}

	// Pass 1: resources recorded in state.
	var unmatched []ffResource
	for _, r := range res {
		s, ok := state[r.Address]
		if !ok {
			unmatched = append(unmatched, r)
			continue
		}
		l := byUnit[int64(s.Unit)]
		if l == nil {
			row := base(r, ffActionDrift)
			row.UnitNumber = int64(s.Unit)
			row.Reason = fmt.Sprintf("state unit %d is missing from the live surface; will be recreated", s.Unit)
			add(row)
			continue
		}
		claimed[l.UnitNumber] = true
		unitOf[r.Address] = l.UnitNumber
		row := base(r, ffActionNoop)
		row.UnitNumber = l.UnitNumber
		if l.ToBeDeconstructed || l.ToBeUpgraded {
			row.Action = ffActionPending
			row.Reason = "live entity is marked for " + map[bool]string{true: "deconstruction", false: "upgrade"}[l.ToBeDeconstructed]
			add(row)
			continue
		}
		var structural []ffDiffChange
		if l.Entity != r.Entity {
			structural = append(structural, ffDiffChange{"entity", l.Entity, r.Entity})
		}
		if r.Quality != "" && ffLiveQuality(l) != r.Quality {
			structural = append(structural, ffDiffChange{"quality", ffLiveQuality(l), r.Quality})
		}
		if !ffPointEq(l.Position, r.Position) {
			structural = append(structural, ffDiffChange{"position", l.Position, r.Position})
		}
		if want, ok := r.Config["underground_type"]; ok {
			if got := l.Config["underground_type"]; !ffConfigEqual("underground_type", want, got) {
				structural = append(structural, ffDiffChange{"underground_type", got, want})
			}
		}
		dirDiff := !ffDirEq(l.Direction, r.Direction)
		if dirDiff && !rotatable(r.Entity, r.Type) {
			structural = append(structural, ffDiffChange{"direction", l.Direction, r.Direction})
		}
		if len(structural) > 0 {
			row.Action = ffActionReplace
			row.Changes = structural
			row.Reason = "structural change cannot be applied in place"
			add(row)
			continue
		}
		var changes []ffDiffChange
		if dirDiff {
			changes = append(changes, ffDiffChange{"direction", l.Direction, r.Direction})
		}
		keys := make([]string, 0, len(r.Config))
		for k := range r.Config {
			if k != "underground_type" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !ffConfigEqual(k, r.Config[k], l.Config[k]) {
				changes = append(changes, ffDiffChange{k, l.Config[k], r.Config[k]})
			}
		}
		if len(changes) > 0 {
			row.Action = ffActionUpdate
			row.Changes = changes
		}
		add(row)
	}

	// Pass 2: resources without state: adopt, pending ghost, conflict, create.
	for _, r := range unmatched {
		var adopt, ghost *ffLive
		for _, l := range live {
			if l.UnitNumber != 0 && claimed[l.UnitNumber] {
				continue
			}
			if _, owned := stateUnits[l.UnitNumber]; owned && l.UnitNumber != 0 {
				continue
			}
			if ffLiveEntity(l) != r.Entity || !ffPointEq(l.Position, r.Position) {
				continue
			}
			if l.GhostName != "" {
				if ghost == nil {
					ghost = l
				}
				continue
			}
			if adopt != nil || l.ToBeDeconstructed || l.ToBeUpgraded {
				continue
			}
			if rotatable(r.Entity, r.Type) && !ffDirEq(l.Direction, r.Direction) {
				continue
			}
			if r.Quality != "" && ffLiveQuality(l) != r.Quality {
				continue
			}
			adopt = l
		}
		switch {
		case adopt != nil:
			claimed[adopt.UnitNumber] = true
			unitOf[r.Address] = adopt.UnitNumber
			row := base(r, ffActionAdopt)
			row.UnitNumber = adopt.UnitNumber
			row.Reason = "unmanaged live entity matches entity, position and direction"
			add(row)
			continue
		case ghost != nil:
			row := base(r, ffActionPending)
			row.Reason = "construction ghost of the desired entity is waiting to be built"
			add(row)
			continue
		}
		var blockers []ffDiffBlocker
		for _, l := range live {
			if l.UnitNumber != 0 {
				if owner, ok := stateUnits[l.UnitNumber]; ok && !wanted[owner] {
					continue // removed by a DELETE row before creates run
				}
			}
			if r.Footprint.Overlaps(l.Footprint) {
				blockers = append(blockers, ffDiffBlocker{l.UnitNumber, ffLiveEntity(l), l.Position})
			}
		}
		if len(blockers) > 0 {
			sort.Slice(blockers, func(i, j int) bool {
				a, b := blockers[i], blockers[j]
				if a.UnitNumber != b.UnitNumber {
					return a.UnitNumber < b.UnitNumber
				}
				if a.Entity != b.Entity {
					return a.Entity < b.Entity
				}
				if a.Position.X != b.Position.X {
					return a.Position.X < b.Position.X
				}
				return a.Position.Y < b.Position.Y
			})
			parts := make([]string, len(blockers))
			for i, b := range blockers {
				parts[i] = fmt.Sprintf("%s (unit %d) at (%g,%g)", b.Entity, b.UnitNumber, b.Position.X, b.Position.Y)
			}
			row := base(r, ffActionConflict)
			row.Blockers = blockers
			row.Reason = "footprint is blocked by " + strings.Join(parts, ", ")
			add(row)
			continue
		}
		row := base(r, ffActionCreate)
		row.Reason = "no matching entity exists"
		add(row)
	}

	// Pass 3: state entries whose address left the manifest.
	var removed []ffStateEntry
	for _, s := range in.State {
		if !wanted[s.Address] {
			removed = append(removed, s)
		}
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i].Address < removed[j].Address })
	for _, s := range removed {
		l := byUnit[int64(s.Unit)]
		if l == nil {
			out.Pruned = append(out.Pruned, ffPrunedState{s.Address, int64(s.Unit), "already gone"})
			continue
		}
		add(ffDiffRow{
			Action: ffActionDelete, Address: s.Address, Entity: s.Entity, Position: ffPoint{s.X, s.Y},
			Direction: s.Direction, Quality: s.Quality, UnitNumber: l.UnitNumber,
			Reason: "address is no longer in the manifest",
		})
	}

	// Wires.
	type liveWire struct {
		a, b   int64
		ac, bc string
		color  string
		used   bool
	}
	var lws []*liveWire
	seenWire := map[string]bool{}
	for _, l := range live {
		for _, w := range l.Wires {
			a, ac, b, bc := l.UnitNumber, w.Connector, w.PeerUnit, w.PeerConnector
			if a > b || (a == b && ac > bc) {
				a, ac, b, bc = b, bc, a, ac
			}
			key := fmt.Sprintf("%s|%d|%s|%d|%s", w.Color, a, ac, b, bc)
			if seenWire[key] {
				continue
			}
			seenWire[key] = true
			lws = append(lws, &liveWire{a: a, b: b, ac: ac, bc: bc, color: w.Color})
		}
	}
	sort.Slice(lws, func(i, j int) bool {
		x, y := lws[i], lws[j]
		if x.color != y.color {
			return x.color < y.color
		}
		if x.a != y.a {
			return x.a < y.a
		}
		if x.ac != y.ac {
			return x.ac < y.ac
		}
		if x.b != y.b {
			return x.b < y.b
		}
		return x.bc < y.bc
	})
	// stable endpoints: units whose wires survive untouched this plan.
	stable := map[int64]string{} // unit -> address
	for addr, u := range unitOf {
		if a := rows[addr].Action; a != ffActionReplace && a != ffActionPending {
			stable[u] = addr
		}
	}
	dws := append([]ffDesiredWire(nil), desired.Wires...)
	sort.Slice(dws, func(i, j int) bool { return dws[i].Address < dws[j].Address })
	var wireRows []ffDiffRow
	for _, w := range dws {
		ref := &ffDiffWireRef{Color: w.Color, FromAddress: w.From, FromConnector: w.FromConnector, ToAddress: w.To, ToConnector: w.ToConnector}
		ref.FromUnit, ref.ToUnit = unitOf[w.From], unitOf[w.To]
		row := ffDiffRow{Address: w.Address, Wire: ref}
		fs, ts := stable[ref.FromUnit], stable[ref.ToUnit]
		if ref.FromUnit == 0 || ref.ToUnit == 0 || fs != w.From || ts != w.To {
			row.Action = ffActionCreate
			row.Reason = "an endpoint is created or replaced"
			wireRows = append(wireRows, row)
			continue
		}
		found := false
		for _, lw := range lws {
			if lw.used || lw.color != w.Color {
				continue
			}
			if ffWireMatch(lw.a, lw.ac, lw.b, lw.bc, ref.FromUnit, ref.FromConnector, ref.ToUnit, ref.ToConnector) {
				lw.used, found = true, true
				break
			}
		}
		if found {
			row.Action = ffActionNoop
		} else {
			row.Action = ffActionCreate
			row.Reason = "wire is missing"
		}
		wireRows = append(wireRows, row)
	}
	for _, lw := range lws {
		if lw.used {
			continue
		}
		fa, ta := stable[lw.a], stable[lw.b]
		if fa == "" || ta == "" {
			continue // wires to unmanaged (or replaced) entities are ignored
		}
		fu, fc, tu, tc := lw.a, lw.ac, lw.b, lw.bc
		if fa > ta {
			fa, ta, fu, tu, fc, tc = ta, fa, tu, fu, tc, fc
		}
		wireRows = append(wireRows, ffDiffRow{
			Action: ffActionDelete, Address: fmt.Sprintf("wire:%s->%s:%s", fa, ta, lw.color),
			Reason: "live wire between managed entities is not in the manifest",
			Wire:   &ffDiffWireRef{Color: lw.color, FromAddress: fa, FromUnit: fu, FromConnector: fc, ToAddress: ta, ToUnit: tu, ToConnector: tc},
		})
	}

	for _, a := range order {
		out.Rows = append(out.Rows, *rows[a])
	}
	sort.SliceStable(out.Rows, func(i, j int) bool { return out.Rows[i].Address < out.Rows[j].Address })
	sort.SliceStable(wireRows, func(i, j int) bool {
		if wireRows[i].Address != wireRows[j].Address {
			return wireRows[i].Address < wireRows[j].Address
		}
		return wireRows[i].Action < wireRows[j].Action
	})
	out.Rows = append(out.Rows, wireRows...)

	for _, r := range out.Rows {
		s := &out.Summary
		switch r.Action {
		case ffActionCreate:
			s.Create++
		case ffActionUpdate:
			s.Update++
		case ffActionReplace:
			s.Replace++
		case ffActionDelete:
			s.Delete++
		case ffActionDrift:
			s.Drift++
			s.Create++
		case ffActionAdopt:
			s.Adopt++
		case ffActionConflict:
			s.Conflict++
		case ffActionPending:
			s.Pending++
		case ffActionNoop:
			s.Noop++
		}
	}
	s := out.Summary
	switch {
	case s.Conflict > 0:
		out.Status = ffStatusConflict
	case s.Drift > 0:
		out.Status = ffStatusDrift
	case len(out.Rows) == s.Noop:
		out.Status = ffStatusConverged
	default:
		out.Status = ffStatusChanges
	}
	return out
}

func ffWireMatch(a int64, ac string, b int64, bc string, fu int64, fc string, tu int64, tc string) bool {
	m := func(x int64, xc string, y int64, yc string) bool {
		return x == fu && y == tu && (fc == "" || xc == fc) && (tc == "" || yc == tc)
	}
	return m(a, ac, b, bc) || m(b, bc, a, ac)
}

// ffConfigEqual compares a desired config value against the live one.
// Modules compare as multisets, other lists as ordered slots, numbers with a
// 1e-6 tolerance, desired maps as subsets of the live map, and absent values
// equal empty ones.
func ffConfigEqual(path string, want, got any) bool {
	if path == "modules" {
		return reflect.DeepEqual(ffModuleCounts(want), ffModuleCounts(got))
	}
	if path == "inserter.filters" {
		return ffValueEqual(ffTrimSlots(want), ffTrimSlots(got))
	}
	return ffValueEqual(want, got)
}

// ffModuleCounts reduces a module list to "name:quality" -> count. Entries are
// "name", "name@quality" or "name:quality:count"; quality defaults to normal.
func ffModuleCounts(v any) map[string]int {
	out := map[string]int{}
	for _, e := range ffSlice(v) {
		s, ok := e.(string)
		if !ok {
			continue
		}
		name, quality, count := s, "normal", 1
		if i := strings.Index(s, "@"); i >= 0 {
			name, quality = s[:i], s[i+1:]
		} else if parts := strings.Split(s, ":"); len(parts) == 3 {
			name, quality = parts[0], parts[1]
			if quality == "" {
				quality = "normal"
			}
			if n, err := strconv.Atoi(parts[2]); err == nil {
				count = n
			}
		}
		out[name+":"+quality] += count
	}
	return out
}

func ffTrimSlots(v any) any {
	s := ffSlice(v)
	for len(s) > 0 {
		if str, ok := s[len(s)-1].(string); ok && str == "" {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// ffSlice converts any slice kind to []any; non-slices give nil.
func ffSlice(v any) []any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out
}

func ffNumber(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}

func ffMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	if v == nil {
		return nil, false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	m := make(map[string]any, rv.Len())
	for _, k := range rv.MapKeys() {
		m[k.String()] = rv.MapIndex(k).Interface()
	}
	return m, true
}

func ffEmpty(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return s == ""
	}
	if m, ok := ffMap(v); ok {
		return len(m) == 0
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return rv.Len() == 0
	}
	return false
}

func ffValueEqual(want, got any) bool {
	if ffEmpty(want) && ffEmpty(got) {
		return true
	}
	if a, ok := ffNumber(want); ok {
		b, ok := ffNumber(got)
		return ok && math.Abs(a-b) <= ffEpsilon
	}
	if wm, ok := ffMap(want); ok {
		gm, ok := ffMap(got)
		if !ok {
			return false
		}
		for k, wv := range wm {
			if !ffValueEqual(wv, gm[k]) {
				return false
			}
		}
		return true
	}
	if ws := ffSlice(want); ws != nil {
		gs := ffSlice(got)
		if len(ws) != len(gs) {
			return false
		}
		for i := range ws {
			if !ffValueEqual(ws[i], gs[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, got)
}
