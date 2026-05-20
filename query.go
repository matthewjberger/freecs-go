package freecs

import (
	"iter"
	"unsafe"
)

// Query yields every entity matching include (every bit must be set) and not
// matching any bit in exclude. The iteration order is the archetype walk
// order; it is deterministic only within a single world's lifetime, not
// across runs.
func Query(world *World, include, exclude Mask) iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		for _, tableIndex := range world.cachedTables(include) {
			table := world.tables[tableIndex]
			if table.mask&exclude != 0 {
				continue
			}
			for _, entity := range table.entities {
				if !yield(entity) {
					return
				}
			}
		}
	}
}

// QueryFirst returns the first entity that matches include and not exclude.
// Returns the zero Entity and false if no match exists.
func QueryFirst(world *World, include, exclude Mask) (Entity, bool) {
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.mask&exclude != 0 {
			continue
		}
		if len(table.entities) > 0 {
			return table.entities[0], true
		}
	}
	return Entity{}, false
}

// CountQuery returns the number of entities matching include and not exclude.
func CountQuery(world *World, include, exclude Mask) int {
	total := 0
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.mask&exclude != 0 {
			continue
		}
		total += len(table.entities)
	}
	return total
}

// ForEach walks every entity satisfying include and not exclude, invoking
// callback with direct table access. The callback receives the archetype and
// the row index inside it; component slices are at table.Column(bit).
//
// Mutations through the table do not auto-stamp the change-detection tick.
// Use MarkChanged or Mut if you want change tracking on bulk iteration.
func ForEach(world *World, include, exclude Mask, callback func(entity Entity, table *Archetype, index int)) {
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.mask&exclude != 0 {
			continue
		}
		for arrayIndex := 0; arrayIndex < len(table.entities); arrayIndex++ {
			callback(table.entities[arrayIndex], table, arrayIndex)
		}
	}
}

// MarkChanged stamps component T's slot for entity with the current tick.
// Use this inside a ForEach or IterN body when you mutated the column
// through direct pointer access.
func MarkChanged[T any](world *World, entity Entity) {
	info := componentInfoFor[T](world)
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return
	}
	table := world.tables[tableIndex]
	if table.mask&info.mask == 0 {
		return
	}
	table.columns[info.bitIndex].markChanged(arrayIndex, world.currentTick)
}

// Iter1 walks every entity that has component A (plus every bit in extraInclude
// and none in exclude), giving the callback a typed pointer to A and the
// entity handle. Use this for tight inner loops where direct column access
// is the goal. Mutations through *A do not auto-stamp the tick.
func Iter1[A any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A)) {
	aInfo := componentInfoFor[A](world)
	include := extraInclude | aInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.mask&exclude != 0 {
			continue
		}
		count := len(table.entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.entities[arrayIndex], &aSlice[arrayIndex])
		}
	}
}

// Iter2 walks entities that have both A and B.
func Iter2[A, B any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B)) {
	aInfo := componentInfoFor[A](world)
	bInfo := componentInfoFor[B](world)
	include := extraInclude | aInfo.mask | bInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.mask&exclude != 0 {
			continue
		}
		count := len(table.entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		bSlice := unsafe.Slice((*B)(table.columns[bInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex])
		}
	}
}

// Iter3 walks entities that have A, B, and C.
func Iter3[A, B, C any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C)) {
	aInfo := componentInfoFor[A](world)
	bInfo := componentInfoFor[B](world)
	cInfo := componentInfoFor[C](world)
	include := extraInclude | aInfo.mask | bInfo.mask | cInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.mask&exclude != 0 {
			continue
		}
		count := len(table.entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		bSlice := unsafe.Slice((*B)(table.columns[bInfo.bitIndex].dataPtr), count)
		cSlice := unsafe.Slice((*C)(table.columns[cInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex])
		}
	}
}

// Iter4 walks entities that have A, B, C, and D.
func Iter4[A, B, C, D any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C, d *D)) {
	aInfo := componentInfoFor[A](world)
	bInfo := componentInfoFor[B](world)
	cInfo := componentInfoFor[C](world)
	dInfo := componentInfoFor[D](world)
	include := extraInclude | aInfo.mask | bInfo.mask | cInfo.mask | dInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.mask&exclude != 0 {
			continue
		}
		count := len(table.entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		bSlice := unsafe.Slice((*B)(table.columns[bInfo.bitIndex].dataPtr), count)
		cSlice := unsafe.Slice((*C)(table.columns[cInfo.bitIndex].dataPtr), count)
		dSlice := unsafe.Slice((*D)(table.columns[dInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex], &dSlice[arrayIndex])
		}
	}
}

// Column returns the underlying typed []T view of an archetype's column for
// component T. Returns nil if T is not part of this archetype. The slice
// aliases the column's backing storage and is invalidated by any structural
// change (spawn, despawn, add, remove) that touches this archetype.
func Column[T any](world *World, table *Archetype) []T {
	info := componentInfoFor[T](world)
	if table.mask&info.mask == 0 {
		return nil
	}
	column := table.columns[info.bitIndex]
	if column.length == 0 {
		return nil
	}
	return unsafe.Slice((*T)(column.dataPtr), column.length)
}

// Entities returns the archetype's entity list. Read-only.
func Entities(table *Archetype) []Entity { return table.entities }
