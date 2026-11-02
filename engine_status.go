package main

import "strings"

func engineStatus(raw string) *EngineStatus {
	name := strings.ToUpper(strings.TrimSpace(raw))
	category := StatusCategoryOther
	terminal := false
	switch name {
	case "PENDING", "QUEUED", "PATHFINDING":
		category = StatusCategoryPending
		terminal = false
	case "RUNNING", "MOVING", "ACTIVE", "SHOOTING":
		category = StatusCategoryRunning
		terminal = false
	case "COMPLETE", "COMPLETED", "SUCCEEDED", "ARRIVED", "FINISHED":
		category = StatusCategorySucceeded
	case "FAILED", "INVALIDATED", "TIMED_OUT", "TARGET_MISSING", "NO_PATH", "CHARACTER_MISSING":
		category = StatusCategoryFailed
	case "CANCELLED", "CANCELED":
		category = StatusCategoryCancelled
	case "BLOCKED":
		category = StatusCategoryBlocked
	case "IDLE":
		category = StatusCategoryIdle
	}
	if category != StatusCategoryOther && category != StatusCategoryPending && category != StatusCategoryRunning {
		terminal = true
	}
	if name == "" {
		return &EngineStatus{Category: category, Terminal: terminal}
	}
	return &EngineStatus{Category: category, RawName: &raw, Terminal: terminal}
}

func statusRawName(status *EngineStatus) string {
	if status == nil || status.RawName == nil {
		return ""
	}
	return *status.RawName
}
