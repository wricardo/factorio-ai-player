package main

import "testing"

// belts must report the engine transport-line identity and the lane mix without
// a second backend read: lineName and side come from defines.transport_line and
// mixed is computed from that line's own contents.
func TestBeltsQueryReportsLaneMetadata(t *testing.T) {
	page, err := beltsQuery(serveBeltReplay(t, "belts"), BeltsInput{
		Surface: "nauvis",
		Area:    &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 1, RightBottomY: 1},
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Belts) != 1 {
		t.Fatalf("belts = %d, want 1", len(page.Belts))
	}
	belt := page.Belts[0]
	if belt.UnitNumber == nil || *belt.UnitNumber != "700" {
		t.Fatalf("unit number = %v, want 700", belt.UnitNumber)
	}
	left, right := belt.Line1, belt.Line2
	if left == nil || right == nil {
		t.Fatalf("both lanes must decode: %#v", belt)
	}
	if left.LineName != "left_line" || left.Side != "left" {
		t.Fatalf("left lane identity = %q/%q", left.LineName, left.Side)
	}
	if !left.Mixed {
		t.Fatal("a lane carrying iron-plate and copper-plate must report mixed")
	}
	if len(left.Items) != 2 || left.Items[0].Name != "copper-plate" || left.Items[1].Name != "iron-plate" {
		t.Fatalf("left items = %#v", left.Items)
	}
	if right.LineName != "right_line" || right.Side != "right" {
		t.Fatalf("right lane identity = %q/%q", right.LineName, right.Side)
	}
	if right.Mixed {
		t.Fatal("a single-item lane must not report mixed")
	}
	if len(right.Items) != 1 || right.Items[0].Name != "copper-plate" || right.Items[0].Count != 3 {
		t.Fatalf("right items = %#v", right.Items)
	}
}
