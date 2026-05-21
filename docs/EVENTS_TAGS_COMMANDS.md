# Events, tags, commands, resources

These four are the coordination primitives. They let systems communicate without coupling, mark entities with state that wouldn't fit cleanly in the archetype, defer mutations until iteration finishes, and share global values that don't belong to any one entity.

## Events

An event is a typed message published by one system and consumed by another, possibly in a different system within the same frame or in the next frame. The publishing system doesn't know who reads it; the reading system doesn't know who sent it. They're connected only by the event type.

The structure is a double buffer per event type:

```go
type eventQueue[T any] struct {
    current  []T  // events sent this frame
    previous []T  // events sent last frame
}
```

The full lifecycle:

- Frame N: `freecs.Send(world, evt)` pushes onto `current`
- Frame N: `freecs.ReadEvents[T](world)` yields `previous ++ current` (both visible)
- End of frame N: `world.Step()` rotates queues: the old `previous` is cleared, `current` becomes the new `previous`, `current` is now empty
- Frame N+1: `freecs.Send(world, evt')` pushes onto the new `current`
- Frame N+1: `freecs.ReadEvents[T](world)` yields `[evt, evt']` (frame N event still visible alongside the frame N+1 event)
- End of frame N+1: `world.Step()` rotates again; the frame N event is dropped
- Frame N+2: only `evt'` remains

