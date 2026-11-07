package main

import (
	"context"
	"strings"
	"testing"
)

func labReader(made []string, arms [][]any, seen *[]MemgraphQueryInput) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		if seen != nil {
			*seen = append(*seen, in)
		}
		switch in.Cypher {
		case labFeedMadeCypher:
			rows := make([][]any, 0, len(made))
			for _, m := range made {
				rows = append(rows, []any{m})
			}
			return &MemgraphQueryResult{Rows: rows}, nil
		case labFeedArmsCypher:
			return &MemgraphQueryResult{Rows: arms}, nil
		}
		panic("unexpected cypher")
	}
}

func labNode(kind string, unit int64, name string) map[string]any {
	return removalImpactBoltNode("nauvis:"+kind+string(rune('A'+unit%26))+string(rune('a'+unit/26%26)), kind, unit, map[string]any{"name": name})
}

func labFindingKeys(a *MemgraphAudit) []string {
	var out []string
	for _, f := range a.Findings {
		out = append(out, *f.Node.UnitNumber+":"+f.Code+":"+string(f.Severity))
	}
	return out
}

func TestLabFeedAuditFlagsUnreachablePacks(t *testing.T) {
	lab1, lab2 := labNode("Machine", 690, "lab"), labNode("Machine", 691, "lab")
	chest := labNode("Chest", 800, "infinity-chest")
	belt1, belt2 := labNode("Belt", 696, "transport-belt"), labNode("Belt", 698, "transport-belt")
	arms := [][]any{
		{lab1, int64(693), belt1, []any{"automation-science-pack"}},
		{lab1, int64(804), chest, []any{"production-science-pack", "chemical-science-pack"}},
		{lab2, int64(694), belt2, []any{"automation-science-pack", "chemical-science-pack"}},
	}
	made := []string{"automation-science-pack", "chemical-science-pack", "production-science-pack"}
	a, err := labFeedAudit(context.Background(), labReader(made, arms, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Lab 690 reaches every pack (chest holds two, belt one): silent. Lab 691's
	// only chest holder sits beside lab 690, so production is unreachable.
	if got := strings.Join(labFindingKeys(a), ","); got != "691:LAB_PACK_UNREACHABLE:BLOCKER" {
		t.Fatalf("findings = %s", got)
	}
	f := a.Findings[0]
	if f.Impact != nil || len(f.Related) != 1 || *f.Related[0].UnitNumber != "698" {
		t.Errorf("impact=%v related=%v", f.Impact, f.Related)
	}
	for _, want := range []string{"production-science-pack", "automation-science-pack, chemical-science-pack"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message %q lacks %q", f.Message, want)
		}
	}
}

func TestLabFeedAuditOneFindingPerMissingPackAndLabWithoutArms(t *testing.T) {
	lab1, lab2 := labNode("Machine", 690, "lab"), labNode("Machine", 692, "lab")
	belt := labNode("Belt", 696, "transport-belt")
	arms := [][]any{
		{lab1, int64(693), belt, []any{"automation-science-pack"}},
		{lab1, int64(787), belt, []any{"logistic-science-pack"}},
		{lab2, nil, nil, []any{}},
	}
	made := []string{"automation-science-pack", "chemical-science-pack", "logistic-science-pack"}
	a, err := labFeedAudit(context.Background(), labReader(made, arms, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := "690:LAB_PACK_UNREACHABLE:BLOCKER,692:LAB_PACK_UNREACHABLE:BLOCKER,692:LAB_PACK_UNREACHABLE:BLOCKER,692:LAB_PACK_UNREACHABLE:BLOCKER"
	if got := strings.Join(labFindingKeys(a), ","); got != want {
		t.Fatalf("findings = %s", got)
	}
	if len(a.Findings[0].Related) != 1 {
		t.Errorf("deduplicated related = %v", a.Findings[0].Related)
	}
	last := a.Findings[3]
	if len(last.Related) != 0 || !strings.Contains(last.Message, "can get: none") {
		t.Errorf("armless lab: related=%v message=%q", last.Related, last.Message)
	}
	if a.Counts[0].Name != "LAB_PACK_UNREACHABLE" || a.Counts[0].Count != 4 {
		t.Errorf("counts = %v", a.Counts)
	}
}

func TestLabFeedAuditSilentWithoutMadePacksOrLabs(t *testing.T) {
	lab := labNode("Machine", 690, "lab")
	for name, r := range map[string]memgraphReader{
		"no packs made": labReader(nil, [][]any{{lab, nil, nil, []any{}}}, nil),
		"no labs":       labReader([]string{"automation-science-pack"}, nil, nil),
	} {
		a, err := labFeedAudit(context.Background(), r, MemgraphAuditInput{Surface: "nauvis", Limit: 10})
		if err != nil || len(a.Findings) != 0 || a.Summary != "no findings" {
			t.Errorf("%s: audit=%+v err=%v", name, a, err)
		}
	}
}

func TestLabFeedAuditLimitParamsAndValidation(t *testing.T) {
	lab := labNode("Machine", 690, "lab")
	var seen []MemgraphQueryInput
	made := []string{"a-science-pack", "b-science-pack"}
	a, err := labFeedAudit(context.Background(), labReader(made, [][]any{{lab, nil, nil, []any{}}}, &seen), MemgraphAuditInput{Surface: "nauvis", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 1 || !a.Truncated || a.Counts[0].Count != 2 {
		t.Fatalf("findings=%d truncated=%v counts=%v", len(a.Findings), a.Truncated, a.Counts)
	}
	for _, in := range seen {
		if in.Params.(map[string]any)["surface"] != "nauvis" {
			t.Errorf("params = %v", in.Params)
		}
	}
	for _, in := range []MemgraphAuditInput{{Surface: "", Limit: 5}, {Surface: "nauvis", Limit: 0}, {Surface: "nauvis", Limit: 1001}} {
		if _, err := labFeedAudit(context.Background(), labReader(nil, nil, nil), in); err == nil {
			t.Errorf("%+v: want error", in)
		}
	}
}

func TestLabFeedQueriesPassReadOnlyGuard(t *testing.T) {
	for _, q := range []string{labFeedMadeCypher, labFeedArmsCypher} {
		if err := validateReadOnlyCypher(q); err != nil {
			t.Fatal(err)
		}
	}
}

// The arm query folds the pickup belt's own lane sample into "provides", so a
// pack that only lies on the pickup belt counts. A pack that exists only on a
// line that never joins the pickup line (no BELT_OUT or dropper path, so it is
// absent from provides) is still flagged.
func TestLabFeedAuditPickupBeltLaneSampleCountsParkedPackDoesNot(t *testing.T) {
	lab := labNode("Machine", 691, "lab")
	belt := labNode("Belt", 698, "transport-belt")
	arms := [][]any{{lab, int64(694), belt, []any{"automation-science-pack", "chemical-science-pack"}}}
	made := []string{"automation-science-pack", "chemical-science-pack", "utility-science-pack"}
	a, err := labFeedAudit(context.Background(), labReader(made, arms, nil), MemgraphAuditInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 1 || !strings.Contains(a.Findings[0].Message, "makes utility-science-pack") {
		t.Fatalf("findings = %v", labFindingKeys(a))
	}
	if !strings.Contains(a.Findings[0].Message, "automation-science-pack, chemical-science-pack") {
		t.Errorf("message = %q", a.Findings[0].Message)
	}
}
