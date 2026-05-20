package freecs

import "unsafe"

// IterChanged1 yields every entity whose component A was stamped after the
// previous frame's watermark. Useful for "redraw only what moved" systems.
func IterChanged1[A any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A)) {
	aInfo := mustComponentInfo[A](world)
	since := world.lastTick
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
		column := table.columns[aInfo.bitIndex]
		aSlice := unsafe.Slice((*A)(column.dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			if column.changed[arrayIndex] > since {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex])
			}
		}
	}
}

// IterChanged2 yields entities whose A or B was stamped after the watermark.
// The OR semantics match the freecs Rust kernel: "iterate anything whose
// visual representation moved" is the typical use case.
func IterChanged2[A, B any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	since := world.lastTick
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
		aColumn := table.columns[aInfo.bitIndex]
		bColumn := table.columns[bInfo.bitIndex]
		aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
		bSlice := unsafe.Slice((*B)(bColumn.dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			if aColumn.changed[arrayIndex] > since || bColumn.changed[arrayIndex] > since {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex])
			}
		}
	}
}

// IterChanged3 yields entities whose A, B, or C was stamped after the
// watermark. OR semantics across columns, same as IterChanged2.
func IterChanged3[A, B, C any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	cInfo := mustComponentInfo[C](world)
	since := world.lastTick
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
		aColumn := table.columns[aInfo.bitIndex]
		bColumn := table.columns[bInfo.bitIndex]
		cColumn := table.columns[cInfo.bitIndex]
		aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
		bSlice := unsafe.Slice((*B)(bColumn.dataPtr), count)
		cSlice := unsafe.Slice((*C)(cColumn.dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			if aColumn.changed[arrayIndex] > since ||
				bColumn.changed[arrayIndex] > since ||
				cColumn.changed[arrayIndex] > since {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex])
			}
		}
	}
}

// IterChanged4 yields entities whose A, B, C, or D was stamped after the
// watermark.
func IterChanged4[A, B, C, D any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B, c *C, d *D)) {
	aInfo := mustComponentInfo[A](world)
	bInfo := mustComponentInfo[B](world)
	cInfo := mustComponentInfo[C](world)
	dInfo := mustComponentInfo[D](world)
	since := world.lastTick
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
		aColumn := table.columns[aInfo.bitIndex]
		bColumn := table.columns[bInfo.bitIndex]
		cColumn := table.columns[cInfo.bitIndex]
		dColumn := table.columns[dInfo.bitIndex]
		aSlice := unsafe.Slice((*A)(aColumn.dataPtr), count)
		bSlice := unsafe.Slice((*B)(bColumn.dataPtr), count)
		cSlice := unsafe.Slice((*C)(cColumn.dataPtr), count)
		dSlice := unsafe.Slice((*D)(dColumn.dataPtr), count)
		for arrayIndex := 0; arrayIndex < count; arrayIndex++ {
			if aColumn.changed[arrayIndex] > since ||
				bColumn.changed[arrayIndex] > since ||
				cColumn.changed[arrayIndex] > since ||
				dColumn.changed[arrayIndex] > since {
				callback(table.Entities[arrayIndex], &aSlice[arrayIndex], &bSlice[arrayIndex], &cSlice[arrayIndex], &dSlice[arrayIndex])
			}
		}
	}
}

// Changed reports whether component T on entity was stamped after the
// previous frame's watermark. False for stale handles, missing components,
// or types not registered on this world.
func Changed[T any](world *World, entity Entity) bool {
	info, ok := componentInfoFor[T](world)
	if !ok {
		return false
	}
	tableIndex, arrayIndex, ok := world.entityLocs.get(entity)
	if !ok {
		return false
	}
	table := world.tables[tableIndex]
	if table.Mask&info.mask == 0 {
		return false
	}
	return table.columns[info.bitIndex].changed[arrayIndex] > world.lastTick
}
