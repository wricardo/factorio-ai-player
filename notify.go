package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// announceMutation prints an in-game notification after a successful mutation.
// message is printed verbatim when nonempty; otherwise the generated summary is
// used. Notification failures never change the mutation outcome.
func announceMutation(r *Resolver, message *string, generated string) {
	text := generated
	if message != nil && *message != "" {
		text = *message
	}
	printGameMessage(r, text)
}

func printGameMessage(r *Resolver, text string) {
	if text == "" {
		return
	}
	// game.print returns no console output, so the multi-packet protocol would
	// wait for a response that never arrives.
	_, _ = execute(r.addr, r.password, "/silent-command game.print("+luaString(text)+")", true)
}

// announce prints a caller-supplied message to every connected player in chat.
// Unlike mutation notifications, a failed print fails the mutation.
func announce(r *Resolver, message string) (*AnnounceResult, error) {
	if strings.TrimSpace(message) == "" {
		return nil, errors.New("message must not be empty")
	}
	if _, err := execute(r.addr, r.password, "/silent-command game.print("+luaString(message)+")", false); err != nil {
		return nil, fmt.Errorf("print message: %w", err)
	}
	return &AnnounceResult{Message: message}, nil
}

// mutationSummary derives a short human-readable summary from a mutation result.
func mutationSummary(name string, result any) string {
	switch r := result.(type) {
	case *ActionResult:
		return actionResultSummary(name, r)
	case *ClearAreaResult:
		return fmt.Sprintf("cleared %s: %d trees and %d rocks removed", r.Surface, r.TreesRemoved, r.RocksRemoved)
	case *HarvestAreaResult:
		return fmt.Sprintf("harvested %s: %d trees and %d rocks mined, %d out of reach, %d failed",
			r.Surface, r.TreesMined, r.RocksMined, r.OutOfReach, r.Failed)
	case *RemoveResult:
		return "removed " + r.Entity + positionText(r.Position)
	case *PickupAreaResult:
		return fmt.Sprintf("picked up on %s: %d stacks found, %d out of reach",
			r.Surface, r.StacksFound, r.StacksOutOfReach)
	case *ResearchState:
		if name == "cancel-research" {
			return "research canceled: " + r.Technology
		}
		return "research started: " + r.Technology
	case *CraftQueue:
		return fmt.Sprintf("canceled crafts; %s remain queued", numberValue(r.QueueSize))
	case *PlaceManyResult:
		return placeManySummary(r)
	case *NavigationJob:
		return navigationSummary(r)
	}
	return name
}

func actionResultSummary(name string, r *ActionResult) string {
	at := positionText(r.Position)
	switch name {
	case "place":
		return "placed " + stringValue(r.Name) + at
	case "fuel":
		return fmt.Sprintf("inserted %s %s into %s%s", numberValue(r.Inserted), stringValue(r.Item), stringValue(r.Name), at)
	case "craft":
		return fmt.Sprintf("queued %s of %s %s", numberValue(r.Started), numberValue(r.Requested), stringValue(r.Recipe))
	case "character-create":
		return fmt.Sprintf("created character %s for %s%s", stringValue(r.Character), stringValue(r.Player), at)
	case "character-spawn":
		return "spawned character " + stringValue(r.Character) + at
	case "player-restore-character":
		return fmt.Sprintf("restored character %s for %s%s", stringValue(r.Character), stringValue(r.Player), at)
	case "character-return":
		return fmt.Sprintf("returned %s stacks to %s", intValue(r.ReturnedStacks), stringValue(r.Player))
	case "mine":
		return "mined " + stringValue(r.Entity) + at
	case "pickup":
		return fmt.Sprintf("picked up %s %s%s", numberValue(r.Moved), stringValue(r.Item), at)
	case "belt-take":
		return fmt.Sprintf("took %s %s from belt%s", numberValue(r.Moved), stringValue(r.Item), at)
	case "rotate":
		return fmt.Sprintf("rotated %s by %s quarter turn(s)%s", stringValue(r.Name), intValue(r.Turns), at)
	case "transfer":
		if r.Direction != nil && *r.Direction == "to-player" {
			return fmt.Sprintf("took %s %s from %s%s", numberValue(r.Moved), stringValue(r.Item), stringValue(r.Entity), at)
		}
		return fmt.Sprintf("moved %s %s into %s%s", numberValue(r.Moved), stringValue(r.Item), stringValue(r.Entity), at)
	case "set-recipe":
		return fmt.Sprintf("set %s recipe to %s%s", stringValue(r.Entity), stringValue(r.Recipe), at)
	case "equip":
		where := "into " + stringValue(r.Inventory)
		if r.Direction != nil && *r.Direction == "to-main" {
			where = "from " + stringValue(r.Inventory) + " to main"
		}
		return fmt.Sprintf("moved %s %s %s", numberValue(r.Moved), stringValue(r.Item), where)
	}
	return name
}

func navigationSummary(r *NavigationJob) string {
	summary := fmt.Sprintf("navigation %s: %s%s", r.ID, r.Status, positionText(r.Destination))
	if r.Reason != nil && *r.Reason != "" {
		summary += " (" + *r.Reason + ")"
	}
	return summary
}

func positionText(p *Position) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" at (%s, %s)", numberValue(&p.X), numberValue(&p.Y))
}

func stringValue(v *string) string {
	if v == nil {
		return "?"
	}
	return *v
}

func numberValue(v *float64) string {
	if v == nil {
		return "?"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

func intValue(v *int) string {
	if v == nil {
		return "?"
	}
	return strconv.Itoa(*v)
}
