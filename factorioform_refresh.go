package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
)

// factorioformRefresh records the unit numbers of live entities that match the
// manifest into state, in one storage write. It never touches entities.

// ffRefreshPlan is the pure outcome of reconciling a diff into state changes.
type ffRefreshPlan struct {
	Upsert    []ffStateEntry
	Remove    []string
	Result    FactorioformRefreshResult
	Surface   string
	Force     string
	Manifest  string
	NewStatus string
}

// ffRefreshCompute classifies diff rows. Rows are never mutated.
func ffRefreshCompute(res *ffPipelineResult) *ffRefreshPlan {
	out := &ffRefreshPlan{}
	if res.Expanded != nil {
		out.Manifest = res.Expanded.Header.Name
		out.Surface = res.Expanded.Header.Surface
		out.Force = res.Expanded.Header.Force
	}
	out.Result = FactorioformRefreshResult{
		Manifest: out.Manifest, Recorded: []*FactorioformRefreshRecorded{},
		Pruned: []string{}, Unmatched: []*FactorioformRefreshUnmatched{},
	}
	if res.Live != nil {
		out.Result.Tick = res.Live.Tick
	}
	byUnit := map[int64]*ffLive{}
	if res.Live != nil {
		for _, l := range res.Live.Entities {
			if l.UnitNumber != 0 && l.GhostName == "" {
				byUnit[l.UnitNumber] = l
			}
		}
	}
	state := map[string]ffStateEntry{}
	for _, s := range res.StateEntries {
		state[s.Address] = s
	}
	removed := map[string]bool{}
	remove := func(a string) {
		if !removed[a] {
			removed[a] = true
			out.Remove = append(out.Remove, a)
		}
	}
	next := map[string]ffStateEntry{}
	for k, v := range state {
		next[k] = v
	}
	unmatched := func(a, reason string) {
		out.Result.Unmatched = append(out.Result.Unmatched, &FactorioformRefreshUnmatched{Address: a, Reason: reason})
	}
	for _, row := range res.Diff.Rows {
		if row.Wire != nil {
			continue
		}
		switch row.Action {
		case ffActionAdopt, ffActionNoop, ffActionUpdate:
			l := byUnit[row.UnitNumber]
			if l == nil {
				unmatched(row.Address, fmt.Sprintf("unit %d is not in the live read", row.UnitNumber))
				continue
			}
			e := ffStateEntry{
				Address: row.Address, Unit: uint64(l.UnitNumber), Entity: l.Entity,
				X: l.Position.X, Y: l.Position.Y, Direction: l.Direction, Quality: ffLiveQuality(l),
			}
			if old, ok := state[row.Address]; ok && old == e {
				continue
			}
			out.Upsert = append(out.Upsert, e)
			next[row.Address] = e
			out.Result.Recorded = append(out.Result.Recorded, &FactorioformRefreshRecorded{
				Address: row.Address, UnitNumber: strconv.FormatInt(l.UnitNumber, 10),
			})
		case ffActionDrift:
			remove(row.Address)
			delete(next, row.Address)
			unmatched(row.Address, row.Reason)
		case ffActionCreate, ffActionConflict, ffActionPending:
			unmatched(row.Address, row.Reason)
		}
	}
	for _, p := range res.Diff.Pruned {
		remove(p.Address)
		delete(next, p.Address)
		out.Result.Pruned = append(out.Result.Pruned, p.Address)
	}
	sort.Strings(out.Remove)
	sort.Strings(out.Result.Pruned)

	entries := make([]ffStateEntry, 0, len(next))
	for _, e := range next {
		entries = append(entries, e)
	}
	var desired *ffNormalized
	if res.Normalized != nil {
		desired = res.Normalized
	}
	var live []*ffLive
	if res.Live != nil {
		live = res.Live.Entities
	}
	out.NewStatus = ffDiff(ffDiffInput{Desired: desired, State: entries, Live: live}).Status
	out.Result.Status = out.NewStatus
	return out
}

func factorioformRefresh(ctx context.Context, r *Resolver, in FactorioformRefreshInput) (*FactorioformRefreshResult, error) {
	req, err := ffPlanRequestFromInput(FactorioformPlanInput{Files: in.Files, RootDir: in.RootDir, Variables: in.Variables})
	if err != nil {
		return nil, err
	}
	res, diags, err := ffRunPipeline(ctx, r, ffPipelineInput{Files: req.Files, Vars: req.Vars, RawVars: req.RawVars, RootDir: req.RootDir})
	if err != nil {
		return nil, err
	}
	if diags.HasErrors() {
		return nil, ffDiagError(diags)
	}
	return ffRefreshApply(r, res, in.DryRun, in.Message)
}

// ffRefreshApply writes the computed changes in one storage put unless dryRun
// or nothing changed.
func ffRefreshApply(r *Resolver, res *ffPipelineResult, dryRun bool, message *string) (*FactorioformRefreshResult, error) {
	p := ffRefreshCompute(res)
	p.Result.DryRun = dryRun
	if dryRun || (len(p.Upsert) == 0 && len(p.Remove) == 0) {
		return &p.Result, nil
	}
	// State absent and only removals would create an empty manifest; skip.
	if res.State == nil && len(p.Upsert) == 0 {
		return &p.Result, nil
	}
	if err := ffStatePut(r, p.Manifest, p.Surface, p.Force, p.Upsert, p.Remove); err != nil {
		return nil, err
	}
	announceMutation(r, message, fmt.Sprintf("factorioform %s: refreshed state (%d recorded, %d removed)", p.Manifest, len(p.Upsert), len(p.Remove)))
	return &p.Result, nil
}