The two-frame visibility window is what guarantees a reading system sees an event regardless of where it lives in the schedule. A collision system that runs early in the frame publishes a `CollisionEvent`; a damage system that runs later in the same frame sees it; a sound system that runs in the next frame (perhaps because it's batched per-frame) still sees it.

### Per-type queue storage

The event queues live on the World keyed by `reflect.Type`:

```go
type World struct {
    eventQueues []eventDriver       // for the polymorphic Step() update
    eventByType map[reflect.Type]int // type -> index into eventQueues
    // ...
}
```

`eventDriver` is a type-erased interface with one method, `update()`, called by `Step()`. The concrete `eventQueue[T]` implements that. Per-type access via `freecs.Send[T]`, `freecs.ReadEvents[T]`, `freecs.DrainEvents[T]`, etc. looks up `reflect.TypeOf((*T)(nil)).Elem()` in the map, type-asserts the result to `*eventQueue[T]`, and operates on it.

### The full event API

```go
freecs.Send(world, event)                       // append to current
freecs.ReadEvents[T](world) []T                 // peek both buffers
freecs.DrainEvents[T](world) []T                // consume both buffers
freecs.ClearEvents[T](world)                    // drop both buffers immediately
freecs.LenEvents[T](world) int                  // count across both buffers
freecs.PeekEvent[T](world) (T, bool)            // first event, no consume
```

`ReadEvents` and `DrainEvents` differ in whether they leave the queue intact for other consumers. The convention is `Read` for "I want to peek and let other readers see them too" and `Drain` for "I'm the sole consumer and want to clear the queue myself rather than wait for Step." Either is valid; pick by intent.

## Tags

Tags are markers attached to entities that don't carry per-instance data. "This entity is the player." "This unit is currently selected." "This emitter took damage this frame and should flash."

The archetype design would say "make Player a unit struct, give it a mask bit, query for it like any other component." That works, but it has a hidden cost: every flip of the tag triggers an `AddComponents` or `RemoveComponents` call, which migrates the entity to a different archetype, which means pulling every other component off the entity and pushing it into a new table. For markers that flip often (selection in an RTS, frame-local flags), the migration is pure waste.

The cheaper representation is a hash set:

```go
type World struct {
    tagSets map[reflect.Type]map[Entity]struct{}
    // ...
}
```

`freecs.AddTag[Player](world, entity)` inserts the entity into the set keyed by the `Player` type. `freecs.HasTag[Player](world, entity)` is a hash lookup. Adding and removing tags do not touch the archetype storage at all.

The type system distinguishes tag sets: `AddTag[Player]` and `AddTag[Enemy]` operate on different maps. You define a marker type per tag (`type Player struct{}`) and use it as the type parameter.

### When to use tags vs components

- Use **components** for stable categorizations that don't change much after spawn (Player, Enemy, NPC, Building). The archetype mask makes queries fast.
- Use **tags** for markers that flip frequently (Selected, Hovered, JustDamaged, FrozenThisFrame, AlertedToPlayer). The hash-set storage avoids archetype churn.

The line isn't sharp; a long-lived player has the same lifetime as a long-lived "Player" tag, and either works. The cost question is "does the marker flip during gameplay?" not "what does it represent semantically?"

### Despawn integrity

`despawnFromArchetype` walks every tag set and removes the entity, so stale entity handles never accumulate in tag sets:

```go
for _, set := range world.tagSets {
    delete(set, entity)
}
```

This is the only side effect of despawn that touches the tag system; tag operations otherwise don't see the archetype storage at all.

### The full tag API

```go
freecs.AddTag[Marker](world, entity)
freecs.RemoveTag[Marker](world, entity) bool
freecs.HasTag[Marker](world, entity) bool
freecs.QueryTag[Marker](world) iter.Seq[Entity]
freecs.CountTag[Marker](world) int
```

`QueryTag` returns an iterator over every entity carrying the tag. The order is the underlying map's iteration order, which Go intentionally randomizes; if you need a stable order, sort the entities afterwards.

## Commands

Iterating over entities while mutating world topology is unsafe. The cached `count := len(table.Entities)` at the top of the loop becomes stale when a row is added or removed; the typed `[]T` view materialized via `unsafe.Slice` may alias freed memory. The library's iteration callbacks document this constraint and the workaround is the command buffer.

The buffer is a slice of closures on the World:

```go
type World struct {
    commandBuffer []func(*World)
    // ...
}
```

The closures defer arbitrary work. Most of them are produced by typed helpers:

```go
world.QueueSpawn(mask)
world.QueueDespawn(entity)
world.QueueAddComponents(entity, mask)
world.QueueRemoveComponents(entity, mask)
freecs.QueueSet[T](world, entity, value)
freecs.QueueAdd[T](world, entity)
freecs.QueueRemove[T](world, entity)
freecs.QueueAddTag[T](world, entity)
freecs.QueueRemoveTag[T](world, entity)
world.Queue(func(w *World) { /* anything */ })   // escape hatch
```

`world.ApplyCommands()` drains the buffer in FIFO order:

```go
func (w *World) ApplyCommands() {
    pending := w.commandBuffer
    w.commandBuffer = nil
    for _, command := range pending {
        command(w)
    }
}
```

The swap-out of the buffer at the start is what makes nested queuing work. A command that runs and itself calls `world.QueueDespawn` lands its new closure on the fresh `w.commandBuffer`, not on the slice being iterated. The nested commands run on the next `ApplyCommands()` call rather than recursing within the current one.

### Usage pattern

The typical shape is one `ApplyCommands` call at the end of the frame, or after each system that needs to flush:

```go
freecs.Iter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
    if health.Value <= 0 {
        world.QueueDespawn(entity)
    }
})
world.ApplyCommands()
```

Or, if multiple systems want to deferred-spawn things and you don't care about ordering between systems' commands, drain once at the end:

```go
schedule.Run(world)
world.ApplyCommands()
world.Step()
```

The breakout example follows this pattern.

### Why closures instead of a typed enum

The Rust freecs design uses a typed enum of commands. We use closures. The Rust shape gives you static enumeration of every operation that can be deferred, which doubles as documentation. The Go shape trades that for flexibility: any operation expressible as `func(*World)` can be deferred, including one-off custom work, without growing the API surface. `world.Queue(fn)` is the escape hatch.

The cost is a small closure allocation per queued command. For game frame counts (a few hundred or low thousands of commands per frame), this is negligible compared to the archetype migrations the commands themselves perform.

## Resources

Resources are world-scoped values keyed by Go type. Delta time, current input snapshot, time of day, score, references to GPU contexts: anything that doesn't belong to a specific entity but does belong to the world.

```go
type World struct {
    resources map[reflect.Type]any
    // ...
}
```

Storage is one box per type, identified by `reflect.TypeOf((*T)(nil)).Elem()`. The box is `any` (which is the value of an `interface{}`, holding a `*T`). Access goes through generic helpers:

```go
freecs.SetResource(world, DeltaTime(0.016))
delta, ok := freecs.Resource[DeltaTime](world)   // (*DeltaTime, bool)
if ok {
    *delta = 0.033                                // mutate in place
}
delta = freecs.MustResource[DeltaTime](world)    // panics if missing
freecs.HasResource[DeltaTime](world)
freecs.RemoveResource[DeltaTime](world)
```

`SetResource` writes through the existing pointer if the resource is already set, so cached `*DeltaTime` references stay valid across re-sets:

```go
func SetResource[T any](world *World, value T) {
    key := reflect.TypeOf((*T)(nil)).Elem()
    if existing, ok := world.resources[key]; ok {
        *(existing.(*T)) = value
        return
    }
    world.resources[key] = &value
}
```

This matters when a system caches `delta := freecs.MustResource[DeltaTime](world)` at startup and reads `*delta` every frame. Without the in-place update, `SetResource` would replace the boxed pointer and the cached `*delta` would point at the old value.

### Define named types

If you want two scalar resources with the same underlying type, give them distinct named types:

```go
type DeltaTime float32
type GameTime float32

freecs.SetResource(world, DeltaTime(0.016))
freecs.SetResource(world, GameTime(0))

// distinct, indexed by reflect.Type
*freecs.MustResource[DeltaTime](world) = 0.033
*freecs.MustResource[GameTime](world) += 0.016
```

Using bare `float32` for both would conflict; the second `SetResource` would overwrite the first.

## Where to look in the code

- [`event.go`](../event.go), `eventQueue`, `Send`, `ReadEvents`, `DrainEvents`, etc.
- [`tag.go`](../tag.go), `AddTag`, `HasTag`, `QueryTag`, etc.
- [`command.go`](../command.go), `Queue`, `QueueSpawn`, `QueueDespawn`, `ApplyCommands`
- [`resource.go`](../resource.go), `SetResource`, `Resource`, `HasResource`
- [`world.go`](../world.go), the World fields that hold all of the above, plus `Step` for event rotation
