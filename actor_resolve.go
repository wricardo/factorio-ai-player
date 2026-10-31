package main

import (
	"fmt"
)

// resolveActorCharacter turns the GraphQL pair of player and character into the
// single character unit number the game backend accepts. GraphQL inputs allow
// addressing an actor by player name, but the command line flags of most
// backend commands only take --character, so the player name is resolved to its
// current character here.
//
// Exactly one of player and character must be given, using the same wording as
// the settings commands.
func resolveActorCharacter(r *Resolver, player, character *string) (string, error) {
	name := stringValueOr(player, "")
	unit := stringValueOr(character, "")
	if _, _, err := settingsActor(unit, name); err != nil {
		return "", err
	}
	if unit != "" {
		return unit, nil
	}
	actor, err := command[Actor](r, "player", ActorInput{Player: &name})
	if err != nil {
		return "", err
	}
	if actor == nil || actor.Character == nil || *actor.Character == "" {
		return "", fmt.Errorf("player %s has no character", name)
	}
	return *actor.Character, nil
}
