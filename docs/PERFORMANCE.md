# Performance

A frame-rate-bound game spends most of its CPU time in iteration. A handful of systems run each frame, each system walks one or more queries, and each query touches every matching entity. Structural change (spawn, despawn, add or remove components) runs at much lower volume: a few dozen per frame in steady state, sometimes a few thousand in a single burst at level load. freecs-go is shaped around that asymmetry. The hot iteration path is sequential reads through typed memory with no per-element reflection. The cold structural-change path uses `reflect` for column allocation and migration, because the GC has to track the pointers and the reflect cost amortizes across the lifetime of the column.

This post is the honest accounting of where the cycles go.

## The hot path

A typical inner loop:

```go
freecs.Iter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
    position.X += velocity.X * delta
    position.Y += velocity.Y * delta
})
```

Per archetype, the library does the same handful of operations every iteration. It hits the query cache for the matching archetype list (a hashmap lookup, one probe). For each archetype, it reads `table.Mask & exclude` to skip excluded archetypes (one CPU instruction) and `count := len(table.Entities)` to bound the inner loop (one load). It builds typed slice views over the columns with `unsafe.Slice`, which is two slice header constructions on the stack with no allocation. Then it runs the inner loop, which loads `table.Entities[i]` if the callback uses it (the compiler elides the load when the entity parameter is `_`), computes `&aSlice[i]` and `&bSlice[i]` with one bounds check each unless the compiler proves them safe, and calls the closure.

The closure call is the only thing that is not pure pointer arithmetic. The Go compiler cannot inline across closure boundaries today, so each iteration has the function call cost. For closures that do real work (vector math, multiple field updates) the call cost is amortized. For trivial closures (one field add) the call cost becomes a meaningful fraction of the loop.

If you profile an inner loop with `pprof` and the closure call shows up as a hotspot, the workaround is `world.ForEach` plus `freecs.Column[T]`. The slices come into scope and the loop body is written directly:

```go
world.ForEach(POSITION|VELOCITY, 0, func(_ freecs.Entity, table *freecs.Archetype, _ int) {
    positions, _ := freecs.Column[Position](world, table)
    velocities, _ := freecs.Column[Velocity](world, table)
    for i := range positions {
        positions[i].X += velocities[i].X * delta
        positions[i].Y += velocities[i].Y * delta
    }
})
```

Now the inner loop has no per-element function call. The compiler can vectorize it the same way it would for any plain slice loop, and the bounds checks tend to eliminate cleanly because the loop bound and the index come from the same `len`.

## The cold path

Structural change is `Spawn`, `Despawn`, `AddComponents`, `RemoveComponents`, and the typed wrappers around them. These do not run per-frame for every entity; they run per spawn or per state transition. Modest churn does not matter. A level loading thousands of entities at once is when the costs add up.

The biggest cold-path cost is reflection in column operations. `column.pushZero` calls `reflect.Append(c.slice, reflect.Zero(c.elemType))`. Each call allocates a fresh `reflect.Value` for the zero, and `reflect.Append` may reallocate the backing array. `column.migrateFrom` calls `reflect.Append(c.slice, src.slice.Index(srcIndex))` with the same allocation pattern. `column.swapRemove` calls `c.slice.Index(index).Set(c.slice.Index(last))` followed by `c.slice.Slice(0, last)`; the `Set` path goes through `reflect.Value.Set` which type-checks at runtime.

For an entity with five components, a single `Spawn` involves five `reflect.Append` calls. A migration that moves three columns from source to destination and pushes two new defaults is eight reflection calls plus three swap-removes. Each reflect call is in the hundreds of nanoseconds. A migration of an entity with five components is probably in the 5-15 microsecond range, depending on whether `reflect.Append` reallocates.

For a typical game spawning a few dozen entities per frame, this is well below the per-frame budget. For a project that spawns thousands of entities at once (level load, mass particle emission), the cost is real but front-loaded; it does not sit on the per-frame critical path.

## Allocations per frame

After warmup, the per-frame allocation profile is small. `world.cachedTables` returns the cached slice for any query mask that has been seen before; new query masks pay a one-time allocation. `freecs.Iter*` and `freecs.ParallelIter*` build typed slice views with `unsafe.Slice`, and the slice headers are stack values. `world.QueueX` methods allocate one closure each, which is a small heap object plus whatever variables the closure captures; for a few hundred queued commands per frame this is negligible. `freecs.Send[T]` may grow the event queue's `current` slice via `append`, which is amortized O(1). `world.Step` does not allocate.

Per-structural-change, the allocations are concentrated. `reflect.MakeSlice` runs once when an archetype is first created. `reflect.Append` may allocate when a column grows. `getOrCreateTable` allocates an `Archetype` and its `tableEdges` on first use of a new mask, also a one-time cost per unique component set.

The two patterns that allocate the most per frame in real workloads are spawn-heavy systems (bullet hells, particle emitters) and command-buffer-heavy systems. The spawn-heavy case pushes onto every relevant column, each push goes through `reflect.Append`, and each `reflect.Append` may reallocate. `world.SpawnBatch` is the mitigation; it amortizes the slice growth across the batch. The command-buffer-heavy case allocates one closure per `Queue*` call. For one-off queues this is negligible; for thousands per frame it adds up. If the cost matters, restructure to collect pending operations in a typed slice and apply them in a loop after the iteration, skipping the closure entirely.

