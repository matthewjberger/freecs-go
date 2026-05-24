# Architecture overview

A frame-rate-bound game runs a handful of systems each frame, each system walks one or more queries, and each query touches every matching entity. The cost of iteration directly determines how many entities the game can afford to have. Everything in freecs-go (lifecycle, structural change, change detection, events, the schedule) is shaped so the inner loop is sequential reads through typed memory with no per-element indirection. The rest of the design is what it has to be to support that.

This document is the map of where the pieces are and how they fit. The deeper writeups for each piece live alongside it in this directory. For the full design reference in one place (requirements, invariants, the contract each subsystem holds, alternatives considered, and per-operation complexity), see [DESIGN.md](DESIGN.md).

## The layered picture

```
Coordination:    Events, Tags, Commands, Resources, Schedule
                            |
Iteration:       Query, ForEach, Iter*, ParallelIter*, IterChanged*
                            |
Topology:        Spawn, Despawn, AddComponents, RemoveComponents
                            |
Storage:         Archetype tables, columns, entity locations
                            |
Identity:        Entity (generational handle), allocator
```

The layers are stacked because the design depends on it. Identity hands out the handles the storage layer routes by. Storage organizes the rows the handles point at. Topology moves rows between archetypes when components are added or removed; the move only works because the storage layer keeps each component type in its own contiguous column. Iteration walks those rows in archetype order; the walk is only fast because storage is column-aligned. Coordination is the bookkeeping that lets systems talk to each other across the frame without coupling, and it sits on top because none of it has to know how the rows are laid out.

## Identity

[`Entity`](../entity.go) is a 64-bit value carrying an ID (`uint32`) and a generation (`uint32`). The ID locates the entity's slot in the storage layer. The generation guards against stale handles: when an entity is despawned, the allocator pre-bumps the generation it will hand out the next time that ID gets recycled. A stale handle held by some old code fails closed because the generation no longer matches.

[`entityLocations`](../entity.go) is a `[]entityLocation` indexed by entity ID. Each slot stores `(generation, tableIndex, arrayIndex, allocated)`. The `allocated=false` zero value covers two cases: "this slot has never been assigned" and "the entity that lived here was despawned." `entityLocations.get` rejects both. The zero-value default is load-bearing for stale-handle safety, since a fresh slot grown into by `ensureSlot` looks identical to a despawned one and the gatekeeper does not need to distinguish them.

## Storage

The library never stores components by entity. It stores them by archetype, the set of component types an entity carries. Every entity with exactly `Position + Velocity + Health` lives in one [`Archetype`](../archetype.go) struct that holds a `[]Position`, a `[]Velocity`, a `[]Health`, and a parallel `[]Entity` indexed in lockstep. Adding a fourth component migrates the entity to a different archetype. The archetype mask is a `uint64`, so a project gets up to 64 component types per `World` before having to split across worlds with a `MultiWorld`.

The columns inside an archetype are typed Go slices. They live behind a small helper ([`column`](../column.go)) that materializes them via `reflect.MakeSlice` so the Go runtime tracks any embedded pointers correctly for the garbage collector. The hot path then reads the column through a cached `unsafe.Pointer` and `unsafe.Slice`, which is plain `[]T` access with no reflection per element. The reflect cost is paid once per archetype creation; the unsafe access happens every iteration.

See [STORAGE.md](STORAGE.md) for the deeper writeup.

## Topology

Adding or removing a component from a live entity means physically moving its row to a different archetype. [`moveEntity`](../spawn.go) handles the migration: allocate a row in the destination archetype, copy each component column that exists in both source and destination, push zero values for newly-added components, then swap-remove the source row. The swap-remove keeps the source archetype contiguous so iteration over it stays a tight loop.

Two caches keep this fast on the common path.

The archetype graph stores, on each table, the destination archetype reached by adding or removing each single-component bit. The first time an entity is moved from `POSITION` to `POSITION | VELOCITY`, the graph records the edge. Every subsequent single-bit add or remove from `POSITION` skips the mask-to-table hashmap and goes straight to the cached destination index. Single-bit transitions cover the common case (a status effect toggling, a piece of equipment going on or off), so the cache hit rate in real workloads is close to one.

The query cache memoizes, for each include mask the user has queried, the list of archetype indices whose mask satisfies it. The first call to `world.Query(POSITION | VELOCITY, 0)` walks every archetype and stores the matching set. Subsequent calls do an array lookup. When a new archetype is created, the library walks the query cache and appends the new index to entries whose query mask is a subset of the new archetype's mask. No full invalidation, no rebuild from scratch.

See [QUERIES.md](QUERIES.md) for both caches in detail.

## Iteration

There are three iteration shapes, picked by how much typing you want.

`world.Query(include, exclude)` returns an `iter.Seq[Entity]` and is the lightest-weight surface. Use it when you want a `for entity := range world.Query(...)` loop and you'll fetch components with `freecs.Get[T]` inside.

