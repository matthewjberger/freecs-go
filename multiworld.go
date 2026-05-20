package freecs

// MultiWorld groups several Worlds behind one shared entity allocator. Each
// child world has its own 64-component-per-world ceiling, so a multi-world
// project can register, for example, 40 gameplay components in a CoreWorld
// and 50 render components in a RenderWorld and reference both from the
// same entity. Per-world component access (Set, Get, Iter*) is unchanged;
// only entity lifetime moves up to the MultiWorld.
//
// MultiWorld owns nothing else (no tags, events, resources, or commands).
// Keep those on a "primary" child world if you want one place to consult.
//
// A MultiWorld must not be copied after first use. Children hold a pointer
// to the embedded allocator; copying the MultiWorld would silently alias
// every child to the original's allocator. The embedded noCopy sentinel
// makes go vet flag accidental copies.
type MultiWorld struct {
	_         noCopy
	allocator allocator
	worlds    []*World
	live      map[Entity]struct{}
}

// NewMultiWorld returns an empty MultiWorld. Add worlds with NewWorld before
// spawning anything.
func NewMultiWorld() *MultiWorld {
	return &MultiWorld{live: make(map[Entity]struct{})}
}

// NewWorld creates a fresh child World whose entity allocator is shared
// with this MultiWorld. Component registration on the new world is
// independent of every other child; bit positions start at zero.
func (m *MultiWorld) NewWorld() *World {
	world := newWorldWithAllocator(&m.allocator)
	m.worlds = append(m.worlds, world)
	return world
}

// Worlds returns the child worlds in creation order. The returned slice
// aliases internal state; do not modify it.
func (m *MultiWorld) Worlds() []*World { return m.worlds }

// Spawn allocates a new entity id from the shared allocator without placing
// it in any child world's archetype. The entity is tracked as live until
// Despawn returns it to the allocator, so Spawn followed by zero or more
// SpawnEntityInto calls does not leak the id.
func (m *MultiWorld) Spawn() Entity {
	entity := m.allocator.allocate()
	m.live[entity] = struct{}{}
	return entity
}

// Despawn removes entity from every child world (each removal no-ops if the
// entity is not present in that world) and returns the id to the shared
// allocator with a bumped generation. Returns true if the entity was tracked
// as live (whether or not any world held an archetype row for it).
func (m *MultiWorld) Despawn(entity Entity) bool {
	if _, ok := m.live[entity]; !ok {
		return false
	}
	delete(m.live, entity)
	for _, world := range m.worlds {
		despawnFromArchetype(world, entity)
	}
	m.allocator.deallocate(entity)
	return true
}

// IsLive reports whether entity was issued by Spawn and not yet Despawned.
func (m *MultiWorld) IsLive(entity Entity) bool {
	_, ok := m.live[entity]
	return ok
}

// Step advances the frame on every child world.
func (m *MultiWorld) Step() {
	for _, world := range m.worlds {
		world.Step()
	}
}
