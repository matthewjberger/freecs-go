package freecs

import "reflect"

// Resources are world-scoped values that do not belong to any entity (delta
// time, input snapshot, score, time of day). Each resource is identified by
// its Go type, so a `type DeltaTime float32` is distinct from a `type
// GameTime float32`. Define your own named types and use them as the type
// parameter.

// SetResource installs the resource of type T, or writes through the
// existing storage if T is already set. Pointers previously returned by
// Resource[T] therefore remain valid across re-sets; the pointed-at value
// is updated in place.
func SetResource[T any](world *World, value T) {
	key := reflect.TypeOf((*T)(nil)).Elem()
	if existing, ok := world.resources[key]; ok {
		*(existing.(*T)) = value
		return
	}
	world.resources[key] = &value
}

// Resource returns a pointer to the resource of type T. It panics if T has
// not been set; use HasResource to check first when the resource is
// optional.
func Resource[T any](world *World) *T {
	key := reflect.TypeOf((*T)(nil)).Elem()
	value, ok := world.resources[key]
	if !ok {
		panic("freecs: resource " + key.String() + " is not set, call SetResource first")
	}
	return value.(*T)
}

// HasResource reports whether the resource of type T is currently set.
func HasResource[T any](world *World) bool {
	key := reflect.TypeOf((*T)(nil)).Elem()
	_, ok := world.resources[key]
	return ok
}

// RemoveResource clears the resource of type T. Returns true if it was set.
func RemoveResource[T any](world *World) bool {
	key := reflect.TypeOf((*T)(nil)).Elem()
	if _, ok := world.resources[key]; !ok {
		return false
	}
	delete(world.resources, key)
	return true
}
