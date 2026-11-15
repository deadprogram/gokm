package kernel

// Handle is an integer that refers to a Go value, for storing in kernel
// memory: private-data fields such as sb->s_fs_info, inode->i_private or
// file->private_data.
//
// The garbage collector doesn't scan kernel memory, so a Go pointer stored
// there would not keep its object alive, and the GC could free it while the
// kernel still uses it. A Handle keeps the value alive (it's referenced from
// a table of GC roots) until Delete is called.
//
// It works like cgo.Handle:
//
//	h := kernel.NewHandle(state)        // store uintptr(h) in kernel memory
//	s := kernel.Handle(p).Value().(*State)
//	kernel.Handle(p).Delete()           // when the kernel object goes away
//
// Handles are never 0, so 0 can mean "no handle".
type Handle uintptr

var (
	handles    = map[Handle]any{}
	nextHandle Handle
)

// NewHandle returns a new handle for v. Go code always runs holding the
// module's Go lock, so this needs no extra synchronization.
func NewHandle(v any) Handle {
	for {
		nextHandle++
		if nextHandle == 0 {
			nextHandle = 1
		}
		if _, used := handles[nextHandle]; !used {
			break
		}
	}
	handles[nextHandle] = v
	return nextHandle
}

// Value returns the value h refers to. It panics if h is invalid or deleted.
func (h Handle) Value() any {
	v, ok := handles[h]
	if !ok {
		panic("kernel: invalid or deleted Handle")
	}
	return v
}

// Valid reports whether h refers to a value.
func (h Handle) Valid() bool {
	_, ok := handles[h]
	return ok
}

// Delete releases h, letting the value be garbage collected once nothing else
// references it. Deleting an invalid handle panics.
func (h Handle) Delete() {
	if _, ok := handles[h]; !ok {
		panic("kernel: Delete of invalid or deleted Handle")
	}
	delete(handles, h)
}

// LiveHandles returns the number of handles not yet deleted (useful for leak
// checks at module exit).
func LiveHandles() int {
	return len(handles)
}
