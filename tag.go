package freecs

import (
	"iter"
	"reflect"
)

// Tags are lightweight markers stored in side hash sets rather than as bits
// in the archetype mask. Flipping a tag costs an O(1) hash op instead of an
// archetype migration, making them the right tool for state that changes
// frequently (selection, alertness, frame-local flags).
//
// Each tag is identified by a Go marker type T. Define an empty struct per
// tag (`type Player struct{}`) and use it as the type parameter to every tag
// method. The world holds one map[Entity]struct{} per tag type.

func tagSetFor[T any](world *World) map[Entity]struct{} {
	key := reflect.TypeOf((*T)(nil)).Elem()
	if set, ok := world.tagSets[key]; ok {
		return set
	}
	set := make(map[Entity]struct{})
	world.tagSets[key] = set
	return set
}

// AddTag adds tag T to entity. No-op for stale handles.
func AddTag[T any](world *World, entity Entity) {
	if _, _, ok := world.entityLocs.get(entity); !ok {
		return
	}
	tagSetFor[T](world)[entity] = struct{}{}
}

// RemoveTag strips tag T from entity. Returns true if the tag was present.
func RemoveTag[T any](world *World, entity Entity) bool {
	set := tagSetFor[T](world)
	if _, ok := set[entity]; !ok {
		return false
	}
	delete(set, entity)
	return true
}

// HasTag reports whether entity carries tag T. False for stale handles
// because tag sets are cleared during Despawn.
func HasTag[T any](world *World, entity Entity) bool {
	_, ok := tagSetFor[T](world)[entity]
	return ok
}

// QueryTag yields every entity carrying tag T. Iteration order matches the
// underlying map and is therefore not stable across runs.
func QueryTag[T any](world *World) iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		for entity := range tagSetFor[T](world) {
			if !yield(entity) {
				return
			}
		}
	}
}

// CountTag returns how many entities currently carry tag T.
func CountTag[T any](world *World) int {
	return len(tagSetFor[T](world))
}
