package freecs

import (
	"reflect"
	"unsafe"
)

// column is one component vec inside an archetype. The backing storage is
// a reflect-allocated []T so the GC tracks any pointers inside T correctly.
// dataPtr caches the address of the first element for hot-path indexing and
// is refreshed whenever the backing array may have moved (on append).
type column struct {
	slice    reflect.Value
	elemType reflect.Type
	elemSize uintptr
	dataPtr  unsafe.Pointer
	length   int
	changed  []uint32
}

func newColumn(elemType reflect.Type) *column {
	sliceType := reflect.SliceOf(elemType)
	return &column{
		slice:    reflect.MakeSlice(sliceType, 0, 0),
		elemType: elemType,
		elemSize: elemType.Size(),
	}
}

func (c *column) at(index int) unsafe.Pointer {
	return unsafe.Add(c.dataPtr, uintptr(index)*c.elemSize)
}

func (c *column) refresh() {
	if c.slice.Cap() > 0 {
		c.dataPtr = c.slice.UnsafePointer()
	} else {
		c.dataPtr = nil
	}
}

func (c *column) pushZero(tick uint32) {
	c.slice = reflect.Append(c.slice, reflect.Zero(c.elemType))
	c.refresh()
	c.length++
	c.changed = append(c.changed, tick)
}

// migrateFrom moves the element at srcIndex out of src and appends it onto c.
// The two columns must be for the same element type. src is compacted by the
// caller via swapRemove after every column has been migrated.
func (c *column) migrateFrom(src *column, srcIndex int, tick uint32) {
	c.slice = reflect.Append(c.slice, src.slice.Index(srcIndex))
	c.refresh()
	c.length++
	c.changed = append(c.changed, tick)
}

func (c *column) swapRemove(index int) {
	last := c.length - 1
	if index != last {
		c.slice.Index(index).Set(c.slice.Index(last))
		c.changed[index] = c.changed[last]
	}
	c.slice = c.slice.Slice(0, last)
	c.length = last
	c.changed = c.changed[:last]
	c.refresh()
}

func (c *column) markChanged(index int, tick uint32) {
	c.changed[index] = tick
}
