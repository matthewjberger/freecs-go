# Multi-world

A nontrivial game engine runs out of component bits. Nightshade, the engine that freecs-go's Rust sibling shipped under, has around 60-70 component types across rendering, physics, audio, animation, UI, and gameplay. The 64-bit archetype mask in a single `World` does not have room for that, and growing the mask is not free: a wider mask costs more in cache footprint on the hot iteration path. The fix is to split components across several worlds that share one entity allocator, so a given entity can have rows in more than one world without colliding on IDs. `MultiWorld` is the structure that owns the split.

## The shape

```go
type MultiWorld struct {
    _         noCopy
    allocator allocator
    worlds    []*World
    live      map[Entity]struct{}
}
```

Four fields. The allocator (held by value, so the multi-world owns the only copy), the ordered list of child worlds, and a `live` set of entities the multi-world has issued and not yet despawned. The `noCopy` sentinel is what `go vet`'s copylocks analyzer keys off; it makes accidental copies of a `MultiWorld` value a vet error rather than a runtime corruption bug, and is covered separately below.

Children get a pointer into the allocator via `newWorldWithAllocator(&m.allocator)`. Every world hands out IDs from the same pool, so a spawn against any child world (or against `multi.Spawn` directly) draws from the same counter and the same free list. Two worlds will never produce the same ID.

## Allocation and placement are separate calls

In a single-world setup, `world.Spawn(mask)` allocates an ID and places the entity in the archetype for `mask` in one call. In multi-world that has to come apart, because the user wants the entity in some component combination in world A and possibly a different combination in world B. The single call cannot decide for itself which world to write to.

The split:

```go
entity := multi.Spawn()                              // mint an ID, mark live, do not place
core.SpawnEntityInto(entity, POSITION | VELOCITY)    // place in core world's archetype
render.SpawnEntityInto(entity, SPRITE)               // place in render world's archetype
```

`multi.Spawn` runs `m.allocator.allocate()` and inserts the entity into `m.live`. It does not touch any child world. The entity exists as a generational handle and nothing else. `world.SpawnEntityInto(entity, mask)` then places the externally-allocated entity into that specific world's archetype. The second call panics if the entity already has a row in this world. That panic catches the silent-corruption case where a duplicate `SpawnEntityInto` would leave a ghost row in the old archetype while the routing layer points only at the new one.

If you want the entity to carry components in only one world, skip the other `SpawnEntityInto` calls. The entity remains live until `multi.Despawn` removes it.

## Despawn has to cascade

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

Four steps. Reject the call if the entity is not in `m.live` (either never-spawned or already despawned). Drop it from `m.live`. Visit every child and call `despawnFromArchetype`, which either swap-removes the entity's row or no-ops silently when the world doesn't carry it. Deallocate the ID exactly once at the end, bumping the generation for future recycling.

The "exactly once" is the reason `world.Despawn` is the wrong call in multi-world mode. `world.Despawn` is `despawnFromArchetype` followed by `world.allocator.deallocate(entity)`. Called against every child, it would deallocate the entity N times for N worlds. The current allocator does not crash on a double-deallocate (the free list just accumulates duplicate entries), but the next `Spawn` would hand out the same `(id, generation+1)` pair to two callers, which is the silent-corruption shape this design is trying to avoid. `multi.Despawn` is the only correct way to despawn anything that lives in a multi-world.

`world.Despawn` is still the right call in single-world projects. The package documentation directs multi-world users explicitly to `multi.Despawn`.

## Why the live set exists

Consider this sequence:

```go
entity := multi.Spawn()    // allocate, mark live
// caller forgets to call SpawnEntityInto
multi.Despawn(entity)
```

Without the live set, `multi.Despawn` would have nothing to key off other than "did any child world hold a row for this entity." Every child returns false (none of them ever saw the entity), the cascade loop walks away with nothing removed, and the `allocator.deallocate` call gets skipped. The ID leaks. Next call to `multi.Spawn` mints a fresh one. Repeat the bug a few thousand times during a level load and the allocator runs through its `uint32` counter for no reason.

With the live set, `multi.Despawn` keys off `m.live` directly. An entity that was issued by `Spawn` and never placed anywhere is still in `m.live`, so the deallocation runs and the ID returns to the pool. The set is also what gates the early-return on stale-handle despawn, since an entity that has already been despawned is no longer in `m.live`.

## Tags, events, resources, and commands stay per-world

The coordination state (tag sets, event queues, resources, command buffer) lives on the child `*World` instances, not on the `MultiWorld`. The motivating use case for multi-world is splitting components across worlds that handle distinct concerns. Gameplay versus rendering. Simulation versus UI. Server-authoritative state versus client-side prediction. Each world has its own coordination needs. A render world does not care about gameplay collision events, and a server world does not care about UI commands. Putting tags or events on the multi-world would couple them in ways the design wants to keep apart.

