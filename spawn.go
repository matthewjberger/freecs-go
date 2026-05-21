package freecs

import "math/bits"

// Spawn allocates a new entity and places it in the archetype identified
// by mask. Every component listed in mask is initialized to the zero value
// of its type; use Set or SpawnBatch with an initializer to fill them in.
func (w *World) Spawn(mask Mask) Entity {
	w.guardStructuralMutation("Spawn")
	entity := w.allocator.allocate()
	w.placeEntityInArchetype(entity, mask)
	return entity
}

// SpawnEntityInto places an externally-allocated entity into this world's
// archetype for mask. Pair it with [MultiWorld.Spawn], which mints an
// entity from a shared allocator that the caller then places into one or
// more worlds.
//
// Panics if the entity is already placed in this world. Use AddComponents
// or RemoveComponents to migrate a placed entity between archetypes.
func (w *World) SpawnEntityInto(entity Entity, mask Mask) {
	w.guardStructuralMutation("SpawnEntityInto")
	if _, _, ok := w.entityLocs.get(entity); ok {
		panic("freecs: SpawnEntityInto called for an entity that already has a row in this world; use AddComponents/RemoveComponents to change its archetype")
	}
	w.placeEntityInArchetype(entity, mask)
}

func (w *World) placeEntityInArchetype(entity Entity, mask Mask) {
	tableIndex := w.getOrCreateTable(mask)
	tick := w.currentTick
	table := w.tables[tableIndex]
	arrayIndex := len(table.Entities)
	table.Entities = append(table.Entities, entity)
	for bit := uint8(0); bit < w.registry.nextBit; bit++ {
		if mask&(Mask(1)<<bit) == 0 {
			continue
		}
		table.columns[bit].pushZero(tick)
	}
	w.entityLocs.set(entity, tableIndex, arrayIndex)
}

// SpawnBatch allocates count entities sharing mask and runs init on each
// freshly-pushed slot with direct table access, in the order they were
// spawned. Returns the new entity handles.
func (w *World) SpawnBatch(mask Mask, count int, init func(table *Archetype, index int)) []Entity {
	w.guardStructuralMutation("SpawnBatch")
	if count <= 0 {
		return nil
	}
	tableIndex := w.getOrCreateTable(mask)
	tick := w.currentTick
	table := w.tables[tableIndex]
	entities := make([]Entity, count)
	startIndex := len(table.Entities)
	for slot := 0; slot < count; slot++ {
		entity := w.allocator.allocate()
		entities[slot] = entity
		arrayIndex := startIndex + slot
		table.Entities = append(table.Entities, entity)
		for bit := uint8(0); bit < w.registry.nextBit; bit++ {
			if mask&(Mask(1)<<bit) == 0 {
				continue
			}
			table.columns[bit].pushZero(tick)
		}
		w.entityLocs.set(entity, tableIndex, arrayIndex)
		if init != nil {
			init(table, arrayIndex)
		}
	}
	return entities
}

// Despawn removes an entity from the world. Stale handles are rejected
// and the call is a no-op. The slot is compacted with swap-remove, so
// the entity previously at the last index of the source table moves to
// fill the gap. In multi-world mode use [MultiWorld.Despawn] instead;
// calling Despawn against one of the child worlds would deallocate the
// entity id while other worlds still hold archetype rows for it.
func (w *World) Despawn(entity Entity) bool {
	w.guardStructuralMutation("Despawn")
	if !despawnFromArchetype(w, entity) {
		return false
	}
	w.allocator.deallocate(entity)
	return true
}

// despawnFromArchetype removes the entity's row from the world without
// touching the allocator. MultiWorld.Despawn calls this on every child
// world and then deallocates the id exactly once.
func despawnFromArchetype(world *World, entity Entity) bool {
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	world.entityLocs.markDeallocated(entity.ID)
	Send(world, EntityDespawned{Entity: entity})

	for _, set := range world.tagSets {
		delete(set, entity)
	}

	table := world.tables[tableIndex]
	lastIndex := len(table.Entities) - 1
	var swapped Entity
	hasSwap := arrayIndex < lastIndex
	if hasSwap {
		swapped = table.Entities[lastIndex]
	}

	if hasSwap {
		table.Entities[arrayIndex] = table.Entities[lastIndex]
	}
	table.Entities = table.Entities[:lastIndex]

	for bit := uint8(0); bit < world.registry.nextBit; bit++ {
		if table.Mask&(Mask(1)<<bit) == 0 {
			continue
		}
		table.columns[bit].swapRemove(arrayIndex)
	}

	if hasSwap {
		world.entityLocs.set(swapped, tableIndex, arrayIndex)
	}
	return true
}

