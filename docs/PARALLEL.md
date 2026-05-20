# Parallel iteration

freecs-go provides `ParallelIter1` through `ParallelIter4`. Each one walks every matching archetype in parallel with one goroutine per archetype, joined by a `sync.WaitGroup`. The hot path inside each goroutine is byte-for-byte identical to the serial `Iter*` family.

This document covers what the parallel model actually buys you, the constraints on the callback, and the cases where the parallel form helps versus the cases where it doesn't.

## The shape

```go
func ParallelIter2[A, B any](world *World, extraInclude, exclude Mask, callback func(entity Entity, a *A, b *B)) {
    aInfo := mustComponentInfo[A](world)
    bInfo := mustComponentInfo[B](world)
    include := extraInclude | aInfo.mask | bInfo.mask
    var wg sync.WaitGroup
    for _, tableIndex := range world.cachedTables(include) {
        table := world.tables[tableIndex]
        if table.Mask & exclude != 0 { continue }
        count := len(table.Entities)
        if count == 0 { continue }
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
```

For each archetype that matches the query, we launch one goroutine. Each goroutine owns its archetype's columns for the duration of the call. No two goroutines touch the same column. The main goroutine waits for all of them at the end.

`unsafe.Slice` materializes typed `[]A` and `[]B` views inside each goroutine. The slice headers live on the goroutine's stack; the backing arrays are owned by the column. The loop body has no synchronization overhead because nothing is shared.

## Why per-archetype, not per-row

The fan-out granularity is "one goroutine per archetype." Each goroutine then walks its own rows sequentially.

The alternative would be "one goroutine per chunk of N rows." That'd give finer-grained parallelism for archetypes that hold lots of entities but few archetypes overall. The reason we don't do this:

- Each archetype is naturally an independent unit. No coordination between archetypes is needed.
- Per-row fan-out requires a chunking strategy, a per-chunk goroutine launch (or a worker pool), and synchronization across the goroutines within an archetype.
- Most realistic workloads have multiple matching archetypes (because component-set diversity is high in a real game) so per-archetype gives natural parallelism already.

If your workload has one giant archetype holding 90% of the entities, ParallelIter doesn't help you. You'd want to handle that case manually with `freecs.Column[T]` and your own goroutine chunking, or use the regular `Iter2` with a multi-core CPU and rely on per-frame parallelism between systems.

## What you can't do in the callback

Two rules. Both are documented at the function comments and matter for correctness.

### No structural mutation

The callback must not call `world.Spawn`, `world.Despawn`, `world.AddComponents`, `world.RemoveComponents`, `freecs.Set` with a missing component, `freecs.AddTag`, or any other operation that changes which archetype an entity lives in.

The reason is that those operations rearrange archetype storage. The cached `count := len(table.Entities)` taken at the top of the goroutine becomes stale; the `unsafe.Slice` view over the column may alias freed memory after a swap-remove. Worse, a sibling goroutine walking a different archetype might be the target of the migration, so the corruption can show up cross-archetype.

The safe alternative is the command buffer:

```go
freecs.ParallelIter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
    if health.Value <= 0 {
        world.QueueDespawn(entity)
    }
})
world.ApplyCommands()
```

Caveat: the command buffer is not goroutine-safe by default. Multiple goroutines calling `world.QueueDespawn` concurrently would race on `world.commandBuffer = append(...)`. If you want to queue from inside `ParallelIter*`, wrap with a mutex or collect locally and queue after `wg.Wait()` returns. The latter pattern reads more cleanly:

```go
var toDespawn []freecs.Entity
var mu sync.Mutex
freecs.ParallelIter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
    if health.Value <= 0 {
        mu.Lock()
        toDespawn = append(toDespawn, entity)
        mu.Unlock()
    }
})
for _, entity := range toDespawn {
    world.QueueDespawn(entity)
}
world.ApplyCommands()
```

Or use a channel. Or restructure the callback to write a per-archetype slice of pending operations that you concatenate after the wait. The library doesn't pick one; depends on your patterns.

### Touch only your own pointers

A callback that reads or writes other entities' components from inside `ParallelIter*` can race with a sibling goroutine walking a different archetype. The typed pointers `*A` and `*B` the callback receives are safe; they point into the archetype this goroutine owns exclusively. But `freecs.Get[OtherComponent](world, otherEntity)` could resolve to a column on a different archetype that another goroutine is mutating.

The rule of thumb: read and write through the supplied pointers; treat the rest of the world as off-limits inside the callback. If you need to consult resources or other entities, capture the relevant values into the callback's closure beforehand.

## One ParallelIter at a time per world

The query cache fills on first use of a given include mask. The fill is a write to `world.queryCache` from the main goroutine before fan-out, so a single `ParallelIter*` call is safe.

But running two `ParallelIter*` calls concurrently against the same world (from two different goroutines) would race on `world.queryCache` if both miss on different masks. The library does not lock the query cache; locking it would slow the hot path for the supported use case.

Concurrent parallel calls is not a supported pattern. Within a single frame, run `ParallelIter*` calls serially. The parallelism is *inside* each call, not across calls.

If you really need concurrent parallel iteration against the same world, pre-warm the query cache by issuing a quick `world.CountQuery(mask, 0)` for each query you plan to run, before kicking off the goroutines that will call `ParallelIter*`. Pre-warming pays the cache-fill cost on the main goroutine; subsequent `ParallelIter*` calls just read.

## When ParallelIter actually wins

You get a speedup when:

- The query matches several archetypes.
- Each archetype holds enough entities that the per-entity work outweighs the goroutine launch overhead.
- The per-entity work itself is non-trivial (more than a few arithmetic ops).

Rough rule of thumb: at least a few hundred entities per archetype doing real work (vector math, sampling, even simple physics). Below that, the goroutine setup dominates.

You don't get a speedup when:

- Only one archetype matches the query, and it holds all the entities. (No fan-out happens.)
- The per-entity work is tiny (a couple of float adds). The goroutine overhead eats the gain.
- You're calling `ParallelIter*` many times per frame in series. Each call pays goroutine setup, and serial calls add up.

Profiling tells you which side of this line your workload is on. The library does not auto-decide.

## Goroutine cost

Go goroutines are cheap as concurrency primitives go, but they're not free. Setting one up takes a few hundred nanoseconds; scheduling it onto a core costs more. For very small archetypes (a few rows of trivial work), the parallel version is slower than the serial version due to setup overhead.

The library does not use a worker pool. Each call to `ParallelIter*` launches fresh goroutines and lets them exit when they're done. A worker pool would amortize the setup cost across calls but add complexity (pool sizing, lifetime, shutdown). For a library-level helper at the granularity of "one archetype per goroutine," fresh-launch is simpler and competitive.

If profiling shows the launch overhead is the bottleneck, the right next step is either fewer parallel calls (consolidate systems that walk the same archetypes), bigger archetypes (more entities per archetype), or a worker pool inside your own code that pulls archetype indices off a channel.

## Where to look in the code

- [`parallel.go`](../parallel.go), `ParallelIter1` through `ParallelIter4`
- [`query.go`](../query.go), the serial `Iter1` through `Iter4` (parallel mirrors their structure)
- [`world.go`](../world.go), `cachedTables` (the query cache that ParallelIter relies on)
- [`column.go`](../column.go), the column structure that each goroutine reads
