# Change detection

The render system wants to push transforms to the GPU only for entities that moved this frame. The network sync system wants to serialize only the components that changed since last tick. Most "incremental work" patterns benefit from knowing which slots were touched recently.

freecs-go records modifications with a watermark scheme: each component slot carries a tick stamp, the World carries a current tick and a "last frame" tick, and a slot counts as changed when its stamp exceeds the watermark.

## The data structure

Each `column` stores a `changed []uint32` parallel to its component data:

```go
type column struct {
    slice    reflect.Value
    elemSize uintptr
    dataPtr  unsafe.Pointer
    length   int
    changed  []uint32  // tick stamp per row
    // ...
}
```

`changed[i]` is the tick at which `column[i]` was last modified. Push, swap-remove, and migration all keep this slice in lockstep with the component data.

`World` carries two counters:

```go
type World struct {
    currentTick uint32  // stamp value for writes happening now
    lastTick    uint32  // watermark: anything stamped after this is "changed"
    // ...
}
```

A row is considered changed when `changed[i] > lastTick`. Both counters increment together at the end of each frame.

## The frame advance

`world.Step()` is the once-per-frame hinge:

```go
func (w *World) Step() {
    for _, queue := range w.eventQueues {
        queue.update()
    }
    w.lastTick = w.currentTick
    w.currentTick++
}
```

Two things happen in order. First, every event queue rotates (current events become previous; the old previous is dropped). Then `lastTick` catches up to what `currentTick` just was, and `currentTick` advances by one.

After `Step()`, slots stamped during the just-finished frame have tick values equal to the new `lastTick`. The `> lastTick` check fails for them; they no longer count as changed. Slots stamped during the new frame will get the new `currentTick`, which is `> lastTick`, so they will. No clearing pass between frames. The watermark moves; the old stamps are stale relative to it without ever being touched.

The `currentTick` is `uint32`. After `2^32` frames (about 2.3 years at 60 FPS) it wraps to zero, and a slot stamped at the old `UINT32_MAX` looks newer than `lastTick == UINT32_MAX` even though it isn't. Production engines either widen the tick to `uint64` or run a periodic rebasing pass. We don't, matching the article series design.

## What stamps automatically

The single-entity accessors stamp:

- `freecs.GetMut[T](world, entity)` stamps the slot before returning the pointer
- `freecs.Set[T](world, entity, value)` stamps on the existing-component path and on the add-then-write path
- `freecs.Add[T](world, entity)` adds the component with a fresh stamp via `moveEntity`
- `world.Spawn(mask)` and `world.SpawnBatch(mask, count, init)` stamp every newly-pushed row

What doesn't stamp:

- `freecs.Iter1` through `Iter4`, `freecs.ParallelIter*`, `world.ForEach` give you direct pointers without stamping. A system that mutates through those pointers is expected to call `freecs.MarkChanged[T](world, entity)` manually if it wants change detection.

The split is deliberate. Single-entity access is the slow, ergonomic path; one branch and a write per call is fine. Bulk iteration is the fast path; stamping every slot whether or not it was actually modified would multiply the inner-loop cost. Letting the user opt in via `MarkChanged` is the right tradeoff for systems that want both speed and tracking.

```go
freecs.Iter2[Position, Velocity](world, 0, 0, func(entity freecs.Entity, position *Position, velocity *Velocity) {
    position.X += velocity.X * delta
    position.Y += velocity.Y * delta
    freecs.MarkChanged[Position](world, entity)  // explicit
})
```

## The IterChanged family

Once stamping is in place, "iterate only the changed slots" is just an extra check inside the loop:

```go
freecs.IterChanged1[Position](world, 0, 0, func(entity freecs.Entity, position *Position) {
    // only fires for entities whose Position was stamped after lastTick
})
```

The internal shape mirrors the regular Iter helpers but reads the column's `changed` slice before invoking the callback:

```go
column := table.columns[positionBit]
aSlice := unsafe.Slice((*Position)(column.dataPtr), count)
for i := 0; i < count; i++ {
    if column.changed[i] > since {
        callback(table.Entities[i], &aSlice[i])
    }
}
```

`IterChanged1` through `IterChanged4` exist. Multi-component variants use OR semantics across columns:

```go
freecs.IterChanged2[Position, Velocity](world, 0, 0, func(entity freecs.Entity, position *Position, velocity *Velocity) {
    // fires when EITHER Position or Velocity was stamped after lastTick
})
```

OR is the natural fit for the typical use case: "redraw this entity if anything about its visual representation moved." If you want strict AND semantics ("only when both columns changed this frame"), check `freecs.Changed[T]` individually in the callback.

## `Changed[T]` for single-entity checks

```go
if freecs.Changed[Position](world, entity) { /* ... */ }
```

Returns true when the slot's stamp exceeds `lastTick`. Returns false for stale handles, missing components, or types not registered on this world. Useful when you have a specific entity you care about (the active camera, the player) and want to skip work when it didn't move.

## A subtle detail about newly-spawned entities

When `world.Spawn(mask)` pushes a row, it stamps `changed[arrayIndex] = currentTick`. At the moment of the spawn, `currentTick` is whatever value the frame is using. If you then call `IterChanged1[Position]` later in the same frame, the spawned entity fires because its stamp (`currentTick`) exceeds `lastTick` (which is `currentTick - 1` for any non-initial frame).

The corner case is the very first frame, when `currentTick == 0` and `lastTick == 0` because `Step()` has never run. A spawn during that frame stamps `0`. The check `0 > 0` is false, so the spawned entity does NOT appear in `IterChanged*` on frame zero. If you want startup-spawned entities to trigger `IterChanged*` on the first frame they're visible, call `world.Step()` once before the spawn loop so the watermark advances.

This is the same behavior as the Rust freecs design and isn't usually noticed in practice; most games run their setup before any IterChanged-based system fires.

## Where to look in the code

- [`column.go`](../column.go), the `changed` slice and `markChanged` method
- [`world.go`](../world.go), `Step`, `currentTick`, `lastTick`
- [`mutate.go`](../mutate.go), `GetMut` and `Set` stamping
- [`change.go`](../change.go), `IterChanged1` through `IterChanged4`, `Changed`
- [`query.go`](../query.go), `MarkChanged`
