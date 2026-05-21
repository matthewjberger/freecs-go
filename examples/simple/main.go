package main

import (
	"fmt"

	"github.com/matthewjberger/freecs-go"
)

type Position struct{ X, Y float32 }
type Velocity struct{ X, Y float32 }
type Health struct{ Value float32 }

type Player struct{}
type Enemy struct{}

type CollisionEvent struct {
	A, B freecs.Entity
}

type DeltaTime float32

func main() {
	world := freecs.New()

	POSITION := freecs.Register[Position](world)
	VELOCITY := freecs.Register[Velocity](world)
	HEALTH := freecs.Register[Health](world)

	freecs.SetResource(world, DeltaTime(0.016))

	player := world.Spawn(POSITION | VELOCITY | HEALTH)
	freecs.Set(world, player, Position{X: 0, Y: 0})
	freecs.Set(world, player, Velocity{X: 5, Y: 0})
	freecs.Set(world, player, Health{Value: 100})
	freecs.AddTag[Player](world, player)
	fmt.Printf("Created player entity: %+v\n", player)

	enemies := world.SpawnBatch(POSITION|VELOCITY|HEALTH, 3, func(table *freecs.Archetype, index int) {
		// The batch initializer hands you direct table access, but typed
		// access through Set keeps the example readable.
		_ = table
		_ = index
	})
	for index, entity := range enemies {
		freecs.Set(world, entity, Position{X: float32(index+1) * 10, Y: 0})
		freecs.Set(world, entity, Velocity{X: -2, Y: 1})
		freecs.Set(world, entity, Health{Value: 50})
		freecs.AddTag[Enemy](world, entity)
	}
	fmt.Printf("Created %d enemies\n", len(enemies))

	schedule := freecs.NewSchedule()
	schedule.Push("physics", physicsSystem(POSITION, VELOCITY))
	schedule.Push("collision", collisionSystem(POSITION))
	schedule.Push("damage", damageSystem)
	schedule.Push("health-decay", healthDecaySystem)

	for frame := 0; frame < 3; frame++ {
		fmt.Printf("\n--- frame %d ---\n", frame)
		schedule.Run(world)
		world.ApplyCommands()
		world.Step()
	}

	fmt.Println("\nAll entities still alive:")
	for entity := range world.Query(POSITION, 0) {
		position, _ := freecs.Get[Position](world, entity)
		tag := "?"
		switch {
		case freecs.HasTag[Player](world, entity):
			tag = "player"
		case freecs.HasTag[Enemy](world, entity):
			tag = "enemy"
		}
		fmt.Printf("  %s %+v at (%.2f, %.2f)\n", tag, entity, position.X, position.Y)
	}
}

func physicsSystem(posMask, velMask freecs.Mask) freecs.SystemFn {
	return func(world *freecs.World) {
		delta := float32(*freecs.MustResource[DeltaTime](world))
		freecs.Iter2[Position, Velocity](world, 0, 0, func(_ freecs.Entity, position *Position, velocity *Velocity) {
			position.X += velocity.X * delta
			position.Y += velocity.Y * delta
		})
		_ = posMask
		_ = velMask
	}
}

func collisionSystem(posMask freecs.Mask) freecs.SystemFn {
	return func(world *freecs.World) {
		type snapshot struct {
			entity   freecs.Entity
			position Position
		}
		var snapshots []snapshot
		freecs.Iter1[Position](world, 0, 0, func(entity freecs.Entity, position *Position) {
			snapshots = append(snapshots, snapshot{entity: entity, position: *position})
		})
		for indexA := 0; indexA < len(snapshots); indexA++ {
			for indexB := indexA + 1; indexB < len(snapshots); indexB++ {
				dx := snapshots[indexA].position.X - snapshots[indexB].position.X
				dy := snapshots[indexA].position.Y - snapshots[indexB].position.Y
				if dx*dx+dy*dy < 4 {
					freecs.Send(world, CollisionEvent{A: snapshots[indexA].entity, B: snapshots[indexB].entity})
				}
			}
		}
		_ = posMask
	}
}

func damageSystem(world *freecs.World) {
	for _, event := range freecs.DrainEvents[CollisionEvent](world) {
		if health, ok := freecs.GetMut[Health](world, event.A); ok {
			health.Value -= 5
		}
		if health, ok := freecs.GetMut[Health](world, event.B); ok {
			health.Value -= 5
		}
		fmt.Printf("  collision %+v vs %+v\n", event.A, event.B)
	}
}

func healthDecaySystem(world *freecs.World) {
	freecs.Iter1[Health](world, 0, 0, func(entity freecs.Entity, health *Health) {
		health.Value *= 0.99
		if health.Value <= 0 {
			world.QueueDespawn(entity)
		}
	})
}
