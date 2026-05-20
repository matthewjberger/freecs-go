# Architecture overview

freecs-go is an archetype-based Entity Component System. Every design choice descends from one performance goal. Systems that iterate over thousands of entities each frame should walk contiguous typed memory, with no per-element indirection, no hash lookups, and no virtual dispatch. Everything else (lifecycle management, structural change, change detection, events) is built so it doesn't get in the way of that hot loop.

This document is the map. Deeper writeups for each piece live alongside it in this directory.

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

Each layer depends only on the ones below it. Identity gives you handles. Storage organizes the rows those handles point at. Topology lets you migrate rows between archetypes when a component is added or removed. Iteration walks those rows in archetype-friendly order. Coordination is the bookkeeping layer that lets systems talk to each other across the frame without coupling.

## Identity

[`Entity`](../entity.go) is a 64-bit value with an ID (`uint32`) and a generation (`uint32`). The ID locates the entity's slot in the storage layer. The generation guards against stale handles: when an entity is despawned, the allocator pre-bumps the generation it will hand out next time that ID gets recycled. A stale handle held by some old code fails closed because the generation no longer matches.

[`entityLocations`](../entity.go) is a `[]entityLocation` indexed by entity ID. Each slot stores `(generation, tableIndex, arrayIndex, allocated)`. `allocated=false` from the zero value means "this slot has never been assigned" or "the entity that lived here was despawned." Either way, `entityLocations.get` returns false.

## Storage

The library never stores components by entity. It stores them by archetype, the set of component types an entity carries. Every entity with exactly `Position + Velocity + Health` lives in one [`Archetype`](../archetype.go) struct that holds a `[]Position`, a `[]Velocity`, a `[]Health`, plus a `[]Entity` parallel to all of them. Add a fourth component and the entity migrates to a different archetype. The archetype mask is a `uint64`, and a project gets up to 64 component types per `World`.

The columns inside an archetype are typed Go slices. They live behind a small helper ([`column`](../column.go)) that materializes them via `reflect.MakeSlice` so the Go runtime tracks any embedded pointers correctly for the garbage collector. The hot path then reads the column through a cached `unsafe.Pointer` and `unsafe.Slice`, which is plain `[]T` access with no reflection per element.

See [STORAGE.md](STORAGE.md) for the deeper writeup.

## Topology

Adding or removing a component from a live entity means physically moving its row to a different archetype. The migration is implemented in [`moveEntity`](../spawn.go): allocate a row in the destination archetype, migrate each component column that exists in both source and destination, push zero values for newly-added components, then swap-remove the source row.

Two caches keep this fast.

The **archetype graph** stores, on each table, the destination archetype reached by adding or removing each single-component bit. The first time an entity is moved from `POSITION` to `POSITION | VELOCITY`, the graph records the edge. Every subsequent single-bit add or remove from `POSITION` skips the mask-to-table hashmap and goes straight to the cached destination index.

The **query cache** memoizes, for each include mask the user has queried, the list of archetype indices whose mask satisfies it. The first call to `world.Query(POSITION | VELOCITY, 0)` walks every archetype and stores the matching set; subsequent calls do an array lookup. When a new archetype is created, the library walks the query cache and appends the new index to entries whose query mask is a subset of the new archetype's mask. No full invalidation.

See [QUERIES.md](QUERIES.md) for both caches in detail.

## Iteration

There are three iteration shapes, picked by how much typing you want.

`world.Query(include, exclude)` returns an `iter.Seq[Entity]` and is the most lightweight surface. Use it when you want a `for entity := range world.Query(...)` loop and you'll fetch components with `freecs.Get[T]` inside.

`world.ForEach(include, exclude, callback)` hands the callback `(entity, *Archetype, index)`. The user pulls typed slices with `freecs.Column[T](world, table)` and walks them with their own loop. This is the lowest-overhead path when you want direct table access without committing to a fixed arity.

