# Events, tags, commands, resources

A frame loop runs systems in order. The systems do not call each other directly; they are functions that take `&World`, and the World is what they talk through. Four primitives sit on the World and carry that conversation. Events let one system message another across the schedule without naming a receiver. Tags mark entities with state that flips too often to live in the archetype. Commands defer mutations until iteration finishes. Resources hold global values that do not belong to any entity.

## Events

A collision system finds two entities that overlap and the damage system needs to know. Calling damage methods from inside the collision loop couples the two together; the collision system has to know damage exists, and the damage system has to expose an entry point the loop can reach. Scribbling a pending-damage component on one of the entities works for one-off cases and turns into a mess once ten systems want to broadcast. The clean shape is a queue. The collision system writes `CollisionEvent`s without naming a receiver. The damage system reads them without naming a sender.

The tricky part is lifetime. The queue cannot empty itself the moment an event is written, because a system later in the same frame would miss it. It cannot keep events forever, because nothing would ever drain. The compromise here is a two-frame rule. An event sent on frame N is readable through the end of frame N+1 and gone by the start of frame N+2. Every system gets a full frame to react regardless of schedule order, and memory is bounded at twice the per-frame volume.

The implementation is a double buffer per event type:

```go
type eventQueue[T any] struct {
    current  []T  // events sent this frame
    previous []T  // events sent last frame
}
```

The full lifecycle:

```
Frame N:        Send(world, evt)         pushes onto current
Frame N:        ReadEvents[T](world)     yields previous ++ current
End of frame N: Step()                   clears previous, swaps current into previous, current is now empty
Frame N+1:      Send(world, evt')        pushes onto the new current
Frame N+1:      ReadEvents[T](world)     yields [evt, evt']
End of frame N+1: Step()                 drops evt
Frame N+2:      only evt' remains
```

The two-frame visibility window is what guarantees a reading system sees an event regardless of where it lives in the schedule. A collision system early in the frame publishes a `CollisionEvent`. A damage system later in the same frame sees it. A sound system that runs in the next frame (perhaps because it batches per-frame) still sees it.

### Per-type queue storage

The event queues live on the World keyed by `reflect.Type`:

```go
type World struct {
    eventQueues []eventDriver         // polymorphic, for Step() to drive
    eventByType map[reflect.Type]int  // type -> index into eventQueues
    // ...
}
```

`eventDriver` is a type-erased interface with one method, `update()`, called by `Step()`. The concrete `eventQueue[T]` implements that. Per-type access via `freecs.Send[T]`, `freecs.ReadEvents[T]`, `freecs.DrainEvents[T]`, and so on looks up `reflect.TypeOf((*T)(nil)).Elem()` in the map, type-asserts the result to `*eventQueue[T]`, and operates on it. The reflect lookup happens once per call, not per element, so the cost is fixed at the call boundary.

### The full event API

```go
freecs.Send(world, event)                       // append to current
freecs.ReadEvents[T](world) []T                 // peek both buffers
freecs.DrainEvents[T](world) []T                // consume both buffers
freecs.ClearEvents[T](world)                    // drop both buffers immediately
freecs.LenEvents[T](world) int                  // count across both buffers
freecs.PeekEvent[T](world) (T, bool)            // first event, no consume
```

`ReadEvents` and `DrainEvents` differ in whether they leave the queue intact for other consumers. The convention is `Read` for "I want to peek and let other readers see them too" and `Drain` for "I'm the sole consumer, clear the queue now rather than waiting for Step." Either is valid; pick by intent.

### Lifecycle events

`EntityDespawned` is emitted on the event queue every time `world.Despawn` (or the cascade from `multi.Despawn`) successfully removes an entity. Consumers read it through `ReadEvents[EntityDespawned]` and react without polling every entity each frame.

```go
type EntityDespawned struct {
    Entity Entity
}
```

The semantics are the same two-frame queue as any other event. A despawn in frame N is visible through frame N+1 and dropped at the start of N+2. As long as a consumer runs at least once every two frames, it will not miss one.

## Tags

A tag is a marker attached to an entity that carries no per-instance data. "This entity is the player." "This unit is currently selected." "This emitter took damage this frame and should flash."

The archetype design would say: make `Player` a unit struct, give it a mask bit, query for it like any other component. That works, and for markers that never change after spawn (`Player`, `Enemy`, `Building`), it is fine. The reason tags exist as a separate primitive is the markers that flip often. Selection in an RTS toggles dozens of times a second. A frame-local "took damage" flag is set and cleared every frame. For those, putting the marker in the archetype mask means every flip triggers an `AddComponents` or `RemoveComponents` call, which migrates the entity to a different archetype, which means pulling every other component off the entity and pushing it into a new table. The migration cost is wasted on a flag that the next frame will flip back.

