package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zclconf/go-cty/cty"
)

const factorioformUsage = `Usage: factorio-ai-player [--addr HOST:PORT] factorioform <command> [options]

Commands:
  plan    Print the execution plan JSON (same document as the factorioformPlan
          GraphQL query): diffs, ordered GraphQL steps and materials

Arguments:
  DIR               directory holding *.ftf manifest files (used with plan):
                    every *.ftf file directly inside DIR is merged as one
                    root module, like Terraform: variables, locals and resources
                    are shared across files in the directory. A module block's
                    source ("./modules/x", relative to the file holding the
                    block) pulls in that directory as a reusable module with its
                    own variables, locals, resources and outputs. Plan options
                    must come before DIR (Go stops flag parsing at the first
                    non-flag, so e.g. --format text goes before DIR).

Plan options:
  --dir DIR           every *.ftf file directly inside DIR, merged like Terraform
                      (same as the positional DIR)
  --var k=v           variable value; repeatable. Parsed as an HCL literal when the
                      variable declares a non-string type, otherwise used verbatim
  --var-file PATH     file of "name = literal" variable assignments; repeatable
  --actor-player NAME inject this player as input.actor into every step; without
                      it every step sets requiresActor and omits the actor

Plan is read-only. Manifest errors print as file:line:col lines and exit nonzero.
`

func runFactorioform(args []string, out io.Writer, addr, password string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, factorioformUsage)
		return err
	}
	if args[0] != "plan" {
		return fmt.Errorf("unknown factorioform command %q; use factorioform help", args[0])
	}
	return runFactorioformPlan(args[1:], out, addr, password)
}

func runFactorioformPlan(args []string, out io.Writer, addr, password string) error {
	fs := flag.NewFlagSet("factorioform plan", flag.ContinueOnError)
	fs.SetOutput(out)
	var vars, varFiles stringList
	dir := fs.String("dir", "", "directory holding *.ftf manifest files")
	actor := fs.String("actor-player", "", "player injected as input.actor into every step")
	format := fs.String("format", "json", "output format: json or text (LLM-friendly)")
	fs.Var(&vars, "var", "variable k=v (repeatable)")
	fs.Var(&varFiles, "var-file", "variable file (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("factorioform plan takes at most one DIR argument")
	}
	if fs.NArg() == 1 {
		if *dir != "" {
			return errors.New("give DIR or --dir DIR, not both")
		}
		*dir = fs.Arg(0)
	}
	if password == "" {
		return errors.New("FACTORIO_RCON_PASSWORD is required")
	}
	req := ffPlanRequest{Files: map[string][]byte{}, Vars: map[string]cty.Value{}, RawVars: map[string]string{}}
	var paths []string
	rootDir := *dir
	if *dir != "" {
		matches, err := filepath.Glob(filepath.Join(*dir, "*.ftf"))
		if err != nil {
			return err
		}
		sort.Strings(matches)
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		return errors.New("give [DIR] or --dir DIR holding *.ftf files")
	}
	req.RootDir = ffCleanDir(rootDir)
	readFile := func(path string) error {
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, dup := req.Files[path]; dup {
			return fmt.Errorf("manifest file %s given twice", path)
		}
		req.Files[path] = src
		return nil
	}
	for _, path := range paths {
		if ffDirOf(path) != req.RootDir {
			return fmt.Errorf("manifest file %s is not in directory %s; files in other directories are loaded through module blocks", path, req.RootDir)
		}
		if err := readFile(path); err != nil {
			return err
		}
	}
	// Load the directories the manifests' module blocks point at, and theirs in
	// turn, until nothing refers to a directory that has not been read.
	for range ffMaxModuleDepth + 2 {
		loaded := false
		for _, md := range ffMissingModuleDirs(req.Files, req.RootDir) {
			matches, err := filepath.Glob(filepath.Join(md, "*.ftf"))
			if err != nil {
				return err
			}
			sort.Strings(matches)
			for _, path := range matches {
				if err := readFile(path); err != nil {
					return err
				}
				paths = append(paths, path)
				loaded = true
			}
		}
		if !loaded {
			break
		}
	}
	for _, path := range varFiles {
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		values, diags := ffParseVarFile(path, src)
		if diags.HasErrors() {
			return errors.New(strings.Join(ffDiagLines(diags), "\n"))
		}
		for k, v := range values {
			if _, dup := req.Vars[k]; dup {
				return fmt.Errorf("variable %q is set by more than one --var-file", k)
			}
			req.Vars[k] = v
		}
	}
	for _, kv := range vars {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("--var %q must be name=value", kv)
		}
		if _, dup := req.RawVars[k]; dup {
			return fmt.Errorf("--var %s given twice", k)
		}
		req.RawVars[k] = v
	}
	if *actor != "" {
		req.Actor = &ActorInput{Player: actor}
	}

	started := time.Now()
	plan, diags, err := ffPlan(context.Background(), &Resolver{addr: addr, password: password}, req)
	entry := graphQLOperationLog{
		Time: started, Source: "cli", Command: "factorioform plan",
		Document:   fmt.Sprintf("factorioform plan files=%s", strings.Join(paths, ",")),
		DurationMS: float64(time.Since(started).Microseconds()) / 1000,
	}
	switch {
	case err != nil:
		entry.Error = err.Error()
	case diags.HasErrors():
		entry.Error = strings.Join(ffDiagLines(diags), "; ")
	default:
		entry.Response, _ = json.Marshal(map[string]any{"manifest": plan.Manifest, "planId": plan.PlanID, "status": plan.Status, "tick": plan.Tick, "steps": len(plan.Steps)})
	}
	appendGraphQLOperationLog(entry)
	if err != nil {
		return err
	}
	if diags.HasErrors() {
		return errors.New("factorioform manifest has errors:\n" + strings.Join(ffDiagLines(diags), "\n"))
	}
	raw, err := ffPlanJSON(plan)
	if err != nil {
		return err
	}
	if *format == "text" {
		text, err := ffPlanText(plan)
		if err != nil {
			return err
		}
		_, err = out.Write(append(text, '\n'))
		return err
	}
	_, err = out.Write(append(raw, '\n'))
	return err
}

