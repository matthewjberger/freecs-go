package freecs

import (
	"runtime"
	"testing"
)

// Sprite contains a heap pointer so the GC must track its reference inside
// the archetype's column. If the column allocates as []byte the pointer is
// invisible to the marker and the underlying object can be freed while still
// "owned" by an entity. This test forces a GC cycle between spawn and read.
type Sprite struct {
	Name *string
}

type Transform struct{ X, Y, Z float32 }

func TestGCSafeWithPointerComponents(t *testing.T) {
	world := New()
	Register[Sprite](world)
	Register[Transform](world)

	const count = 200
	entities := make([]Entity, count)
	for index := 0; index < count; index++ {
		name := "sprite-" + string(rune('A'+index%26))
		entity := world.Spawn(MaskOf[Sprite](world) | MaskOf[Transform](world))
		Set(world, entity, Sprite{Name: &name})
		Set(world, entity, Transform{X: float32(index)})
		entities[index] = entity
	}

	runtime.GC()
	runtime.GC()

	for index, entity := range entities {
		sprite, ok := Get[Sprite](world, entity)
		if !ok || sprite.Name == nil {
			t.Fatalf("entity %d lost its sprite pointer after GC", index)
		}
		expected := "sprite-" + string(rune('A'+index%26))
		if *sprite.Name != expected {
			t.Fatalf("entity %d sprite name corrupted: got %q want %q", index, *sprite.Name, expected)
		}
		transform, ok := Get[Transform](world, entity)
		if !ok || transform.X != float32(index) {
			t.Fatalf("entity %d transform corrupted: %+v ok=%v", index, transform, ok)
		}
	}
}

func TestRepeatedMigrationKeepsValues(t *testing.T) {
	world, posMask, velMask, hpMask := setup(t)
	entity := world.Spawn(posMask)
	Set(world, entity, Position{X: 100, Y: 200})

	for round := 0; round < 50; round++ {
		Add[Velocity](world, entity)
		Set(world, entity, Velocity{X: float32(round)})
		Add[Health](world, entity)
		Set(world, entity, Health{Value: float32(round * 2)})
		Remove[Velocity](world, entity)
		Remove[Health](world, entity)
	}

	position, ok := Get[Position](world, entity)
	if !ok || position.X != 100 || position.Y != 200 {
		t.Fatalf("position lost across 50 migrations: %+v ok=%v", position, ok)
	}
	if Has[Velocity](world, entity) || Has[Health](world, entity) {
		t.Fatal("velocity and health should be gone after final remove")
	}
	_ = velMask
	_ = hpMask
}

func TestLargeBatchAndQueryWalk(t *testing.T) {
	world, posMask, velMask, _ := setup(t)
	const count = 5000
	entities := world.SpawnBatch(posMask|velMask, count, func(table *Archetype, index int) {
		positions, _ := Column[Position](world, table)
		velocities, _ := Column[Velocity](world, table)
		positions[index] = Position{X: float32(index)}
		velocities[index] = Velocity{X: 1, Y: 2}
	})
	if len(entities) != count {
		t.Fatalf("expected %d entities, got %d", count, len(entities))
	}

	visited := 0
	Iter2[Position, Velocity](world, 0, 0, func(_ Entity, position *Position, velocity *Velocity) {
		position.X += velocity.X
		position.Y += velocity.Y
		visited++
	})
	if visited != count {
		t.Fatalf("expected to visit %d, got %d", count, visited)
	}

	for index, entity := range entities {
		position, _ := Get[Position](world, entity)
		if position.X != float32(index)+1 {
			t.Fatalf("entity %d: expected X=%v got %v", index, float32(index)+1, position.X)
		}
		if position.Y != 2 {
			t.Fatalf("entity %d: expected Y=2 got %v", index, position.Y)
		}
	}
}