The cheaper representation is a hash set:

```go
type World struct {
    tagSets map[reflect.Type]map[Entity]struct{}
    // ...
}
```

`freecs.AddTag[Player](world, entity)` inserts the entity into the set keyed by the `Player` type. `freecs.HasTag[Player](world, entity)` is a hash lookup. Adding and removing tags do not touch the archetype storage at all. The type system distinguishes tag sets, so `AddTag[Player]` and `AddTag[Enemy]` operate on different maps. Define a marker type per tag (`type Player struct{}`) and use it as the type parameter.

### When to use tags versus components

Use components for stable categorizations that do not change much after spawn. `Player`, `Enemy`, `NPC`, `Building`. The archetype mask makes queries over them fast and the migration cost only pays at spawn time.

Use tags for markers that flip during gameplay. `Selected`, `Hovered`, `JustDamaged`, `FrozenThisFrame`, `AlertedToPlayer`. The hash-set storage avoids archetype churn that would otherwise dominate the per-frame cost.

The line is not sharp. A long-lived player has the same lifetime as a long-lived "Player" tag, and either works. The deciding question is whether the marker flips during gameplay, not what it represents semantically.

### Despawn integrity

`despawnFromArchetype` walks every tag set and removes the entity, so stale entity handles never accumulate in tag sets:

```go
for _, set := range world.tagSets {
    delete(set, entity)
}
```

This is the only side effect of despawn that touches the tag system. Tag operations otherwise do not see the archetype storage at all.

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

Iterating over entities while mutating world topology is unsafe. The cached `count := len(table.Entities)` at the top of the loop becomes stale when a row is added or removed; the typed `[]T` view materialized via `unsafe.Slice` may alias freed memory. The iteration callbacks document this constraint, and the in-iter mutation guard (`enterIter`/`leaveIter` on the World) turns an accidental Spawn or Despawn from inside `Iter*` into a clean panic at the call site. The workaround for actual deferred mutation is the command buffer.

The buffer is a slice of closures on the World:

```go
type World struct {
    commandBuffer []func(*World)
    // ...
}
```

The closures defer arbitrary work. Most are produced by typed helpers:

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

The swap-out of the buffer at the start is what makes nested queuing work. A command that runs and itself calls `world.QueueDespawn` lands its new closure on the fresh `w.commandBuffer`, not on the slice currently being iterated. The nested commands run on the next `ApplyCommands` call rather than recursing within the current one.

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

Or, if multiple systems want to deferred-spawn things and you do not care about ordering between systems' commands, drain once at the end:

```go
schedule.Run(world)
world.ApplyCommands()
world.Step()
```

The breakout example follows this pattern.

### Why closures instead of a typed enum

The Rust freecs design uses a typed enum of commands. freecs-go uses closures. The Rust shape gives you static enumeration of every operation that can be deferred, which doubles as documentation of the supported API surface. The Go shape trades that for flexibility. Any operation expressible as `func(*World)` can be deferred, including one-off custom work, without growing the public API. `world.Queue(fn)` is the escape hatch.

The cost is a small closure allocation per queued command. For game frame counts (a few hundred or low thousands of commands per frame), this is negligible compared to the archetype migrations the commands themselves perform.

## Resources

Resources are world-scoped values keyed by Go type. Delta time, the current input snapshot, time of day, score, references to GPU contexts: anything that belongs to the world but not to a specific entity.

```go
type World struct {
    resources map[reflect.Type]any
    // ...
}
```

Storage is one box per type, identified by `reflect.TypeOf((*T)(nil)).Elem()`. The box is `any` (an `interface{}` value holding a `*T`). Access goes through generic helpers:

```go
freecs.SetResource(world, DeltaTime(0.016))
delta, ok := freecs.Resource[DeltaTime](world)   // (*DeltaTime, bool)
if ok {
    *delta = 0.033                               // mutate in place
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

- [`event.go`](../event.go), `eventQueue`, `Send`, `ReadEvents`, `DrainEvents`, and friends
- [`lifecycle.go`](../lifecycle.go), the `EntityDespawned` event type
- [`tag.go`](../tag.go), `AddTag`, `HasTag`, `QueryTag`, and friends
- [`command.go`](../command.go), `Queue`, `QueueSpawn`, `QueueDespawn`, `ApplyCommands`
- [`resource.go`](../resource.go), `SetResource`, `Resource`, `MustResource`, `HasResource`
- [`world.go`](../world.go), the World fields that hold all of the above, plus `Step` for event rotation
