package freecs

import (
	"sync/atomic"
	"testing"
)

type Sprite2 struct{ ID int32 }
type Mass struct{ Value float32 }

func TestMultiWorldSharedAllocator(t *testing.T) {
	multi := NewMultiWorld()
	core := multi.NewWorld()
	render := multi.NewWorld()

	posMask := Register[Position](core)
	spriteMask := Register[Sprite2](render)

	entity := multi.Spawn()
	core.SpawnEntityInto(entity, posMask)
	render.SpawnEntityInto(entity, spriteMask)

	Set(core, entity, Position{X: 5, Y: 6})
	Set(render, entity, Sprite2{ID: 42})

	position, ok := Get[Position](core, entity)
	if !ok || position.X != 5 || position.Y != 6 {
		t.Fatalf("core position read: %+v ok=%v", position, ok)
	}
	sprite, ok := Get[Sprite2](render, entity)
	if !ok || sprite.ID != 42 {
		t.Fatalf("render sprite read: %+v ok=%v", sprite, ok)
	}
	if _, ok := Get[Sprite2](core, entity); ok {
		t.Fatal("core should not see the render component")
	}
}

func TestMultiWorldCascadeDespawn(t *testing.T) {
	multi := NewMultiWorld()
	core := multi.NewWorld()
	render := multi.NewWorld()

	posMask := Register[Position](core)
	spriteMask := Register[Sprite2](render)

	entity := multi.Spawn()
	core.SpawnEntityInto(entity, posMask)
	render.SpawnEntityInto(entity, spriteMask)
	Set(core, entity, Position{X: 1})
	Set(render, entity, Sprite2{ID: 1})

	if !multi.Despawn(entity) {
		t.Fatal("expected Despawn to report true")
	}

	if _, ok := Get[Position](core, entity); ok {
		t.Fatal("core should have dropped the entity")
	}
	if _, ok := Get[Sprite2](render, entity); ok {
		t.Fatal("render should have dropped the entity")
	}

	recycled := multi.Spawn()
	if recycled.ID != entity.ID {
		t.Fatalf("expected recycled id %d, got %d", entity.ID, recycled.ID)
	}
	if recycled.Generation == entity.Generation {
		t.Fatal("recycled handle must bump generation")
	}
}

func TestMultiWorldDespawnNoOpWhenAbsent(t *testing.T) {
	multi := NewMultiWorld()
	_ = multi.NewWorld()
	if multi.Despawn(Entity{ID: 999, Generation: 0}) {
		t.Fatal("Despawn on a never-spawned entity should return false")
	}
}

func TestParallelIter2(t *testing.T) {
	world := New()
	posMask := Register[Position](world)
	velMask := Register[Velocity](world)
	massMask := Register[Mass](world)

	const count = 600
	for index := 0; index < count; index++ {
		mask := posMask | velMask
		if index%3 == 0 {
			mask |= massMask
		}
		entity := world.Spawn(mask)
		Set(world, entity, Position{X: float32(index)})
		Set(world, entity, Velocity{X: 1})
	}

	var visited atomic.Int64
	ParallelIter2[Position, Velocity](world, 0, 0, func(_ Entity, position *Position, velocity *Velocity) {
		position.X += velocity.X
		visited.Add(1)
	})

	if visited.Load() != int64(count) {
		t.Fatalf("expected %d visits, got %d", count, visited.Load())
	}

	mismatched := 0
	for index := 0; index < count; index++ {
		entity := Entity{ID: uint32(index)}
		position, _ := Get[Position](world, entity)
		if position.X != float32(index)+1 {
			mismatched++
		}
	}
	if mismatched != 0 {
		t.Fatalf("%d entities did not pick up the velocity update", mismatched)
	}
}

func TestParallelIterAcrossArchetypesRaceFree(t *testing.T) {
	// One ParallelIter call fans out across archetypes. Each archetype is
	// owned by exactly one goroutine, so column writes never race with each
	// other. The test runs under -race and exercises three archetypes
	// concurrently.
	world := New()
	posMask := Register[Position](world)
	velMask := Register[Velocity](world)
	hpMask := Register[Health](world)

	const perArchetype = 250
	makeArchetype := func(mask Mask, baseX float32) {
		for slot := 0; slot < perArchetype; slot++ {
			entity := world.Spawn(mask)
			Set(world, entity, Position{X: baseX + float32(slot)})
		}
	}
	makeArchetype(posMask, 0)
	makeArchetype(posMask|velMask, 1000)
	makeArchetype(posMask|velMask|hpMask, 2000)

	var visited atomic.Int64
	ParallelIter1[Position](world, 0, 0, func(_ Entity, position *Position) {
		position.X += 1
		visited.Add(1)
	})

	if visited.Load() != int64(perArchetype*3) {
		t.Fatalf("expected %d visits, got %d", perArchetype*3, visited.Load())
	}
}

func TestSequentialParallelItersAreSafe(t *testing.T) {
	// Multiple ParallelIter calls in series must not race on the query cache.
	// (Concurrent ParallelIter calls from separate goroutines are not a
	// supported pattern: cachedTables populates the world's cache on miss
	// and is single-writer by design.)
	world := New()
	posMask := Register[Position](world)
	velMask := Register[Velocity](world)
	_ = velMask
	for index := 0; index < 50; index++ {
		entity := world.Spawn(posMask)
		Set(world, entity, Position{X: float32(index)})
	}

	for round := 0; round < 4; round++ {
		ParallelIter1[Position](world, 0, 0, func(_ Entity, position *Position) {
			position.X += 1
		})
	}

	for index := 0; index < 50; index++ {
		position, _ := Get[Position](world, Entity{ID: uint32(index)})
		if position.X != float32(index)+4 {
			t.Fatalf("entity %d: expected %v got %v", index, float32(index)+4, position.X)
		}
	}
}
