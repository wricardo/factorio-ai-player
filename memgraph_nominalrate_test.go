package main

import (
	"context"
	"strings"
	"testing"
)

func nominalRateReader(rows [][]any, seen *MemgraphQueryInput) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		if seen != nil {
			*seen = in
		}
		return &MemgraphQueryResult{Rows: rows}, nil
	}
}

func TestNominalRateValidation(t *testing.T) {
	for _, in := range []MemgraphNominalRateBalanceInput{
		{Surface: "bad surface", Limit: 5}, {Surface: "nauvis", Limit: 0}, {Surface: "nauvis", Limit: 501},
	} {
		if _, err := nominalRateBalance(context.Background(), nominalRateReader(nil, nil), in); err == nil {
			t.Errorf("%+v: want error", in)
		}
	}
}

func TestNominalRateDeficitsAndTruncation(t *testing.T) {
	rows := [][]any{
		{"copper-cable", 0.0, 90.0, int64(0), int64(2), int64(0)},
		{"iron-plate", 37.5, 540.0, int64(2), int64(1), int64(0)},
		{"iron-ore", 0.0, 37.5, int64(0), int64(2), int64(1)},
		{"gear", 60.0, 0.0, int64(1), int64(0), int64(0)},
	}
	var seen MemgraphQueryInput
	out, err := nominalRateBalance(context.Background(), nominalRateReader(rows, &seen), MemgraphNominalRateBalanceInput{Surface: "nauvis", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Items) != 3 || seen.Limit != 4 {
		t.Fatalf("truncated=%v items=%d limit=%d", out.Truncated, len(out.Items), seen.Limit)
	}
	if seen.Params.(map[string]any)["item"] != nil {
		t.Error("item param should be nil when unset")
	}
	if len(out.Deficits) != 3 {
		t.Fatalf("deficits = %v", out.Deficits)
	}
	if !strings.Contains(out.Deficits[0], "no producer") || !strings.Contains(out.Deficits[1], "iron-plate") || !strings.Contains(out.Deficits[2], "unbounded") {
		t.Errorf("deficits = %v", out.Deficits)
	}
	if out.Items[1].Net != -502.5 {
		t.Errorf("net = %v", out.Items[1].Net)
	}
}

func TestNominalRateUnboundedNotDeficit(t *testing.T) {
	item := "iron-ore"
	rows := [][]any{{"iron-ore", 0.0, 37.5, int64(0), int64(2), int64(1)}}
	out, err := nominalRateBalance(context.Background(), nominalRateReader(rows, nil), MemgraphNominalRateBalanceInput{Item: &item, Surface: "nauvis", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if out.Truncated || len(out.Deficits) != 1 || !strings.Contains(out.Deficits[0], "unbounded") {
		t.Errorf("%+v", out.Deficits)
	}
}

func TestNominalRateBadRow(t *testing.T) {
	_, err := nominalRateBalance(context.Background(), nominalRateReader([][]any{{"x", "a", 1.0, 1, 1, 1}}, nil), MemgraphNominalRateBalanceInput{Surface: "nauvis", Limit: 5})
	if err == nil {
		t.Error("want error")
	}
}
