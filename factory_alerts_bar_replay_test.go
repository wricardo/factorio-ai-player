package main

import (
	"strings"
	"testing"
)

func serveFactoryAlertsBarReplay(t *testing.T, scenario string) *Resolver {
	t.Helper()
	return serveLuaFixture(t, "factory_alerts_bar_engine.lua", []string{"ALERT_SCENARIO=" + scenario}, func(command string) bool {
		return strings.Contains(command, "waiting_for_space_in_destination")
	})
}

// An inserter waiting for space in a bar-limited chest must name the cause
// instead of looking like a generic blocked output (factorio-ai-player-4rh).
func TestFactoryAlertsReportBarLimitedDropTarget(t *testing.T) {
	alerts, err := factoryAlertsQuery(serveFactoryAlertsBarReplay(t, "bar"), &FactoryAlertsInput{
		Surface: "nauvis",
		Force:   "player",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts.Alerts) != 1 {
		t.Fatalf("expected the waiting inserter, got %+v", alerts.Alerts)
	}
	alert := alerts.Alerts[0]
	if alert.Cause == nil || *alert.Cause != "DESTINATION_BAR_LIMITED" {
		t.Fatalf("cause = %v, want DESTINATION_BAR_LIMITED", alert.Cause)
	}
	if alert.Destination == nil {
		t.Fatalf("a bar-limited drop target must be reported, got %+v", alert)
	}
	if alert.Destination.Name != "wooden-chest" || alert.Destination.Bar != 1 || alert.Destination.UsableSlots != 0 {
		t.Fatalf("destination = %+v, want wooden-chest bar 1 with no usable slot", alert.Destination)
	}
}

// An inserter that reports no drop_target must fall back to reading the entity at
// its drop position; the old scanners may not populate drop_target.
func TestFactoryAlertsResolveDropTargetByPosition(t *testing.T) {
	alerts, err := factoryAlertsQuery(serveFactoryAlertsBarReplay(t, "fallback"), &FactoryAlertsInput{
		Surface: "nauvis",
		Force:   "player",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts.Alerts) != 1 {
		t.Fatalf("expected the waiting inserter, got %+v", alerts.Alerts)
	}
	alert := alerts.Alerts[0]
	if alert.Destination == nil || alert.Destination.Name != "wooden-chest" {
		t.Fatalf("drop position fallback must find the chest, got %+v", alert.Destination)
	}
	if alert.Cause == nil || *alert.Cause != "DESTINATION_BAR_LIMITED" {
		t.Fatalf("cause = %v, want DESTINATION_BAR_LIMITED", alert.Cause)
	}
}

// An unbarred drop target with free slots is a plain full destination: no cause,
// but the target is still reported so a caller can see what the inserter faces.
func TestFactoryAlertsSkipCauseForUnbarredDropTarget(t *testing.T) {
	alerts, err := factoryAlertsQuery(serveFactoryAlertsBarReplay(t, "ok"), &FactoryAlertsInput{
		Surface: "nauvis",
		Force:   "player",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts.Alerts) != 1 {
		t.Fatalf("expected the waiting inserter, got %+v", alerts.Alerts)
	}
	alert := alerts.Alerts[0]
	if alert.Cause != nil {
		t.Fatalf("cause = %v, want null", *alert.Cause)
	}
	if alert.Destination == nil || alert.Destination.Bar != 0 || alert.Destination.UsableSlots != 32 {
		t.Fatalf("destination = %+v, want bar 0 with 32 usable slots", alert.Destination)
	}
}
