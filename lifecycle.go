package freecs

// EntityDespawned is the built-in lifecycle event the world emits at
// the end of every successful Despawn. Consumers read it via
// [ReadEvents] to react to entity death without polling every entity
// each frame.
//
// Event semantics follow the normal queue: an event sent on frame N is
// readable through frame N+1 and dropped at the start of frame N+2.
// As long as a consumer runs at least once every two frames, it will
// not miss a despawn.
type EntityDespawned struct {
	Entity Entity
}
