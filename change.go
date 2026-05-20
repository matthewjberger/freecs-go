package freecs

import "unsafe"

// IterChanged1 yields every entity whose component A was stamped after the
// previous frame's watermark. Useful for "redraw only what moved" systems.
func IterChanged1[A any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A)) {
	aInfo := componentInfoFor[A](world)
	since := world.lastTick
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
		column := table.columns[aInfo.bitIndex]
		aSlice := unsafe.Slice((*A)(column.dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			if column.changed[arrayIndex] > since {
				callback(table.entities[arrayIndex], &aSlice[arrayIndex])
			}
		}
	}
}

// IterChanged2 yields entities whose A or B was stamped after the watermark.
// The OR semantics match the freecs Rust kernel: "iterate anything whose
// visual representation moved" is the typical use case.
func IterChanged2[A, B any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B)) {
	aInfo := componentInfoFor[A](world)
	bInfo := componentInfoFor[B](world)
	since := world.lastTick
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
		aColumn := table.columns[aInfo.bitIndex]
		bColumn := table.columns[bInfo.bitIndex]
		aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
		bSlice := unsafe.Slice((*B)(bColumn.dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			if aColumn.changed[arrayIndex] > since || bColumn.changed[arrayIndex] > since {
				callback(table.entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex])
			}
		}
	}
}

// Changed reports whether component T on entity was stamped after the
// previous frame's watermark. False for stale handles or missing components.
func Changed[T any](world *World, entity Entity) bool {
	info := componentInfoFor[T](world)
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	table := world.tables[tableIndex]
	if table.mask&info.mask == 0 {
		return false
	}
	return table.columns[info.bitIndex].changed[arrayIndex] > world.lastTick
}
