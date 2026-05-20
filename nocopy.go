package freecs

// noCopy may be embedded into structs that must not be copied after first
// use. go vet's copylocks analyzer treats anything implementing sync.Locker
// as non-copyable, so embedding noCopy is enough to get the warning without
// changing runtime behavior.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
