package freecs

import (
	"testing"
)

type Position struct{ X, Y float32 }
type Velocity struct{ X, Y float32 }
type Health struct{ Value float32 }

type Player struct{}
type Enemy struct{}

type CollisionEvent struct {
	A, B Entity
}

type DeltaTime float32

func setup(t *testing.T) (*World, Mask, Mask, Mask) {
	t.Helper()
	world := New()
	pos := Register[Position](world)
	vel := Register[Velocity](world)
	hp := Register[Health](world)
	return world, pos, vel, hp
}

func TestEntityAllocatorBumpsGeneration(t *testing.T) {
	var a allocator
	first := a.allocate()
	if first.ID != 0 || first.Generation != 0 {
		t.Fatalf("expected id 0 gen 0, got %v", first)
	}
	a.deallocate(first)
	recycled := a.allocate()
	if recycled.ID != first.ID {
		t.Fatalf("expected recycled id %d, got %d", first.ID, recycled.ID)
	}
	if recycled.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", recycled.Generation)
	}
}

func TestStaleHandleRejected(t *testing.T) {
	world, posMask, _, _ := setup(t)
	entity := Spawn(world, posMask)
	if !Despawn(world, entity) {
		t.Fatal("despawn should report true")
	}
	if _, ok := Get[Position](world, entity); ok {
		t.Fatal("stale handle should not resolve")
	}
	recycled := Spawn(world, posMask)
	if recycled.ID != entity.ID {
		t.Fatalf("expected reused id %d, got %d", entity.ID, recycled.ID)
	}
	if recycled.Generation == entity.Generation {
		t.Fatal("recycled handle must bump generation")
	}
	if _, ok := Get[Position](world, entity); ok {
		t.Fatal("old handle must remain invalid after recycle")
	}
	if _, ok := Get[Position](world, recycled); !ok {
		t.Fatal("recycled handle must resolve")
	}
}

func TestSpawnReadWrite(t *testing.T) {
	world, posMask, velMask, _ := setup(t)
	entity := Spawn(world, posMask|velMask)
	Set(world, entity, Position{X: 1, Y: 2})
	Set(world, entity, Velocity{X: 0.5, Y: 0.25})

	position, ok := Get[Position](world, entity)
	if !ok || position.X != 1 || position.Y != 2 {
		t.Fatalf("position read wrong: %+v ok=%v", position, ok)
	}
	velocity, ok := Get[Velocity](world, entity)
	if !ok || velocity.X != 0.5 {
		t.Fatalf("velocity read wrong: %+v ok=%v", velocity, ok)
	}
}

func TestAddRemoveComponentsMigration(t *testing.T) {
	world, posMask, velMask, _ := setup(t)
	entity := Spawn(world, posMask)
	Set(world, entity, Position{X: 3, Y: 4})

	Add[Velocity](world, entity)
	if !Has[Velocity](world, entity) {
		t.Fatal("velocity should be present after Add")
	}
	position, ok := Get[Position](world, entity)
	if !ok || position.X != 3 || position.Y != 4 {
		t.Fatalf("position lost across add migration: %+v", position)
	}

	Remove[Velocity](world, entity)
	if Has[Velocity](world, entity) {
		t.Fatal("velocity should be gone after Remove")
	}
	position, ok = Get[Position](world, entity)
	if !ok || position.X != 3 {
		t.Fatalf("position lost across remove migration: %+v", position)
	}

	mask, _ := ComponentMask(world, entity)
	if mask != posMask {
		t.Fatalf("expected final mask %b, got %b", posMask, mask)
	}
	_ = velMask
}

func TestDespawnCompactsAndSwaps(t *testing.T) {
	world, posMask, _, _ := setup(t)
	first := Spawn(world, posMask)
	Set(world, first, Position{X: 10})
	second := Spawn(world, posMask)
	Set(world, second, Position{X: 20})
	third := Spawn(world, posMask)
	Set(world, third, Position{X: 30})

	Despawn(world, second)

	positionFirst, _ := Get[Position](world, first)
	if positionFirst.X != 10 {
		t.Fatalf("first should still read 10, got %v", positionFirst.X)
	}
	positionThird, _ := Get[Position](world, third)
	if positionThird.X != 30 {
		t.Fatalf("third should still read 30 after swap, got %v", positionThird.X)
	}
}