`freecs.Iter1[A]` through `freecs.Iter4[A, B, C, D]` are typed helpers. Each one derives the include mask from its type parameters, walks the matching archetypes, materializes typed `[]A` (and `[]B`, etc.) views over each archetype's columns via `unsafe.Slice`, and calls the callback with `(entity, *A, *B, ...)`. This is the most ergonomic shape for tight inner loops; the generated code reads as if you'd hand-written the slice loop.

`freecs.ParallelIter1` through `freecs.ParallelIter4` fan out one goroutine per matching archetype with a `sync.WaitGroup`. Same per-archetype code path as the serial Iter, parallelized at the archetype boundary. See [PARALLEL.md](PARALLEL.md) for the constraints.

`freecs.IterChanged1` through `freecs.IterChanged4` are the change-detection variants. They visit only entities whose component slot was stamped after the previous frame's watermark. See [CHANGE_DETECTION.md](CHANGE_DETECTION.md).

## Coordination

The coordination layer is what systems use to talk to each other across the frame without coupling.

**Events** are double-buffered queues stored on the World, keyed by Go type. An event sent on frame N is readable through the end of frame N+1 and dropped at the start of N+2. Two-frame visibility means a system later in the schedule sees an event published earlier, and a system in the next frame still sees it before it expires.

**Tags** are sparse-set markers keyed by a user-defined Go type. `freecs.AddTag[Player](world, entity)` puts the entity in a hash set; flipping a tag is O(1) and does not migrate the entity between archetypes. Use tags for state that flips frequently (selected, alerted, frame-local flags). Use archetype components for stable categorizations.

**Commands** are a slice of closures on the World. The point is iteration safety. `freecs.Iter1[Health]` can call `world.QueueDespawn(entity)` inside the callback without invalidating the iteration; the despawn lands in the buffer and runs when `world.ApplyCommands()` does the drain.

**Resources** are world-scoped values keyed by Go type. `freecs.SetResource(world, DeltaTime(0.016))` stores it; `freecs.Resource[DeltaTime](world)` returns the `*DeltaTime` pointer. Re-setting writes through the existing pointer so cached `*DeltaTime` references stay valid.

**Schedule** is an ordered list of named systems. It is intentionally small: no read/write sets, no parallel dispatch, no conditional running. The user composes the frame loop themselves.

See [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md) for the details on each.

## Multi-world

A single `World` holds at most 64 component types because the archetype mask is a `uint64`. Projects past that ceiling group several worlds under a [`MultiWorld`](../multiworld.go) that owns the shared entity allocator. Each child world keeps its own full bitmask space and per-world component access is unchanged. Entity lifetime moves up to the `MultiWorld` so `multi.Despawn(entity)` cascades through every child.

Tags, events, resources, and commands stay per-world. The user pins them on a primary child world if they want them in one place. See [MULTI_WORLD.md](MULTI_WORLD.md).

## What's not here

A few things on purpose.

- No reactive system scheduler. `Schedule.Run` walks systems in declared order, end of story.
- No archetype garbage collection. Empty archetypes stick around in `world.tables` after their last entity is despawned. In practice game projects reuse archetype shapes constantly so this matters less than it sounds.
- No global registry. Each `World` owns its own component-type registry, so a project can spin up isolated worlds for tests or sub-simulations.
- No serialization. Components are plain Go structs; serialize them yourself with `encoding/json`, `gob`, or whatever fits.

## Where to read next

- [STORAGE.md](STORAGE.md), how the column memory layout actually works
- [QUERIES.md](QUERIES.md), the two caches and what gets invalidated when
- [CHANGE_DETECTION.md](CHANGE_DETECTION.md), the tick watermark
- [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md), the three coordination primitives
- [MULTI_WORLD.md](MULTI_WORLD.md), the shared-allocator pattern
- [PARALLEL.md](PARALLEL.md), the goroutine-per-archetype model
- [PERFORMANCE.md](PERFORMANCE.md), where the cycles go
