package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"strconv"
)

type areaBounds struct {
	LeftTopX, LeftTopY, RightBottomX, RightBottomY float64
}

type areaBoundFlags struct {
	leftTopX, leftTopY, rightBottomX, rightBottomY *float64
}

func addAreaBoundFlags(fs *flag.FlagSet) areaBoundFlags {
	return areaBoundFlags{
		leftTopX:     fs.Float64("left-top-x", math.NaN(), "Area left-top X (required)"),
		leftTopY:     fs.Float64("left-top-y", math.NaN(), "Area left-top Y (required)"),
		rightBottomX: fs.Float64("right-bottom-x", math.NaN(), "Area right-bottom X (required)"),
		rightBottomY: fs.Float64("right-bottom-y", math.NaN(), "Area right-bottom Y (required)"),
	}
}

func (f areaBoundFlags) bounds() areaBounds {
	return areaBounds{*f.leftTopX, *f.leftTopY, *f.rightBottomX, *f.rightBottomY}
}

func (a areaBounds) validate() error {
	for _, coord := range []float64{a.LeftTopX, a.LeftTopY, a.RightBottomX, a.RightBottomY} {
		if math.IsNaN(coord) || math.IsInf(coord, 0) {
			return errors.New("area coordinates must be finite")
		}
	}
	if a.LeftTopX >= a.RightBottomX || a.LeftTopY >= a.RightBottomY || a.RightBottomX-a.LeftTopX > 256 || a.RightBottomY-a.LeftTopY > 256 {
		return errors.New("area must have positive dimensions no greater than 256 tiles")
	}
	return nil
}

func (a areaBounds) lua() string {
	format := func(n float64) string { return strconv.FormatFloat(n, 'g', -1, 64) }
	return fmt.Sprintf("{{%s,%s},{%s,%s}}", format(a.LeftTopX), format(a.LeftTopY), format(a.RightBottomX), format(a.RightBottomY))
}
