package main

import (
	"context"
	"strings"
	"testing"
)

func powerTestEntity(id string, unit int64, kind, name string, extra map[string]any) map[string]any {
	props := map[string]any{"id": id, "unit": unit, "name": name}
	for k, v := range extra {
		props[k] = v
	}
	return map[string]any{"id": "b" + id, "labels": []any{"Entity", kind}, "properties": props}
}

func powerTestNetwork(id string, num int64) map[string]any {
	return map[string]any{"id": "b" + id, "labels": []any{"Network", "ElectricNetwork"},
		"properties": map[string]any{"id": id, "network": num}}
}

type powerFixture struct {
	consumers, networks, poles [][]any
	truncated                  bool
}

func (f powerFixture) reader(t *testing.T) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		res := &MemgraphQueryResult{Truncated: f.truncated, Snapshot: &MemgraphSnapshotInfo{ID: "snap"}}
		switch in.Cypher {
		case powerAuditConsumersCypher:
			res.Rows = f.consumers
		case powerAuditNetworksCypher:
			res.Rows = f.networks
		case powerAuditPolesCypher:
			res.Rows = f.poles
		default:
			t.Fatalf("unexpected cypher %q", in.Cypher)
		}
		return res, nil
	}
}

func powerRun(t *testing.T, f powerFixture, limit int) *MemgraphAudit {
	t.Helper()
	out, err := powerAudit(context.Background(), f.reader(t), MemgraphAuditInput{Surface: "nauvis", Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func powerCodes(a *MemgraphAudit) map[string]int {
	m := map[string]int{}
	for _, f := range a.Findings {
		m[f.Code]++
	}
	return m
}

func powerFinding(t *testing.T, a *MemgraphAudit, code string) *MemgraphAuditFinding {
	t.Helper()
	for _, f := range a.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %s finding in %v", code, powerCodes(a))
	return nil
}

func TestPowerAuditUnpowered(t *testing.T) {
	f := powerFixture{consumers: [][]any{
		{powerTestEntity("n:1", 1, "Machine", "assembling-machine-1", nil)},
		{powerTestEntity("n:2", 2, "Inserter", "inserter", nil)},
		// burner entities are not consumers
		{powerTestEntity("n:3", 3, "Machine", "stone-furnace", nil)},
		{powerTestEntity("n:4", 4, "Inserter", "burner-inserter", nil)},
		{powerTestEntity("n:5", 5, "Machine", "custom-boiler-furnace", map[string]any{"fuel": []any{"coal:5"}})},
	}}
	out := powerRun(t, f, 50)
	if got := powerCodes(out); got["UNPOWERED"] != 2 || len(out.Findings) != 2 {
		t.Fatalf("codes = %v, findings %d", got, len(out.Findings))
	}
	fd := powerFinding(t, out, "UNPOWERED")
	if fd.Severity != MemgraphAuditSeverityBlocker {
		t.Fatalf("severity = %v", fd.Severity)
	}
	if !strings.Contains(fd.Message, "not a live brownout reading") {
		t.Fatalf("message must say topology only: %q", fd.Message)
	}
}

func TestPowerAuditNoUnpowered(t *testing.T) {
	out := powerRun(t, powerFixture{}, 50)
	if len(out.Findings) != 0 || out.Summary != "no findings" {
		t.Fatalf("got %+v", out)
	}
}

func TestPowerAuditSinglePoleNetwork(t *testing.T) {
	pole := powerTestEntity("n:10", 10, "Pole", "small-electric-pole", nil)
	f := powerFixture{
		networks: [][]any{{powerTestNetwork("nauvis:electric:7", 7), int64(1), int64(1), int64(0), int64(0)}},
		poles:    [][]any{{pole, int64(0), "nauvis:electric:7"}},
	}
	out := powerRun(t, f, 50)
	// the lone pole is reported once, as the network finding
	if got := powerCodes(out); got["SINGLE_POLE_NETWORK"] != 1 || len(out.Findings) != 1 {
		t.Fatalf("codes = %v", got)
	}
	fd := powerFinding(t, out, "SINGLE_POLE_NETWORK")
	if fd.Severity != MemgraphAuditSeverityConcern || fd.Node.Kind != "ElectricNetwork" || len(fd.Related) != 1 || fd.Related[0].ID != "n:10" {
		t.Fatalf("finding = %+v node %+v", fd, fd.Node)
	}
	if !strings.Contains(out.Summary, "electric-network 7") {
		t.Fatalf("summary = %q", out.Summary)
	}
}

func TestPowerAuditSinglePoleNegative(t *testing.T) {
	// one pole that powers a machine and has a generator is a fine network
	f := powerFixture{
		networks: [][]any{{powerTestNetwork("nauvis:electric:1", 1), int64(3), int64(1), int64(1), int64(1)}},
		poles:    [][]any{{powerTestEntity("n:10", 10, "Pole", "small-electric-pole", nil), int64(0), "nauvis:electric:1"}},
	}
	if out := powerRun(t, f, 50); len(out.Findings) != 0 {
		t.Fatalf("unexpected %v", powerCodes(out))
	}
}

func TestPowerAuditIsolatedPole(t *testing.T) {
	f := powerFixture{
		networks: [][]any{{powerTestNetwork("nauvis:electric:1", 1), int64(3), int64(2), int64(1), int64(0)}},
		poles: [][]any{
			// no network at all: isolated
			{powerTestEntity("n:20", 20, "Pole", "small-electric-pole", nil), int64(0), nil},
			// wired pole: fine
			{powerTestEntity("n:21", 21, "Pole", "small-electric-pole", nil), int64(1), "nauvis:electric:1"},
		},
	}
	out := powerRun(t, f, 50)
	if got := powerCodes(out); got["ISOLATED_POLE"] != 1 || len(out.Findings) != 1 {
		t.Fatalf("codes = %v", got)
	}
	fd := powerFinding(t, out, "ISOLATED_POLE")
	if fd.Node.ID != "n:20" || fd.Severity != MemgraphAuditSeverityConcern {
		t.Fatalf("finding = %+v", fd)
	}
}

func TestPowerAuditWirelessPoleThatPowersSomething(t *testing.T) {
	// no wire, but a machine shares its network: the pole is doing work
	f := powerFixture{
		networks: [][]any{{powerTestNetwork("nauvis:electric:3", 3), int64(3), int64(1), int64(1), int64(1)}},
		poles:    [][]any{{powerTestEntity("n:22", 22, "Pole", "small-electric-pole", nil), int64(0), "nauvis:electric:3"}},
	}
	if out := powerRun(t, f, 50); len(out.Findings) != 0 {
		t.Fatalf("unexpected %v", powerCodes(out))
	}
}

func TestPowerAuditWirelessPolesWithoutConsumers(t *testing.T) {
	// two wireless poles claiming one network without any other member
	f := powerFixture{
		networks: [][]any{{powerTestNetwork("nauvis:electric:4", 4), int64(2), int64(2), int64(0), int64(0)}},
		poles: [][]any{
			{powerTestEntity("n:23", 23, "Pole", "small-electric-pole", nil), int64(0), "nauvis:electric:4"},
			{powerTestEntity("n:24", 24, "Pole", "small-electric-pole", nil), int64(0), "nauvis:electric:4"},
		},
	}
	got := powerCodes(powerRun(t, f, 50))
	if got["ISOLATED_POLE"] != 2 || got["SINGLE_POLE_NETWORK"] != 0 || got["NO_GENERATOR"] != 0 {
		t.Fatalf("codes = %v", got)
	}
}

func TestPowerAuditNoGenerator(t *testing.T) {
	f := powerFixture{
		networks: [][]any{
			// 2 poles, 2 machines, 1 inserter, no source
			{powerTestNetwork("nauvis:electric:1", 1), int64(5), int64(2), int64(0), int64(2)},
			// same plus a source: fine
			{powerTestNetwork("nauvis:electric:2", 2), int64(6), int64(2), int64(1), int64(2)},
			// poles and a source only, no consumers: fine
			{powerTestNetwork("nauvis:electric:3", 3), int64(3), int64(2), int64(1), int64(0)},
		},
	}
	out := powerRun(t, f, 50)
	if got := powerCodes(out); got["NO_GENERATOR"] != 1 || len(out.Findings) != 1 {
		t.Fatalf("codes = %v", got)
	}
	fd := powerFinding(t, out, "NO_GENERATOR")
	if fd.Severity != MemgraphAuditSeverityBlocker || fd.Node.ID != "nauvis:electric:1" || fd.Impact == nil || *fd.Impact != 2 {
		t.Fatalf("finding = %+v", fd)
	}
}

func TestPowerAuditOrderLimitTruncated(t *testing.T) {
	f := powerFixture{
		consumers: [][]any{{powerTestEntity("n:1", 1, "Machine", "assembling-machine-1", nil)}},
		networks:  [][]any{{powerTestNetwork("nauvis:electric:1", 1), int64(2), int64(1), int64(0), int64(1)}},
		poles:     [][]any{{powerTestEntity("n:20", 20, "Pole", "small-electric-pole", nil), int64(0), nil}},
	}
	out := powerRun(t, f, 2)
	if len(out.Findings) != 2 || !out.Truncated {
		t.Fatalf("findings %d truncated %v", len(out.Findings), out.Truncated)
	}
	// blockers first: NO_GENERATOR (impact 1) before UNPOWERED (no impact); the concern is cut
	if out.Findings[0].Code != "NO_GENERATOR" || out.Findings[1].Code != "UNPOWERED" {
		t.Fatalf("order = %s, %s", out.Findings[0].Code, out.Findings[1].Code)
	}
	total := 0
	for _, c := range out.Counts {
		total += c.Count
	}
	if total != 3 {
		t.Fatalf("counts must cover every finding: %+v", out.Counts)
	}
	if out.Snapshot == nil || out.Snapshot.ID != "snap" {
		t.Fatalf("snapshot = %+v", out.Snapshot)
	}

	// a read cap flows into truncated even without a limit cut
	f.truncated = true
	if out := powerRun(t, f, 50); !out.Truncated {
		t.Fatal("read truncation must be reported")
	}
}

func TestPowerAuditInputValidation(t *testing.T) {
	for _, in := range []MemgraphAuditInput{{Surface: "", Limit: 10}, {Surface: "nauvis", Limit: 0}, {Surface: "nauvis", Limit: auditMaxLimit + 1}} {
		if _, err := powerAudit(context.Background(), powerFixture{}.reader(t), in); err == nil {
			t.Fatalf("input %+v must fail", in)
		}
	}
}

func TestPowerAuditReadError(t *testing.T) {
	read := func(context.Context, MemgraphQueryInput) (*MemgraphQueryResult, error) {
		return nil, context.DeadlineExceeded
	}
	if _, err := powerAudit(context.Background(), read, MemgraphAuditInput{Surface: "nauvis", Limit: 5}); err == nil {
		t.Fatal("read error must surface")
	}
}
