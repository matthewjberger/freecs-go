package freecs

import "math/bits"

// Spawn allocates a new entity and places it in the archetype identified by
// mask. Every component listed in mask is initialized to the zero value of
// its type; use Set or SpawnBatch with an initializer to fill them in.
func Spawn(world *World, mask Mask) Entity {
	entity := world.allocator.allocate()
	tableIndex := world.getOrCreateTable(mask)
	tick := world.currentTick
	table := world.tables[tableIndex]
	arrayIndex := len(table.entities)
	table.entities = append(table.entities, entity)
	for bit := uint8(0); bit < world.registry.nextBit; bit++ {
		if mask&(Mask(1)<<bit) == 0 {
			continue
		}
		table.columns[bit].pushZero(tick)
	}
	world.entityLocs.set(entity, tableIndex, arrayIndex)
	return entity
}

// SpawnBatch allocates count entities sharing mask and runs init on each
// freshly-pushed slot with direct table access, in the order they were
// spawned. Returns the new entity handles.
func SpawnBatch(world *World, mask Mask, count int, init func(table *Archetype, index int)) []Entity {
	if count <= 0 {
		return nil
	}
	tableIndex := world.getOrCreateTable(mask)
	tick := world.currentTick
	table := world.tables[tableIndex]
	entities := make([]Entity, count)
	startIndex := len(table.entities)
	for slot := 0; slot < count; slot++ {
		entity := world.allocator.allocate()
		entities[slot] = entity
		arrayIndex := startIndex + slot
		table.entities = append(table.entities, entity)
		for bit := uint8(0); bit < world.registry.nextBit; bit++ {
			if mask&(Mask(1)<<bit) == 0 {
				continue
			}
			table.columns[bit].pushZero(tick)
		}
		world.entityLocs.set(entity, tableIndex, arrayIndex)
		if init != nil {
			init(table, arrayIndex)
		}
	}
	return entities
}

// Despawn removes an entity from the world. Stale handles are rejected and
// the call is a no-op. The slot is compacted with swap-remove, so the entity
// previously at the last index of the source table moves to fill the gap.
func Despawn(world *World, entity Entity) bool {
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	world.entityLocs.markDeallocated(entity.ID)
	world.allocator.deallocate(entity)

	for _, set := range world.tagSets {
		delete(set, entity)
	}

	table := world.tables[tableIndex]
	lastIndex := len(table.entities) - 1
	var swapped Entity
	hasSwap := arrayIndex < lastIndex
	if hasSwap {
		swapped = table.entities[lastIndex]
	}

	if hasSwap {
		table.entities[arrayIndex] = table.entities[lastIndex]
	}
	table.entities = table.entities[:lastIndex]

	for bit := uint8(0); bit < world.registry.nextBit; bit++ {
		if table.mask&(Mask(1)<<bit) == 0 {
			continue
		}
		table.columns[bit].swapRemove(arrayIndex)
	}

	if hasSwap {
		world.entityLocs.set(swapped, tableIndex, arrayIndex)
	}
	return true
}

// Get returns a pointer to component T on entity if the entity is live and
// has T. The pointer aliases the column's backing memory and is invalidated
// the next time the archetype grows or migrates this entity. Reading through
// the pointer does not mark the slot as changed; use Mut for that.
func Get[T any](world *World, entity Entity) (*T, bool) {
	info := componentInfoFor[T](world)
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return nil, false
	}
	table := world.tables[tableIndex]
	if table.mask&info.mask == 0 {
		return nil, false
	}
	column := table.columns[info.bitIndex]
	return (*T)(column.at(arrayIndex)), true
}

// Mut returns a pointer to component T on entity and stamps the slot with the
// current tick so change-detection queries will see it as modified this frame.
// Returns nil, false for stale handles or missing components.
func Mut[T any](world *World, entity Entity) (*T, bool) {
	info := componentInfoFor[T](world)
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return nil, false
	}
	table := world.tables[tableIndex]
	if table.mask&info.mask == 0 {
		return nil, false
	}
	column := table.columns[info.bitIndex]
	column.markChanged(arrayIndex, world.currentTick)
	return (*T)(column.at(arrayIndex)), true
}

// Has reports whether entity has component T. False for stale handles.
func Has[T any](world *World, entity Entity) bool {
	info := componentInfoFor[T](world)
	tableIndex, _, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	return world.tables[tableIndex].mask&info.mask != 0
}

// HasComponents reports whether entity has every component in mask.
func HasComponents(world *World, entity Entity, mask Mask) bool {
	tableIndex, _, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	return world.tables[tableIndex].mask&mask == mask
}

// ComponentMask returns the archetype mask of entity, or (0, false) if the
// handle is stale.
func ComponentMask(world *World, entity Entity) (Mask, bool) {
	tableIndex, _, ok := world.entityLocs.get(entity)
	if !ok {
		return 0, false
	}
	return world.tables[tableIndex].mask, true
}

