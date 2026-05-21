//go:generate go run ./internal/gen_iter

// Package freecs is an archetype-based Entity Component System for Go.
//
// Entities are generational handles. Components are plain Go structs.
// Storage is one archetype table per unique component set, with each
// component held as a contiguous typed column. Queries walk archetypes
// whose mask satisfies the query and iterate the relevant columns
// directly. Hot-path iteration via [Iter1] through [Iter4] materializes
// typed []T views over column memory once per archetype and walks them
// with no per-element reflection.
//
// # Quick start
//
//	world := freecs.New()
//	POSITION := freecs.Register[Position](world)
//	VELOCITY := freecs.Register[Velocity](world)
//
//	entity := world.Spawn(POSITION | VELOCITY)
//	freecs.Set(world, entity, Position{X: 0, Y: 0})
//	freecs.Set(world, entity, Velocity{X: 5, Y: 0})
//
//	freecs.Iter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
//	    position.X += velocity.X
//	    position.Y += velocity.Y
//	})
//
// # API shape
//
// Non-generic operations on a [World] are methods: [World.Spawn],
// [World.Despawn], [World.AddComponents], [World.Query], [World.ForEach],
// [World.ApplyCommands], and so on. Operations parameterized over a
// component type are top-level generic functions: [Get], [Set], [Add],
// [Remove], [Has], [GetMut], [Changed], [MarkChanged], [Iter1] through
// [Iter4], [IterChanged1] through [IterChanged4], [ParallelIter1] through
// [ParallelIter4], [Column], [Send], [ReadEvents], [DrainEvents],
// [AddTag], [HasTag], [QueryTag], [SetResource], [Resource]. The split is
// forced because Go forbids type parameters on methods.
//
// # Multi-world
//
// A single [World] holds at most 64 component types. Projects that need
// more split components across child worlds owned by a [MultiWorld] that
// shares one entity allocator. Each child world keeps its own full
// bitmask space and per-world component access is unchanged. Entity
// lifetime moves up to the [MultiWorld] so [MultiWorld.Despawn] cascades
// across every child.
//
// # Concurrency
//
// A [World] is not safe for concurrent use; wrap it in a mutex if
// multiple goroutines need to touch it. The [ParallelIter1] family fans
// out one goroutine per matching archetype within a single call and is
// the supported way to do row-parallel work; the callback must not
// mutate world topology and must access components only through the
// supplied typed pointers.
//
// # Design notes
//
// freecs-go ports the Rust freecs design described in the
// "Build your own ECS" series. The internal strategies (archetype tables
// in struct-of-arrays layout, generational handles, archetype graph
// cache for single-bit migrations, memoized query cache invalidated on
// new-archetype creation, watermark-based change detection, double-
// buffered events, sparse-set tags, deferred command buffer) are the
// same. Where Rust uses a declarative macro for per-component fan-out,
// freecs-go uses generics over a small unsafe-pointer column core.
package freecs
