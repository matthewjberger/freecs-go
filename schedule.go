package freecs

// SystemFn is the signature every system implements. A system reads or
// writes the world to advance one piece of game state.
type SystemFn func(world *World)

// Schedule is an ordered list of named systems. Run executes them in order
// each frame. Production engines layer parallel execution, conditional
// running, and ordering constraints on top of this; freecs-go keeps the
// scheduler intentionally small.
type Schedule struct {
	names   []string
	systems []SystemFn
}

// NewSchedule returns an empty schedule.
func NewSchedule() *Schedule { return &Schedule{} }

// Push appends system at the end of the schedule under name. Panics if name
// is already in use.
func (s *Schedule) Push(name string, system SystemFn) {
	if s.indexOf(name) >= 0 {
		panic("freecs: schedule already contains system " + name)
	}
	s.names = append(s.names, name)
	s.systems = append(s.systems, system)
}

// InsertBefore places system at the position of target. Panics if target
// is not in the schedule or name is a duplicate.
func (s *Schedule) InsertBefore(target, name string, system SystemFn) {
	pos := s.requireIndex(target)
	if s.indexOf(name) >= 0 {
		panic("freecs: schedule already contains system " + name)
	}
	s.names = append(s.names[:pos], append([]string{name}, s.names[pos:]...)...)
	s.systems = append(s.systems[:pos], append([]SystemFn{system}, s.systems[pos:]...)...)
}

// InsertAfter places system one slot past target.
func (s *Schedule) InsertAfter(target, name string, system SystemFn) {
	pos := s.requireIndex(target) + 1
	if s.indexOf(name) >= 0 {
		panic("freecs: schedule already contains system " + name)
	}
	s.names = append(s.names[:pos], append([]string{name}, s.names[pos:]...)...)
	s.systems = append(s.systems[:pos], append([]SystemFn{system}, s.systems[pos:]...)...)
}

// Replace swaps the implementation of an existing system, preserving order.
func (s *Schedule) Replace(name string, system SystemFn) {
	pos := s.requireIndex(name)
	s.systems[pos] = system
}

// Remove drops the system with name. Returns true if it was present.
func (s *Schedule) Remove(name string) bool {
	pos := s.indexOf(name)
	if pos < 0 {
		return false
	}
	s.names = append(s.names[:pos], s.names[pos+1:]...)
	s.systems = append(s.systems[:pos], s.systems[pos+1:]...)
	return true
}

// Contains reports whether name is in the schedule.
func (s *Schedule) Contains(name string) bool { return s.indexOf(name) >= 0 }

// Names returns the systems in execution order. The returned slice is a copy.
func (s *Schedule) Names() []string {
	out := make([]string, len(s.names))
	copy(out, s.names)
	return out
}

// Len returns the number of systems in the schedule.
func (s *Schedule) Len() int { return len(s.systems) }

// IsEmpty reports whether the schedule has no systems.
func (s *Schedule) IsEmpty() bool { return len(s.systems) == 0 }

// Run executes every system in order against world.
func (s *Schedule) Run(world *World) {
	for _, system := range s.systems {
		system(world)
	}
}

func (s *Schedule) indexOf(name string) int {
	for i, candidate := range s.names {
		if candidate == name {
			return i
		}
	}
	return -1
}

func (s *Schedule) requireIndex(name string) int {
	pos := s.indexOf(name)
	if pos < 0 {
		panic("freecs: no system named " + name + " in schedule")
	}
	return pos
}
