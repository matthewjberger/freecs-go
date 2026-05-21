package freecs

import (
	"fmt"
	"reflect"
)

// Mask is a 64-bit component-set mask. Each registered component type owns
// one bit. The practical ceiling is 64 components per World.
type Mask uint64

const maxComponents = 64

// componentInfo describes a registered component type.
type componentInfo struct {
	bitIndex uint8
	mask     Mask
	elemType reflect.Type
}

type registry struct {
	byType  map[reflect.Type]*componentInfo
	byBit   [maxComponents]*componentInfo
	nextBit uint8
}

func newRegistry() *registry {
	return &registry{byType: make(map[reflect.Type]*componentInfo)}
}

func (r *registry) registerType(elemType reflect.Type) *componentInfo {
	if info, ok := r.byType[elemType]; ok {
		return info
	}
	if r.nextBit >= maxComponents {
		panic(fmt.Sprintf("freecs: cannot register more than %d component types per world", maxComponents))
	}
	bit := r.nextBit
	r.nextBit++
	info := &componentInfo{
		bitIndex: bit,
		mask:     Mask(1) << bit,
		elemType: elemType,
	}
	r.byType[elemType] = info
	r.byBit[bit] = info
	return info
}

func (r *registry) infoForType(elemType reflect.Type) (*componentInfo, bool) {
	info, ok := r.byType[elemType]
	return info, ok
}

func (r *registry) infoForBit(bit uint8) *componentInfo {
	return r.byBit[bit]
}

// Register registers component type T with the world if it has not been
// registered already and returns its single-bit Mask. Component bit positions
// are assigned in registration order, so callers that want stable masks
// across runs should register all component types at startup in a fixed order.
func Register[T any](world *World) Mask {
	elemType := reflect.TypeOf((*T)(nil)).Elem()
	info := world.registry.registerType(elemType)
	return info.mask
}

// MaskOf returns the Mask for component type T and true if T is
// registered, or (0, false) otherwise.
func MaskOf[T any](world *World) (Mask, bool) {
	elemType := reflect.TypeOf((*T)(nil)).Elem()
	info, ok := world.registry.infoForType(elemType)
	if !ok {
		return 0, false
	}
	return info.mask, true
}

// MustMaskOf returns the Mask for T or panics if T is not registered.
// Use MaskOf for optional lookups.
func MustMaskOf[T any](world *World) Mask {
	m, ok := MaskOf[T](world)
	if !ok {
		elemType := reflect.TypeOf((*T)(nil)).Elem()
		panic(fmt.Sprintf("freecs: component %s is not registered, call freecs.Register[%s] first", elemType, elemType))
	}
	return m
}

// componentInfoFor returns the registered info for T, or (nil, false) when
// T was never registered on this world. Used by read-only accessors that
// should answer "this world does not have T" rather than panic, which
// matters in multi-world setups where a component lives on one child world
// and a query against a sibling world should return false.
func componentInfoFor[T any](world *World) (*componentInfo, bool) {
	elemType := reflect.TypeOf((*T)(nil)).Elem()
	return world.registry.infoForType(elemType)
}

// mustComponentInfo is the panicking variant used by write paths where
// targeting a world that does not own the column is a programming error
// worth surfacing loudly.
func mustComponentInfo[T any](world *World) *componentInfo {
	elemType := reflect.TypeOf((*T)(nil)).Elem()
	info, ok := world.registry.infoForType(elemType)
	if !ok {
		panic(fmt.Sprintf("freecs: component %s is not registered, call freecs.Register[%s] first", elemType, elemType))
	}
	return info
}
