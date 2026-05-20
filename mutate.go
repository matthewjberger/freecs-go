package freecs

// Get returns a pointer to component T on entity if the entity is live
// and has T. The pointer aliases the column's backing memory and is
// invalidated the next time the archetype grows or migrates this entity.
// Reading through the pointer does not mark the slot as changed; use
// GetMut for that.
//
// Returns nil, false when T is not registered on this world. That case
// is meaningful in multi-world setups: a query against a sibling world
// that does not carry T answers "no" rather than panicking.
func Get[T any](world *World, entity Entity) (*T, bool) {
	info, ok := componentInfoFor[T](world)
	if !ok {
		return nil, false
	}
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return nil, false
	}
	table := world.tables[tableIndex]
	if table.Mask&info.mask == 0 {
		return nil, false
	}
	column := table.columns[info.bitIndex]
	return (*T)(column.at(arrayIndex)), true
}

// GetMut returns a pointer to component T on entity and stamps the slot
// with the current tick so change-detection queries will see it as
// modified this frame. Returns nil, false for stale handles, missing
// components, or types not registered on this world.
func GetMut[T any](world *World, entity Entity) (*T, bool) {
	info, ok := componentInfoFor[T](world)
	if !ok {
		return nil, false
	}
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return nil, false
	}
	table := world.tables[tableIndex]
	if table.Mask&info.mask == 0 {
		return nil, false
	}
	column := table.columns[info.bitIndex]
	column.markChanged(arrayIndex, world.currentTick)
	return (*T)(column.at(arrayIndex)), true
}

// Has reports whether entity has component T. False for stale handles or
// for types not registered on this world.
func Has[T any](world *World, entity Entity) bool {
	info, ok := componentInfoFor[T](world)
	if !ok {
		return false
	}
	tableIndex, _, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	return world.tables[tableIndex].Mask&info.mask != 0
}

// HasComponents reports whether entity has every component in mask.
func (w *World) HasComponents(entity Entity, mask Mask) bool {
	tableIndex, _, ok := w.entityLocs.get(entity)
	if !ok {
		return false
	}
	return w.tables[tableIndex].Mask&mask == mask
}

// ComponentMask returns the archetype mask of entity, or (0, false) if
// the handle is stale.
func (w *World) ComponentMask(entity Entity) (Mask, bool) {
	tableIndex, _, ok := w.entityLocs.get(entity)
	if !ok {
		return 0, false
	}
	return w.tables[tableIndex].Mask, true
}

// Set writes value into entity's component T, adding the component first
// if it is missing. Stamps the slot with the current tick. No-op for
// stale handles.
func Set[T any](world *World, entity Entity, value T) {
	info := mustComponentInfo[T](world)
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return
	}
	table := world.tables[tableIndex]
	if table.Mask&info.mask != 0 {
		column := table.columns[info.bitIndex]
		*(*T)(column.at(arrayIndex)) = value
		column.markChanged(arrayIndex, world.currentTick)
		return
	}
	world.AddComponents(entity, info.mask)
	tableIndex, arrayIndex, ok = world.entityLocs.get(entity)
	if !ok {
		return
	}
	table = world.tables[tableIndex]
	column := table.columns[info.bitIndex]
	*(*T)(column.at(arrayIndex)) = value
	column.markChanged(arrayIndex, world.currentTick)
}

// Add gives entity component T with the zero value if it does not
// already have it. Equivalent to world.AddComponents(entity,
// MaskOf[T](world)).
func Add[T any](world *World, entity Entity) {
	info := mustComponentInfo[T](world)
	world.AddComponents(entity, info.mask)
}

// Remove strips component T from entity. No-op if entity does not have it.
func Remove[T any](world *World, entity Entity) {
	info := mustComponentInfo[T](world)
	world.RemoveComponents(entity, info.mask)
}
