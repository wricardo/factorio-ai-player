package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func navigationStart(ctx context.Context, r *Resolver, input NavigateStartInput) (*NavigationJob, error) {
	character, err := strconv.ParseUint(input.Character, 10, 32)
	if err != nil || character == 0 {
		return nil, fmt.Errorf("character must be a unit number from 1 to 4294967295")
	}
	if math.IsNaN(input.Tolerance) || math.IsInf(input.Tolerance, 0) || input.Tolerance <= 0 || input.Tolerance > 10 {
		return nil, fmt.Errorf("tolerance must be greater than 0 and at most 10")
	}
	forms := 0
	if input.X != nil || input.Y != nil {
		forms++
	}
	if input.TargetCharacter != nil {
		forms++
	}
	if input.TargetEntity != nil {
		forms++
	}
	if forms != 1 {
		return nil, fmt.Errorf("provide exactly one of x and y, targetCharacter, or targetEntity")
	}
	if (input.X == nil) != (input.Y == nil) {
		return nil, fmt.Errorf("x and y must be provided together")
	}
	targetCharacter, targetEntity := "nil", "nil"
	if input.TargetCharacter != nil {
		unit, err := strconv.ParseUint(*input.TargetCharacter, 10, 32)
		if err != nil || unit == 0 {
			return nil, fmt.Errorf("targetCharacter must be a unit number from 1 to 4294967295")
		}
		if unit == character {
			return nil, fmt.Errorf("targetCharacter must differ from character")
		}
		if input.Tolerance < 1 {
			return nil, fmt.Errorf("tolerance must be at least 1 when targetCharacter is set because characters collide")
		}
		targetCharacter = strconv.FormatUint(unit, 10)
	}
	if input.TargetEntity != nil {
		unit, err := strconv.ParseUint(*input.TargetEntity, 10, 32)
		if err != nil || unit == 0 {
			return nil, fmt.Errorf("targetEntity must be a unit number from 1 to 4294967295")
		}
		if unit == character {
			return nil, fmt.Errorf("targetEntity must differ from character")
		}
		targetEntity = strconv.FormatUint(unit, 10)
	}
	if (input.X != nil && (math.IsNaN(*input.X) || math.IsInf(*input.X, 0))) || (input.Y != nil && (math.IsNaN(*input.Y) || math.IsInf(*input.Y, 0))) {
		return nil, fmt.Errorf("destination coordinates must be finite")
	}
	if input.TimeoutTicks < 1 || input.TimeoutTicks > 216000 {
		return nil, fmt.Errorf("timeoutTicks must be from 1 to 216000")
	}
	if input.WaitTimeoutSeconds < 1 || input.WaitTimeoutSeconds > 3600 {
		return nil, fmt.Errorf("waitTimeoutSeconds must be from 1 to 3600")
	}
	x, y := "nil", "nil"
	if input.X != nil {
		x = strconv.FormatFloat(*input.X, 'g', -1, 64)
	}
	if input.Y != nil {
		y = strconv.FormatFloat(*input.Y, 'g', -1, 64)
	}
	args := fmt.Sprintf("%d,%s,%s,%s,%d,%s,%s", character, x, y,
		strconv.FormatFloat(input.Tolerance, 'g', -1, 64), input.TimeoutTicks, targetCharacter, targetEntity)
	job, err := navigationCallContext(ctx, r, "start", args)
	if err != nil {
		return job, err
	}
	announceMutation(r, input.Message, navigationSummary(job))
	if !input.Wait {
		return job, nil
	}
	return waitForNavigation(ctx, job, time.Duration(input.WaitTimeoutSeconds)*time.Second, func(ctx context.Context, id string) (*NavigationJob, error) {
		return navigationCallContext(ctx, r, "status", luaString(id))
	})
}

func navigationStatus(r *Resolver, id string) (*NavigationJob, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("navigation job ID must not be empty")
	}
	return navigationCall(r, "status", luaString(id))
}

func navigationCancel(r *Resolver, id string, message *string) (*NavigationJob, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("navigation job ID must not be empty")
	}
	job, err := navigationCall(r, "cancel", luaString(id))
	if err != nil {
		return nil, err
	}
	announceMutation(r, message, navigationSummary(job))
	return job, nil
}

func waitForNavigation(ctx context.Context, job *NavigationJob, timeout time.Duration, status func(context.Context, string) (*NavigationJob, error)) (*NavigationJob, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for job.Status == "PATHFINDING" || job.Status == "MOVING" {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return nil, fmt.Errorf("navigation job %s may still be running: %w", job.ID, ctx.Err())
			}
			return job, nil
		case <-ticker.C:
			latest, err := status(waitCtx, job.ID)
			if err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("navigation job %s may still be running: %w", job.ID, ctx.Err())
				}
				if waitCtx.Err() != nil {
					return job, nil
				}
				return nil, fmt.Errorf("navigation job %s may still be running: %w", job.ID, err)
			}
			job = latest
		}
	}
	return job, nil
}

func navigationCall(r *Resolver, method, args string) (*NavigationJob, error) {
	return navigationCallContext(context.Background(), r, method, args)
}

func navigationCallContext(ctx context.Context, r *Resolver, method, args string) (*NavigationJob, error) {
	lua := luaFail + fmt.Sprintf(`local interface=remote.interfaces["factorio_ai_navigation"]; if not interface or not interface[%s] then fail("factorio_ai_navigation mod or remote function is unavailable"); return end; rcon.print(helpers.table_to_json(remote.call("factorio_ai_navigation",%s,%s)))`, luaString(method), luaString(method), args)
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	var job NavigationJob
	if err := json.Unmarshal([]byte(response), &job); err != nil {
		return nil, fmt.Errorf("decode navigation response: %w", err)
	}
	return &job, nil
}
