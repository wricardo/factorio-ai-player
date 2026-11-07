package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func itemSourcesNode(id, kind string, unit int64, props map[string]any) map[string]any {
	p := map[string]any{"id": id}
	if unit != 0 {
		p["unit"] = unit
	}
	for k, v := range props {
		p[k] = v
	}
	return map[string]any{"id": "e" + id, "labels": []any{"Entity", kind}, "properties": p}
}

func itemSourcesStretch(head int64, x, y float64) map[string]any {
	id := "nauvis:stretch:" + strings.TrimSpace(strings.Repeat(" ", 0)) + itoa64(head)
	return map[string]any{"id": "s" + itoa64(head), "labels": []any{"BeltStretch"},
		"properties": map[string]any{"id": id, "headUnit": head, "headX": x, "headY": y}}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func itemSourcesReader(produces, holds, belts [][]any) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		switch {
		case strings.Contains(in.Cypher, "BeltStretch"):
			return &MemgraphQueryResult{Rows: belts}, nil
		case strings.Contains(in.Cypher, ":CONTAINS"):
			return &MemgraphQueryResult{Rows: holds}, nil
		default:
			return &MemgraphQueryResult{Rows: produces}, nil
		}
	}
}

func itemSourcesFixture() memgraphReader {
	produces := [][]any{
		{itemSourcesNode("nauvis:4", "Machine", 4, map[string]any{"name": "stone-furnace", "x": -9.0, "y": -8.0}), 18.75, "recipe"},
		{itemSourcesNode("nauvis:360", "Machine", 360, map[string]any{"name": "stone-furnace", "x": 20.0, "y": -8.0}), 18.75, "recipe"},
		{itemSourcesNode("nauvis:2", "Chest", 2, map[string]any{"name": "infinity-chest", "x": 0.0, "y": 0.0}), nil, "infinity"},
		{itemSourcesNode("nauvis:9", "Chest", 9, map[string]any{"name": "wooden-chest"}), nil, "other"},
	}
	holds := [][]any{
		{itemSourcesNode("nauvis:2", "Chest", 2, map[string]any{"name": "infinity-chest", "x": 0.0, "y": 0.0}), int64(100)},
		{itemSourcesNode("nauvis:7", "Chest", 7, map[string]any{"name": "wooden-chest", "x": 5.0, "y": 5.0}), int64(42)},
	}
	belts := [][]any{
		{itemSourcesStretch(11, 1, 1), []any{"iron-plate:37", "coal:2"}, []any{"iron-plate:3"}},
		{itemSourcesStretch(5, 30, 30), []any{}, []any{"iron-plate:19"}},
	}
	return itemSourcesReader(produces, holds, belts)
}

func TestItemSourcesValidation(t *testing.T) {
	one := 1.0
	base := MemgraphItemSourcesInput{Item: "iron-plate", Surface: "nauvis", Limit: 20}
	cases := map[string]func(*MemgraphItemSourcesInput){
		"empty item":  func(i *MemgraphItemSourcesInput) { i.Item = "" },
		"bad item":    func(i *MemgraphItemSourcesInput) { i.Item = "Iron Plate'}" },
		"bad surface": func(i *MemgraphItemSourcesInput) { i.Surface = "na uvis" },
		"limit zero":  func(i *MemgraphItemSourcesInput) { i.Limit = 0 },
		"limit big":   func(i *MemgraphItemSourcesInput) { i.Limit = 201 },
		"only nearX":  func(i *MemgraphItemSourcesInput) { i.NearX = &one },
		"only nearY":  func(i *MemgraphItemSourcesInput) { i.NearY = &one },
	}
	for name, mod := range cases {
		in := base
		mod(&in)
		called := false
		_, err := itemSources(context.Background(), func(context.Context, MemgraphQueryInput) (*MemgraphQueryResult, error) {
			called = true
			return &MemgraphQueryResult{}, nil
		}, in)
		if err == nil || called {
			t.Errorf("%s: err=%v called=%v, want validation error before any read", name, err, called)
		}
	}
}

