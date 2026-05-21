package freecs

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