## Compared to a typed-column ECS

freecs-go uses reflection because Go has no generic struct fields. A library that did codegen instead (one typed `[]T` field per component) would skip the reflection entirely on the cold path. The inner loop would be identical.

The speedup from codegen-over-reflection lands roughly at two to five times faster on spawn (no `reflect.Append`, just a typed slice append), similar on despawn, two to three times faster on migration. Iteration is unchanged because the hot path already bypasses reflect.

For games that spawn modestly and iterate aggressively, the speed difference does not matter; iteration dominates and is already as fast as it can be. For games with extreme spawn churn (bullet hells, large-particle simulations) codegen would help, but `SpawnBatch` closes most of the gap by amortizing the per-entity reflect cost across the batch.

The Rust freecs design uses a macro to do exactly this codegen. freecs-go could ship a `go generate` helper that emits typed accessors on top of the generic core, but the generic core is what is needed regardless; the codegen would be a thin wrapper.

## Compared to a typed-pointer-arithmetic ECS

A library that stored each column as `[]byte` aliasing typed memory and accessed elements with `unsafe.Add(ptr, i*size)` would skip reflection on the cold path too. That approach is not safe in Go. The garbage collector tracks slices by element type, and `[]byte` is treated as `noscan` (no pointer scanning), so any pointer fields inside components would silently be lost.

freecs-go uses `reflect.MakeSlice` for allocation precisely to keep the GC informed of the slice's true element type. On the hot path it falls back to `unsafe.Slice` for typed views, which is safe because the underlying memory was allocated with a known element type that the GC already tracks.

The library `arche` is the Go ECS that comes closest to "typed pointer arithmetic" for column storage. It is faster than freecs-go on the cold path and has the same hot path performance, with some flexibility traded for the speed.

## Things that look slow but are not

The `reflect.Value` field on `column` carries the underlying slice's type info. The hot path never touches it. `dataPtr` is cached at column construction time; index access goes through `unsafe.Slice` which does not consult the reflect value.

The type-erased event, tag, and resource storage via `reflect.Type` map keys looks like it would cost something. The lookup happens once per `Send` or `AddTag` or `Resource` call, not per element. For a system that sends one event and reads one resource per frame, the cost is invisible.

The parallel `changed []uint32` slice next to every component column looks like extra memory pressure. It is read only by `IterChanged*` and `Changed`. Regular `Iter*` does not touch it. The write happens once per `GetMut`/`Set`/spawn/migration, off the hot iteration path.

The `Archetype.columns [64]*column` sparse array with mostly-nil entries looks wasteful at 512 bytes per archetype. The indexing is a single load on the hot path, and 512 bytes per archetype is negligible compared to the actual column data.

## Things that genuinely cost time

The closure call per element in `Iter*` is the biggest single overhead in the inner loop. The Go compiler cannot inline through a closure parameter. For trivial bodies this dominates the per-iteration cost. The workaround is `world.ForEach` plus `freecs.Column[T]`.

`reflect.Append` on every column push is the second. `SpawnBatch` is the mitigation for spawn-heavy paths.

Goroutine launch in `ParallelIter*` matters when archetypes are small. Each call launches fresh goroutines per archetype, and the launch overhead can swamp the per-archetype work for tiny archetypes. Use the serial form when archetypes are small.

Bounds checks in `aSlice[i]` add a compare and branch per access. The Go compiler eliminates them only when it can prove the index is in range from local context, and for slice views built via `unsafe.Slice` the compiler often cannot. Profile with `go build -gcflags=-d=ssa/check_bce/debug=1` to see which loads cannot be eliminated. Most loops accept the cost because the branches are predictable and cheap; pipelined branches are practically free.

## Practical sizing

For a game with a few hundred entities and a dozen-ish component types in steady state, any pattern works. The library is overkill for this scale but does not hurt.

For a few thousand entities, dozens of archetypes, and moderate spawn churn, the standard hot path holds up. Use `SpawnBatch` for bulk spawns and `Iter*` everywhere else.

For tens of thousands of entities, complex archetypes, and heavy spawn churn (particle systems), use `world.ForEach` plus `freecs.Column[T]` for inner loops, `SpawnBatch` everywhere, and `ParallelIter*` for systems that touch every entity each frame.

Past a hundred thousand entities, profile and re-architect. The 64-component ceiling will probably bite first; multi-world is the answer. Reflection overhead on the cold path will probably bite second; batch the structural changes.

## Where to look in the code

- [`column.go`](../column.go), where the reflect/unsafe split lives
- [`spawn.go`](../spawn.go), the structural-change paths and their cost
- [`query.go`](../query.go), the iteration helpers and `ForEach`
- [`iter_gen.go`](../iter_gen.go) and [`parallel_gen.go`](../parallel_gen.go), the generated `Iter*` and `ParallelIter*`
- [`world.go`](../world.go), `cachedTables` and the query cache
