package freecs

import "reflect"

// eventDriver is the type-erased interface World uses to advance every event
// queue on Step regardless of payload type.
type eventDriver interface {
	update()
}

// eventQueue is the typed double buffer that backs each event type. Events
// sent on frame N are readable through frame N+1 and are cleared by Step at
// the start of frame N+2. Drop the type parameter to stash queues in the
// world by reflect.Type.
type eventQueue[T any] struct {
	current  []T
	previous []T
}

func (q *eventQueue[T]) update() {
	q.previous = q.previous[:0]
	q.current, q.previous = q.previous, q.current
}

func eventQueueFor[T any](world *World) *eventQueue[T] {
	key := reflect.TypeOf((*T)(nil)).Elem()
	if index, ok := world.eventByType[key]; ok {
		return world.eventQueues[index].(*eventQueue[T])
	}
	queue := &eventQueue[T]{}
	world.eventByType[key] = len(world.eventQueues)
	world.eventQueues = append(world.eventQueues, queue)
	return queue
}

// Send appends an event of type T to the current frame's buffer. Any system
// that reads T this frame or the next will see it before it is cleared.
func Send[T any](world *World, event T) {
	queue := eventQueueFor[T](world)
	queue.current = append(queue.current, event)
}

// ReadEvents yields all currently queued events of type T (previous frame
// first, then this frame). The events stay in the queue.
func ReadEvents[T any](world *World) []T {
	queue := eventQueueFor[T](world)
	if len(queue.previous) == 0 {
		return queue.current
	}
	if len(queue.current) == 0 {
		return queue.previous
	}
	out := make([]T, 0, len(queue.previous)+len(queue.current))
	out = append(out, queue.previous...)
	out = append(out, queue.current...)
	return out
}

// DrainEvents returns every queued event of type T (previous, then current)
// and empties the queue.
func DrainEvents[T any](world *World) []T {
	queue := eventQueueFor[T](world)
	out := make([]T, 0, len(queue.previous)+len(queue.current))
	out = append(out, queue.previous...)
	out = append(out, queue.current...)
	queue.previous = queue.previous[:0]
	queue.current = queue.current[:0]
	return out
}

// ClearEvents drops every queued event of type T immediately, ignoring the
// frame schedule.
func ClearEvents[T any](world *World) {
	queue := eventQueueFor[T](world)
	queue.previous = queue.previous[:0]
	queue.current = queue.current[:0]
}

// LenEvents returns how many events of type T are currently queued across
// both buffers.
func LenEvents[T any](world *World) int {
	queue := eventQueueFor[T](world)
	return len(queue.previous) + len(queue.current)
}

// PeekEvent returns the first queued event of type T without consuming it,
// or the zero value and false if the queue is empty.
func PeekEvent[T any](world *World) (T, bool) {
	queue := eventQueueFor[T](world)
	if len(queue.previous) > 0 {
		return queue.previous[0], true
	}
	if len(queue.current) > 0 {
		return queue.current[0], true
	}
	var zero T
	return zero, false
}
