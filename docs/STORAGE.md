# Storage

The storage layer is what every other layer ultimately routes to. Every read goes through it. Every iteration walks it. Every mutation either writes into it or rearranges it. Getting the memory layout right is the whole reason archetype ECS exists.

## Entities are handles, not data

An `Entity` carries no data and owns nothing. It is a 32-bit ID plus a 32-bit generation, packed into 8 bytes by value. Pass it around freely. Store it in components. Put it in a map. It has no methods because there's nothing for it to do on its own; the world is what knows where its row lives.

```go
type Entity struct {
    ID         uint32
    Generation uint32
}
```

The reason it's a struct rather than a bare `uint64` is the generation guard. Imagine a bug where you despawn entity 42 and then spawn a new one. Without generations, the new entity gets ID 42 too, and any code that held the old handle silently starts reading the new entity's data. Generational handles fix that: the allocator pre-bumps the generation when the ID is freed, so the next spawn at ID 42 is `{42, 1}`. Old handles `{42, 0}` now fail to resolve.

The mechanism is in [`entity.go`](../entity.go). The `allocator` struct keeps a free list of `(id, next_generation)` pairs. Despawn pushes `entity.Generation + 1` onto the free list. The next allocate pops a pair and returns `{id, next_generation}`. The first time an ID is ever issued it gets generation 0; recycled IDs get whatever the prior bump produced.

The `next_id` counter is a `uint32`. After about four billion ever-spawned entities, it wraps. Wrap-on-overflow rather than panic-on-overflow is intentional; for any realistic game the counter never gets close, and a long-running service that does should switch IDs to `uint64` or recycle more aggressively. We left it as is to match the article series this library is a port of.

## Locations index by entity ID

`entityLocations` is a `[]entityLocation` where the index is the entity ID:

```go
type entityLocation struct {
    generation uint32
    tableIndex uint32
    arrayIndex uint32
    allocated  bool
}
```

`Generation` is what the current resident at this ID is using. `tableIndex` and `arrayIndex` are the archetype table and the row within it. `allocated` is the load-bearing bit. The zero value of `entityLocation` has `allocated=false`, so a slot that has been grown into but never written looks identical to a slot whose entity was despawned. Both states are rejected by `entityLocations.get`. We get safe defaults for free from Go's zero-value semantics.

`get(entity Entity) (table, index, ok)` is the gatekeeper. Stale handles die here:

```go
func (e *entityLocations) get(entity Entity) (int, int, bool) {
    if int(entity.ID) >= len(e.locations) {
        return 0, 0, false
    }
    location := e.locations[entity.ID]
    if !location.allocated || location.generation != entity.Generation {
        return 0, 0, false
    }
    return int(location.tableIndex), int(location.arrayIndex), true
}
```

Every public read accessor (`freecs.Get`, `freecs.Has`, `world.HasComponents`, `world.ComponentMask`) routes through this function first. A stale handle never reaches the column.

The locations slice is grown on demand by `ensureSlot`, using a fresh `make + copy` rather than `append`. The allocator hands out IDs monotonically when the free list is empty, so the slice grows by an exact size each time the next-ever ID exceeds the current length. Doubling would waste memory; append-with-doubling is the right call when you don't know growth pattern, but here we do.

## Archetypes hold typed columns

An archetype represents one specific component set. Every entity with exactly `Position + Velocity` lives in the archetype for that mask, in one shared `[]Position` and one shared `[]Velocity` and one shared `[]Entity`, all parallel by index.

```go
type Archetype struct {
    Mask     Mask
    Entities []Entity
    columns  [maxComponents]*column
}
```

`columns` is a fixed-size sparse array. The library never uses more than 64 component types per world, so `[64]*column` is 512 bytes per archetype. A column is nil when its bit isn't set in the archetype's mask. Lookup by component bit is `table.columns[componentInfo.bitIndex]`, one array probe.

The alternative would be a packed slice with a separate `bit-to-slot` index, which saves bytes on archetypes that hold few components. The sparse array wins on speed because it's one direct index instead of an extra lookup, and 512 bytes per archetype is negligible compared to the column data.

`Mask` and `Entities` are exported as fields so a `world.ForEach` callback can read them without going through getters. Go convention is exposed fields with a doc warning rather than helper functions. `columns` stays private because it holds unsafe state.

## Columns are typed slices behind an unsafe.Pointer

This is the part that took the most care. Go has no const generics, so we can't have a struct field whose type is "the type of T's component column" for some T that varies per archetype. The library needs runtime polymorphism over component types and per-element typed access. It also needs the garbage collector to track pointer fields inside components, which rules out `[]byte`-backed columns.

The design that works:

```go
type column struct {
    slice    reflect.Value
    elemType reflect.Type
    elemSize uintptr
    dataPtr  unsafe.Pointer
    length   int
    changed  []uint32
}
```

