# Queries and caches

A query is "give me every entity whose archetype mask is a superset of this include mask and shares no bits with this exclude mask." Walking that naively is O(archetypes), and a serious project has dozens of archetypes and runs hundreds of queries per frame. Two caches turn that into amortized O(1) on the common path.

## The mask

`Mask` is `uint64`, one bit per registered component type. `freecs.Register[T](world)` assigns the next available bit (zero-indexed) and returns a single-bit `Mask` for that type. Register up to 64 component types per `World`; past that you split across worlds with a `MultiWorld`.

Composing a query mask is bitwise OR:

```go
mask := POSITION | VELOCITY | HEALTH
```

Testing "does an archetype have all of these" is one CPU instruction:

```go
if table.Mask & mask == mask { /* matches */ }
```

Testing "does it have any of these" (the exclude case) is also one instruction:

```go
if table.Mask & exclude != 0 { /* excluded */ }
```

Both tests are inlined into the iteration loop and don't allocate.

## Archetype tables are created lazily

The first time you spawn an entity with a specific mask, the world creates an `Archetype` for that mask via `getOrCreateTable`. The mask-to-index mapping lives in `world.tableLookup`, a `map[Mask]int`. Subsequent spawns and migrations with the same mask reuse the same table.

`getOrCreateTable` does three things on the cache-miss path:

1. Allocates the new `Archetype` and appends it to `world.tables`.
2. Patches the query cache (see below) to include the new archetype where it satisfies an existing query.
3. Wires the archetype graph edges (see below) so future single-bit migrations into and out of this archetype skip the mask-to-table hashmap.

The miss path costs a few small allocations and an O(archetype-count) scan. The hit path is one hashmap lookup. Since archetype creation is one-time per unique component set, the miss cost amortizes to zero across the lifetime of the program.

## The archetype graph cache

Most structural changes flip exactly one bit. A "stunned" effect toggles the `STUN` component on and off. An equipment system adds and removes individual items. Walking from "the source archetype" plus "the bit being toggled" to "the destination archetype" is a fixed mapping, the same every time.

`tableEdges` (in [archetype.go](../archetype.go)) stores that mapping per source archetype:

```go
type tableEdges struct {
    add    [maxComponents]int32
    remove [maxComponents]int32
}
```

`add[bit]` is the destination archetype's index when component `bit` is added to this archetype's mask. `remove[bit]` is the destination when `bit` is removed. `-1` means the edge has not been resolved yet and the caller falls back to the mask-to-table hashmap.

The edges are wired eagerly when a new archetype is created. `wireEdgesForNewTable` in [world.go](../world.go) does two passes:

```go
// First: incoming edges from existing archetypes that are one bit away.
for bit := uint8(0); bit < w.registry.nextBit; bit++ {
    bitMask := Mask(1) << bit
    for existingIndex, existing := range w.tables {
        if existingIndex == newTableIndex { continue }
        if existing.Mask | bitMask == newMask {
            w.tableEdges[existingIndex].add[bit] = int32(newTableIndex)
        }
        if existing.Mask &^ bitMask == newMask {
            w.tableEdges[existingIndex].remove[bit] = int32(newTableIndex)
        }
    }
}

// Second: outgoing edges from the new archetype to existing ones.
for bit := uint8(0); bit < w.registry.nextBit; bit++ {
    bitMask := Mask(1) << bit
    if destIndex, ok := w.tableLookup[newMask | bitMask]; ok {
        w.tableEdges[newTableIndex].add[bit] = int32(destIndex)
    }
    if newMask & bitMask != 0 {
        if destIndex, ok := w.tableLookup[newMask &^ bitMask]; ok {
            w.tableEdges[newTableIndex].remove[bit] = int32(destIndex)
        }
    }
}
```

The first loop wires every neighbor's incoming edge to the new table. The second wires the new table's outgoing edges to every neighbor that already exists. Both passes happen once at archetype creation; the actual migrations later just read the cached array index.

`world.AddComponents` and `world.RemoveComponents` consult the edge cache only when the mask is a single bit (`bits.OnesCount64(uint64(mask)) == 1`). Multi-bit mask transitions fall back to the hashmap lookup. The single-bit case dominates real workloads.

## The query cache

A query like "every entity with POSITION and VELOCITY" boils down to "every archetype whose mask satisfies POSITION | VELOCITY." That list is fixed until a new archetype is created. Memoizing it turns the per-call O(archetype-count) scan into a single hashmap lookup.

```go
type World struct {
    queryCache map[Mask][]int  // include-mask -> sorted list of archetype indices
    // ...
}
```