func TestEdgeCacheUsedAfterFirstMiss(t *testing.T) {
	world, posMask, _, _ := setup(t)
	first := world.Spawn(posMask)
	Add[Velocity](world, first)
	startEdge := world.tableEdges[world.tableLookup[posMask]].add[1]
	if startEdge < 0 {
		t.Fatal("expected add edge from posMask to posMask|velMask to be cached after first migration")
	}

	for i := 0; i < 100; i++ {
		entity := world.Spawn(posMask)
		Add[Velocity](world, entity)
		Remove[Velocity](world, entity)
	}
	endEdge := world.tableEdges[world.tableLookup[posMask]].add[1]
	if endEdge != startEdge {
		t.Fatalf("edge cache should be stable: start=%d end=%d", startEdge, endEdge)
	}
}

func TestChangeDetectionAcrossFrames(t *testing.T) {
	world, posMask, _, _ := setup(t)
	entity := world.Spawn(posMask)
	Set(world, entity, Position{X: 1})

	world.Step()

	if Changed[Position](world, entity) {
		t.Fatal("frame 1: expected not changed")
	}

	if pos, ok := GetMut[Position](world, entity); ok {
		pos.X = 2
	}

	if !Changed[Position](world, entity) {
		t.Fatal("frame 1: expected changed after Mut")
	}

	world.Step()

	if Changed[Position](world, entity) {
		t.Fatal("frame 2: should no longer be changed after step")
	}
}

func TestIter2ExcludeMask(t *testing.T) {
	world, posMask, velMask, hpMask := setup(t)
	withHealth := world.Spawn(posMask | velMask | hpMask)
	withoutHealth := world.Spawn(posMask | velMask)
	Set(world, withHealth, Position{X: 1})
	Set(world, withoutHealth, Position{X: 2})

	count := 0
	Iter2[Position, Velocity](world, 0, hpMask, func(_ Entity, _ *Position, _ *Velocity) {
		count++
	})
	if count != 1 {
		t.Fatalf("exclude should drop the with-health entity, got count=%d", count)
	}
}

func TestEventBufferRollover(t *testing.T) {
	world := New()
	Send(world, CollisionEvent{A: Entity{ID: 1}})
	world.Step()
	Send(world, CollisionEvent{A: Entity{ID: 2}})

	all := ReadEvents[CollisionEvent](world)
	if len(all) != 2 {
		t.Fatalf("expected both events readable, got %d", len(all))
	}
	if all[0].A.ID != 1 || all[1].A.ID != 2 {
		t.Fatalf("expected previous then current, got %+v", all)
	}

	world.Step()
	all = ReadEvents[CollisionEvent](world)
	if len(all) != 1 || all[0].A.ID != 2 {
		t.Fatalf("after second step, only frame-1 event should remain: got %+v", all)
	}

	world.Step()
	if LenEvents[CollisionEvent](world) != 0 {
		t.Fatal("after third step both events should be gone")
	}
}

func TestQueueSetAddsComponent(t *testing.T) {
	world, posMask, _, _ := setup(t)
	entity := world.Spawn(posMask)

	QueueSet(world, entity, Velocity{X: 9})
	world.ApplyCommands()

	if !Has[Velocity](world, entity) {
		t.Fatal("QueueSet should add the component")
	}
	velocity, _ := Get[Velocity](world, entity)
	if velocity.X != 9 {
		t.Fatalf("expected velocity.X = 9, got %v", velocity.X)
	}
}

func TestNestedQueueDuringApply(t *testing.T) {
	world, posMask, _, _ := setup(t)
	first := world.Spawn(posMask)

	world.Queue(func(w *World) {
		w.Despawn(first)
		w.QueueSpawn(posMask)
	})
	world.ApplyCommands()

	if _, ok := Get[Position](world, first); ok {
		t.Fatal("first should be despawned")
	}
	if world.CommandCount() != 1 {
		t.Fatalf("expected nested QueueSpawn left in buffer, got %d", world.CommandCount())
	}
	world.ApplyCommands()
	if world.CountQuery(posMask, 0) != 1 {
		t.Fatalf("expected one alive entity after nested apply, got %d", world.CountQuery(posMask, 0))
	}
}
