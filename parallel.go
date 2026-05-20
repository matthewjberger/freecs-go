package freecs

import (
	"sync"
	"unsafe"
)

// Parallel iteration dispatches one goroutine per matching archetype. Each
// goroutine materializes typed []T slices from its archetype's columns and
// runs callback over every row. This is a good fit when many archetypes
// match the query and the per-entity work is non-trivial. It is *not*
// helpful when a single archetype holds most of the entities; the goroutine
// fan-out is per archetype, not per row.
//
// The callback must not mutate world topology (no Spawn, Despawn,
// AddComponents, RemoveComponents, Set on a missing component, or tag
// inserts). Any of those would race with sibling goroutines walking other
// archetypes whose tableEdges, queryCache, or tag-set state could be
// touched. Use the command buffer (QueueDespawn, QueueSet, etc.) to defer
// structural changes safely.
//
// Reading and writing component values through the supplied pointers is
// safe: each archetype is the sole owner of its columns and no two
// goroutines touch the same column. Reading shared world.resources is fine;
// writing to them from inside the callback is the caller's responsibility
// to synchronize.

// ParallelIter1 runs callback over every entity that has component A in
// parallel across archetypes.
func ParallelIter1[A any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A)) {
	aInfo := mustComponentInfo[A](world)
	include := extraInclude | aInfo.mask
	var wg sync.WaitGroup
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aColumn := table.columns[aInfo.bitIndex]
		wg.Add(1)
		go func(table *Archetype, aColumn *column, count int) {
			defer wg.Done()
			aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
			for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex])
			}
		}(table, aColumn, count)
	}
	wg.Wait()
}

// ParallelIter2 runs callback over every entity that has both A and B in
// parallel across archetypes.
func ParallelIter2[A, B any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	include := extraInclude | aInfo.mask | bInfo.mask
	var wg sync.WaitGroup
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aColumn := table.columns[aInfo.bitIndex]
		bColumn := table.columns[bInfo.bitIndex]
		wg.Add(1)
		go func(table *Archetype, aColumn, bColumn *column, count int) {
			defer wg.Done()
			aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
			bSlice := unsafe.Slice((*B)(bColumn.dataPtr), count)
			for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex])
			}
		}(table, aColumn, bColumn, count)
	}
	wg.Wait()
}

// ParallelIter3 runs callback over every entity that has A, B, and C in
// parallel across archetypes.
func ParallelIter3[A, B, C any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	cInfo := mustComponentInfo[C](world)
	include := extraInclude | aInfo.mask | bInfo.mask | cInfo.mask
	var wg sync.WaitGroup
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aColumn := table.columns[aInfo.bitIndex]
		bColumn := table.columns[bInfo.bitIndex]
		cColumn := table.columns[cInfo.bitIndex]
		wg.Add(1)
		go func(table *Archetype, aColumn, bColumn, cColumn *column, count int) {
			defer wg.Done()
			aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
			bSlice := unsafe.Slice((*B)(bColumn.dataPtr), count)
			cSlice := unsafe.Slice((*C)(cColumn.dataPtr), count)
			for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex])
			}
		}(table, aColumn, bColumn, cColumn, count)
	}
	wg.Wait()
}

// ParallelIter4 runs callback over every entity that has A, B, C, and D in
// parallel across archetypes.
func ParallelIter4[A, B, C, D any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C, d *D)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	cInfo := mustComponentInfo[C](world)
	dInfo := mustComponentInfo[D](world)
	include := extraInclude | aInfo.mask | bInfo.mask | cInfo.mask | dInfo.mask
	var wg sync.WaitGroup
	for _, tableIndex := range world.cachedTables(include) {
		table := world.tables[tableIndex]
		if table.Mask&exclude != 0 {
			continue
		}
		count := len(table.Entities)
		if count == 0 {
			continue
		}
		aColumn := table.columns[aInfo.bitIndex]
		bColumn := table.columns[bInfo.bitIndex]
		cColumn := table.columns[cInfo.bitIndex]
		dColumn := table.columns[dInfo.bitIndex]
		wg.Add(1)
		go func(table *Archetype, aColumn, bColumn, cColumn, dColumn *column, count int) {
			defer wg.Done()
			aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
			bSlice := unsafe.Slice((*B)(bColumn.dataPtr), count)
			cSlice := unsafe.Slice((*C)(cColumn.dataPtr), count)
			dSlice := unsafe.Slice((*D)(dColumn.dataPtr), count)
			for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex], &dSlice[arrayIndex])
			}
		}(table, aColumn, bColumn, cColumn, dColumn, count)
	}
	wg.Wait()
}