The cache is populated on first use via `cachedTables`:

```go
func (w *World) cachedTables(include Mask) []int {
    if cached, ok := w.queryCache[include]; ok {
        return cached
    }
    matching := make([]int, 0, len(w.tables))
    for index, table := range w.tables {
        if table.Mask & include == include {
            matching = append(matching, index)
        }
    }
    w.queryCache[include] = matching
    return matching
}
```

Every call to `world.Query`, `world.ForEach`, `freecs.Iter1`...`Iter4`, `freecs.ParallelIter1`...`ParallelIter4`, and `freecs.IterChanged1`...`IterChanged4` routes through `cachedTables`. The exclude mask is applied at iteration time inside each entry, not at cache time, so a single cache entry serves multiple exclude variants efficiently.

## Incremental invalidation

When `getOrCreateTable` creates a new archetype, the query cache might be stale: existing query masks that the new archetype satisfies aren't in their cached lists yet. The fix is small and surgical.

```go
func (w *World) invalidateQueryCacheForNewTable(newMask Mask, newTableIndex int) {
    for queryMask, cached := range w.queryCache {
        if newMask & queryMask == queryMask {
            w.queryCache[queryMask] = append(cached, newTableIndex)
        }
    }
}
```

This walks every cached query, checks whether the new archetype's mask is a superset of the query mask, and appends the index to the cache entry if so. We never invalidate entries that are still correct; we never rebuild the cache from scratch. The walk is O(distinct-query-masks-ever-issued), and adding a new archetype is also amortized O(1) per query.

Removing archetypes (which freecs-go doesn't do) would require true invalidation. Since archetypes are created on demand and never removed, the cache only ever grows.

## The query surface

There are three iteration shapes, plus the change-detection and parallel variants.

```go
// 1. Entity-yielding, lightest weight, generic ranges.
for entity := range world.Query(POSITION|VELOCITY, 0) {
    position, _ := freecs.Get[Position](world, entity)
    // ...
}

// 2. Direct table access, untyped per-component.
world.ForEach(POSITION|VELOCITY, 0, func(entity freecs.Entity, table *freecs.Archetype, index int) {
    positions, _ := freecs.Column[Position](world, table)
    velocities, _ := freecs.Column[Velocity](world, table)
    positions[index].X += velocities[index].X
})

// 3. Typed inner loop, ergonomic and fast.
freecs.Iter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
    position.X += velocity.X
})
```

Use `world.Query` when you want a simple loop and don't care about per-entity overhead. Use `world.ForEach` when you want direct table access without committing to a fixed arity (useful for systems that pull two or three components by case). Use `Iter1` through `Iter4` for tight inner loops that touch a known set of component types; the compiler inlines the typed pointer derefs and the loop body looks like hand-written column code.

## Helpers

`world.QueryFirst(include, exclude) (Entity, bool)` returns the first match and short-circuits. Useful when you just need any entity that satisfies the query (a singleton, the active camera, etc.).

`world.CountQuery(include, exclude) int` returns the count without yielding entities. Implemented as a sum of `len(table.Entities)` over matching archetypes; no allocation.

## Constraint on iteration callbacks

Callbacks run by `ForEach`, `Iter*`, and `ParallelIter*` must not mutate world topology. Spawning, despawning, adding or removing components, or setting a component that's currently missing all rearrange the archetype slices that the callback is reading. The cached `count := len(table.Entities)` taken at the top of each archetype's loop becomes stale; the typed `[]A` view materialized via `unsafe.Slice` may alias freed or reordered memory.

The safe way to mutate during iteration is the command buffer:

```go
freecs.Iter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
    if health.Value <= 0 {
        world.QueueDespawn(entity)
    }
})
world.ApplyCommands()
```

`world.QueueDespawn` appends a closure to a deferred slice. The iteration finishes uninvalidated; `world.ApplyCommands` drains the closures afterward. See [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md) for the full set of queue methods.

## Where to look in the code

- [`registry.go`](../registry.go), the type-to-bit mapping that drives masks
- [`archetype.go`](../archetype.go), `Archetype` and `tableEdges`
- [`world.go`](../world.go), `getOrCreateTable`, `cachedTables`, `wireEdgesForNewTable`, `invalidateQueryCacheForNewTable`
- [`spawn.go`](../spawn.go), `AddComponents` and `RemoveComponents` with the edge fastpath
- [`query.go`](../query.go), `Query`, `ForEach`, `Iter1` through `Iter4`, `Column`
- [`parallel.go`](../parallel.go), `ParallelIter1` through `ParallelIter4`
