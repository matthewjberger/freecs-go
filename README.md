# freecs-go

An archetype-based Entity Component System for Go, port of [freecs](https://github.com/matthewjberger/freecs).

The internal strategies are identical to the Rust version. Where Rust uses a declarative macro to fan out per-component code, the Go version uses generics over a small unsafe-pointer column core, so components are normal Go types and the typed API is `freecs.Get[Position](world, entity)` rather than `world.get_position(entity)`. No build step, no codegen, no reflection on the hot path.

## What carries over from freecs

- Archetype tables in struct-of-arrays layout, one table per unique component set
- Generational `Entity` handles, stale handles fail closed
- Archetype graph cache for O(1) single-bit add/remove migrations
- Memoized query cache invalidated incrementally on new-archetype creation
- Watermark-based change detection (`Mut` stamps, `IterChanged*` reads since the previous frame)
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

    player := freecs.Spawn(world, POSITION|VELOCITY)
    freecs.Set(world, player, Position{X: 0, Y: 0})
    freecs.Set(world, player, Velocity{X: 5, Y: 0})
    freecs.AddTag[Player](world, player)

    physics := func(w *freecs.World) {
        delta := float32(*freecs.Resource[DeltaTime](w))
        freecs.Iter2[Position, Velocity](w, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
            position.X += velocity.X * delta
            position.Y += velocity.Y * delta
        })
    }

    schedule := freecs.NewSchedule().Push("physics", physics)
    for frame := 0; frame < 60; frame++ {
        schedule.Run(world)
        freecs.ApplyCommands(world)
        world.Step()
    }

    position, _ := freecs.Get[Position](world, player)
    fmt.Printf("player at (%.2f, %.2f)\n", position.X, position.Y)
}
```

## API surface

### Registration and masks

Component types must be registered before they can be spawned or queried. Registration is per-`World`, returns a single-bit `Mask`, and assigns bits in order. Up to 64 component types per world.

```go
POSITION := freecs.Register[Position](world)   // bit 0
VELOCITY := freecs.Register[Velocity](world)   // bit 1
HEALTH   := freecs.Register[Health](world)     // bit 2

mask := POSITION | VELOCITY
```

`freecs.MaskOf[Position](world)` returns the mask for a previously-registered type. It panics if `T` was never registered.

### Spawning

```go
entity := freecs.Spawn(world, POSITION|VELOCITY)

// Batch spawn with an initializer that gets direct table access:
entities := freecs.SpawnBatch(world, POSITION|VELOCITY, 1000, func(table *freecs.Archetype, index int) {
    column := freecs.Column[Position](world, table)
    column[index] = Position{X: float32(index)}
})
```

### Single-entity access

```go
position, ok := freecs.Get[Position](world, entity)        // *Position, ok
position, ok := freecs.Mut[Position](world, entity)        // *Position, ok; stamps tick

freecs.Set(world, entity, Position{X: 1, Y: 2})            // adds if missing
freecs.Add[Velocity](world, entity)                         // zero value
freecs.Remove[Velocity](world, entity)
freecs.Has[Position](world, entity)
freecs.HasComponents(world, entity, POSITION|VELOCITY)
freecs.ComponentMask(world, entity)
freecs.AddComponents(world, entity, HEALTH|VELOCITY)        // multi-bit migration
freecs.RemoveComponents(world, entity, VELOCITY)
freecs.Despawn(world, entity)
```

### Queries and iteration

```go
// Entity-yielding iteration:
for entity := range freecs.Query(world, POSITION|VELOCITY, 0) {
    // ...
}
freecs.QueryFirst(world, POSITION|VELOCITY, 0)
freecs.CountQuery(world, POSITION|VELOCITY, 0)

// Direct table access (no tick stamping):
freecs.ForEach(world, POSITION|VELOCITY, 0, func(entity freecs.Entity, table *freecs.Archetype, index int) {
    positions := freecs.Column[Position](world, table)
    positions[index].X += 1
})

// Typed inner-loop iteration (no stamping):
freecs.Iter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
    position.X += velocity.X
})

// Iter1, Iter2, Iter3, Iter4 are provided. Extend the file with Iter5/Iter6 if you need more
// component arities; the pattern is mechanical.
```

### Change detection

`Mut` and `Set` both stamp the slot with the current tick. The bulk `Iter*` family does not — call `freecs.MarkChanged[T](world, entity)` inside the body if you mutated a component you want change-detected.

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
        freecs.QueueDespawn(world, entity)
    }
})
freecs.ApplyCommands(world)
```

Available: `Queue` (raw closure), `QueueSpawn`, `QueueDespawn`, `QueueAddComponents`, `QueueRemoveComponents`, `QueueSet[T]`, `QueueAdd[T]`, `QueueRemove[T]`, `QueueAddTag[T]`, `QueueRemoveTag[T]`.

### Resources

```go
type GameTime float32

freecs.SetResource(world, DeltaTime(0.016))
freecs.SetResource(world, GameTime(0))

delta := freecs.Resource[DeltaTime](world)   // *DeltaTime
*delta = 0.033

freecs.HasResource[GameTime](world)
freecs.RemoveResource[GameTime](world)
```

Resources are keyed by Go type, so define named types (`type DeltaTime float32`) rather than bare `float32` when you want two scalar resources distinguished.

### Schedule

```go
schedule := freecs.NewSchedule().
    Push("input", inputSystem).
    Push("physics", physicsSystem).
    Push("collision", collisionSystem)

schedule.InsertBefore("physics", "ai", aiSystem)
schedule.Replace("physics", physicsV2)
schedule.Remove("ai")

for {
    schedule.Run(world)
    freecs.ApplyCommands(world)
    world.Step()
}
```

## Why generics instead of codegen

In Rust, `freecs::ecs!` is a declarative macro that takes one component declaration and fans out the per-component struct fields, accessors, mask constants, and event/tag plumbing. In Go, generics fill the same role: every typed operation is parameterized over the component type, and the underlying column storage is a `reflect.MakeSlice` allocation backed by a cached `unsafe.Pointer` for indexing. The hot path materializes a `[]T` view via `unsafe.Slice` once per archetype per query and iterates that, so per-element access compiles to the same memory operations as hand-written `for i := range positions`.

The thing the Go version cannot offer that codegen would is named accessors like `world.GetPosition(entity)`. If that matters for your project, you can run a small `go generate` wrapper that emits typed forwarders on top of this library; the engine underneath does not need to change.

## Limitations relative to freecs (Rust)

- 64 component types per world. Multi-world support is not implemented yet; create separate `*World` values and share a mutable allocator manually if you need more.
- No parallel iteration helper. `Iter*` is single-threaded. Rust freecs uses Rayon; Go's equivalent (`golang.org/x/sync/errgroup` or hand-rolled goroutines) is straightforward to layer on top of `ForEach` per-archetype if you want it.
- The bulk `Iter*` family does not stamp change-detection ticks; call `MarkChanged[T]` explicitly. The single-entity `Mut` and `Set` do stamp automatically.

## License

MIT. See LICENSE.md.
