package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	stretchMapMaxSide  = 1024
	stretchMapMaxTiles = 65536
	stretchMapTiles    = `MATCH (b:Belt)-[:IN_STRETCH]->(s:BeltStretch) WHERE s.surface = $surface RETURN s.id, b.x, b.y`
	stretchMapLetters  = "abcdefghijklmnopqrstuvwxyz"
	stretchMapMarks    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

func validateStretchMapInput(in MemgraphBeltStretchMapInput) error {
	if in.Mode != MemgraphStretchMapModeItem && in.Mode != MemgraphStretchMapModeStretch {
		return fmt.Errorf("mode must be ITEM or STRETCH, got %q", in.Mode)
	}
	a := in.Area
	if a == nil {
		return fmt.Errorf("area is required")
	}
	w, h := a.RightBottomX-a.LeftTopX, a.RightBottomY-a.LeftTopY
	if math.IsNaN(w+h) || math.IsInf(w+h, 0) || w <= 0 || h <= 0 {
		return fmt.Errorf("area must be finite with positive size")
	}
	if w > stretchMapMaxSide || h > stretchMapMaxSide || math.Ceil(w)*math.Ceil(h) > stretchMapMaxTiles {
		return fmt.Errorf("area is at most %d tiles per side and %d tiles in total, got %.0f x %.0f", stretchMapMaxSide, stretchMapMaxTiles, w, h)
	}
	return nil
}

type stretchTile struct {
	stretch int // index into the stretch slice
	x, y    float64
}

// beltStretchMap draws the filtered stretches inside the area as ASCII: one
// character per tile, north first, with a legend of what each character means.
func beltStretchMap(ctx context.Context, read memgraphReader, in MemgraphBeltStretchMapInput) (*MemgraphStretchMap, error) {
	filter := MemgraphBeltStretchesInput{Surface: "nauvis", Limit: 1}
	if in.Filter != nil {
		filter = *in.Filter
	}
	if err := validateStretchesInput(filter); err != nil {
		return nil, err
	}
	if err := validateStretchMapInput(in); err != nil {
		return nil, err
	}
	rows, snap, truncated, err := loadStretches(ctx, read, filter)
	if err != nil {
		return nil, err
	}
	out := &MemgraphStretchMap{Rows: []string{}, Legend: []*MemgraphStretchMapLegend{}, Snapshot: snap}
	index := map[string]int{}
	for i, s := range rows {
		index[s.ID] = i
	}
	res, err := read(ctx, MemgraphQueryInput{Cypher: stretchMapTiles, Params: map[string]any{"surface": filter.Surface}, Limit: stretchesMaxRows, TimeoutMs: stretchesDefaultTO})
	if err != nil {
		return nil, fmt.Errorf("stretch tiles: %w", err)
	}
	a := in.Area
	minX, minY := a.LeftTopX, a.LeftTopY
	cols, nrows := int(math.Ceil(a.RightBottomX-minX)), int(math.Ceil(a.RightBottomY-minY))
	out.Columns, out.OriginX, out.OriginY = cols, minX, minY
	out.Truncated = res.Truncated || truncated
	var tiles []stretchTile
	for _, row := range res.Rows {
		if len(row) < 3 {
			continue
		}
		id, _ := row[0].(string)
		i, ok := index[id]
		x, y := optionalFloat(row[1]), optionalFloat(row[2])
		if !ok || x == nil || y == nil || *x < a.LeftTopX || *x >= a.RightBottomX || *y < a.LeftTopY || *y >= a.RightBottomY {
			continue
		}
		tiles = append(tiles, stretchTile{i, *x, *y})
	}

	chars, legend := stretchMapChars(rows, in.Mode)
	grid := make([][]int, nrows) // stretch index per cell, -1 none
	for r := range grid {
		grid[r] = make([]int, cols)
		for c := range grid[r] {
			grid[r][c] = -1
		}
	}
	prefer := func(a, b int) bool { // is stretch a better to show than b
		if b < 0 {
			return true
		}
		if rows[a].carries() != rows[b].carries() {
			return rows[a].carries()
		}
		return a < b
	}
	for _, t := range tiles {
		c, r := int(math.Floor(t.x-minX)), int(math.Floor(t.y-minY))
		if c >= cols || r >= nrows {
			continue
		}
		if prefer(t.stretch, grid[r][c]) {
			grid[r][c] = t.stretch
		}
	}
	cells := map[byte]int{}
	for r := range grid {
		var b strings.Builder
		for c := range grid[r] {
			ch := byte(' ')
			if i := grid[r][c]; i >= 0 {
				ch = chars[i]
				cells[ch]++
			}
			b.WriteByte(ch)
		}
		out.Rows = append(out.Rows, strings.TrimRight(b.String(), " "))
	}
	for _, e := range legend {
		if n := cells[e.Char[0]]; n > 0 {
			e.Cells = n
			out.Legend = append(out.Legend, e)
		}
	}
	return out, nil
}

// stretchMapChars assigns each stretch its drawing character and returns the
// legend entries (cell counts are filled in after drawing).
func stretchMapChars(rows []*stretchRow, mode MemgraphStretchMapMode) ([]byte, []*MemgraphStretchMapLegend) {
	chars := make([]byte, len(rows))
	var legend []*MemgraphStretchMapLegend
	if mode == MemgraphStretchMapModeStretch {
		for i, s := range rows {
			ch := byte('*')
			if i < len(stretchMapMarks) {
				ch = stretchMapMarks[i]
			}
			chars[i] = ch
			if ch == '*' {
				continue
			}
			legend = append(legend, &MemgraphStretchMapLegend{Char: string(ch), Label: s.ID,
				Detail: fmt.Sprintf("%s, ends %s", stretchLaneText(s), strings.ToLower(string(s.EndKind)))})
		}
		legend = append(legend, &MemgraphStretchMapLegend{Char: "*", Label: "more stretches than characters", Detail: ""})
		return chars, legend
	}
	totals := map[string]int{}
	for _, s := range rows {
		if !s.Mixed {
			for k, v := range s.tileItems {
				totals[k] += v
			}
		}
	}
	names := make([]string, 0, len(totals))
	for k := range totals {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})
	letter := map[string]byte{}
	for i, n := range names {
		ch := byte('*')
		if i < len(stretchMapLetters) {
			ch = stretchMapLetters[i]
			legend = append(legend, &MemgraphStretchMapLegend{Char: string(ch), Label: n})
		}
		letter[n] = ch
	}
	for i, s := range rows {
		switch {
		case s.Mixed:
			chars[i] = '#'
		case !s.carries():
			chars[i] = '.'
		default:
			// the single item of a pure stretch, or its most common one
			top := stretchItemCounts(s.tileItems)[0].Name
			chars[i] = letter[top]
		}
	}
	legend = append(legend,
		&MemgraphStretchMapLegend{Char: "#", Label: "mixed lane", Detail: "two or more items on one lane"},
		&MemgraphStretchMapLegend{Char: ".", Label: "empty stretch"},
		&MemgraphStretchMapLegend{Char: "*", Label: "more items than letters"})
	return chars, legend
}
