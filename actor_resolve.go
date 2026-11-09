package main

import (
	"errors"
	"fmt"
)
// defaultActorPlayer returns the name of the first connected player, the
// default actor used when a required-actor mutation is called with a
// present-but-empty actor. Status.Players only carries connected players.
// It is an RCON read, so it can only run where a Resolver is available;
// resolver-free validators such as settingsActor keep rejecting empty.
func defaultActorPlayer(r *Resolver) (string, error) {
	status, err := command[Status](r, "status", nil)
	if err != nil {
		return "", err
	}
	if len(status.Players) == 0 {
		return "", errors.New("actor is empty and no player is connected")
	}
	return status.Players[0].Name, nil
}

// defaultActor resolves a present-but-empty actor to the first connected
// player, returning a new ActorInput and leaving the input untouched. A nil
// actor (optional-actor mutations) and a populated actor pass through
// unchanged, so the default never silently replaces an explicit choice and
// never applies where "no actor" is a meaningful selection.
func defaultActor(r *Resolver, actor *ActorInput) (*ActorInput, error) {
	if actor == nil {
		return nil, nil
	}
	player, character := actorFields(actor)
	if stringValueOr(player, "") == "" && stringValueOr(character, "") == "" {
		name, err := defaultActorPlayer(r)
		if err != nil {
			return nil, err
		}
		return &ActorInput{Player: &name}, nil
	}
	return actor, nil
}

// resolveActorCharacter turns the GraphQL pair of player and character into the
// single character unit number the game backend accepts. GraphQL inputs allow
// addressing an actor by player name, but the command line flags of most
// backend commands only take --character, so the player name is resolved to its
// current character here.
//
// Exactly one of player and character must be given, using the same wording as
// the settings commands. A present-but-empty pair defaults to the first
// connected player, so required-actor mutations can omit the actor body.

func resolveActorCharacter(r *Resolver, player, character *string) (string, error) {
	name := stringValueOr(player, "")
	unit := stringValueOr(character, "")
	if name == "" && unit == "" {
		resolved, err := defaultActorPlayer(r)
		if err != nil {
			return "", err
		}
		name = resolved
	}
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
