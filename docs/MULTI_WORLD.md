# Multi-world

A single `World` carries an archetype mask of `uint64`, so it can host at most 64 component types. Projects that exceed that ceiling split components across multiple worlds sharing one entity allocator. A `MultiWorld` is the container for that split.

This document covers how the design works, why each piece is shaped the way it is, and how to use it in practice.

## The shape

```go
type MultiWorld struct {
    _         noCopy
    allocator allocator
    worlds    []*World
    live      map[Entity]struct{}
}
```

The allocator is a value field. Children get a pointer to it via `newWorldWithAllocator(&m.allocator)` so every world hands out IDs from the same pool. Spawning in any world (or directly via `multi.Spawn`) draws from this shared allocator.

`worlds` is the ordered list of child worlds. Order is fixed at the time of `multi.NewWorld()`; despawn cascades iterate in this order.

`live` is the set of entities that the multi-world has issued and not yet despawned. It guards against ID leaks for entities that were spawned but never placed in any child world.

The `noCopy` sentinel is what makes `go vet`'s copylocks analyzer flag accidental copies of a `MultiWorld` value. If you copy one, you'd silently alias all of its children to the original's allocator while the copy holds a different `live` set, which would be hard to debug. See the [noCopy section](#nocopy-on-the-multiworld) below.

## Why decoupled spawn and placement

In a single-world setup, `world.Spawn(mask)` does two things in one call: allocate an ID and place the entity in the archetype for `mask`. In multi-world, those two steps decouple because the user wants to place the entity in some component combination in world A and possibly a different combination in world B.

The split:

```go
entity := multi.Spawn()                              // allocate the ID, mark live, no placement
core.SpawnEntityInto(entity, POSITION | VELOCITY)    // place in core world's archetype
render.SpawnEntityInto(entity, SPRITE)               // place in render world's archetype
```

`multi.Spawn` calls `m.allocator.allocate()` and inserts the entity into `m.live`. It does not touch any child world; the entity exists only as a generational handle.

`world.SpawnEntityInto(entity, mask)` places an externally-allocated entity into that specific world's archetype for `mask`. It panics if the entity already has a row in this world, to catch the silent-corruption case where a duplicate `SpawnEntityInto` would create a ghost row in the old archetype.

If you want the entity to carry components in only one world, you can skip the other `SpawnEntityInto` calls. The entity remains live (in `m.live`) until `multi.Despawn` removes it.

## Cascade despawn

```go
func (m *MultiWorld) Despawn(entity Entity) bool {
    if _, ok := m.live[entity]; !ok {
        return false
    }
    delete(m.live, entity)
    for _, world := range m.worlds {
        despawnFromArchetype(world, entity)
    }
    m.allocator.deallocate(entity)
    return true
}
```

The flow:

1. Look up `entity` in `m.live`. If absent, the entity is either never-spawned or already despawned; return false.
2. Remove from `m.live`.
3. Call `despawnFromArchetype` on every child world. Each child either holds a row for `entity` (in which case it swap-removes the row, clears tag sets, etc.) or it doesn't (in which case the call is a silent no-op).
4. Deallocate the ID exactly once on the shared allocator, bumping the generation for future recycling.

The "deallocate once" is the key thing that makes per-world `world.Despawn` wrong in multi-world mode. `world.Despawn` calls `despawnFromArchetype` followed by `world.allocator.deallocate(entity)`. If you called it on every child world manually, you'd deallocate the entity N times for N worlds, which is harmless in the current allocator (the free list would accumulate duplicate entries, leading to multiple distinct entities with the same ID and bumped generations) but is a bug waiting to bite. `multi.Despawn` is the only correct way to despawn in multi-world mode.

`world.Despawn` is still useful in single-world mode where there's no MultiWorld, but the package documentation explicitly directs multi-world users to `multi.Despawn`.

## The live-set guard

Why does `MultiWorld` track a live set?

Consider this sequence:

```go
entity := multi.Spawn()    // allocate, mark live
// the user forgets to place the entity in any world
multi.Despawn(entity)
```

