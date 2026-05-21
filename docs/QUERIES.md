# Queries and caches

A physics system asks the world for every entity with `Position` and `Velocity`. A render system asks for every entity with `Transform` and `Mesh`. A pathfinding system asks for every navmesh-aware unit minus the ones currently frozen. Across a serious frame loop the world fields hundreds of queries per frame, against dozens of archetypes. Walking that naively is O(archetype-count) per query; doing it hundreds of times per frame turns into real CPU time even when each individual scan is cheap. Two caches in freecs-go turn the common case into amortized O(1): an archetype graph for single-bit structural transitions, and a query cache for iteration.

## The mask

`Mask` is `uint64`, one bit per registered component type. `freecs.Register[T](world)` assigns the next available bit (zero-indexed) and returns the single-bit `Mask` for that type. The bit budget is sixty-four per `World`; past that you split across worlds with a `MultiWorld`.

Composing a query mask is bitwise OR:

```go
mask := POSITION | VELOCITY | HEALTH
```

The test "does an archetype carry all of these" is one CPU instruction:

```go
if table.Mask & mask == mask { /* matches */ }
```

The test "does it carry any of these" (for the exclude case) is also one instruction:

```go
if table.Mask & exclude != 0 { /* excluded */ }
```

Both tests inline into the iteration loop and do not allocate. The whole reason masks are bitfields is that the matching test is a single instruction; switching to a more elaborate include/exclude predicate would cost more than the storage layout saves.

## Archetypes are created lazily

The first time you spawn an entity with a specific mask, the world creates an `Archetype` for that mask via `getOrCreateTable`. The mask-to-index mapping lives in `world.tableLookup`, a `map[Mask]int`. Subsequent spawns and migrations with the same mask reuse the same table.

`getOrCreateTable` does three things on the cache-miss path. It allocates the new `Archetype` and appends it to `world.tables`. It patches the query cache (below) to include the new archetype where it satisfies an existing cached query. And it wires the archetype graph edges (also below) so future single-bit migrations into and out of this archetype skip the mask-to-table hashmap.

The miss path costs a few small allocations and an O(archetype-count) scan. The hit path is one hashmap lookup. Since archetype creation is one-time per unique component set, the miss cost amortizes to zero across the lifetime of the program.

## The archetype graph cache

Most structural changes flip exactly one bit. A "stunned" effect toggles a `STUN` component on and off. An equipment system adds and removes individual items. The destination archetype when going from "source archetype" plus "one bit toggled" is a fixed mapping, the same every time. Memoizing it turns each transition into an array lookup.

`tableEdges` (in [archetype.go](../archetype.go)) stores the mapping per source archetype:

```go
type tableEdges struct {
    add    [maxComponents]int32
    remove [maxComponents]int32
}
```

`add[bit]` is the destination archetype's index when component `bit` is added to this archetype's mask. `remove[bit]` is the destination when `bit` is removed. `-1` means the edge has not been resolved yet and the caller falls back to the mask-to-table hashmap.

The edges are wired eagerly when a new archetype appears. `wireEdgesForNewTable` in [world.go](../world.go) does two passes:

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

The first loop walks the existing tables and sets edges into the new table from any older table that is one bit-flip away. The second walks the component bits and sets edges out of the new table to any older table that is one bit-flip away in the other direction. Both passes happen once at archetype creation. The actual migrations later just read the cached array index.

Why two passes. The first one is one-directional: every existing archetype gets its outgoing edges to the new archetype filled, but the new archetype's outgoing edges to existing ones do not get filled by anybody else's creation event. The second loop is what fills the new archetype's outgoing edges so every cached migration path is hot from the moment both endpoints exist.

`world.AddComponents` and `world.RemoveComponents` consult the edge cache only when the mask is a single bit (`bits.OnesCount64(uint64(mask)) == 1`). Multi-bit transitions fall back to the hashmap lookup. The single-bit case dominates real workloads; multi-bit changes are rare enough that the extra cache complexity is not worth it.

## The query cache

A query like "every entity with `POSITION` and `VELOCITY`" boils down to "every archetype whose mask satisfies `POSITION | VELOCITY`." That list is fixed until a new archetype is created. Memoizing it turns the per-call O(archetype-count) scan into a single hashmap lookup:

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

