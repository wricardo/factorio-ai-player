package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func beltThroughputStart(ctx context.Context, r *Resolver, input BeltThroughputStartInput) (*BeltThroughputMeasurement, error) {
	surfaceField, _, unitNumber, inputX, inputY := entityRefFields(input.Target)
	surface := stringValueOr(surfaceField, "nauvis")
	if strings.TrimSpace(surface) == "" {
		return nil, fmt.Errorf("surface is required")
	}
	resolvedSurface, x, y, err := resolveBeltAddress(r, surface, unitNumber, inputX, inputY)
	if err != nil {
		return nil, err
	}
	surface = resolvedSurface
	if unitNumber != nil && *unitNumber != "" {
		belt, err := entityByUnitNumber(r, *unitNumber, surface)
		if err != nil {
			return nil, err
		}
		if belt.Type == nil || *belt.Type != "transport-belt" {
			return nil, fmt.Errorf("entity is not a transport belt")
		}
	}
	for _, value := range []float64{x, y} {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1000000 {
			return nil, fmt.Errorf("belt coordinates must be finite and within +/-1000000")
		}
	}
	if input.DurationTicks < 1 || input.DurationTicks > 36000 {
		return nil, fmt.Errorf("durationTicks must be from 1 to 36000")
	}
	if input.WaitTimeoutSeconds < 1 {
		return nil, fmt.Errorf("waitTimeoutSeconds must be at least 1")
	}
	args := fmt.Sprintf("%s,%s,%s,%d", luaString(surface),
		strconv.FormatFloat(x, 'g', -1, 64), strconv.FormatFloat(y, 'g', -1, 64), input.DurationTicks)
	measurement, err := beltThroughputCall(ctx, r, "start", args)
	if err != nil || !input.Wait || measurement.Status == nil || measurement.Status.RawName == nil || *measurement.Status.RawName != "RUNNING" {
		return measurement, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, clampWaitSeconds(input.WaitTimeoutSeconds))
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for measurement.Status != nil && measurement.Status.RawName != nil && *measurement.Status.RawName == "RUNNING" {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return nil, fmt.Errorf("belt measurement %s may still be running: %w", measurement.ID, ctx.Err())
			}
			return measurement, nil
		case <-ticker.C:
			latest, err := beltThroughputStatus(waitCtx, r, measurement.ID)
			if err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("belt measurement %s may still be running: %w", measurement.ID, ctx.Err())
				}
				if waitCtx.Err() != nil {
					return measurement, nil
				}
				return nil, fmt.Errorf("belt measurement %s may still be running: %w", measurement.ID, err)
			}
			measurement = latest
		}
	}
	return measurement, nil
}

func beltThroughputStatus(ctx context.Context, r *Resolver, id string) (*BeltThroughputMeasurement, error) {
	if len(id) == 0 || len(id) > 32 || strings.Trim(id, "0123456789") != "" {
		return nil, fmt.Errorf("belt measurement ID must contain 1 to 32 decimal digits")
	}
	return beltThroughputCall(ctx, r, "status", luaString(id))
}

func beltThroughputCall(ctx context.Context, r *Resolver, method, args string) (*BeltThroughputMeasurement, error) {
	lua := luaFail + fmt.Sprintf(`
local interface=remote.interfaces["factorio_ai_belt_flow"]
if not interface or not interface.version or not interface[%s] then fail("belt measurement requires updated factorio_ai_navigation loaded in this save (factorio_ai_belt_flow protocol 1)"); return end
if remote.call("factorio_ai_belt_flow","version") ~= 1 then fail("unsupported factorio_ai_belt_flow protocol; expected 1"); return end
local result=remote.call("factorio_ai_belt_flow",%s,%s)
local raw=result.status
local category="OTHER"
if raw=="RUNNING" then category="RUNNING" elseif raw=="COMPLETE" then category="SUCCEEDED" elseif raw=="INVALIDATED" then category="FAILED" end
result.status={category=category,rawName=raw,terminal=raw~="RUNNING"}
result.tick=game.tick
rcon.print(helpers.table_to_json(result))`, luaString(method), luaString(method), args)
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltThroughputMeasurement](response)
}

// beltStretchMeasure pairs a stretch snapshot with a measurement started on one
// of its transport-belt tiles, so a snapshot mixture can be checked over time.
func beltStretchMeasure(ctx context.Context, r *Resolver, input BeltStretchMeasureInput) (*BeltStretchMeasurement, error) {
	if input.DurationTicks < 1 || input.DurationTicks > 36000 {
		return nil, fmt.Errorf("durationTicks must be from 1 to 36000")
	}
	if input.WaitTimeoutSeconds < 1 {
		return nil, fmt.Errorf("waitTimeoutSeconds must be at least 1")
	}
	surfaceField, _, unitNumber, inputX, inputY := entityRefFields(input.Target)
	stretch, err := beltStretchQuery(r, stringValueOr(surfaceField, "nauvis"), inputX, inputY, unitNumber, 1000)
	if err != nil {
		return nil, err
	}
	node, err := selectStretchSample(stretch.Nodes, input.Sample)
	if err != nil {
		return nil, err
	}
	surface := stretch.Surface
	measurement, err := beltThroughputStart(ctx, r, BeltThroughputStartInput{
		Target: &EntityRefInput{
			Surface:  &surface,
			Position: &PositionInput{X: node.Position.X, Y: node.Position.Y},
		},
		DurationTicks:      input.DurationTicks,
		Wait:               input.Wait,
		WaitTimeoutSeconds: input.WaitTimeoutSeconds,
	})
	if err != nil {
		return nil, err
	}
	return &BeltStretchMeasurement{Stretch: stretch, Measurement: measurement}, nil
}

// selectStretchSample picks one measurable tile of a stretch. Only normal
// transport belts carry the two-line geometry beltThroughputStart samples.
func selectStretchSample(nodes []*BeltStretchNode, sample BeltStretchSample) (*BeltStretchNode, error) {
	measurable := make([]*BeltStretchNode, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && node.Type == "transport-belt" && node.Position != nil {
			measurable = append(measurable, node)
		}
	}
	if len(measurable) == 0 {
		return nil, errors.New("stretch has no transport-belt tile to measure; splitters, underground belts and loaders cannot be measured")
	}
	switch sample {
	case BeltStretchSampleFirst:
		return measurable[0], nil
	case BeltStretchSampleLast:
		return measurable[len(measurable)-1], nil
	case BeltStretchSampleMiddle:
		return measurable[len(measurable)/2], nil
	default:
		return nil, fmt.Errorf("unknown stretch sample %q", sample)
	}
}