If you do want a single place for global state, pin it on a primary child world:

```go
multi := freecs.NewMultiWorld()
primary := multi.NewWorld()           // owns coordination state
visuals := multi.NewWorld()           // owns only visual components

freecs.SetResource(primary, DeltaTime(0.016))
freecs.AddTag[Player](primary, playerEntity)
freecs.Send(primary, CollisionEvent{...})
```

Reading from one specific world is easy enough that the library does not need to wrap it. If a future use case wants truly-shared coordination across worlds, the right next step is layered helpers (`freecs.MultiWorldResource[T]` and friends) sitting on top of the current shape rather than a rewrite of the multi-world container.

## Read accessors are soft-miss across worlds

Multi-world makes it possible to ask a world for a component that lives somewhere else. Say `Position` is registered on `core` and `Sprite` is registered on `render`. What happens when a system running against `core` calls `freecs.Get[Sprite](core, entity)`?

The read-side accessors (`Get`, `GetMut`, `Has`, `Changed`) return `false` rather than panic when `T` is not registered on the world being asked:

```go
func Get[T any](world *World, entity Entity) (*T, bool) {
    info, ok := componentInfoFor[T](world)
    if !ok {
        return nil, false
    }
    // ... normal lookup ...
}
```

`componentInfoFor[T]` returns `(nil, false)` when the type is not in the world's registry. The accessor turns the registry miss into a soft accessor miss. `freecs.Get[Sprite](core, entity)` answers `(nil, false)` because `Sprite` was never registered on `core`. Sibling-world queries are safe by default.

The write-side accessors (`Set`, `Add`, `Remove`, `MarkChanged`, `Iter*`, `IterChanged*`, `ParallelIter*`) go through `mustComponentInfo[T]` and panic on a registry miss. Writing to a world that does not own the column is always a programming error, and an iterator with no matching archetypes would silently do nothing, which is harder to diagnose than a clean panic. The asymmetry is intentional. Reads tolerate missing types because asking is cheap and the answer might be useful; writes do not, because the silent-no-op alternative would corrupt the program's notion of where data lives.

## The noCopy sentinel

Both `World` and `MultiWorld` embed a zero-sized struct that implements `sync.Locker`:

```go
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
```

`go vet`'s copylocks analyzer flags any struct embedding something that implements `sync.Locker` as non-copyable. The methods are never called at runtime, and the embed adds no fields. The whole machinery exists to surface accidental copies at vet time.

The sentinel matters more on `MultiWorld` than on `World`. A copy of `World` would at least have a separate allocator pointer that still aliases the same allocator, which is defensible. A copy of `MultiWorld` cannot work safely: the copy duplicates the children slice and the live map but keeps the embedded allocator value by value, so the copy aliases the original's allocator. Two `MultiWorld` values would hand out the same entity IDs while tracking different live sets, and the bugs that follow would be silent.

`NewMultiWorld()` is the only intended way to construct one. Pass `*MultiWorld` around, not `MultiWorld`. `go vet` will tell you if you slip.

## When multi-world is worth the bookkeeping

The 64-component ceiling is the hard reason. A game engine that touches rendering, physics, audio, animation, UI, and gameplay will hit it. There is no point in being clever about the bit budget; if the project's component count is climbing past forty, plan for multi-world before the migration is forced.

The soft reasons are about scope rather than capacity. A simulation world that should not be touched by rendering code is easier to keep clean if rendering components live in their own world; the compiler stops you from accidentally registering `Sprite` on the gameplay world. Tests that want to exercise one subsystem in isolation can spin up a tiny world with just the relevant components. Hot reload boundaries get cleaner: nuking and rebuilding the visual representation without disturbing the simulation is surgical when the two live in separate worlds.

If the project is under the ceiling and there is no architectural reason to split, single-world is the right answer. Multi-world adds bookkeeping (the live set, cascade despawn, the placement step) that single-world does not pay for, and the bookkeeping is fixed cost rather than amortized.

## Where to look in the code

- [`multiworld.go`](../multiworld.go), the `MultiWorld` struct and every method on it
- [`nocopy.go`](../nocopy.go), the sentinel that gates copylocks
- [`spawn.go`](../spawn.go), `SpawnEntityInto` and `despawnFromArchetype`
- [`world.go`](../world.go), `newWorldWithAllocator` (used by `MultiWorld.NewWorld`)
- [`mutate.go`](../mutate.go), the soft-miss read accessors
- [`registry.go`](../registry.go), `componentInfoFor` and `mustComponentInfo`, the read/write split
