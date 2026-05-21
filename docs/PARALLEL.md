# Parallel iteration

A frame on a multi-core machine has more cores than systems. The schedule runs systems in order, so two systems do not run side by side. Inside a single system, though, the iteration over matching archetypes is embarrassingly parallel: each archetype owns its own column memory and no two archetypes share storage. `ParallelIter1` through `ParallelIter4` fan that work out across goroutines, one per matching archetype, joined by a `sync.WaitGroup`. The hot path inside each goroutine is byte-for-byte identical to the serial `Iter*` family.

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

For each archetype that matches the query, we launch one goroutine. Each goroutine owns its archetype's columns for the duration of the call; no two goroutines touch the same column. The main goroutine waits for all of them at the end.

`unsafe.Slice` materializes typed `[]A` and `[]B` views inside each goroutine. The slice headers live on the goroutine's stack; the backing arrays are owned by the column. The loop body has no synchronization overhead because nothing is shared.

## Why per-archetype, not per-row

Per-archetype fan-out is the natural unit. Each archetype is independent storage and no coordination between archetypes is needed. A finer-grained scheme (chunks of N rows within each archetype) would give more parallelism on workloads where one archetype dominates, but the cost is a chunking strategy, a per-chunk launch or a worker pool, and synchronization across goroutines within an archetype. None of that is worth the complexity for the typical case where a query matches several archetypes and the per-entity work is already amortized across multiple cores by the archetype fan-out.

If your workload has one giant archetype holding ninety percent of the entities, `ParallelIter*` does not help. The right move there is to handle that archetype directly with `freecs.Column[T]` and your own goroutine chunking, or to keep the serial `Iter*` and rely on per-frame parallelism between systems running on other cores.

## What the callback cannot do

The callback runs concurrently with sibling goroutines walking other archetypes. Two things are forbidden inside it. Both are documented at the function comments and matter for correctness.

### No structural mutation

The callback must not call `world.Spawn`, `world.Despawn`, `world.AddComponents`, `world.RemoveComponents`, `freecs.Set` with a missing component, `freecs.AddTag`, or any other operation that changes which archetype an entity lives in.

Those operations rearrange archetype storage. The cached `count := len(table.Entities)` taken at the top of the goroutine becomes stale; the `unsafe.Slice` view over the column may alias freed memory after a swap-remove. Worse, a sibling goroutine walking a different archetype might be the migration target, so the corruption can show up cross-archetype.

The in-iter mutation guard on the serial `Iter*` family panics on these calls. `ParallelIter*` does not bracket with `enterIter`/`leaveIter` because the panic would land on a worker goroutine instead of the caller. The constraint is documented and the safe alternative is the same as for the serial form: the command buffer.

```go
freecs.ParallelIter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
    if health.Value <= 0 {
        world.QueueDespawn(entity)
    }
})
world.ApplyCommands()
```

There is a wrinkle. The command buffer is not goroutine-safe by default. Multiple goroutines calling `world.QueueDespawn` concurrently would race on `world.commandBuffer = append(...)`. The cleaner shape is to collect locally and queue after `wg.Wait` returns:

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

A channel works. A per-archetype slice of pending operations concatenated after the wait works. The library does not pick one; it depends on what fits the surrounding code.

### Touch only your own pointers

A callback that reads or writes other entities' components from inside `ParallelIter*` can race with a sibling goroutine walking a different archetype. The typed pointers `*A` and `*B` the callback receives are safe; they point into the archetype this goroutine owns exclusively. But `freecs.Get[OtherComponent](world, otherEntity)` could resolve to a column on a different archetype that another goroutine is mutating.

The rule of thumb is to read and write through the supplied pointers and treat the rest of the world as off-limits inside the callback. If the callback needs to consult resources or other entities, capture the relevant values into its closure beforehand.

## One ParallelIter at a time per world

The query cache fills on first use of a given include mask. The fill is a write to `world.queryCache` from the main goroutine before fan-out, so a single `ParallelIter*` call is safe.

Running two `ParallelIter*` calls concurrently against the same world (from two different goroutines) would race on `world.queryCache` if both miss on different masks. The library does not lock the query cache; the lock would slow the hot path for the supported use case, which is "one parallel iteration at a time within a frame."

Within a single frame, run `ParallelIter*` calls serially. The parallelism is inside each call, not across calls. If you genuinely need concurrent parallel iteration against the same world, pre-warm the query cache by issuing a quick `world.CountQuery(mask, 0)` for each query you plan to run, before kicking off the goroutines that will call `ParallelIter*`. Pre-warming pays the cache-fill cost on the main goroutine, and subsequent `ParallelIter*` calls just read.

## When ParallelIter actually wins

The speedup conditions are concrete. The query has to match several archetypes (otherwise there is no fan-out). Each archetype has to hold enough entities that the per-entity work outweighs the goroutine launch overhead. And the per-entity work itself has to be non-trivial; a couple of float adds are eaten by the launch cost. The rough threshold is a few hundred entities per archetype doing real work (vector math, sampling, light physics).

The speedup is absent or negative when only one archetype matches and holds all the entities, when the per-entity work is tiny, or when `ParallelIter*` is called many times per frame in series. Each call pays goroutine setup, and serial parallel calls add up. Profiling tells you which side of the line your workload is on; the library does not auto-decide.

## Goroutine cost

Go goroutines are cheap as concurrency primitives go, but they are not free. Setting one up costs a few hundred nanoseconds; scheduling it onto a core costs more. For very small archetypes (a few rows of trivial work), the parallel version is slower than the serial version because the setup overhead dominates.

The library does not use a worker pool. Each call to `ParallelIter*` launches fresh goroutines and lets them exit when they are done. A worker pool would amortize the setup cost across calls but add complexity in pool sizing, lifetime, and shutdown. For a library-level helper at the granularity of "one archetype per goroutine," fresh-launch is simpler and competitive. If profiling shows the launch overhead is the bottleneck, the right next step is fewer parallel calls (consolidate systems that walk the same archetypes), bigger archetypes (denser worlds), or a worker pool inside your own code that pulls archetype indices off a channel.

## Where to look in the code

- [`parallel.go`](../parallel.go), the package doc for parallel iteration
- [`parallel_gen.go`](../parallel_gen.go), the generated `ParallelIter1` through `ParallelIter4`
- [`iter_gen.go`](../iter_gen.go), the serial `Iter1` through `Iter6` (parallel mirrors their structure)
- [`world.go`](../world.go), `cachedTables` (the query cache that ParallelIter relies on)
- [`column.go`](../column.go), the column structure that each goroutine reads
