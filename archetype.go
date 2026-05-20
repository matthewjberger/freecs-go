package freecs

// Archetype is one storage table holding every entity that has exactly the
// component set described by mask. Component columns live at the bit index
// of their component; bits not set in mask have a nil column pointer.
type Archetype struct {
	mask     Mask
	entities []Entity
	columns  [maxComponents]*column
}

func newArchetype(mask Mask, reg *registry) *Archetype {
	table := &Archetype{mask: mask}
	for bit := uint8(0); bit < maxComponents; bit++ {
		if mask&(Mask(1)<<bit) == 0 {
			continue
		}
		info := reg.infoForBit(bit)
		if info == nil {
			panic("freecs: archetype mask references unregistered bit")
		}
		table.columns[bit] = newColumn(info.elemType)
	}
	return table
}

// tableEdges is the per-archetype graph cache. addEdges[bit] is the table to
// move into when component bit is added; removeEdges[bit] is the table for
// removing that bit. -1 means the edge has not been resolved yet and the
// caller falls back to a mask lookup.
type tableEdges struct {
	add    [maxComponents]int32
	remove [maxComponents]int32
}

func newTableEdges() *tableEdges {
	edges := &tableEdges{}
	for i := 0; i < maxComponents; i++ {
		edges.add[i] = -1
		edges.remove[i] = -1
	}
	return edges
}