// ffPlanJSON renders the plan exactly as the GraphQL query returns it with
// every field selected: nullable fields the Go models omit are written as null.
func ffPlanJSON(plan *FactorioformPlan) ([]byte, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	fill := func(m map[string]any, keys ...string) {
		for _, k := range keys {
			if _, ok := m[k]; !ok {
				m[k] = nil
			}
		}
	}
	diffs, _ := doc["diffs"].([]any)
	for _, d := range diffs {
		dm := d.(map[string]any)
		fill(dm, "entity", "quality", "unitNumber", "reason", "wire")
		for _, c := range dm["changes"].([]any) {
			fill(c.(map[string]any), "before", "after")
		}
		if w, ok := dm["wire"].(map[string]any); ok {
			fill(w, "fromUnit", "toUnit")
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// ffPlanText renders the plan in a human/LLM-friendly format.
func ffPlanText(plan *FactorioformPlan) ([]byte, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "FACTORIOFORM PLAN: %s\n", plan.Manifest)
	fmt.Fprintf(&buf, "Status: %s\n", plan.Status)
	fmt.Fprintf(&buf, "Tick: %.0f\n", plan.Tick)
	fmt.Fprintf(&buf, "\n")

	// Materials needed
	fmt.Fprintf(&buf, "MATERIALS NEEDED:\n")
	for _, m := range plan.Materials {
		fmt.Fprintf(&buf, "  • %dx %s\n", m.Count, m.Name)
	}
	fmt.Fprintf(&buf, "\n")
	// Raw materials needed
	fmt.Fprintf(&buf, "RAW MATERIALS NEEDED:\n")
	for _, m := range plan.RawMaterials {
		if m.Quality != "" && m.Quality != "normal" {
			fmt.Fprintf(&buf, "  • %dx %s@%s\n", m.Count, m.Name, m.Quality)
		} else {
			fmt.Fprintf(&buf, "  • %dx %s\n", m.Count, m.Name)
		}
	}

	// Summary of changes
	actionCounts := map[string]int{}
	for _, d := range plan.Diffs {
		actionCounts[d.Action]++
	}
	fmt.Fprintf(&buf, "CHANGES SUMMARY:\n")
	for _, action := range []string{"CREATE", "UPDATE", "REPLACE", "DELETE", "ADOPT", "DRIFT", "CONFLICT", "PENDING", "NOOP"} {
		if count := actionCounts[action]; count > 0 {
			fmt.Fprintf(&buf, "  • %s: %d\n", action, count)
		}
	}
	fmt.Fprintf(&buf, "\n")

	// Detailed diffs
	fmt.Fprintf(&buf, "DETAILED CHANGES:\n")
	for _, d := range plan.Diffs {
		if d.Action == "NOOP" {
			continue // skip NOOPs in detailed view
		}
		entity := ""
		if d.Entity != nil {
			entity = *d.Entity
		}
		fmt.Fprintf(&buf, "  %s %s (%s)\n", d.Action, d.Address, entity)
		if d.UnitNumber != nil && *d.UnitNumber != "" {
			fmt.Fprintf(&buf, "    Unit: %s\n", *d.UnitNumber)
		}
		if d.Reason != nil && *d.Reason != "" {
			fmt.Fprintf(&buf, "    Reason: %s\n", *d.Reason)
		}
		if d.Position != nil {
			fmt.Fprintf(&buf, "    Position: (%.1f, %.1f)\n", d.Position.X, d.Position.Y)
		}
		if len(d.Changes) > 0 {
			fmt.Fprintf(&buf, "    Changes:\n")
			for _, c := range d.Changes {
				fmt.Fprintf(&buf, "      %s: %v → %v\n", c.Path, c.Before, c.After)
			}
		}
		if len(d.Blockers) > 0 {
			fmt.Fprintf(&buf, "    Blockers:\n")
			for _, b := range d.Blockers {
				fmt.Fprintf(&buf, "      Unit %s (%s) at (%.1f, %.1f)\n", b.UnitNumber, b.Entity, b.Position.X, b.Position.Y)
			}
		}
	}
	fmt.Fprintf(&buf, "\n")

	// Action steps
	unitByAddress := map[string]string{}
	for _, d := range plan.Diffs {
		if d.UnitNumber != nil && *d.UnitNumber != "" {
			unitByAddress[d.Address] = *d.UnitNumber
		}
	}
	fmt.Fprintf(&buf, "EXECUTION STEPS (%d total):\n", len(plan.Steps))
	for _, s := range plan.Steps {
		fmt.Fprintf(&buf, "  Step %d [%s] %s\n", s.Order, s.Kind, s.Operation)
		fmt.Fprintf(&buf, "    Addresses: %v\n", s.Addresses)
		units := []string{}
		seen := map[string]bool{}
		for _, a := range s.Addresses {
			if u, ok := unitByAddress[a]; ok && !seen[u] {
				seen[u] = true
				units = append(units, u)
			}
		}
		if len(units) > 0 {
			fmt.Fprintf(&buf, "    Units: %s\n", strings.Join(units, ", "))
		}
		if s.RequiresActor {
			fmt.Fprintf(&buf, "    Requires: actor with inventory\n")
		}
	}

	return buf.Bytes(), nil
}