Every call to `world.Query`, `world.ForEach`, `freecs.Iter1`...`Iter6`, `freecs.ParallelIter1`...`ParallelIter4`, and `freecs.IterChanged1`...`IterChanged6` routes through `cachedTables`. The exclude mask is applied at iteration time inside each entry, not at cache time, so a single cache entry serves multiple exclude variants efficiently. An iteration with `(POSITION, PLAYER)` excluded reuses the cached match list for `POSITION` and filters at the table boundary.

## Incremental invalidation

When `getOrCreateTable` creates a new archetype, the query cache might be stale: existing query masks that the new archetype satisfies are not in their cached lists yet. The fix is small and surgical.

```go
func (w *World) invalidateQueryCacheForNewTable(newMask Mask, newTableIndex int) {
    for queryMask, cached := range w.queryCache {
        if newMask & queryMask == queryMask {
            w.queryCache[queryMask] = append(cached, newTableIndex)
        }
    }
}
```

This walks every cached query, checks whether the new archetype's mask is a superset of the query mask, and appends the index to the cache entry if so. Entries that are still correct are never touched. Nothing rebuilds from scratch. The walk is O(distinct-query-masks-ever-issued), and adding a new archetype is amortized O(1) per cached query.

Removing archetypes would require true invalidation. freecs-go does not remove archetypes; once created, an archetype lives for the lifetime of the world. The query cache only ever grows.

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

Use `world.Query` when you want a simple loop and do not care about per-entity overhead. Use `world.ForEach` when you want direct table access without committing to a fixed arity, which is useful for systems that pull two or three components by case. Use `Iter1` through `Iter6` for tight inner loops over a known set of component types; the compiler inlines the typed pointer derefs and the loop body looks like hand-written column code.

The `Iter*`, `IterChanged*`, and `ParallelIter*` families are generated from a single template in `internal/gen_iter`. Extending Iter past six components or IterChanged past six is a one-line edit to the generator and a `go generate` run.

## Helpers

`world.QueryFirst(include, exclude) (Entity, bool)` returns the first match and short-circuits. Useful when you just need any entity that satisfies the query (a singleton, the active camera, the player).

`world.CountQuery(include, exclude) int` returns the count without yielding entities. Implemented as a sum of `len(table.Entities)` over matching archetypes; no allocation.

## The in-iter mutation guard

Callbacks run by `ForEach` and `Iter*` must not mutate world topology. Spawning, despawning, adding or removing components, or setting a component that is currently missing all rearrange the archetype slices that the callback is reading. The cached `count := len(table.Entities)` taken at the top of each archetype's loop becomes stale; the typed `[]A` view materialized via `unsafe.Slice` may alias freed or reordered memory.

The serial iteration helpers bracket their work with `enterIter`/`leaveIter` on the World, which bumps an iter-depth counter. The structural-change methods (`Spawn`, `Despawn`, `AddComponents`, `RemoveComponents`) check the counter at entry and panic with a clear message if it is non-zero. An accidental Spawn from inside `Iter*` becomes a clean stack trace at the call site instead of silent corruption.

The safe way to mutate during iteration is the command buffer:

```go
freecs.Iter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
    if health.Value <= 0 {
        world.QueueDespawn(entity)
    }
})
world.ApplyCommands()
```

`world.QueueDespawn` appends a closure to a deferred slice; the iteration finishes uninvalidated; `world.ApplyCommands` drains the closures afterward. `ParallelIter*` does not bracket with the iter guard because the panic would land on a worker goroutine instead of the caller; the constraint is the same and the safe alternative is still the command buffer. See [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md) for the full set of queue methods.

## Where to look in the code

- [`registry.go`](../registry.go), the type-to-bit mapping that drives masks
- [`archetype.go`](../archetype.go), `Archetype` and `tableEdges`
- [`world.go`](../world.go), `getOrCreateTable`, `cachedTables`, `wireEdgesForNewTable`, `invalidateQueryCacheForNewTable`, and the iter guard
- [`spawn.go`](../spawn.go), `AddComponents` and `RemoveComponents` with the edge fastpath
- [`query.go`](../query.go), `Query`, `ForEach`, `Column`
- [`iter_gen.go`](../iter_gen.go), the generated `Iter1` through `Iter6`
- [`change_gen.go`](../change_gen.go), the generated `IterChanged1` through `IterChanged6`
- [`parallel_gen.go`](../parallel_gen.go), the generated `ParallelIter1` through `ParallelIter4`
