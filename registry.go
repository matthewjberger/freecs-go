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

// MaskOf returns the Mask for component type T. It panics if T has not been
// registered. Use Register first.
func MaskOf[T any](world *World) Mask {
	elemType := reflect.TypeOf((*T)(nil)).Elem()
	info, ok := world.registry.infoForType(elemType)
	if !ok {
		panic(fmt.Sprintf("freecs: component %s is not registered, call freecs.Register[%s] first", elemType, elemType))
	}
	return info.mask
}

func componentInfoFor[T any](world *World) *componentInfo {
	elemType := reflect.TypeOf((*T)(nil)).Elem()
	info, ok := world.registry.infoForType(elemType)
	if !ok {
		panic(fmt.Sprintf("freecs: component %s is not registered, call freecs.Register[%s] first", elemType, elemType))
	}
	return info
}