func TestQueryIter2(t *testing.T) {
	world, posMask, velMask, _ := setup(t)
	for i := 0; i < 4; i++ {
		entity := Spawn(world, posMask|velMask)
		Set(world, entity, Position{X: float32(i)})
		Set(world, entity, Velocity{X: 1})
	}
	standalone := Spawn(world, posMask)
	Set(world, standalone, Position{X: 99})

	count := 0
	sum := float32(0)
	Iter2[Position, Velocity](world, 0, 0, func(_ Entity, position *Position, velocity *Velocity) {
		position.X += velocity.X
		sum += position.X
		count++
	})
	if count != 4 {
		t.Fatalf("expected 4 matched entities, got %d", count)
	}
	if sum != (1 + 2 + 3 + 4) {
		t.Fatalf("expected sum 10 after applying velocity once, got %v", sum)
	}
	position, _ := Get[Position](world, standalone)
	if position.X != 99 {
		t.Fatalf("standalone position should be untouched, got %v", position.X)
	}
}

func TestQueryCacheGrowsWithNewArchetype(t *testing.T) {
	world, posMask, velMask, hpMask := setup(t)
	first := Spawn(world, posMask|velMask)
	Set(world, first, Position{X: 1})

	primed := CountQuery(world, posMask, 0)
	if primed != 1 {
		t.Fatalf("expected 1 match for posMask, got %d", primed)
	}

	second := Spawn(world, posMask|velMask|hpMask)
	Set(world, second, Position{X: 2})

	updated := CountQuery(world, posMask, 0)
	if updated != 2 {
		t.Fatalf("expected 2 matches after new archetype, got %d", updated)
	}
}

func TestChangeDetection(t *testing.T) {
	world, posMask, _, _ := setup(t)
	entity := Spawn(world, posMask)
	Set(world, entity, Position{X: 1})

	world.Step()

	moved := 0
	IterChanged1[Position](world, 0, 0, func(_ Entity, _ *Position) { moved++ })
	if moved != 0 {
		t.Fatalf("nothing changed this frame, expected 0, got %d", moved)
	}

	if position, ok := Mut[Position](world, entity); ok {
		position.X = 7
	}

	moved = 0
	IterChanged1[Position](world, 0, 0, func(_ Entity, _ *Position) { moved++ })
	if moved != 1 {
		t.Fatalf("expected 1 changed entity, got %d", moved)
	}
}

func TestEvents(t *testing.T) {
	world := New()
	Send(world, CollisionEvent{A: Entity{ID: 1}, B: Entity{ID: 2}})

	events := ReadEvents[CollisionEvent](world)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	world.Step()
	if LenEvents[CollisionEvent](world) != 1 {
		t.Fatal("event should still be readable next frame")
	}

	world.Step()
	if LenEvents[CollisionEvent](world) != 0 {
		t.Fatal("event should expire after two steps")
	}
}

func TestTags(t *testing.T) {
	world, posMask, _, _ := setup(t)
	entity := Spawn(world, posMask)
	AddTag[Player](world, entity)
	if !HasTag[Player](world, entity) {
		t.Fatal("tag should be present after AddTag")
	}
	if HasTag[Enemy](world, entity) {
		t.Fatal("Enemy tag should not be present")
	}
	count := 0
	for range QueryTag[Player](world) {
		count++
	}
	if count != 1 {
		t.Fatalf("expected 1 player, got %d", count)
	}

	Despawn(world, entity)
	if HasTag[Player](world, entity) {
		t.Fatal("despawn should drop tags")
	}
}

func TestCommandBufferDeferredDespawn(t *testing.T) {
	world, posMask, _, hpMask := setup(t)
	deadOne := Spawn(world, posMask|hpMask)
	Set(world, deadOne, Health{Value: 0})
	alive := Spawn(world, posMask|hpMask)
	Set(world, alive, Health{Value: 100})

	Iter1[Health](world, 0, 0, func(entity Entity, health *Health) {
		if health.Value <= 0 {
			QueueDespawn(world, entity)
		}
	})

	if CommandCount(world) != 1 {
		t.Fatalf("expected 1 queued command, got %d", CommandCount(world))
	}
	ApplyCommands(world)

	if _, ok := Get[Health](world, deadOne); ok {
		t.Fatal("deadOne should be despawned")
	}
	if _, ok := Get[Health](world, alive); !ok {
		t.Fatal("alive should remain")
	}
}

func TestResources(t *testing.T) {
	world := New()
	SetResource(world, DeltaTime(0.016))
	delta := Resource[DeltaTime](world)
	if *delta != 0.016 {
		t.Fatalf("expected 0.016, got %v", *delta)
	}
	*delta = 0.033
	if *Resource[DeltaTime](world) != 0.033 {
		t.Fatal("mutation through pointer should persist")
	}
}

func TestScheduleOrdering(t *testing.T) {
	world := New()
	order := []string{}
	schedule := NewSchedule()
	schedule.Push("a", func(_ *World) { order = append(order, "a") })
	schedule.Push("c", func(_ *World) { order = append(order, "c") })
	schedule.InsertBefore("c", "b", func(_ *World) { order = append(order, "b") })
	schedule.Run(world)
	if got := order; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("schedule order wrong: %v", got)
	}
}