`world.ForEach(include, exclude, callback)` hands the callback `(entity, *Archetype, index)`. The caller pulls typed slices with `freecs.Column[T](world, table)` and walks them with their own loop. This is the lowest-overhead path when you want direct table access without committing to a fixed arity.

`freecs.Iter1[A]` through `freecs.Iter6[A, ..., F]` are typed helpers. Each one derives the include mask from its type parameters, walks the matching archetypes, materializes typed `[]A` (and `[]B`, etc.) views over each archetype's columns via `unsafe.Slice`, and calls the callback with `(entity, *A, *B, ...)`. This is the most ergonomic shape for tight inner loops, and the generated code reads as if you had hand-written the slice loop. The Iter family is generated from a single template in `internal/gen_iter`, so extending it past six components is a one-line edit to the generator.

`freecs.ParallelIter1` through `freecs.ParallelIter4` fan out one goroutine per matching archetype with a `sync.WaitGroup`. Same per-archetype code path as the serial Iter, parallelized at the archetype boundary. See [PARALLEL.md](PARALLEL.md) for the constraints on the callback.

`freecs.IterChanged1` through `freecs.IterChanged6` are the change-detection variants. They visit only entities whose component slot was stamped after the previous frame's watermark. See [CHANGE_DETECTION.md](CHANGE_DETECTION.md).

## Coordination

The coordination layer is what systems use to talk to each other across the frame without naming each other.

Events are double-buffered queues stored on the World, keyed by Go type. An event sent on frame N is readable through the end of frame N+1 and dropped at the start of N+2. The two-frame window guarantees that a system reading later in the schedule sees an event published earlier, and a system in the next frame still catches one published the frame before, regardless of schedule order between them.

Tags are sparse-set markers keyed by a user-defined Go type. `freecs.AddTag[Player](world, entity)` puts the entity in a hash set keyed by the `Player` type. Flipping a tag is O(1) and does not migrate the entity between archetypes, which is the whole reason tags exist as a separate primitive. Use tags for state that flips frequently (selected, alerted, frame-local flags). Use archetype components for stable categorizations.

Commands are a slice of closures on the World. The point is iteration safety. `freecs.Iter1[Health]` can call `world.QueueDespawn(entity)` inside the callback without invalidating the iteration; the despawn lands in the buffer and runs when `world.ApplyCommands()` drains it.

Resources are world-scoped values keyed by Go type. `freecs.SetResource(world, DeltaTime(0.016))` stores it; `freecs.Resource[DeltaTime](world)` returns `(*DeltaTime, bool)` and `freecs.MustResource[DeltaTime](world)` panics if the resource is missing. Re-setting writes through the existing pointer, so cached `*DeltaTime` references stay valid across re-sets.

Schedule is an ordered list of named systems. It is intentionally small: no read/write sets, no parallel dispatch, no conditional running. The frame loop is composed by the user.

See [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md) for the details on each.

## Multi-world

A single `World` holds at most 64 component types because the archetype mask is a `uint64`. Projects past that ceiling group several worlds under a [`MultiWorld`](../multiworld.go) that owns the shared entity allocator. Each child world keeps its own full bitmask space; per-world component access does not change. Entity lifetime moves up to the `MultiWorld` so `multi.Despawn(entity)` cascades through every child and deallocates the ID exactly once.

Tags, events, resources, and commands stay per-world. The user pins them on a primary child world when they want a single coordination point. See [MULTI_WORLD.md](MULTI_WORLD.md).

## What is not here

A few absences are on purpose.

There is no reactive scheduler. `Schedule.Run` walks systems in declared order, end of story. Read/write sets, automatic parallelism, and conditional running are all things a real frame loop builds on top, not things the schedule should be inferring.

There is no archetype garbage collection. Empty archetypes stick around in `world.tables` after their last entity is despawned. In practice game projects reuse archetype shapes constantly, so the dead-archetype set stays small.

There is no global component registry. Each `World` owns its own, so a project can spin up isolated worlds for tests or sub-simulations without colliding on bit assignments.

There is no serialization. Components are plain Go structs; serialize them with `encoding/json`, `gob`, or whatever fits the project. Building serialization on top of the storage is straightforward (walk `world.tables`, ask each for its mask and columns) and would not benefit from being built into the kernel.

## Where to read next

- [DESIGN.md](DESIGN.md), the full software design document: goals, requirements, invariants, alternatives, complexity
- [STORAGE.md](STORAGE.md), how the column memory layout actually works
- [QUERIES.md](QUERIES.md), the two caches and what gets invalidated when
- [CHANGE_DETECTION.md](CHANGE_DETECTION.md), the tick watermark
- [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md), the four coordination primitives
- [MULTI_WORLD.md](MULTI_WORLD.md), the shared-allocator pattern
- [PARALLEL.md](PARALLEL.md), the goroutine-per-archetype model
- [PERFORMANCE.md](PERFORMANCE.md), where the cycles go
