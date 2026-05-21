package freecs

import "reflect"

// Resources are world-scoped values that do not belong to any entity (delta
// time, input snapshot, score, time of day). Each resource is identified by
// its Go type, so a `type DeltaTime float32` is distinct from a `type
// GameTime float32`. Define your own named types and use them as the type
// parameter.

// resourceKeyFor returns the reflect.Type used to index resources for T.
// Centralized so a future optimization (per-world integer IDs assigned
// on first use, faster slice-indexed storage) has one site to swap.
// reflect.TypeOf on a compile-time-known T returns a runtime-cached
// *rtype pointer, so the per-call cost today is essentially a pointer
// dereference plus the map lookup.
func resourceKeyFor[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// SetResource installs the resource of type T, or writes through the
// existing storage if T is already set. Pointers previously returned by
// Resource[T] therefore remain valid across re-sets; the pointed-at value
// is updated in place.
func SetResource[T any](world *World, value T) {
	key := resourceKeyFor[T]()
	if existing, ok := world.resources[key]; ok {
		*(existing.(*T)) = value
		return
	}
	world.resources[key] = &value
}

// Resource returns a pointer to the resource of type T and true if
// it is set, or (nil, false) if it is missing.
func Resource[T any](world *World) (*T, bool) {
	value, ok := world.resources[resourceKeyFor[T]()]
	if !ok {
		return nil, false
	}
	return value.(*T), true
}

// MustResource returns a pointer to the resource of type T or panics
// if it is missing. Use Resource for optional lookups.
func MustResource[T any](world *World) *T {
	r, ok := Resource[T](world)
	if !ok {
		panic("freecs: resource " + resourceKeyFor[T]().String() + " is not set, call SetResource first")
	}
	return r
}

// HasResource reports whether the resource of type T is currently set.
func HasResource[T any](world *World) bool {
	_, ok := world.resources[resourceKeyFor[T]()]
	return ok
}

// RemoveResource clears the resource of type T. Returns true if it was set.
func RemoveResource[T any](world *World) bool {
	key := resourceKeyFor[T]()
	if _, ok := world.resources[key]; !ok {
		return false
	}
	delete(world.resources, key)
	return true
}