`slice` is a `reflect.Value` wrapping the underlying `[]T`. It is allocated via `reflect.MakeSlice(reflect.SliceOf(T), 0, 0)`, so the runtime knows the slice contains values of type `T` and will trace any embedded pointers correctly during garbage collection.

`dataPtr` is a cached `unsafe.Pointer` to the first element of the slice. We refresh it whenever the slice may have moved (after `reflect.Append`, after `swapRemove`). For element access on the hot path, we ignore `slice` and read through `dataPtr` directly.

`elemSize` is `reflect.Type.Size()`, used for pointer arithmetic in `at(index int) unsafe.Pointer`:

```go
func (c *column) at(index int) unsafe.Pointer {
    return unsafe.Add(c.dataPtr, uintptr(index)*c.elemSize)
}
```

The hot path in an `Iter2[A, B]` call materializes a typed view once per archetype:

```go
aSlice := unsafe.Slice((*A)(table.columns[aBit].dataPtr), count)
bSlice := unsafe.Slice((*B)(table.columns[bBit].dataPtr), count)
for i := 0; i < count; i++ {
    callback(table.Entities[i], &aSlice[i], &bSlice[i])
}
```

`unsafe.Slice` constructs a `[]T` slice header pointing at our backing array, with length `count`. There is no allocation; the slice header is a stack value. Indexing into it is identical to indexing a normal Go slice. The compiler sees a typed slice and emits the usual bounds-checked load.

## Why reflect for allocation, unsafe for access

Allocation has to be reflect-based because the Go garbage collector tracks slices by type. A `[]Position` knows that each element starts with `X` and `Y` `float32` fields and contains no pointers; a `[]Sprite` where `Sprite { Name *string }` knows the element has a pointer at offset zero. The runtime needs the type information to scan correctly during a collection. Allocating as `[]byte` and casting on the fly bypasses that scanning. Pointer-containing components would be GC-unsafe.

Access does not need that machinery. Once the slice is allocated correctly, we know the elements' offsets and sizes statically, so reading them through `unsafe.Pointer + index*size` is just integer arithmetic. The GC doesn't need to be told about reads.

Mutation through `*(*T)(column.at(index)) = value` does fire write barriers correctly when `T` contains pointers, because the compiler treats the dereferenced `*T` as a typed pointer store. We tested this with `TestGCSafeWithPointerComponents`, which spawns 200 entities holding a `*string`, forces two `runtime.GC()` cycles between spawn and read, and verifies the pointers survive.

## Growth and migration

`column.pushZero(tick)` and `column.migrateFrom(src, srcIndex, tick)` both go through `reflect.Append`. That call may or may not reallocate the backing array; we refresh `dataPtr` after every push to cover both cases. The refresh is `column.slice.UnsafePointer()`, which reads the slice header's data field.

`column.swapRemove(index)` does the inverse: copy the last element over the deleted slot via `reflect.Value.Set`, slice the underlying slice to one less element, and update the parallel `changed` slice in lockstep. Slicing doesn't reallocate so `dataPtr` stays valid in this path.

Migration between archetypes happens in [`moveEntity`](../spawn.go). For each bit in the destination archetype's mask, we either migrate the value from the source column (if the source archetype also had that bit) or push a zero value. After all destination columns are populated, the source row is swap-removed in lockstep. The source row's data is overwritten by the swap-remove or simply discarded if the row was already at the end.

The Rust freecs design uses `std::mem::take` to avoid cloning components during migration. Our Go version uses `reflect.Append` to copy from source to destination and then swap-removes the source. The net effect is the same: each component value ends up at exactly one slot, and the source slot is gone by the end of the function. The reflect-based path is slower than `mem::take` would be, but it's only on the cold path (spawn, despawn, structural change), not the hot iteration loop.

## A subtle GC retention case

There is one minor GC quirk worth noting. When `swapRemove` shrinks a column, it uses `reflect.Value.Slice` to reduce the length. The underlying backing array stays the same; the cap doesn't drop. So a row that was at index 5, then swap-removed, has its value bytes overwritten by what was at the last index. Fine. But if the column has cap 8 and length now 4, the slots at index 4 through 7 still hold old data in the backing array, beyond the visible length. For pointer-containing components, those pointers remain reachable from the slice's backing array and the GC will keep their targets alive until the column grows past them and overwrites the bytes, or until the column itself is freed.

This is a minor retention leak, not a correctness bug. A future version could explicitly zero the bytes past the live length on swap-remove. For now we don't.

## Where to look in the code

- [`entity.go`](../entity.go), `Entity`, `allocator`, `entityLocations`
- [`column.go`](../column.go), the `column` struct and all its operations
- [`archetype.go`](../archetype.go), `Archetype` and `tableEdges`
- [`registry.go`](../registry.go), the type-to-bit mapping
- [`spawn.go`](../spawn.go), spawning, despawning, and `moveEntity`
- [`world.go`](../world.go), the World struct that owns all of the above
