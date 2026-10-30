package main

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func beltThroughputStart(ctx context.Context, r *Resolver, input BeltThroughputStartInput) (*BeltThroughputMeasurement, error) {
	if strings.TrimSpace(input.Surface) == "" {
		return nil, fmt.Errorf("surface is required")
	}
	surface, x, y, err := resolveBeltAddress(r, input.Surface, input.UnitNumber, input.X, input.Y)
	if err != nil {
		return nil, err
	}
	if input.UnitNumber != nil && *input.UnitNumber != "" {
		belt, err := entityByUnitNumber(r, *input.UnitNumber, input.Surface)
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
	if input.WaitTimeoutSeconds < 1 || input.WaitTimeoutSeconds > 600 {
		return nil, fmt.Errorf("waitTimeoutSeconds must be from 1 to 600")
	}
	args := fmt.Sprintf("%s,%s,%s,%d", luaString(surface),
		strconv.FormatFloat(x, 'g', -1, 64), strconv.FormatFloat(y, 'g', -1, 64), input.DurationTicks)
	measurement, err := beltThroughputCall(ctx, r, "start", args)
	if err != nil || !input.Wait || measurement.Status != "RUNNING" {
		return measurement, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(input.WaitTimeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for measurement.Status == "RUNNING" {
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
rcon.print(helpers.table_to_json(remote.call("factorio_ai_belt_flow",%s,%s)))`, luaString(method), luaString(method), args)
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[BeltThroughputMeasurement](response)
}
