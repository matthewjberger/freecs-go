package freecs

// Commands defer structural changes (spawn, despawn, add, remove, set) until
// ApplyCommands runs. The motivating use case is iterating over a query and
// needing to mutate world topology mid-iteration: queue the changes, finish
// the loop, then flush. The buffer is a slice of closures, so any operation
// expressible as a function of (*World) can be deferred.

// Queue pushes a raw command closure onto the buffer. Most callers should
// prefer the typed helpers (QueueSpawn, QueueSet, etc.) but this escape
// hatch is here for one-off operations that do not have a dedicated helper.
func Queue(world *World, command func(*World)) {
	world.commandBuffer = append(world.commandBuffer, command)
}

// QueueSpawn defers a Spawn until ApplyCommands. The new entity handle is
// not available to the caller because it does not exist yet; if you need
// the handle synchronously, allocate it directly via Spawn instead.
func QueueSpawn(world *World, mask Mask) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		Spawn(w, mask)
	})
}

// QueueDespawn defers a Despawn.
func QueueDespawn(world *World, entity Entity) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		Despawn(w, entity)
	})
}

// QueueAddComponents defers an AddComponents call.
func QueueAddComponents(world *World, entity Entity, mask Mask) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		AddComponents(w, entity, mask)
	})
}

// QueueRemoveComponents defers a RemoveComponents call.
func QueueRemoveComponents(world *World, entity Entity, mask Mask) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		RemoveComponents(w, entity, mask)
	})
}

// QueueSet defers a typed Set[T] call. The value is captured by the closure
// so the caller can hand off the operation and forget about the payload.
func QueueSet[T any](world *World, entity Entity, value T) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		Set(w, entity, value)
	})
}

// QueueAdd defers an Add[T] call (zero-value initialization).
func QueueAdd[T any](world *World, entity Entity) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		Add[T](w, entity)
	})
}

// QueueRemove defers a Remove[T] call.
func QueueRemove[T any](world *World, entity Entity) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		Remove[T](w, entity)
	})
}

// QueueAddTag defers a tag insertion.
func QueueAddTag[T any](world *World, entity Entity) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		AddTag[T](w, entity)
	})
}

// QueueRemoveTag defers a tag removal.
func QueueRemoveTag[T any](world *World, entity Entity) {
	world.commandBuffer = append(world.commandBuffer, func(w *World) {
		RemoveTag[T](w, entity)
	})
}

// ApplyCommands drains the command buffer and runs each command in FIFO
// order. The buffer is swapped out at the start of the call so any command
// that enqueues additional commands lands them in the next buffer, to be
// drained on the next ApplyCommands.
func ApplyCommands(world *World) {
	pending := world.commandBuffer
	world.commandBuffer = nil
	for _, command := range pending {
		command(world)
	}
}

// CommandCount returns how many commands are currently queued.
func CommandCount(world *World) int { return len(world.commandBuffer) }

// ClearCommands discards every queued command without applying them.
func ClearCommands(world *World) { world.commandBuffer = nil }