func TestItemSourcesDefaultOrderAndKinds(t *testing.T) {
	out, err := itemSources(context.Background(), itemSourcesFixture(), MemgraphItemSourcesInput{Item: "iron-plate", Surface: "nauvis", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range out.Sources {
		got = append(got, string(s.Kind)+":"+s.Node.ID)
	}
	want := []string{"INFINITY_CHEST:nauvis:2", "PRODUCER:nauvis:4", "CHEST:nauvis:7", "PRODUCER:nauvis:360", "BELT:nauvis:stretch:5", "BELT:nauvis:stretch:11"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
	if out.Truncated || out.Sources[0].Distance != nil {
		t.Fatalf("unexpected truncated/distance: %+v", out)
	}
	if p := out.Sources[1].PerMinute; p == nil || *p != 18.75 {
		t.Fatalf("producer perMinute = %v", p)
	}
	if c := out.Sources[2].Count; c == nil || *c != 42 {
		t.Fatalf("chest count = %v", c)
	}
	if c := out.Sources[5].Count; c == nil || *c != 40 {
		t.Fatalf("belt count = %v, want 37+3 across lanes", c)
	}
	if out.Sources[0].Count != nil {
		t.Fatalf("infinity chest must not carry a count (duplicate CONTAINS ignored)")
	}
}

func TestItemSourcesNearOrderAndLimit(t *testing.T) {
	x, y := 0.0, 0.0
	out, err := itemSources(context.Background(), itemSourcesFixture(), MemgraphItemSourcesInput{Item: "iron-plate", Surface: "nauvis", NearX: &x, NearY: &y, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 3 || !out.Truncated {
		t.Fatalf("len=%d truncated=%v, want 3/true", len(out.Sources), out.Truncated)
	}
	if out.Sources[0].Node.ID != "nauvis:2" || *out.Sources[0].Distance != 0 {
		t.Fatalf("first = %s, want infinity chest at the origin", out.Sources[0].Node.ID)
	}
	if out.Sources[1].Node.ID != "nauvis:stretch:11" {
		t.Fatalf("second = %s, want nearest belt stretch (dist ~1.4)", out.Sources[1].Node.ID)
	}
	for i := 1; i < len(out.Sources); i++ {
		if *out.Sources[i].Distance < *out.Sources[i-1].Distance {
			t.Fatalf("not ascending by distance")
		}
	}
}

func TestItemSourcesUnknownItemEmpty(t *testing.T) {
	out, err := itemSources(context.Background(), itemSourcesReader(nil, nil, nil), MemgraphItemSourcesInput{Item: "nonesuch", Surface: "nauvis", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if out.Sources == nil || len(out.Sources) != 0 || out.Truncated {
		t.Fatalf("want empty non-nil sources, got %+v", out)
	}
}

func TestItemSourcesReaderErrorAndTruncatedPropagation(t *testing.T) {
	boom := errors.New("down")
	_, err := itemSources(context.Background(), func(context.Context, MemgraphQueryInput) (*MemgraphQueryResult, error) { return nil, boom }, MemgraphItemSourcesInput{Item: "x", Surface: "nauvis", Limit: 5})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	out, err := itemSources(context.Background(), func(context.Context, MemgraphQueryInput) (*MemgraphQueryResult, error) {
		return &MemgraphQueryResult{Truncated: true}, nil
	}, MemgraphItemSourcesInput{Item: "x", Surface: "nauvis", Limit: 5})
	if err != nil || !out.Truncated {
		t.Fatalf("reader truncation must propagate: %v %+v", err, out)
	}
}

func TestItemSourcesParamsNotInlined(t *testing.T) {
	_, err := itemSources(context.Background(), func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		if strings.Contains(in.Cypher, "iron-plate") {
			t.Errorf("item inlined in cypher: %s", in.Cypher)
		}
		if err := validateReadOnlyCypher(in.Cypher); err != nil {
			t.Errorf("guard rejects cypher: %v", err)
		}
		return &MemgraphQueryResult{}, nil
	}, MemgraphItemSourcesInput{Item: "iron-plate", Surface: "nauvis", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
}

func TestItemSourcesBeltCount(t *testing.T) {
	got := itemSourcesBeltCount("iron-plate", []any{"iron-plate:5", "iron-plate-x:9", "coal:2"}, []any{"iron-plate:4"}, nil)
	if got != 9 {
		t.Fatalf("got %d, want 9", got)
	}
}
