# Performance

freecs-go's performance story is "hot path stays hot, cold path is acceptable." This document is an honest accounting of where the cycles go, what's fast, what's slow, and what the design tradeoffs cost in practice.

## The hot path

The hot path is iteration. A frame-rate-bound game runs a handful of systems each frame, each system walks one or more queries, and each query touches every matching entity. The cost of iteration directly determines how many entities the game can afford to have.

Consider a typical inner loop:

```go
freecs.Iter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
    position.X += velocity.X * delta
    position.Y += velocity.Y * delta
})
```

What actually happens per archetype:

1. `world.cachedTables(POSITION | VELOCITY)` is a hashmap lookup. One probe.
2. For each matching archetype, read `table.Mask & exclude` (one CPU instruction).
3. Read `count := len(table.Entities)` (a slice header field load).
4. Build typed slice views via `unsafe.Slice` on the column data pointers. Two slice header constructions on the stack. No allocation.
5. Loop `count` iterations. Each iteration:
   - Load `table.Entities[i]` (it's read but the callback ignores it; the compiler can optimize this load away when the callback parameter is `_`).
   - Compute `&aSlice[i]` and `&bSlice[i]` (one bounds check each unless the compiler proves them safe, plus pointer arithmetic).
   - Call the closure with three pointers.

The closure call is the only thing that's not pure pointer arithmetic. The Go compiler can't inline across closure boundaries today, so each iteration has the function call cost. For closures that do real work (vector math, multiple field updates), the call cost is amortized. For trivial closures (one field add), the call cost becomes meaningful.

If you measure the inner loop with `pprof` and the closure call shows up as a hotspot, the workaround is `world.ForEach` plus `freecs.Column[T]` to get typed slices in scope and write the loop body directly:

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

Now the inner loop has no per-element function call. The compiler can vectorize it the same way it would for any plain slice loop.

## The cold path

The cold path is structural change: spawn, despawn, add components, remove components. These don't run per-frame for every entity; they run per spawn or per state transition. Modest churn doesn't matter; a level loading thousands of entities at once does.

The biggest cold-path cost is reflection in column operations:

- `column.pushZero` calls `reflect.Append(c.slice, reflect.Zero(c.elemType))`. Each call allocates a fresh `reflect.Value` for the zero, then `reflect.Append` may reallocate the backing array. Multiple allocations per push.
- `column.migrateFrom` calls `reflect.Append(c.slice, src.slice.Index(srcIndex))`. Same allocation pattern.
- `column.swapRemove` calls `c.slice.Index(index).Set(c.slice.Index(last))` and `c.slice.Slice(0, last)`. The `Set` path goes through `reflect.Value.Set` which type-checks at runtime.

For an entity with five components, a single `Spawn` involves five `reflect.Append` calls. For a migration that moves three columns from source to destination and pushes two new defaults, it's eight reflection calls plus three `swap_remove` operations.

A back-of-envelope estimate: each reflect call is in the hundreds of nanoseconds. A migration of an entity with 5 components is probably in the 5-15 microseconds range, depending on whether `reflect.Append` reallocates.

For a typical game spawning a few dozen entities per frame, this is well below the per-frame budget. For a project that spawns thousands of entities at once (level load, mass particle emission), the cost is real but front-loaded; it's not on the per-frame critical path.

## Allocation summary

Per-frame allocations from the library:

- `world.cachedTables` returns a slice that may be cached or freshly built. After warmup, every iteration returns the same backing slice, no allocation.
- `freecs.Iter*` and `freecs.ParallelIter*` build typed slice views via `unsafe.Slice`. The slice headers are stack values; no allocation.
- `world.QueueX` methods allocate one closure each (a small heap object plus the captured variables). For a few hundred queued commands per frame this is fine.
- `freecs.Send[T]` may grow the event queue's `current` slice via `append`. Amortized O(1).
- `world.Step` doesn't allocate.

Per-structural-change allocations:

- `reflect.MakeSlice` when an archetype is first created. One-time per unique component set.
- `reflect.Append` may allocate when a column grows. Amortized.
- `getOrCreateTable` allocates an `Archetype` and its `tableEdges` on first use of a new mask. One-time per unique component set.

The two patterns that allocate the most per frame:

1. Spawn-heavy systems (bullet hells, particle emitters). Each spawn pushes onto every relevant column, each push goes through `reflect.Append`, each `reflect.Append` may reallocate. Mitigate with `world.SpawnBatch` which amortizes the slice growth across the batch.

2. Command-buffer-heavy systems. Each `Queue*` call allocates a closure. For one-off queues this is negligible; for thousands per frame it adds up. If the cost matters, restructure: collect pending operations in a typed slice and apply them in a loop after the iteration, avoiding the closure cost.

## Compared to a typed-column ECS

freecs-go uses reflection because Go has no generic struct fields. A library that did codegen instead (one typed `[]T` field per component) would skip the reflection entirely on the cold path. The inner loop would be identical.

Concretely, the speedup from codegen-over-reflection would be:

- Spawn: ~2-5x faster (no reflect.Append, just typed slice append).
- Despawn: similar.
- Migration: ~2-3x faster.
- Iteration: no difference.

For games that spawn modestly and iterate aggressively, the speed difference is irrelevant; iteration dominates and is already as fast as it can be. For games with extreme spawn churn (bullet hells, large-particle simulations), codegen would help, but `SpawnBatch` closes much of the gap.

The Rust freecs design uses a macro to do exactly this codegen. freecs-go could ship a `go generate` helper that emits typed accessors on top of the generic core, but the generic core is what's needed regardless; the codegen would be a thin wrapper.

## Compared to a typed-pointer-arithmetic ECS

A library that stored each column as `[]byte` aliasing typed memory and accessed elements via `unsafe.Add(ptr, i*size)` would skip reflection on the cold path too. But that's not safe in Go: the GC tracks slices by element type, and `[]byte` is treated as `noscan` (no pointer scanning), so any pointer fields inside components would silently be lost.

freecs-go uses `reflect.MakeSlice` for allocation precisely to keep the GC informed. On the hot path it falls back to `unsafe.Slice` for typed views, which works because the underlying memory was allocated with a known element type.

The library `arche` is the Go ECS that's closest to "typed pointer arithmetic" for column storage. It's faster than freecs-go on the cold path but has the same hot path performance, and trades some flexibility for the speed.

## Things that look slow but aren't

Several patterns in the codebase look like they might cost something but don't, at least not in the inner loop.

**`reflect.Value` field on column**. The reflect value carries the underlying slice's type info, but the hot path never touches it. `dataPtr` is cached; index access goes through `unsafe.Slice` which doesn't consult the reflect value.

**Type-erased event/tag/resource storage via `reflect.Type` map keys**. The lookup happens once per `Send` / `AddTag` / `Resource` call, not per element. For a system that sends one event and reads one resource per frame, the lookup cost is negligible.

**The `changed []uint32` parallel slice**. It's only read by `IterChanged*` and `Changed`. Regular `Iter*` doesn't touch it. The write happens once per `GetMut`/`Set`/spawn/migration, off the hot iteration path.

**`Archetype.columns [64]*column`**. Sparse array with most entries nil. Looks wasteful at 512 bytes per archetype, but the indexing is a single load on the hot path, and 512 bytes is negligible compared to the actual column data.

## Things that genuinely cost time

**Closure call per element in `Iter*`**. The Go compiler can't inline through a closure parameter. For trivial inner bodies this is the biggest single overhead. Workaround: use `world.ForEach` plus `freecs.Column[T]` and write the loop body directly.

**`reflect.Append` on every column push**. Mitigate with `SpawnBatch` for spawn-heavy paths.

**Goroutine launch in `ParallelIter*`**. Each call launches fresh goroutines per archetype. For tiny archetypes, the launch dominates. Use the serial form when archetypes are small.

**Bounds checks in `aSlice[i]`**. The Go compiler eliminates these only when it can prove the index is in range from local context. For typed slice views built via `unsafe.Slice`, the compiler often can't prove safety. Each access pays one compare and branch.

If a hot loop is bound-check-limited, profile with `go build -gcflags=-d=ssa/check_bce/debug=1` to see which loads can't be eliminated, then either restructure the loop or accept the cost. Most loops accept it because the bound checks are predictable and cheap; pipelined branches are practically free.

## Practical sizing

For a game with:

- A few hundred entities, dozen-ish component types, mostly steady state: any pattern works. The library is overkill for this scale but won't hurt.
- A few thousand entities, dozens of archetypes, moderate spawn churn: the standard hot path holds up. Use `SpawnBatch` for bulk spawns.
- Tens of thousands of entities, complex archetypes, heavy spawn churn (particle systems): use `world.ForEach` plus `freecs.Column[T]` for inner loops, `SpawnBatch` everywhere, consider `ParallelIter*` for systems that touch every entity each frame.
- Hundreds of thousands of entities: profile and re-architect. The 64-component ceiling will probably bite first, multi-world is the answer; reflection overhead on the cold path will probably bite second, batch your structural changes.

## Where to look in the code

- [`column.go`](../column.go), where the reflect/unsafe split lives
- [`spawn.go`](../spawn.go), the structural-change paths and their cost
- [`query.go`](../query.go), the iteration helpers
- [`parallel.go`](../parallel.go), the goroutine-per-archetype fan-out
- [`world.go`](../world.go), `cachedTables` and the query cache
