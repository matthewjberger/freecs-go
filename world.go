package freecs

import "reflect"

// World owns every entity, archetype, and side structure (tags, events,
// commands, resources). A World is not safe for concurrent use; wrap it in
// a mutex if multiple goroutines need to touch it.
type World struct {
	registry      *registry
	allocator     allocator
	entityLocs    entityLocations
	tables        []*Archetype
	tableLookup   map[Mask]int
	tableEdges    []*tableEdges
	queryCache    map[Mask][]int
	currentTick   uint32
	lastTick      uint32
	eventQueues   []eventDriver
	eventByType   map[reflect.Type]int
	tagSets       map[reflect.Type]map[Entity]struct{}
	commandBuffer []func(*World)
	resources     map[reflect.Type]any
}

// New creates an empty world. Components must be registered with
// Register before they can be spawned, set, or queried.
func New() *World {
	return &World{
		registry:    newRegistry(),
		tableLookup: make(map[Mask]int),
		queryCache:  make(map[Mask][]int),
		eventByType: make(map[reflect.Type]int),
		tagSets:     make(map[reflect.Type]map[Entity]struct{}),
		resources:   make(map[reflect.Type]any),
	}
}

// CurrentTick returns the world's current frame counter.
func (w *World) CurrentTick() uint32 { return w.currentTick }

// LastTick returns the watermark of the previous frame, used by change-
// detection queries to find slots modified since.
func (w *World) LastTick() uint32 { return w.lastTick }

// Step advances the frame. It updates all event queues (current → previous)
// and bumps the change-detection tick. Call once per frame at the end, after
// every system has had a chance to read the current frame's changes.
func (w *World) Step() {
	for _, queue := range w.eventQueues {
		queue.update()
	}
	w.lastTick = w.currentTick
	w.currentTick++
}

// getOrCreateTable returns the index of the archetype for mask, creating it
// (and its edges, and patching the query cache) the first time mask appears.
func (w *World) getOrCreateTable(mask Mask) int {
	if index, ok := w.tableLookup[mask]; ok {
		return index
	}
	newIndex := len(w.tables)
	w.tables = append(w.tables, newArchetype(mask, w.registry))
	w.tableEdges = append(w.tableEdges, newTableEdges())
	w.tableLookup[mask] = newIndex

	w.invalidateQueryCacheForNewTable(mask, newIndex)
	w.wireEdgesForNewTable(mask, newIndex)
	return newIndex
}

func (w *World) invalidateQueryCacheForNewTable(newMask Mask, newTableIndex int) {
	for queryMask, cached := range w.queryCache {
		if newMask&queryMask == queryMask {
			w.queryCache[queryMask] = append(cached, newTableIndex)
		}
	}
}

func (w *World) wireEdgesForNewTable(newMask Mask, newTableIndex int) {
	// Fill incoming edges from existing tables that are one bit-flip away.
	for bit := uint8(0); bit < w.registry.nextBit; bit++ {
		bitMask := Mask(1) << bit
		for existingIndex, existing := range w.tables {
			if existingIndex == newTableIndex {
				continue
			}
			if existing.mask|bitMask == newMask {
				w.tableEdges[existingIndex].add[bit] = int32(newTableIndex)
			}
			if existing.mask&^bitMask == newMask {
				w.tableEdges[existingIndex].remove[bit] = int32(newTableIndex)
			}
		}
	}

	// Fill outgoing edges from the new table to existing tables.
	for bit := uint8(0); bit < w.registry.nextBit; bit++ {
		bitMask := Mask(1) << bit
		if destIndex, ok := w.tableLookup[newMask|bitMask]; ok {
			w.tableEdges[newTableIndex].add[bit] = int32(destIndex)
		}
		if newMask&bitMask != 0 {
			if destIndex, ok := w.tableLookup[newMask&^bitMask]; ok {
				w.tableEdges[newTableIndex].remove[bit] = int32(destIndex)
			}
		}
	}
}

// cachedTables returns the table indices satisfying include, populating
// the cache on first use.
func (w *World) cachedTables(include Mask) []int {
	if cached, ok := w.queryCache[include]; ok {
		return cached
	}
	matching := make([]int, 0, len(w.tables))
	for index, table := range w.tables {
		if table.mask&include == include {
			matching = append(matching, index)
		}
	}
	w.queryCache[include] = matching
	return matching
}
