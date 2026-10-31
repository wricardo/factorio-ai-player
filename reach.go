package main

import (
	"fmt"
	"os"
	"strings"
)

// Reach checks are a policy layer, not an engine guarantee: Factorio's script
// API executes surface/entity operations regardless of actor distance.
//
// Default: reach checks are bypassed. Set FACTORIO_ENFORCE_REACH=1 (also
// true/yes/on, case-insensitive) to enforce actor build and interaction reach;
// unset it to return to the permissive default. The flag is read per program
// build, so a long-running `serve` process needs a restart to change it.
func reachDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FACTORIO_ENFORCE_REACH"))) {
	case "1", "true", "yes", "on":
		return false
	}
	return true
}

// reachViolation renders the Lua condition that is true when actor cannot
// reach entity, or the literal false when checks are disabled.
func reachViolation(actor, entity string) string {
	if reachDisabled() {
		return "false"
	}
	return fmt.Sprintf("not %s.can_reach_entity(%s)", actor, entity)
}

// reachable renders the positive Lua reach condition, or true when checks are
// disabled. Use it where the check selects behaviour instead of failing.
func reachable(actor, entity string) string {
	if reachDisabled() {
		return "true"
	}
	return fmt.Sprintf("%s.can_reach_entity(%s)", actor, entity)
}

// distanceViolation renders dx*dx+dy*dy>distance*distance, or false when
// checks are disabled. dx and dy must already be Lua locals.
func distanceViolation(dx, dy, distance string) string {
	if reachDisabled() {
		return "false"
	}
	return fmt.Sprintf("%s*%s+%s*%s>(%s)*(%s)", dx, dx, dy, dy, distance, distance)
}