Without the live set, `multi.Despawn` would call `despawnFromArchetype` on every child world, each one would return false (the entity isn't in any archetype), the cascade loop would `removed` remain false, and the function would skip the `allocator.deallocate` call. The ID would be leaked.

With the live set, `multi.Despawn` keys off `m.live` instead of "did any child world have this row." An entity that was issued by `Spawn` and never placed is still in `m.live`, so the deallocation runs.

This is a real footgun in the design that I caught during the post-implementation review. The fix is minimal but the behavior matters.

## Per-world tags, events, resources, commands

`MultiWorld` does not own coordination state. Tags, events, resources, and the command buffer all live on the child `*World` instances.

This is intentional. The motivating use case for multi-world is splitting components across worlds that handle different concerns: gameplay vs rendering, simulation vs UI, server-authoritative vs client-side. Each world has its own coordination needs. A render-world doesn't usually care about gameplay collision events; a server world doesn't care about UI commands. Putting tags/events/resources on the multi-world would couple them in ways the design doesn't want.

If you want global coordination, pin it on a designated "primary" child world:

```go
multi := freecs.NewMultiWorld()
primary := multi.NewWorld()           // owns coordination state
visuals := multi.NewWorld()           // owns only visual components

freecs.SetResource(primary, DeltaTime(0.016))
freecs.AddTag[Player](primary, playerEntity)
freecs.Send(primary, CollisionEvent{...})
```

Reading from one specific world is easy enough that the library doesn't need to wrap it. If a future use case needs truly-shared coordination, the right next step is layered helpers (`freecs.MultiWorldResource[T]` etc.), not changing the base structure.

## What read/write asymmetry looks like across worlds

Multi-world makes it possible to query a world for a component that lives in a different world. Imagine `Position` is registered on the `core` world and `Sprite` on the `render` world. What happens when you call `freecs.Get[Sprite](core, entity)`?

The read-side accessors (`Get`, `GetMut`, `Has`, `Changed`) return `false` rather than panic when `T` is not registered on the world you ask:

```go
func Get[T any](world *World, entity Entity) (*T, bool) {
    info, ok := componentInfoFor[T](world)
    if !ok {
        return nil, false
    }
    // ... normal lookup ...
}
```

`componentInfoFor[T]` returns `(nil, false)` when the type is not in the world's registry. The accessor turns that into a soft miss. So `freecs.Get[Sprite](core, entity)` returns `(nil, false)` because `Sprite` was never registered on `core`. This makes sibling-world queries safe.

The write-side accessors (`Set`, `Add`, `Remove`, `MarkChanged`, `Iter*`, `IterChanged*`, `ParallelIter*`) call `mustComponentInfo[T]` instead, which panics. Writing to a world that doesn't own the column is always a programming error. Iterating a world for a component it doesn't carry is also always a programming error: the iterator would have no matching archetypes and silently do nothing, which is more confusing than a clean panic.

## `noCopy` on the MultiWorld

Both `World` and `MultiWorld` embed a `noCopy` sentinel:

```go
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
```

`go vet`'s copylocks analyzer flags any struct that embeds something implementing `sync.Locker` as non-copyable. The methods are never called at runtime; the struct is zero-sized so the embed adds no fields. The whole mechanism exists to surface accidental copies at vet time.

It matters more on `MultiWorld` than on `World`. `World` could be copied (the copy would have a separate allocator pointer to the same allocator, which is at least defensible). `MultiWorld` cannot: copying duplicates the children slice and the live map but keeps the embedded allocator value, so the copy aliases the original's allocator. Two `MultiWorld` values would then hand out the same entity IDs while tracking different live sets. The `noCopy` sentinel catches this at vet time before the silent corruption can happen at runtime.

If you actually want to construct a fresh `MultiWorld`, use `NewMultiWorld()`. If you need to pass one around, pass `*MultiWorld`, not `MultiWorld`. `go vet` will tell you if you make a mistake.

## When to reach for multi-world

The 64-component ceiling is the hard reason. A nontrivial game engine genuinely runs out: Nightshade (which uses freecs in Rust) has around 60-70 component types across rendering, physics, audio, animation, UI, and gameplay, and splits them across a `CoreWorld` and a `RenderWorld` to fit.

The soft reasons:

- **Domain isolation**: A simulation world that shouldn't be touched by rendering code is easier to keep clean if rendering components live somewhere else.
- **Test setup**: Spinning up a tiny world for a specific test is easier when you can ignore unrelated component registrations.
- **Hot reload boundaries**: If you ever want to nuke and rebuild the visual representation without affecting the simulation, separate worlds make that surgical.

If you're under the ceiling and have no architectural reason to split, stick with single-world. Multi-world adds bookkeeping (the live set, cascade despawn, the placement step) that single-world doesn't pay for.

## Where to look in the code

- [`multiworld.go`](../multiworld.go), the `MultiWorld` struct and all its methods
- [`nocopy.go`](../nocopy.go), the no-copy sentinel
- [`spawn.go`](../spawn.go), `SpawnEntityInto` and `despawnFromArchetype`
- [`world.go`](../world.go), `newWorldWithAllocator` (used by `MultiWorld.NewWorld`)
- [`mutate.go`](../mutate.go), the soft-miss read accessors
- [`registry.go`](../registry.go), `componentInfoFor` and `mustComponentInfo` (the read vs write split)