// AddComponents migrates entity to the archetype carrying its current
// mask OR mask. Newly-added components are initialized to their zero
// value. Returns false for stale handles, true otherwise (including when
// the operation is a no-op because the entity already has every requested
// component).
func (w *World) AddComponents(entity Entity, mask Mask) bool {
	tableIndex, arrayIndex, ok := w.entityLocs.get(entity)
	if !ok {
		return false
	}
	currentMask := w.tables[tableIndex].Mask
	if currentMask&mask == mask {
		return true
	}
	w.guardStructuralMutation("AddComponents")

	var destTableIndex int
	if bits.OnesCount64(uint64(mask)) == 1 {
		bit := bits.TrailingZeros64(uint64(mask))
		cached := w.tableEdges[tableIndex].add[bit]
		if cached >= 0 {
			destTableIndex = int(cached)
		} else {
			destTableIndex = w.getOrCreateTable(currentMask | mask)
		}
	} else {
		destTableIndex = w.getOrCreateTable(currentMask | mask)
	}
	moveEntity(w, entity, tableIndex, arrayIndex, destTableIndex)
	return true
}

// RemoveComponents migrates entity to the archetype carrying its current
// mask AND-NOT mask. Components present in source but not destination are
// dropped. Returns false for stale handles.
func (w *World) RemoveComponents(entity Entity, mask Mask) bool {
	tableIndex, arrayIndex, ok := w.entityLocs.get(entity)
	if !ok {
		return false
	}
	currentMask := w.tables[tableIndex].Mask
	if currentMask&mask == 0 {
		return true
	}
	w.guardStructuralMutation("RemoveComponents")

	var destTableIndex int
	if bits.OnesCount64(uint64(mask)) == 1 {
		bit := bits.TrailingZeros64(uint64(mask))
		cached := w.tableEdges[tableIndex].remove[bit]
		if cached >= 0 {
			destTableIndex = int(cached)
		} else {
			destTableIndex = w.getOrCreateTable(currentMask &^ mask)
		}
	} else {
		destTableIndex = w.getOrCreateTable(currentMask &^ mask)
	}
	moveEntity(w, entity, tableIndex, arrayIndex, destTableIndex)
	return true
}

// moveEntity physically relocates entity from one archetype to another.
// Components present in both archetypes are migrated by reflect-assignment
// so the GC tracks any embedded pointers correctly. Components added by
// the move are pushed as zero values. Components dropped by the move are
// swap-removed off the source table along with the rest of the source row.
func moveEntity(world *World, entity Entity, fromTableIndex, fromArrayIndex, toTableIndex int) {
	if fromTableIndex == toTableIndex {
		return
	}
	tick := world.currentTick
	fromTable := world.tables[fromTableIndex]
	toTable := world.tables[toTableIndex]

	toArrayIndex := len(toTable.Entities)
	toTable.Entities = append(toTable.Entities, entity)

	for bit := uint8(0); bit < world.registry.nextBit; bit++ {
		bitMask := Mask(1) << bit
		if toTable.Mask&bitMask == 0 {
			continue
		}
		if fromTable.Mask&bitMask != 0 {
			toTable.columns[bit].migrateFrom(fromTable.columns[bit], fromArrayIndex, tick)
		} else {
			toTable.columns[bit].pushZero(tick)
		}
	}

	world.entityLocs.set(entity, toTableIndex, toArrayIndex)

	lastIndex := len(fromTable.Entities) - 1
	var swapped Entity
	hasSwap := fromArrayIndex < lastIndex
	if hasSwap {
		swapped = fromTable.Entities[lastIndex]
		fromTable.Entities[fromArrayIndex] = fromTable.Entities[lastIndex]
	}
	fromTable.Entities = fromTable.Entities[:lastIndex]

	for bit := uint8(0); bit < world.registry.nextBit; bit++ {
		if fromTable.Mask&(Mask(1)<<bit) == 0 {
			continue
		}
		fromTable.columns[bit].swapRemove(fromArrayIndex)
	}

	if hasSwap {
		world.entityLocs.set(swapped, fromTableIndex, fromArrayIndex)
	}
}
