package kernel

import "unsafe"

// Memory in a gokm module:
//
//   - The Go heap: everything new/make/append/closures/maps allocate. It's
//     garbage collected and grows from heap_size up to heap_max (`gokm build
//     -heap-size/-heap-max`, or `insmod mod.ko heap_size=... heap_max=...`),
//     giving memory back when it shrinks. Use runtime.GC() and
//     runtime.ReadMemStats as usual.
//
//   - kernel.MakeBytes(n) is make([]byte, n) that returns ENOMEM instead of
//     failing fatally when the heap is full (running out of memory is a
//     fatal error in Go, as for make/new/append): use it for large buffers
//     whose size depends on the input.
//
//   - Off-heap buffers from AllocBytes: kernel memory (kvzalloc) outside the GC
//     heap, freed explicitly with FreeBytes. Use them for large or long-lived
//     byte buffers, so they don't take up GC heap or scanning time. Any
//     buffer still allocated at rmmod is freed automatically.

//export gokm_alloc
func gokmAlloc(size uintptr) unsafe.Pointer

//export gokm_free
func gokmFree(ptr unsafe.Pointer)

// AllocBytes allocates a zeroed n-byte buffer outside the Go heap. It returns
// nil if n <= 0 or the kernel is out of memory.
//
// The buffer must only hold plain data, never Go pointers: the GC doesn't scan
// it, so objects referenced only from inside it would be collected. Release
// it with FreeBytes. Don't use the slice (or any slice of it) afterwards.
func AllocBytes(n int) []byte {
	if n <= 0 {
		return nil
	}
	p := gokmAlloc(uintptr(n))
	if p == nil {
		return nil
	}
	return unsafe.Slice((*byte)(p), n)
}

// FreeBytes releases a buffer returned by AllocBytes. b must be the slice
// AllocBytes returned (or a reslice starting at the same address). Passing
// nil is a no-op.
func FreeBytes(b []byte) {
	if cap(b) == 0 {
		return
	}
	gokmFree(unsafe.Pointer(unsafe.SliceData(b)))
}

//go:linkname tryAlloc runtime.kmodTryAlloc
func tryAlloc(size uintptr) unsafe.Pointer

// MakeBytes returns a zeroed, garbage-collected []byte of length n, or
// ENOMEM if the Go heap can't hold it (after collecting garbage and growing
// up to heap_max). Unlike make, it never fails fatally.
func MakeBytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, EINVAL
	}
	if n == 0 {
		return []byte{}, nil
	}
	p := tryAlloc(uintptr(n))
	if p == nil {
		return nil, ENOMEM
	}
	return unsafe.Slice((*byte)(p), n), nil
}
