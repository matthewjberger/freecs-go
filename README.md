# freecs-go

An archetype-based Entity Component System for Go, port of [freecs](https://github.com/matthewjberger/freecs).

> Live demo: [Breakout in the browser](https://matthewjberger.github.io/freecs-go/), built from `examples/breakout`. Requires a [WebGPU-capable browser](https://caniuse.com/webgpu).

The internal strategies are identical to the Rust version. Where Rust uses a declarative macro to fan out per-component code, the Go version uses generics over a small unsafe-pointer column core, so components are normal Go types and the typed API is `freecs.Get[Position](world, entity)` rather than `world.get_position(entity)`. No build step, no codegen, no reflection on the hot path.

## What carries over from freecs

- Archetype tables in struct-of-arrays layout, one table per unique component set
- Generational `Entity` handles, stale handles fail closed
- Archetype graph cache for O(1) single-bit add/remove migrations
- Memoized query cache invalidated incrementally on new-archetype creation
- Watermark-based change detection (`GetMut` stamps, `IterChanged*` reads since the previous frame)
- Double-buffered events, kept readable for two frames
- Sparse-set tags that flip without an archetype migration
- Command buffer for deferred structural changes during iteration
- World-scoped resources keyed by Go type
- Ordered named system schedule

The design choices behind each of these are walked through in the article series the Rust freecs is based on:

- [Build your own ECS, part 1: archetype storage](https://matthewberger.dev/articles/posts/build-your-own-ecs-archetype-storage)
- [Part 2: structural change and queries](https://matthewberger.dev/articles/posts/build-your-own-ecs-structural-change)
- [Part 3: change detection, events, tags, and commands](https://matthewberger.dev/articles/posts/build-your-own-ecs-events-changes-tags-commands)

## Install

```
go get github.com/matthewjberger/freecs-go
```

Requires Go 1.23 (for `iter.Seq`).

## Quick start

```go
package main

import (
    "fmt"

    "github.com/matthewjberger/freecs-go"
)

type Position struct{ X, Y float32 }
type Velocity struct{ X, Y float32 }

type Player struct{}

type DeltaTime float32

func main() {
    world := freecs.New()
    POSITION := freecs.Register[Position](world)
    VELOCITY := freecs.Register[Velocity](world)

    freecs.SetResource(world, DeltaTime(0.016))

    player := world.Spawn(POSITION | VELOCITY)
    freecs.Set(world, player, Position{X: 0, Y: 0})
    freecs.Set(world, player, Velocity{X: 5, Y: 0})
    freecs.AddTag[Player](world, player)

    physics := func(w *freecs.World) {
        delta := float32(*freecs.MustResource[DeltaTime](w))
        freecs.Iter2[Position, Velocity](w, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
            position.X += velocity.X * delta
            position.Y += velocity.Y * delta
        })
    }

    schedule := freecs.NewSchedule()
    schedule.Push("physics", physics)
    for frame := 0; frame < 60; frame++ {
        schedule.Run(world)
        world.ApplyCommands()
        world.Step()
    }

    position, _ := freecs.Get[Position](world, player)
    fmt.Printf("player at (%.2f, %.2f)\n", position.X, position.Y)
}
```

## API surface

The convention is the one Go's standard library uses: non-generic
operations on `*World` are methods (`world.Spawn(...)`,
`world.ApplyCommands()`), and operations parameterized over a component
type are top-level generic functions (`freecs.Get[Position](world, e)`,
`freecs.AddTag[Player](world, e)`). Go forbids type parameters on methods,
so the split is forced.

### Registration and masks

Component types must be registered before they can be spawned or queried. Registration is per-`World`, returns a single-bit `Mask`, and assigns bits in order. Up to 64 component types per world.

```go
POSITION := freecs.Register[Position](world)   // bit 0
VELOCITY := freecs.Register[Velocity](world)   // bit 1
HEALTH   := freecs.Register[Health](world)     // bit 2

mask := POSITION | VELOCITY
```

`freecs.MaskOf[Position](world)` returns `(Mask, true)` for a previously-registered type or `(0, false)` otherwise. Use `freecs.MustMaskOf[Position](world)` for the panicking variant when registration is an invariant.

### Spawning

```go
entity := world.Spawn(POSITION | VELOCITY)

// Batch spawn with an initializer that gets direct table access:
entities := world.SpawnBatch(POSITION|VELOCITY, 1000, func(table *freecs.Archetype, index int) {
    column, _ := freecs.Column[Position](world, table)
    column[index] = Position{X: float32(index)}
})
```

### Single-entity access

```go
position, ok := freecs.Get[Position](world, entity)        // *Position, ok
position, ok := freecs.GetMut[Position](world, entity)     // *Position, ok; stamps tick

freecs.Set(world, entity, Position{X: 1, Y: 2})            // adds if missing
freecs.Add[Velocity](world, entity)                         // zero value
freecs.Remove[Velocity](world, entity)
freecs.Has[Position](world, entity)
world.HasComponents(entity, POSITION|VELOCITY)
world.ComponentMask(entity)
world.AddComponents(entity, HEALTH|VELOCITY)               // multi-bit migration
world.RemoveComponents(entity, VELOCITY)
world.Despawn(entity)
```

### Queries and iteration

```go
// Entity-yielding iteration:
for entity := range world.Query(POSITION|VELOCITY, 0) {
    // ...
}
world.QueryFirst(POSITION|VELOCITY, 0)
world.CountQuery(POSITION|VELOCITY, 0)

// Direct table access (no tick stamping):
world.ForEach(POSITION|VELOCITY, 0, func(entity freecs.Entity, table *freecs.Archetype, index int) {
    positions, _ := freecs.Column[Position](world, table)
    positions[index].X += 1
})

// Typed inner-loop iteration (no stamping):
freecs.Iter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
    position.X += velocity.X
})

// Iter1, Iter2, Iter3, Iter4 are provided. Extend the file with Iter5/Iter6 if you need more
// component arities; the pattern is mechanical.
```

`Iter*` materializes a `[]T` view per archetype via `unsafe.Slice` and indexes through that; per-element access is equivalent to a plain `for i := range slice`.

`Archetype.Mask` and `Archetype.Entities` are exported so a `ForEach`
callback can read them directly. They are read-only; structural changes
must go through the `*World` methods.

### Change detection

`GetMut` and `Set` both stamp the slot with the current tick. The bulk `Iter*` family does not. Call `freecs.MarkChanged[T](world, entity)` inside the body if you mutated a component you want change-detected.

```go
freecs.IterChanged1[Position](world, 0, 0, func(entity freecs.Entity, position *Position) {
    // only entities whose Position was stamped after the previous frame
})
freecs.IterChanged2[Position, Velocity](world, 0, 0, func(entity freecs.Entity, position *Position, velocity *Velocity) {
    // OR semantics: either column changed
})

freecs.Changed[Position](world, entity)
```

Call `world.Step()` at the end of each frame; this advances the tick and ages event queues by one.

### Events

```go
type CollisionEvent struct{ A, B freecs.Entity }

freecs.Send(world, CollisionEvent{A: x, B: y})

for _, event := range freecs.ReadEvents[CollisionEvent](world) { /* peek */ }
for _, event := range freecs.DrainEvents[CollisionEvent](world) { /* consume */ }

freecs.LenEvents[CollisionEvent](world)
freecs.PeekEvent[CollisionEvent](world)
freecs.ClearEvents[CollisionEvent](world)
```

Events sent on frame N are readable through frame N+1 and dropped at the start of frame N+2.

### Tags

```go
type Player struct{}
type Selected struct{}

freecs.AddTag[Player](world, entity)
freecs.HasTag[Player](world, entity)
freecs.RemoveTag[Player](world, entity)
for entity := range freecs.QueryTag[Player](world) { /* ... */ }
freecs.CountTag[Player](world)
```

Tag membership is a hash set keyed by entity, so adding or removing a tag does not migrate the entity between archetypes. Tags are automatically cleared from every set on `Despawn`.

### Command buffer

```go
freecs.Iter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
    if health.Value <= 0 {
        world.QueueDespawn(entity)
    }
})
world.ApplyCommands()
```

Methods on `*World`: `Queue`, `QueueSpawn`, `QueueDespawn`,
`QueueAddComponents`, `QueueRemoveComponents`, `ApplyCommands`,
`CommandCount`, `ClearCommands`. Top-level generic helpers: `QueueSet[T]`,
`QueueAdd[T]`, `QueueRemove[T]`, `QueueAddTag[T]`, `QueueRemoveTag[T]`.

### Resources

```go
type GameTime float32

freecs.SetResource(world, DeltaTime(0.016))
freecs.SetResource(world, GameTime(0))

delta, ok := freecs.Resource[DeltaTime](world)   // (*DeltaTime, bool)
if ok {
    *delta = 0.033
}
delta = freecs.MustResource[DeltaTime](world)    // panics if missing

freecs.HasResource[GameTime](world)
freecs.RemoveResource[GameTime](world)
```

Resources are keyed by Go type, so define named types (`type DeltaTime float32`) rather than bare `float32` when you want two scalar resources distinguished.

### Schedule

```go
schedule := freecs.NewSchedule()
schedule.Push("input", inputSystem)
schedule.Push("physics", physicsSystem)
schedule.Push("collision", collisionSystem)

schedule.InsertBefore("physics", "ai", aiSystem)
schedule.Replace("physics", physicsV2)
schedule.Remove("ai")

for {
    schedule.Run(world)
    world.ApplyCommands()
    world.Step()
}
```

## Multi-world

When 64 components per world is not enough, split components across several
worlds that share one entity allocator. Each `*World` still gets its own
full bitmask space (start from bit 0 again) and per-world component access
is unchanged. Only entity lifetime moves up to the `MultiWorld`.

```go
multi := freecs.NewMultiWorld()
core := multi.NewWorld()
render := multi.NewWorld()

POSITION := freecs.Register[Position](core)
SPRITE   := freecs.Register[Sprite](render)

entity := multi.Spawn()                    // shared allocator, no placement
core.SpawnEntityInto(entity, POSITION)
render.SpawnEntityInto(entity, SPRITE)

freecs.Set(core, entity, Position{X: 1})
freecs.Set(render, entity, Sprite{ID: 7})

multi.Despawn(entity)                       // cascades across every world
multi.Step()                                // calls Step on every world
```

`Get[T]`, `Has[T]`, `GetMut[T]`, `Changed[T]` return `false` (rather than
panic) when `T` is not registered on the world you ask, so a query for a
component that lives on a sibling world is a soft miss. `Set[T]`,
`Add[T]`, `Remove[T]` still panic on an unregistered type because writing
to a world that does not own the column is a programming error.

`MultiWorld` does not own tags, events, resources, or the command buffer;
those stay per-`*World`. If you want a single place to keep them, pin them
on a "primary" child world.

## Parallel iteration

`ParallelIter1` through `ParallelIter4` fan out one goroutine per matching
archetype and wait for all to finish. The hot path inside each goroutine is
identical to the serial `Iter*` family (`unsafe.Slice` materialization,
typed pointer per component, no reflection).

```go
freecs.ParallelIter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
    position.X += velocity.X * delta
    position.Y += velocity.Y * delta
})
```

Constraints:

- The callback **must not** mutate world topology (no `Spawn`, `Despawn`,
  `Add*`, `Remove*`, `Set` on a missing component, or tag inserts). Each
  archetype is owned by exactly one goroutine; structural mutations would
  touch shared `tableEdges`, `queryCache`, or tag sets. Use the command
  buffer (`QueueDespawn`, `QueueSet`, ...) for deferred topology changes.
- One `ParallelIter*` call at a time per world. The query cache is
  populated single-writer on miss; concurrent `ParallelIter*` calls from
  separate goroutines against the same world will race. Sequential calls
  are fine, and the parallelism within a single call is what does the work.
- The fan-out is per archetype, not per row. Workloads dominated by one
  large archetype don't benefit; split rows manually with goroutines and
  `freecs.Column[T]` if needed.

## Other notes

The bulk `Iter*` and `ParallelIter*` families do not stamp the change-
detection tick. Call `MarkChanged[T]` inside the body when you mutated a
component you want change-detected, or use the single-entity `GetMut` and
`Set` which stamp automatically.

## Examples

- `examples/simple`, a minimal CLI demo of every API (no graphics)
- `examples/breakout`, a Breakout game using freecs-go + [cogentcore/webgpu](https://github.com/cogentcore/webgpu) + GLFW. Builds for desktop and WebAssembly. The wasm bundle is auto-deployed to GitHub Pages on every push to `main`.

## Tasks

Common tasks live in the `justfile`. Run `just --list` to see them.

## License

freecs-go is free, open source and permissively licensed. All code in this repository is dual-licensed under either:

- MIT License ([LICENSE-MIT](LICENSE-MIT) or http://opensource.org/licenses/MIT)
- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE) or http://www.apache.org/licenses/LICENSE-2.0)

at your option.