// Set writes value into entity's component T, adding the component first if
// it is missing. Stamps the slot with the current tick. No-op for stale
// handles.
func Set[T any](world *World, entity Entity, value T) {
	info := componentInfoFor[T](world)
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return
	}
	table := world.tables[tableIndex]
	if table.mask&info.mask != 0 {
		column := table.columns[info.bitIndex]
		*(*T)(column.at(arrayIndex)) = value
		column.markChanged(arrayIndex, world.currentTick)
		return
	}
	AddComponents(world, entity, info.mask)
	tableIndex, arrayIndex, ok = world.entityLocs.get(entity)
	if !ok {
		return
	}
	table = world.tables[tableIndex]
	column := table.columns[info.bitIndex]
	*(*T)(column.at(arrayIndex)) = value
	column.markChanged(arrayIndex, world.currentTick)
}

// Add gives entity component T with the zero value if it does not already
// have it. Equivalent to AddComponents(world, entity, MaskOf[T](world)).
func Add[T any](world *World, entity Entity) {
	info := componentInfoFor[T](world)
	AddComponents(world, entity, info.mask)
}

// Remove strips component T from entity. No-op if entity does not have it.
func Remove[T any](world *World, entity Entity) {
	info := componentInfoFor[T](world)
	RemoveComponents(world, entity, info.mask)
}

// AddComponents migrates entity to the archetype carrying its current mask
// OR mask. Newly-added components are initialized to their zero value.
// Returns false for stale handles, true otherwise (including when the
// operation is a no-op because the entity already has every requested
// component).
func AddComponents(world *World, entity Entity, mask Mask) bool {
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	currentMask := world.tables[tableIndex].mask
	if currentMask&mask == mask {
		return true
	}

	var destTableIndex int
	if bits.OnesCount64(uint64(mask)) == 1 {
		bit := bits.TrailingZeros64(uint64(mask))
		cached := world.tableEdges[tableIndex].add[bit]
		if cached >= 0 {
			destTableIndex = int(cached)
		} else {
			destTableIndex = world.getOrCreateTable(currentMask | mask)
		}
	} else {
		destTableIndex = world.getOrCreateTable(currentMask | mask)
	}
	moveEntity(world, entity, tableIndex, arrayIndex, destTableIndex)
	return true
}

// RemoveComponents migrates entity to the archetype carrying its current
// mask AND-NOT mask. Components present in source but not destination are
// dropped. Returns false for stale handles.
func RemoveComponents(world *World, entity Entity, mask Mask) bool {
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	currentMask := world.tables[tableIndex].mask
	if currentMask&mask == 0 {
		return true
	}

	var destTableIndex int
	if bits.OnesCount64(uint64(mask)) == 1 {
		bit := bits.TrailingZeros64(uint64(mask))
		cached := world.tableEdges[tableIndex].remove[bit]
		if cached >= 0 {
			destTableIndex = int(cached)
		} else {
			destTableIndex = world.getOrCreateTable(currentMask &^ mask)
		}
	} else {
		destTableIndex = world.getOrCreateTable(currentMask &^ mask)
	}
	moveEntity(world, entity, tableIndex, arrayIndex, destTableIndex)
	return true
}

// moveEntity physically relocates entity from one archetype to another.
// Components present in both archetypes are migrated by reflect-assignment
// so the GC tracks any embedded pointers correctly. Components added by the
// move are pushed as zero values. Components dropped by the move are
// swap-removed off the source table along with the rest of the source row.
func moveEntity(world *World, entity Entity, fromTableIndex, fromArrayIndex, toTableIndex int) {
	if fromTableIndex == toTableIndex {
		return
	}
	tick := world.currentTick
	fromTable := world.tables[fromTableIndex]
	toTable := world.tables[toTableIndex]

	toArrayIndex := len(toTable.entities)
	toTable.entities = append(toTable.entities, entity)

	for bit := uint8(0); bit < world.registry.nextBit; bit++ {
		bitMask := Mask(1) << bit
		if toTable.mask&bitMask == 0 {
			continue
		}
		if fromTable.mask&bitMask != 0 {
			toTable.columns[bit].migrateFrom(fromTable.columns[bit], fromArrayIndex, tick)
		} else {
			toTable.columns[bit].pushZero(tick)
		}
	}

	world.entityLocs.set(entity, toTableIndex, toArrayIndex)

	lastIndex := len(fromTable.entities) - 1
	var swapped Entity
	hasSwap := fromArrayIndex < lastIndex
	if hasSwap {
		swapped = fromTable.entities[lastIndex]
		fromTable.entities[fromArrayIndex] = fromTable.entities[lastIndex]
	}
	fromTable.entities = fromTable.entities[:lastIndex]

	for bit := uint8(0); bit < world.registry.nextBit; bit++ {
		if fromTable.mask&(Mask(1)<<bit) == 0 {
			continue
		}
		fromTable.columns[bit].swapRemove(fromArrayIndex)
	}

	if hasSwap {
		world.entityLocs.set(swapped, fromTableIndex, fromArrayIndex)
	}
}
