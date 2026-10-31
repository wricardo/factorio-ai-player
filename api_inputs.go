package main

import (
	"errors"
	"math"
)

func actorFields(actor *ActorInput) (player, character *string) {
	if actor == nil {
		return nil, nil
	}
	return actor.Player, actor.Character
}

func entityRefFields(target *EntityRefInput) (surface, entity, unitNumber *string, x, y *float64) {
	if target == nil {
		return nil, nil, nil, nil, nil
	}
	if target.Position != nil {
		x, y = &target.Position.X, &target.Position.Y
	}
	return target.Surface, target.Entity, target.UnitNumber, x, y
}

func settingsTargetFromRef(actor *ActorInput, target *EntityRefInput) (settingsTarget, error) {
	player, character := actorFields(actor)
	surface, entity, unitNumber, x, y := entityRefFields(target)
	if surface != nil && *surface == "" {
		return settingsTarget{}, errors.New("surface must not be empty")
	}
	return settingsTargetFrom(player, character, surface, entity, unitNumber, x, y)
}

func entityRefCoordinates(target *EntityRefInput) (float64, float64) {
	if target == nil || target.Position == nil {
		return math.NaN(), math.NaN()
	}
	return target.Position.X, target.Position.Y
}

func settingsActorFrom(actor *ActorInput) (uint64, string, error) {
	player, character := actorFields(actor)
	return settingsActor(stringValueOr(character, ""), stringValueOr(player, ""))
}

func optionalActorFrom(actor *ActorInput) (uint64, string, error) {
	player, character := actorFields(actor)
	return optionalActor(stringValueOr(character, ""), stringValueOr(player, ""))
}

func resolveActorCharacterInput(r *Resolver, actor *ActorInput) (string, error) {
	player, character := actorFields(actor)
	return resolveActorCharacter(r, player, character)
}

func actorSelector(obj *Actor) *ActorInput {
	if obj == nil {
		return &ActorInput{}
	}
	if obj.Player != nil && *obj.Player != "" {
		return &ActorInput{Player: obj.Player}
	}
	return &ActorInput{Character: obj.Character}
}

func actorValue(actor *ActorInput) ActorInput {
	if actor == nil {
		return ActorInput{}
	}
	return *actor
}
