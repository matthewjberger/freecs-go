package freecs

// Entity is a generational handle. It owns no data and has no methods.
// The id locates the entity's slot in the storage; the generation
// distinguishes a live entity from a previously-recycled one at the same id.
type Entity struct {
	ID         uint32
	Generation uint32
}

// allocator hands out Entity handles. Freed handles return to a free list
// with their generation pre-bumped so the next allocation reuses the id
// with a fresh generation.
type allocator struct {
	nextID uint32
	free   []freedSlot
}

type freedSlot struct {
	id         uint32
	generation uint32
}

func (a *allocator) allocate() Entity {
	if n := len(a.free); n > 0 {
		slot := a.free[n-1]
		a.free = a.free[:n-1]
		return Entity{ID: slot.id, Generation: slot.generation}
	}
	id := a.nextID
	a.nextID++
	return Entity{ID: id, Generation: 0}
}

func (a *allocator) deallocate(entity Entity) {
	a.free = append(a.free, freedSlot{id: entity.ID, generation: entity.Generation + 1})
}

// entityLocation routes an entity id to its components.
type entityLocation struct {
	generation uint32
	tableIndex uint32
	arrayIndex uint32
	allocated  bool
}

type entityLocations struct {
	locations []entityLocation
}

func (e *entityLocations) ensureSlot(id uint32) {
	needed := int(id) + 1
	if needed > len(e.locations) {
		grown := make([]entityLocation, needed)
		copy(grown, e.locations)
		e.locations = grown
	}
}

// get returns (tableIndex, arrayIndex, ok). Stale handles fail closed.
func (e *entityLocations) get(entity Entity) (int, int, bool) {
	if int(entity.ID) >= len(e.locations) {
		return 0, 0, false
	}
	location := e.locations[entity.ID]
	if !location.allocated || location.generation != entity.Generation {
		return 0, 0, false
	}
	return int(location.tableIndex), int(location.arrayIndex), true
}

func (e *entityLocations) set(entity Entity, tableIndex, arrayIndex int) {
	e.ensureSlot(entity.ID)
	e.locations[entity.ID] = entityLocation{
		generation: entity.Generation,
		tableIndex: uint32(tableIndex),
		arrayIndex: uint32(arrayIndex),
		allocated:  true,
	}
}

func (e *entityLocations) markDeallocated(id uint32) {
	if int(id) < len(e.locations) {
		e.locations[id].allocated = false
	}
}
