package kernel

import (
	"sync"
	"sync/atomic"
)

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
// Handles are never 0, so 0 can mean "no handle". Value is lock-free, so it
// can be used in any context (including atomic ones); NewHandle and Delete
// take a mutex.
type Handle uintptr

var (
	handles     atomic.Pointer[map[Handle]any] // copy-on-write
	handlesLock sync.Mutex
	nextHandle  Handle
)

// NewHandle returns a new handle for v.
func NewHandle(v any) Handle {
	handlesLock.Lock()
	defer handlesLock.Unlock()
	old := handles.Load()
	m := make(map[Handle]any, mapLen(old)+1)
	if old != nil {
		for k, val := range *old {
			m[k] = val
		}
	}
	for {
		nextHandle++
		if nextHandle == 0 {
			nextHandle = 1
		}
		if _, used := m[nextHandle]; !used {
			break
		}
	}
	m[nextHandle] = v
	handles.Store(&m)
	return nextHandle
}

func mapLen(m *map[Handle]any) int {
	if m == nil {
		return 0
	}
	return len(*m)
}

// Value returns the value h refers to. It panics if h is invalid or deleted.
func (h Handle) Value() any {
	if m := handles.Load(); m != nil {
		if v, ok := (*m)[h]; ok {
			return v
		}
	}
	panic("kernel: invalid or deleted Handle")
}

// Valid reports whether h refers to a value.
func (h Handle) Valid() bool {
	m := handles.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[h]
	return ok
}

// Delete releases h, letting the value be garbage collected once nothing else
// references it. Deleting an invalid handle panics.
func (h Handle) Delete() {
	handlesLock.Lock()
	defer handlesLock.Unlock()
	old := handles.Load()
	if old == nil {
		panic("kernel: Delete of invalid or deleted Handle")
	}
	if _, ok := (*old)[h]; !ok {
		panic("kernel: Delete of invalid or deleted Handle")
	}
	m := make(map[Handle]any, len(*old))
	for k, val := range *old {
		if k != h {
			m[k] = val
		}
	}
	handles.Store(&m)
}

// LiveHandles returns the number of handles not yet deleted (useful for leak
// checks at module exit).
func LiveHandles() int {
	return mapLen(handles.Load())
}
