# freecs-go software design document

Status: living document, tracks `main`.
Audience: contributors and integrators who need the why behind the code, not just the API.
Scope: the `freecs` package and its `internal/gen_iter` generator. The example games under `examples/` are out of scope except as motivating workloads.

This document is the single end-to-end design reference. [ARCHITECTURE.md](ARCHITECTURE.md) is the short orientation map; the topic files ([STORAGE.md](STORAGE.md), [QUERIES.md](QUERIES.md), [CHANGE_DETECTION.md](CHANGE_DETECTION.md), [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md), [MULTI_WORLD.md](MULTI_WORLD.md), [PARALLEL.md](PARALLEL.md), [PERFORMANCE.md](PERFORMANCE.md)) are the deep dives. This document pulls them into one narrative and adds the parts a design doc needs that an overview omits: requirements, invariants, the contracts each subsystem holds, alternatives that were rejected, and the complexity each operation actually pays.

---

## 1. Purpose

freecs-go is an archetype-based Entity Component System (ECS) for Go. It stores game-or-simulation state as entities (opaque handles) that own sets of components (plain Go structs), and it lets systems iterate over every entity matching a component set with the inner loop reduced to sequential reads through typed memory.

It is a port of the Rust [freecs](https://github.com/matthewjberger/freecs) library. The storage strategies are identical; the language mechanism differs. Rust fans out per-component code with a declarative macro; freecs-go uses generics over a small `unsafe`-pointer column core, so components stay ordinary Go types and the typed surface is `freecs.Get[Position](world, e)` rather than a generated `world.get_position(e)`. There is no build step, no reflection on the hot path, and no codegen the user has to run (the generator output is committed).

## 2. Goals and non-goals

### Goals

- **Iteration is the budget.** A frame runs a handful of systems; each walks one or more queries; each query touches every matching entity. The cost of that walk caps the entity count the project can afford. Every other decision is subordinate to keeping the inner loop a contiguous typed-memory scan with no per-element indirection.
- **Components are plain structs.** No base type, no interface to implement, no registration macro. A component is any Go type; you register the type to get a bit.
- **Stale handles fail closed.** Holding a handle to a despawned entity must never read another entity's data. Lookups return `ok=false`, not garbage.
- **GC correctness with pointer-bearing components.** A component may contain pointers, slices, maps, or strings. The garbage collector must see those references through the storage.
- **Small, legible kernel.** The whole library is a dozen files. A contributor should be able to read it in an afternoon.

### Non-goals

- **No reactive/parallel scheduler.** `Schedule.Run` walks systems in declared order. Read/write sets, automatic parallelism, and conditional execution belong to the frame loop the user composes, not to the kernel.
- **No archetype garbage collection.** Empty archetype tables persist. Game projects reuse archetype shapes, so the dead set stays small; reclaiming them would add invalidation complexity for little gain.
- **No global registry.** Each `World` owns its component-to-bit mapping. Isolated worlds (tests, sub-simulations) never collide on bit assignment.
- **No built-in serialization.** Components are Go structs; serialize with `encoding/json`, `gob`, or a custom walk of `world.tables`. Baking it into the kernel would constrain it without earning its keep.
- **No thread-safe `World`.** A `World` is single-writer. The one supported parallelism is `ParallelIter*` within a single call (§7).

## 3. Requirements and the governing constraint

The workload is frame-rate-bound real-time simulation. The hard requirement that shapes everything: **per-entity iteration must approach a hand-written loop over typed slices.** In Go terms, the body of a system iterating `(Position, Velocity)` should compile to roughly what `for i := range positions { positions[i].X += velocities[i].X }` compiles to: no boxing, no interface dispatch, no reflection, no map lookup per element.

Two facts about Go drive the mechanism:

1. Go has no `void*[]`. A column of "some component type fixed at runtime" cannot be a typed slice at the column's definition site. The choices are `[]any` (boxes every element, rejected) or `unsafe`-pointer storage (chosen, §6.3).
2. Go forbids type parameters on methods. So the API splits: non-generic operations are `*World` methods (`world.Spawn`); type-parameterized operations are top-level generic functions (`freecs.Get[T]`). This split is forced, not stylistic (see [doc.go](../doc.go)).

## 4. Design principles

- **Group by archetype, never by entity.** Components live in per-archetype columns, not in per-entity records. This is the source of both the cache-friendly iteration and the migration-on-structural-change cost.
- **Pay reflection once, never per element.** Type metadata is consulted at registration and at archetype creation. The hot path uses cached `unsafe.Pointer`s.
- **Cache the common transition.** Single-bit add/remove and previously-seen queries are O(1) array lookups; only first-time transitions pay a hash lookup or a scan.
- **Read paths fail closed; write paths fail loud.** A read against a missing component or unregistered type returns `false`. A write to an unregistered type panics, because it is a programming error worth surfacing (§8).
- **Defer structural change instead of forbidding mutation.** Iteration cannot tolerate topology changes mid-walk, so the command buffer records them and replays them after the walk.

## 5. Architecture

The system is five layers; each depends only on the one below it.

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

Identity hands out the handles storage routes by. Storage organizes the rows handles point at. Topology moves rows between archetypes when components change; the move only works because storage keeps each component type contiguous. Iteration walks rows in archetype order; the walk is only fast because storage is column-aligned. Coordination is cross-system bookkeeping and sits on top because none of it needs to know the row layout.

## 6. Detailed design

### 6.1 Identity: entities, allocator, locations

[`Entity`](../entity.go) is a value type: `{ID, Generation uint32}`. It owns no data and has no methods. The `ID` selects a slot; the `Generation` distinguishes a live entity from a recycled one at the same slot.

The [`allocator`](../entity.go) hands out handles. New IDs come from a monotonically increasing `nextID`. Freed handles go onto a `free` list with their generation **pre-bumped**, so the next allocation at that ID carries a fresh generation:

```go
func (a *allocator) deallocate(entity Entity) {
    a.free = append(a.free, freedSlot{id: entity.ID, generation: entity.Generation + 1})
}
```

[`entityLocations`](../entity.go) is a `[]entityLocation` indexed by ID, each slot `{generation, tableIndex, arrayIndex, allocated}`. It is the routing table from handle to physical row.

**Invariant (stale-handle safety).** `entityLocations.get` returns `ok=true` only when `location.allocated && location.generation == entity.Generation`. Both the never-allocated slot and the despawned slot have `allocated=false`, so they are rejected identically; the zero value is load-bearing, and `ensureSlot` growing the backing array into fresh zeroed slots is automatically safe. Every read accessor (`Get`, `Has`, `Changed`, query iteration) routes through `get` and therefore inherits this guarantee.

### 6.2 Component registry and masks

A component type is registered to one bit in a `Mask uint64` ([registry.go](../registry.go)). `maxComponents = 64` is the hard ceiling per world, dictated by the mask width. `Register[T]` resolves `T`'s `reflect.Type` via `reflect.TypeOf((*T)(nil)).Elem()` (no allocation; it reads the pointee type off the typed-nil pointer), assigns the next bit, and records a `componentInfo{bitIndex, mask, elemType}` in both a `byType` map and a `byBit` array. The returned `Mask` is `1 << bit`.

A component *set* is a bitwise OR of masks. Set algebra is bit math: "has all of X" is `mask & X == X`; "after adding Y" is `mask | Y`; "after removing Y" is `mask &^ Y`. This is the only reason set operations are single instructions, and the 64-bit width is the reason [multi-world](#613-multi-world) exists as the escape hatch.

`MaskOf[T]`/`MustMaskOf[T]` expose the lookup; `componentInfoFor[T]` (returns `ok`) and `mustComponentInfo[T]` (panics) are the internal read/write variants that implement the fail-closed/fail-loud split of §8.

### 6.3 Storage: archetypes and columns

An [`Archetype`](../archetype.go) is one table holding every entity with *exactly* a given component set:

```go
type Archetype struct {
    Mask     Mask
    Entities []Entity
    columns  [maxComponents]*column   // columns[bit] non-nil iff Mask has bit
}
```

`Entities[i]` and `columns[bit][i]` are indexed in lockstep: the entity at row `i` has its component for `bit` at index `i` of that column. `Mask` and `Entities` are exported so `ForEach` callbacks can read them; they are read-only to callers.

The [`column`](../column.go) is where the central design tension resolves. We need a dense array of a type fixed at runtime, GC-visible, with element access that costs nothing per element.

```go
type column struct {
    slice    reflect.Value   // a genuine []T allocated via reflect.MakeSlice
    elemType reflect.Type
    elemSize uintptr
    dataPtr  unsafe.Pointer  // cached address of element 0
    length   int
    changed  []uint32        // per-row change-detection tick (§6.7)
}
```

**The contract.** The backing memory is allocated as the real `[]T` through `reflect.MakeSlice`, so the GC scans it and tracks any pointers inside `T` correctly. But reflection is never used to *index*. The column caches `dataPtr` (the address of element zero) and accesses element `i` with pointer arithmetic, `unsafe.Add(dataPtr, i*elemSize)`. Because an append can reallocate and move the backing array, every growth (`pushZero`, `migrateFrom`) calls `refresh()` to re-read `dataPtr`, and `swapRemove` does too. The reflect cost is paid once per archetype creation and once per append; the unsafe access is paid every iteration.

**Invariant (pointer validity).** A `*T` obtained from `Get`/`GetMut`/`Column`/`Iter*` aliases the column's backing array and is invalidated by any structural change to that archetype (spawn into it, despawn from it, or a migration touching it), because those can reallocate or swap-remove. Callers must not retain such pointers across structural changes. This is enforced indirectly by the iteration guard (§8) for the bulk paths.

This contract is verified by `TestGCSafeWithPointerComponents` in [stress_test.go](../stress_test.go), which stores components holding `*string`, forces two GC cycles, and asserts the pointers survive, the test that would fail if columns were ever backed by `[]byte`.

See [STORAGE.md](STORAGE.md) for the memory-layout walkthrough.

### 6.4 Topology: spawn, despawn, migration

Because archetype membership is *exact*, changing an entity's component set physically relocates its row to a different table.

**Spawn** ([spawn.go](../spawn.go)) allocates a handle, finds or creates the target archetype via `getOrCreateTable`, appends the handle to `Entities`, pushes a zero value onto each column in the mask (stamped with the current tick), and records the location. `SpawnBatch` does this `count` times sharing one table lookup and runs an initializer with direct table access per row. `SpawnEntityInto` places an externally-allocated handle (the multi-world path) and panics if the entity already has a row in this world.

**Despawn** (`despawnFromArchetype`) marks the slot deallocated, emits an [`EntityDespawned`](../lifecycle.go) event, deletes the entity from every tag set, then **swap-removes** the row: the last row of the table moves into the vacated slot, the slices truncate, and the swapped entity's location record is patched to its new index. Swap-remove keeps the table dense, so removal is O(columns) with no hole to skip later. The cost is unstable iteration order, stated explicitly in the API.

**Migration** (`moveEntity`) handles `AddComponents`/`RemoveComponents`. It appends a row to the destination (`migrateFrom`, a reflect-assignment that is GC-safe, for columns present in both source and destination; `pushZero` for newly added columns), patches the location, then swap-removes the source row exactly as despawn does.

**The archetype graph cache** ([`tableEdges`](../archetype.go)) makes single-bit transitions O(1). Each table caches, per bit, the destination table reached by adding or removing that bit; `-1` means unresolved. A single-bit `AddComponents` reads `tableEdges[tableIndex].add[bit]`; on a hit it is an array index, on a miss it builds the table and the miss path fills the edge for next time. `wireEdgesForNewTable` stitches a new table to its one-bit neighbors (both incoming and outgoing) at creation. Single-bit transitions are the common case (a status effect toggling, equipment going on or off), so the hit rate approaches one. Multi-bit transitions skip the edge cache and go straight to a mask lookup.

`TestRepeatedMigrationKeepsValues` ([stress_test.go](../stress_test.go)) migrates an entity through 50 add/remove rounds and asserts component values survive each move.

### 6.5 Queries and the query cache

A query is an `include` mask (every bit must be present) and an `exclude` mask (no bit may be present). `cachedTables(include)` ([world.go](../world.go)) returns the list of table indices whose mask is a superset of `include`, memoized in `queryCache[include]`.

**Incremental invalidation.** When a new archetype is created, `invalidateQueryCacheForNewTable` does not rebuild the cache. It walks existing cache entries and appends the new index only to those whose query mask the new archetype satisfies (`newMask & queryMask == queryMask`). New archetypes are rare (one per distinct component set, then reused forever), so this stays cheap and the cache never goes fully cold.

`exclude` is applied per table at iteration time (`table.Mask & exclude != 0` skips the table), not baked into the cache key, because the cache is keyed on `include` only.

The query surface: `World.Query` (returns `iter.Seq[Entity]`), `QueryFirst`, `CountQuery`, and `ForEach` (direct table access). See [QUERIES.md](QUERIES.md) for both caches.

### 6.6 Iteration families

Four families, all sharing the same per-archetype skeleton (walk `cachedTables`, skip on `exclude`, materialize typed slices via `unsafe.Slice((*T)(column.dataPtr), count)`, run a flat inner loop):

- **`Iter1` through `Iter6`** ([iter_gen.go](../iter_gen.go)): typed pointers per component, no tick stamping. The ergonomic hot path. The generated loop reads as a hand-written slice loop.
- **`IterChanged1` through `IterChanged6`** ([change_gen.go](../change_gen.go)): same, but visits only rows where at least one listed column was stamped after the previous frame's watermark (OR semantics across columns). §6.7.
- **`ParallelIter1` through `ParallelIter4`** ([parallel_gen.go](../parallel_gen.go)): one goroutine per matching archetype, joined with a `sync.WaitGroup`. §7.
- **`World.ForEach`** ([query.go](../query.go)): untyped direct table access; the caller pulls slices with `Column[T]`.

`Iter*` and `IterChanged*` and `ForEach` bracket themselves with `enterIter`/`leaveIter` (§8). `ParallelIter*` does not, because its callback runs concurrently and the guard counter is not atomic; the structural-mutation prohibition is documented instead.

All three families are generated from one `text/template` per shape in [internal/gen_iter/main.go](../internal/gen_iter/main.go) (§6.14).

### 6.7 Change detection

A watermark scheme, detailed in [CHANGE_DETECTION.md](CHANGE_DETECTION.md). The world holds `currentTick` (bumped each `Step`) and `lastTick` (the previous frame's value). Each column carries a parallel `changed []uint32`. Writes stamp the row with `currentTick`: `Set` and `GetMut` do this automatically; `MarkChanged[T]` does it on demand. `Changed[T]` and `IterChanged*` report `changed[i] > lastTick`, "stamped since the last frame boundary."

Two deliberate decisions:

- **The bulk `Iter*` family does not stamp.** Stamping every visited row would defeat the tight loop. A system that mutates through `Iter*` and wants the change seen calls `MarkChanged[T]` in the body, or uses the single-entity `Set`/`GetMut` which stamp.
- **`currentTick` starts at 1, not 0** ([world.go](../world.go), `newWorldWithAllocator`). Slots pushed before the first `Step` are stamped at 1 or higher while `lastTick` is still 0, so setup-time writes are visible to `IterChanged` on frame 0. Starting at 0 would silently hide them.

### 6.8 Events

Type-erased double buffers, one per event type, keyed by `reflect.Type` ([event.go](../event.go)). `Send` appends to the `current` buffer. `Step` calls `update()` on every queue through the `eventDriver` interface (the type erasure that lets the world step queues without knowing payload types): it clears `previous` and swaps `current` and `previous`.

**Two-frame visibility window.** An event sent on frame N is readable through frame N+1 and dropped at the start of N+2. This guarantees a consumer that runs later in the schedule sees an event a producer published earlier this frame, *and* a consumer in the next frame still catches it, independent of system order. `ReadEvents` peeks (previous then current), `DrainEvents` consumes both buffers, and `PeekEvent`/`LenEvents`/`ClearEvents` round out the surface. The built-in `EntityDespawned` ([lifecycle.go](../lifecycle.go)) uses this same queue, so death-reactive systems work as long as they run at least once every two frames.

### 6.9 Tags

Sparse-set markers ([tag.go](../tag.go)): one `map[Entity]struct{}` per tag type, keyed by a user-defined marker type. `AddTag[Player]` inserts into the `Player` set; flipping a tag is an O(1) hash op and **does not migrate the entity between archetypes**. That is the entire reason tags are a separate primitive from components: a frequently-toggled flag (selected, alerted, frame-local state) as a component would trigger a table move on every toggle. Tags are cleared from every set on despawn (§6.4), which is why `HasTag` on a stale handle returns false.

The trade-off rule: tags for state that flips often; archetype components for stable categorization. See [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md).

### 6.10 Commands

A `[]func(*World)` of deferred closures ([command.go](../command.go)). The motivating problem is iteration safety: a system walking `Iter1[Health]` needs to despawn dying entities, but a despawn mid-walk would swap-remove rows out from under the loop (and panic via the guard, §8). Instead it calls `world.QueueDespawn(entity)`, which records a closure; the loop finishes; `world.ApplyCommands()` replays the closures in FIFO order once `iterDepth` is back to zero.

`ApplyCommands` swaps the buffer out before draining, so a command that itself enqueues a command lands it in the next buffer rather than mutating the slice being iterated. Typed helpers (`QueueSet[T]`, `QueueAdd[T]`, `QueueAddTag[T]`, and the rest) capture their payloads in the closure; `Queue` is the raw escape hatch.

### 6.11 Resources

World-scoped singletons keyed by Go type, stored as `map[reflect.Type]any` holding a `*T` ([resource.go](../resource.go)). `SetResource` writes *through* the existing pointer when the type is already present, so a `*DeltaTime` cached by a system stays valid across re-sets. `Resource[T]` returns `(*T, bool)`; `MustResource[T]` panics on absence. Because the key is the Go type, distinct named types (`type DeltaTime float32`, `type GameTime float32`) are required to keep two scalar resources from colliding.

### 6.12 Schedule

An ordered list of named systems ([schedule.go](../schedule.go)): two parallel slices (`names`, `systems`). `Push`/`InsertBefore`/`InsertAfter`/`Replace`/`Remove` manage order; `Run` calls each `SystemFn` in order. It is intentionally minimal: no read/write sets, no parallel dispatch, no conditional running (§2, non-goals). The frame loop is composed by the user, conventionally:

```go
schedule.Run(world)      // systems iterate, queue structural changes
world.ApplyCommands()    // flush deferred topology changes
world.Step()             // age event queues, advance the change tick
```

### 6.13 Multi-world

When 64 components per world is not enough ([multiworld.go](../multiworld.go)). A `MultiWorld` owns one `allocator`; each child `World` created via `NewWorld()` shares it but keeps its own independent bit space (registration starts at bit 0 again). `multi.Spawn()` mints an ID centrally and tracks it in a `live` set without placing it in any world; the caller then `SpawnEntityInto` each child world that should hold columns for that entity. `multi.Despawn` cascades `despawnFromArchetype` across every child (each a no-op where the entity has no row) and deallocates the ID exactly once.

**Read-vs-write asymmetry across worlds.** `Get[T]`/`Has[T]`/`GetMut[T]`/`Changed[T]` return `false` when `T` is not registered on the world asked, so a query for a component living on a sibling world is a soft miss. `Set[T]`/`Add[T]`/`Remove[T]` still panic on an unregistered type, because writing to a world that does not own the column is a programming error. Tags, events, resources, and commands stay per-world; pin them on a primary child if you want one coordination point. See [MULTI_WORLD.md](MULTI_WORLD.md).

### 6.14 Code generation

Go generics cannot vary the *number* of type parameters, so the arity variants are generated from `text/template` in [internal/gen_iter/main.go](../internal/gen_iter/main.go), invoked via `//go:generate` in [doc.go](../doc.go). It emits `iter_gen.go` and `change_gen.go` (arities 1 through 6) and `parallel_gen.go` (arities 1 through 4), runs `go/format` on each, and writes a `DO NOT EDIT` banner. Extending an arity is a one-line change to the `max` argument. This is the Go analogue of the Rust freecs macro fan-out; the generated output is committed so integrators never run the generator.

## 7. Concurrency model

A `World` is **single-writer**: not safe for concurrent use. Wrap it in a mutex if multiple goroutines must touch it.

The one supported intra-world parallelism is the `ParallelIter*` family, which fans out one goroutine per matching archetype within a single call and joins before returning. It is safe because each archetype owns its columns exclusively: no two goroutines touch the same column, so writes through the supplied typed pointers do not race. Constraints (enforced by documentation, not the compiler):

- The callback must not mutate world topology (`Spawn`, `Despawn`, `Add*`, `Remove*`, `Set` on a missing component, tag inserts). Those touch shared `tableEdges`, `queryCache`, or tag maps. Defer them via the command buffer.
- One `ParallelIter*` per world at a time: the query cache is populated single-writer on miss, so concurrent calls against one world race.
- Fan-out is per archetype, not per row. A workload dominated by one large archetype gains nothing; split rows manually with `Column[T]` and goroutines if needed.

See [PARALLEL.md](PARALLEL.md).

## 8. Safety contracts and error handling

**Fail-closed reads, fail-loud writes.** Read accessors return `ok=false` for stale handles, missing components, and types not registered on the world (the last matters for multi-world soft misses). Write accessors (`Set`, `Add`, `Remove`) panic on an unregistered type because it is a programming error. This split is implemented by `componentInfoFor[T]` (returns `ok`) versus `mustComponentInfo[T]` (panics) in [registry.go](../registry.go).

**The iteration guard.** `World` holds an `iterDepth` counter. `Iter*`, `IterChanged*`, and `ForEach` bracket their bodies with `enterIter`/`leaveIter`. Structural operations (`Spawn`, `Despawn`, `AddComponents`, `RemoveComponents`, and `Set`/`Add` when they migrate) call `guardStructuralMutation`, which panics if `iterDepth > 0`. This converts what would be silent memory corruption (a reallocated or swap-removed column under a live `dataPtr`/`count`) into an immediate, located panic. The sanctioned alternative is the command buffer (§6.10). `Query` does **not** enter the guard, because it yields only handles and holds no raw column pointer across the loop, so structural mutation inside a `Query` range is safe.

**No-copy sentinels.** `World` and `MultiWorld` embed a zero-size `noCopy` ([nocopy.go](../nocopy.go)) that implements `sync.Locker`, so `go vet`'s copylocks pass flags accidental value copies. This matters because both share state by pointer (the allocator); a copied value would alias the original's allocator and corrupt ID allocation.

## 9. Performance characteristics

| Operation | Cost |
| --- | --- |
| `Get`/`Set`/`Has` (component present) | O(1): one registry hash + one slice index via cached pointer |
| `Spawn` / row append | O(k) over k set bits; each column append amortized O(1) |
| `Despawn` | O(k) swap-remove + O(t) tag-set deletes (t = tag types) |
| Single-bit `Add`/`Remove` | O(k) row move, O(1) destination lookup on edge-cache hit |
| Multi-bit `Add`/`Remove` | O(k) row move + one mask hash lookup |
| `Query` / `cachedTables` first call | O(T) over all tables T |
| `Query` / `cachedTables` cached | O(matching tables), then O(matching entities) to walk |
| New-archetype query-cache patch | O(Q) over cached query entries Q |
| `Iter*` / `IterChanged*` body | O(matching entities), per element a pointer index (+ one compare for changed) |
| `Send` | amortized O(1); `Step` event aging O(event types) |
| Tag add/remove/has | O(1) hash |

The reflect cost concentrates at registration and archetype creation; steady-state operation is pointer arithmetic and bit math. See [PERFORMANCE.md](PERFORMANCE.md) for where the cycles actually go.

## 10. Alternatives considered

- **`[]any` columns.** Rejected: boxes every element, defeating the iteration requirement (§3). The `reflect.MakeSlice` + cached `unsafe.Pointer` design is the way to get typeless storage with typed-slice access cost.
- **`[]byte` blob columns.** Rejected: hides embedded pointers from the GC, which would free objects an entity still owns. `reflect.MakeSlice` keeps the GC informed; `TestGCSafeWithPointerComponents` guards this.
- **Sparse-set storage (component arrays indexed by entity ID, à la EnTT).** Rejected as the primary store: it makes single-component random access O(1) but multi-component iteration chase parallel sparse sets. Archetype storage makes the multi-component walk contiguous, which is the dominant cost. Tags *are* a sparse set, used precisely where O(1) flip beats contiguity.
- **Component as a bit baked into the archetype for everything including flags.** Rejected for frequently-toggled state: every toggle is a table migration. Tags exist as the cheaper primitive (§6.9).
- **Forbidding mutation during iteration outright.** Rejected: too restrictive for real systems that need to despawn or spawn while walking. The guard-plus-command-buffer pair allows the intent while preserving loop safety (§6.10, §8).
- **A reactive scheduler with read/write sets.** Out of scope (§2). It is a frame-loop concern layered on top, not a kernel responsibility.

## 11. Testing strategy

Tests live beside the package. The notable coverage:

- [stress_test.go](../stress_test.go), `TestGCSafeWithPointerComponents` (the GC contract of §6.3) and `TestRepeatedMigrationKeepsValues` (value survival across repeated migration, §6.4).
- [world_test.go](../world_test.go), core lifecycle, queries, change detection, events, tags, commands, resources.
- [multiworld_test.go](../multiworld_test.go), shared-allocator behavior, cascading despawn, and the read-vs-write asymmetry of §6.13.

The invariants worth a regression test when touched: stale-handle rejection (§6.1), pointer validity across structural change (§6.3), edge-cache correctness after migration (§6.4), incremental query-cache patching (§6.5), and the two-frame event window (§6.8).

## 12. Extension points and future work

- **More iteration arities.** Bump the `max` argument in [internal/gen_iter/main.go](../internal/gen_iter/main.go) and regenerate. The pattern is mechanical.
- **Serialization.** Walk `world.tables`, read each `Mask` and the typed columns via `Column[T]`. Deliberately left to the integrator (§2).
- **Resource storage by integer ID.** `resourceKeyFor[T]` is centralized ([resource.go](../resource.go)) precisely so a future swap to per-world integer IDs and slice-indexed storage has one site to change.
- **Archetype reclamation.** Currently none (§2). If a project churns archetype shapes, a compaction pass over `world.tables` plus query-cache rebuild could be added behind an explicit call rather than automatically.

## 13. References

- [ARCHITECTURE.md](ARCHITECTURE.md), the short orientation map
- [STORAGE.md](STORAGE.md), [QUERIES.md](QUERIES.md), [CHANGE_DETECTION.md](CHANGE_DETECTION.md), [EVENTS_TAGS_COMMANDS.md](EVENTS_TAGS_COMMANDS.md), [MULTI_WORLD.md](MULTI_WORLD.md), [PARALLEL.md](PARALLEL.md), [PERFORMANCE.md](PERFORMANCE.md), the deep dives
- [doc.go](../doc.go), the package-level Go doc and `//go:generate` directive
- The Rust freecs "Build your own ECS" series, parts 1 through 3, linked from the [README](../README.md)
