package freecs

import (
	"iter"
	"unsafe"
)

// Query yields every entity matching include (every bit must be set) and
// not matching any bit in exclude. The iteration order is the archetype
// walk order; it is deterministic within a single world's lifetime but not
// across runs.
func (w *World) Query(include, exclude Mask) iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		for _, tableIndex := range w.cachedTables(include) {
			table := w.tables[tableIndex]
			if table.Mask&exclude != 0 {
				continue
			}
			for _, entity := range table.Entities {
				if !yield(entity) {
					return
				}
			}
		}
	}
}

// QueryFirst returns the first entity that matches include and not exclude.
// Returns the zero Entity and false if no match exists.
func (w *World) QueryFirst(include, exclude Mask) (Entity, bool) {
	for _, tableIndex := range w.cachedTables(include) {
		table := w.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		if len(table.Entities) > 0 {
			return table.Entities[0], true
		}
	}
	return Entity{}, false
}

// CountQuery returns the number of entities matching include and not
// exclude.
func (w *World) CountQuery(include, exclude Mask) int {
	total := 0
	for _, tableIndex := range w.cachedTables(include) {
		table := w.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		total += len(table.Entities)
	}
	return total
}

// ForEach walks every entity satisfying include and not exclude, invoking
// callback with direct table access. Component slices are at
// freecs.Column[T](world, table).
//
// Mutations through the table do not auto-stamp the change-detection tick.
// Use MarkChanged or GetMut if you want change tracking on bulk iteration.
//
// The callback must not mutate world topology (Spawn, Despawn,
// AddComponents, RemoveComponents, Set on a missing component, tag inserts
// on an archetype currently being walked). Structural changes during
// iteration invalidate the cached entity count and may leave the column
// slice aliasing freed or reordered memory. Use the command buffer
// (QueueDespawn, QueueSet, etc.) to defer structural changes safely.
func (w *World) ForEach(include, exclude Mask, callback func(entity Entity, table *Archetype, index int)) {
	for _, tableIndex := range w.cachedTables(include) {
		table := w.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		for arrayIndex := 0; arrayIndex < len(table.Entities); arrayIndex++ {
			callback(table.Entities[arrayIndex], table, arrayIndex)
		}
	}
}

// MarkChanged stamps component T's slot for entity with the current tick.
// Use this inside a ForEach or IterN body when you mutated the column
// through direct pointer access.
func MarkChanged[T any](world *World, entity Entity) {
	info := mustComponentInfo[T](world)
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return
	}
	table := world.tables[tableIndex]
	if table.Mask&info.mask == 0 {
		return
	}
	table.columns[info.bitIndex].markChanged(arrayIndex, world.currentTick)
}

// Iter1 walks every entity that has component A (plus every bit in
// extraInclude and none in exclude), giving the callback a typed pointer
// to A and the entity handle. Mutations through *A do not auto-stamp the
// tick.
//
// The same structural-mutation constraint as [World.ForEach] applies: do
// not Spawn, Despawn, Add, Remove, or Set a missing component from inside
// the callback. Defer those via the command buffer.
func Iter1[A any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A)) {
	aInfo := mustComponentInfo[A](world)
	include := extraInclude | aInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.Entities[arrayIndex], &aSlice[arrayIndex])
		}
	}
}

// Iter2 walks entities that have both A and B.
func Iter2[A, B any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	include := extraInclude | aInfo.mask | bInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		bSlice := unsafe.Slice((*B)(table.columns[bInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex])
		}
	}
}

// Iter3 walks entities that have A, B, and C.
func Iter3[A, B, C any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	cInfo := mustComponentInfo[C](world)
	include := extraInclude | aInfo.mask | bInfo.mask | cInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		bSlice := unsafe.Slice((*B)(table.columns[bInfo.bitIndex].dataPtr), count)
		cSlice := unsafe.Slice((*C)(table.columns[cInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex])
		}
	}
}

// Iter4 walks entities that have A, B, C, and D.
func Iter4[A, B, C, D any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C, d *D)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	cInfo := mustComponentInfo[C](world)
	dInfo := mustComponentInfo[D](world)
	include := extraInclude | aInfo.mask | bInfo.mask | cInfo.mask | dInfo.mask
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aSlice := unsafe.Slice((*A)(table.columns[aInfo.bitIndex].dataPtr), count)
		bSlice := unsafe.Slice((*B)(table.columns[bInfo.bitIndex].dataPtr), count)
		cSlice := unsafe.Slice((*C)(table.columns[cInfo.bitIndex].dataPtr), count)
		dSlice := unsafe.Slice((*D)(table.columns[dInfo.bitIndex].dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex], &dSlice[arrayIndex])
		}
	}
}

// Column returns the typed []T view of an archetype's column for T and a
// flag indicating whether the archetype carries T at all. ok=false means T
// is not in this archetype's mask; ok=true with an empty slice means T is
// part of the mask but the archetype currently holds zero rows. The slice
// aliases the column's backing storage and is invalidated by any structural
// change (spawn, despawn, add, remove) that touches this archetype.
func Column[T any](world *World, table *Archetype) ([]T, bool) {
	info, present := componentInfoFor[T](world)
	if !present {
		return nil, false
	}
	if table.Mask&info.mask == 0 {
		return nil, false
	}
	column := table.columns[info.bitIndex]
	if column.length == 0 {
		return nil, true
	}
	return unsafe.Slice((*T)(column.dataPtr), column.length), true
}
