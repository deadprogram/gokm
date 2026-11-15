//go:build gc.custom && linuxkernel

// Garbage collector for Go code in a Linux kernel module.
//
// A mark/sweep collector over a set of chunks of kernel memory (vzalloc, see
// kmod/heap.c) that grows as needed, up to heap_max, and gives empty chunks
// back. Each chunk is an array of 32-byte blocks plus one state byte per
// block (free / head / tail / marked head). An object is a run of blocks
// starting with a 16-byte header holding TinyGo's layout of the object and
// its size; heap objects are scanned precisely with that layout (pointer-free
// objects like []byte aren't scanned at all). Stacks and registers are
// scanned conservatively.
//
// Concurrency: allocation takes a BH-safe spinlock (kmod/heap.c), so any
// task, including atomic-context code, can allocate. A collection stops the
// world through the entry gate (kmod/threads.c): it waits until no other
// task is running Go, then marks from globals, every task's stack and the
// parked tasks' spilled registers. Collections and heap growth need process
// context; atomic-context allocation uses free space, plus a spare chunk
// kept in reserve (it can be added without sleeping), and a background
// kthread collects, grows and refills the reserve ahead of time.
//
// Running out of memory is a fatal error, as in Go (TinyGo's code assumes
// allocation never panics, so it couldn't be recovered anyway). Code that
// wants to handle a full heap allocates with kernel.MakeBytes, which
// returns ENOMEM instead.
//
// This replaces TinyGo's gc_custom.go (excluded for this target).

package runtime

import (
	"internal/gclayout"
	"unsafe"
)

const needsStaticHeap = false

// Implemented in kmod/heap.c and kmod/threads.c.
//
//export gokm_chunk_alloc
func gcChunkAlloc(size uintptr) uintptr

//export gokm_chunk_free
func gcChunkFree(mem uintptr)

//export gokm_heap_lock
func gcHeapLock()

//export gokm_heap_unlock
func gcHeapUnlock()

//export gokm_can_block
func gcCanBlock() bool

//export gokm_stop_world
func gcStopWorld() bool

//export gokm_start_world
func gcStartWorld()

//export gokm_scan_stacks
func gcScanStacks()

//export gokm_gc_kick
func gcKick()

const (
	gcBlockSize  = 32
	gcHeaderSize = 16 // layout, size
	gcMaxChunks  = 1024
	gcMinChunk   = 64 << 10

	gcStateFree   = 0
	gcStateHead   = 1
	gcStateTail   = 2
	gcStateMarked = 3 // a marked head

	gcMarkStackEntries = 32 << 10
)

type gcChunk struct {
	mem     uintptr // kernel allocation; state bytes start here
	size    uintptr // bytes at mem
	start   uintptr // first block
	nblocks uintptr
	free    uintptr // free blocks
	hint    uintptr // block to start searching from
}

// All plain integers: the precise global roots skip them, so chunk
// addresses don't keep anything alive.
var (
	gcChunks  [gcMaxChunks]gcChunk // sorted by start
	gcNChunks int

	gcInitialSize uintptr
	gcMaxSize     uintptr
	gcChunkSize   uintptr // size of the next chunk to add
	gcHeapSys     uintptr // bytes of all chunks
	gcInUse       uintptr // bytes in allocated blocks
	gcNextGC      uintptr // collect in the background beyond this

	gcMallocs, gcFrees, gcTotalAlloc, gcNumGC uint64

	gcMarkStack    uintptr // kernel memory, gcMarkStackEntries words
	gcMarkLen      uintptr
	gcMarkOverflow bool

	gcReleased  [16]uintptr // empty chunks to give back after a collection
	gcNReleased int

	gcReserve     uintptr // spare chunk for atomic contexts (0 if used up)
	gcReserveSize uintptr
)

var zeroSizedAlloc uint8

func gcAlignUp(n, a uintptr) uintptr { return (n + a - 1) &^ (a - 1) }

func (c *gcChunk) state(b uintptr) uint8 {
	return *(*uint8)(unsafe.Pointer(c.mem + b))
}

func (c *gcChunk) setState(b uintptr, s uint8) {
	*(*uint8)(unsafe.Pointer(c.mem + b)) = s
}

// gcInit sets up the heap with its first chunk. Called once at module load.
func gcInit(initial, max uintptr) bool {
	gcInitialSize = initial
	gcMaxSize = max
	gcChunkSize = initial
	gcMarkStack = gcChunkAlloc(gcMarkStackEntries * unsafe.Sizeof(uintptr(0)))
	if gcMarkStack == 0 || !gcAddChunk(gcAlignUp(initial, 4096)) {
		return false
	}
	gcNextGC = gcHeapSys / 2
	gcReserveSize = gcAlignUp(initial/4, 4096)
	if gcReserveSize < gcMinChunk {
		gcReserveSize = gcMinChunk
	}
	gcRefillReserve()
	return true
}

// gcRefillReserve allocates the spare chunk atomic contexts can add to the
// heap (process context; counts toward heap_max once added).
func gcRefillReserve() {
	if gcReserve == 0 && gcHeapSys+gcReserveSize <= gcMaxSize {
		gcReserve = gcChunkAlloc(gcReserveSize)
	}
}

// gcGrow adds a chunk of at least minBytes (process context only).
func gcGrow(minBytes uintptr) bool {
	size := gcChunkSize
	if size < gcMinChunk {
		size = gcMinChunk
	}
	need := gcAlignUp(minBytes+minBytes/gcBlockSize+2*gcBlockSize, 4096)
	if size < need {
		size = need
	}
	size = gcAlignUp(size, 4096)
	if gcHeapSys+size > gcMaxSize {
		if gcHeapSys+need > gcMaxSize {
			return false
		}
		size = gcMaxSize - gcHeapSys
	}
	return gcAddChunk(size)
}

// gcAddChunk adds a chunk of exactly size bytes (a multiple of the page size).
func gcAddChunk(size uintptr) bool {
	if gcNChunks == gcMaxChunks || gcHeapSys+size > gcMaxSize {
		return false
	}
	mem := gcChunkAlloc(size)
	if mem == 0 {
		return false
	}
	gcHeapLock()
	gcInsertChunk(mem, size)
	gcHeapUnlock()
	return true
}

// gcInsertChunk makes the memory at mem part of the heap (heap lock held;
// doesn't sleep, so atomic contexts can add the reserve).
func gcInsertChunk(mem, size uintptr) {
	// size bytes = nblocks state bytes + padding + nblocks blocks.
	nblocks := size / (gcBlockSize + 1)
	start := gcAlignUp(mem+nblocks, gcBlockSize)
	for start+nblocks*gcBlockSize > mem+size {
		nblocks--
	}
	c := gcChunk{mem: mem, size: size, start: start, nblocks: nblocks, free: nblocks}

	i := gcNChunks
	for i > 0 && gcChunks[i-1].start > start {
		gcChunks[i] = gcChunks[i-1]
		i--
	}
	gcChunks[i] = c
	gcNChunks++
	gcHeapSys += size
	// Grow geometrically: the next chunk is half the heap (at least).
	if gcChunkSize < gcHeapSys/2 {
		gcChunkSize = gcHeapSys / 2
	}
}

// gcFind returns the chunk whose blocks contain addr.
func gcFind(addr uintptr) *gcChunk {
	lo, hi := 0, gcNChunks
	for lo < hi {
		mid := (lo + hi) / 2
		c := &gcChunks[mid]
		switch {
		case addr < c.start:
			hi = mid
		case addr >= c.start+c.nblocks*gcBlockSize:
			lo = mid + 1
		default:
			return c
		}
	}
	return nil
}

// allocBlocks finds n free consecutive blocks (heap lock held).
func (c *gcChunk) allocBlocks(n uintptr) uintptr {
	if c.free < n {
		return 0
	}
	for pass := 0; pass < 2; pass++ {
		i, end := c.hint, c.nblocks
		if pass == 1 {
			i, end = 0, c.hint+n
			if end > c.nblocks {
				end = c.nblocks
			}
		}
		run := uintptr(0)
		for ; i < end; i++ {
			if c.state(i) != gcStateFree {
				run = 0
				continue
			}
			run++
			if run == n {
				first := i + 1 - n
				c.setState(first, gcStateHead)
				for b := first + 1; b <= i; b++ {
					c.setState(b, gcStateTail)
				}
				c.hint = i + 1
				c.free -= n
				return c.start + first*gcBlockSize
			}
		}
	}
	return 0
}

func alloc(size uintptr, layout unsafe.Pointer) unsafe.Pointer {
	p := gcAlloc(size, layout)
	if p == nil {
		// Fatal, as in Go: TinyGo doesn't expect allocation to panic.
		if gcCanBlock() {
			runtimeFatal("out of memory (Go heap reached heap_max)")
		}
		runtimeFatal("out of memory (Go heap full in atomic context)")
	}
	return p
}

// kmodTryAlloc allocates pointer-free memory, returning nil instead of
// failing fatally when the heap can't hold it (kernel.MakeBytes).
func kmodTryAlloc(size uintptr) unsafe.Pointer {
	return gcAlloc(size, gclayout.NoPtrs.AsPtr())
}

// gcAlloc allocates, collecting and growing the heap as needed; nil if the
// memory can't be found.
func gcAlloc(size uintptr, layout unsafe.Pointer) unsafe.Pointer {
	if size == 0 {
		return unsafe.Pointer(&zeroSizedAlloc)
	}
	n := (size + gcHeaderSize + gcBlockSize - 1) / gcBlockSize
	for attempt := 0; ; attempt++ {
		var p uintptr
		gcHeapLock()
		for i := 0; i < gcNChunks && p == 0; i++ {
			p = gcChunks[i].allocBlocks(n)
		}
		if p == 0 && attempt == 0 && !gcCanBlock() && gcReserve != 0 &&
			gcReserveSize >= n*(gcBlockSize+1)+gcBlockSize && gcNChunks < gcMaxChunks {
			// Atomic context with a full heap: add the reserve chunk.
			gcInsertChunk(gcReserve, gcReserveSize)
			gcReserve = 0
			p = gcChunks[gcNChunks-1].allocBlocks(n)
			if p == 0 {
				for i := 0; i < gcNChunks && p == 0; i++ {
					p = gcChunks[i].allocBlocks(n)
				}
			}
			gcKick() // refill it
		}
		if p != 0 {
			gcInUse += n * gcBlockSize
			gcMallocs++
			gcTotalAlloc += uint64(size)
			kick := gcInUse > gcNextGC
			gcHeapUnlock()

			*(*uintptr)(unsafe.Pointer(p)) = uintptr(layout)
			*(*uintptr)(unsafe.Pointer(p + 8)) = size
			obj := unsafe.Pointer(p + gcHeaderSize)
			memzero(obj, size)
			if kick {
				gcKick()
			}
			return obj
		}
		gcHeapUnlock()

		if !gcCanBlock() {
			gcKick()
			return nil // atomic context: can't collect or grow here
		}
		switch attempt {
		case 0:
			GC()
		case 1:
			if !gcGrow(n * gcBlockSize) {
				GC() // another task may have freed memory meanwhile
			}
		case 2:
		default:
			return nil
		}
	}
}

func free(ptr unsafe.Pointer) {
	addr := uintptr(ptr)
	gcHeapLock()
	c := gcFind(addr)
	if c == nil {
		gcHeapUnlock()
		return
	}
	b := (addr - c.start) / gcBlockSize
	if s := c.state(b); (s == gcStateHead || s == gcStateMarked) && addr == c.start+b*gcBlockSize+gcHeaderSize {
		c.setState(b, gcStateFree)
		n := uintptr(1)
		for b+n < c.nblocks && c.state(b+n) == gcStateTail {
			c.setState(b+n, gcStateFree)
			n++
		}
		c.free += n
		gcInUse -= n * gcBlockSize
		gcFrees++
	}
	gcHeapUnlock()
}

//go:noinline
func freeTaskStack(ptr uintptr) {}

func initHeap() {}

func setHeapEnd(newHeapEnd uintptr) {}

func SetFinalizer(obj interface{}, finalizer interface{}) {}

func mallocs() uint64 { return gcMallocs }

// ---- Marking ----

// markRoots conservatively marks everything that looks like a heap pointer
// in [start, end).
func markRoots(start, end uintptr) {
	start = gcAlignUp(start, unsafe.Alignof(start))
	for p := start; p+unsafe.Sizeof(p) <= end; p += unsafe.Sizeof(p) {
		gcMark(*(*uintptr)(unsafe.Pointer(p)))
	}
}

// tinygo_kmod_markrange lets the C runtime mark parked tasks' stacks and
// registers.
//
//export tinygo_kmod_markrange
func kmodMarkRange(start, end uintptr) {
	if start < end {
		markRoots(start, end)
	}
}

func gcMark(v uintptr) {
	c := gcFind(v)
	if c == nil {
		return
	}
	b := (v - c.start) / gcBlockSize
	s := c.state(b)
	if s == gcStateFree {
		return
	}
	for s == gcStateTail {
		b--
		s = c.state(b)
	}
	if s == gcStateMarked {
		return
	}
	c.setState(b, gcStateMarked)
	if gcMarkLen == gcMarkStackEntries {
		gcMarkOverflow = true // rescanned in gcFinishMark
		return
	}
	*(*uintptr)(unsafe.Pointer(gcMarkStack + gcMarkLen*unsafe.Sizeof(v))) = c.start + b*gcBlockSize
	gcMarkLen++
}

const gcSizeFieldBits = 4 + unsafe.Sizeof(uintptr(0))/4

// gcScanObject marks what the object at block address obj points to, using
// its layout.
func gcScanObject(obj uintptr) {
	layout := *(*uintptr)(unsafe.Pointer(obj))
	size := *(*uintptr)(unsafe.Pointer(obj + 8))
	start := obj + gcHeaderSize
	word := unsafe.Sizeof(uintptr(0))
	switch {
	case layout == 0 || layout == uintptr(gclayout.Conservative):
		markRoots(start, start+size)
	case layout&1 != 0:
		elem := ((layout >> 1) & (1<<gcSizeFieldBits - 1)) * word
		mask := layout >> (1 + gcSizeFieldBits)
		if mask == 0 || elem == 0 {
			return // no pointers
		}
		for off := uintptr(0); off+elem <= size; off += elem {
			m, a := mask, start+off
			for m != 0 {
				if m&1 != 0 {
					gcMark(*(*uintptr)(unsafe.Pointer(a)))
				}
				m >>= 1
				a += word
			}
		}
	default:
		words := *(*uintptr)(unsafe.Pointer(layout))
		bitmap := layout + word
		elem := words * word
		if elem == 0 {
			return
		}
		for off := uintptr(0); off+elem <= size; off += elem {
			for w := uintptr(0); w < words; w++ {
				if *(*uint8)(unsafe.Pointer(bitmap + w/8))&(1<<(w%8)) != 0 {
					gcMark(*(*uintptr)(unsafe.Pointer(start + off + w*word)))
				}
			}
		}
	}
}

func gcDrain() {
	for gcMarkLen > 0 {
		gcMarkLen--
		gcScanObject(*(*uintptr)(unsafe.Pointer(gcMarkStack + gcMarkLen*unsafe.Sizeof(uintptr(0)))))
	}
}

func gcFinishMark() {
	gcDrain()
	for gcMarkOverflow {
		// The mark stack overflowed: some marked objects weren't scanned.
		// Scan every marked object again (idempotent) until it doesn't.
		gcMarkOverflow = false
		for i := 0; i < gcNChunks; i++ {
			c := &gcChunks[i]
			for b := uintptr(0); b < c.nblocks; b++ {
				if c.state(b) == gcStateMarked {
					gcScanObject(c.start + b*gcBlockSize)
					gcDrain()
				}
			}
		}
	}
}

// ---- Sweeping ----

func gcSweep() {
	gcInUse = 0
	gcNReleased = 0
	for i := 0; i < gcNChunks; i++ {
		c := &gcChunks[i]
		freeing := false
		used := uintptr(0)
		for b := uintptr(0); b < c.nblocks; b++ {
			switch c.state(b) {
			case gcStateHead:
				c.setState(b, gcStateFree)
				freeing = true
				gcFrees++
			case gcStateTail:
				if freeing {
					c.setState(b, gcStateFree)
				} else {
					used++
				}
			case gcStateMarked:
				c.setState(b, gcStateHead)
				freeing = false
				used++
			default:
				freeing = false
			}
		}
		c.free = c.nblocks - used
		c.hint = 0
		gcInUse += used * gcBlockSize
	}
	// Give empty chunks back, keeping at least the initial heap size.
	for i := gcNChunks - 1; i >= 0; i-- {
		c := gcChunks[i]
		if c.free != c.nblocks || gcHeapSys-c.size < gcInitialSize || gcNReleased == len(gcReleased) {
			continue
		}
		gcReleased[gcNReleased] = c.mem
		gcNReleased++
		gcHeapSys -= c.size
		copy(gcChunks[i:gcNChunks], gcChunks[i+1:gcNChunks])
		gcNChunks--
	}
}

// GC runs a collection. In atomic context it only asks the background
// collector to run.
func GC() {
	if !gcCanBlock() {
		gcKick()
		return
	}
	if !gcStopWorld() {
		return // another task just collected
	}
	gcMarkLen = 0
	gcMarkOverflow = false
	markGlobals()
	gcScanStacks()
	gcFinishMark()
	gcSweep()
	gcNumGC++
	gcNextGC = gcInUse * 2
	if gcNextGC < gcHeapSys/2 {
		gcNextGC = gcHeapSys / 2
	}
	released := gcNReleased
	gcStartWorld()

	for i := 0; i < released; i++ {
		gcChunkFree(gcReleased[i])
	}
}

// tinygo_kmod_gc_background runs in the background collector thread when
// the heap fills up: collect, then grow if there's less than 25% free.
//
//export tinygo_kmod_gc_background
func gcBackground() {
	GC()
	gcHeapLock()
	free := gcHeapSys - gcInUse
	short := free < gcHeapSys/4
	gcHeapUnlock()
	if short {
		gcGrow(gcChunkSize)
	}
	gcRefillReserve()
}

func ReadMemStats(m *MemStats) {
	gcHeapLock()
	m.HeapSys = uint64(gcHeapSys)
	m.HeapInuse = uint64(gcInUse)
	m.HeapAlloc = uint64(gcInUse)
	m.HeapIdle = uint64(gcHeapSys - gcInUse)
	m.HeapReleased = 0
	m.Sys = uint64(gcHeapSys)
	m.Mallocs = gcMallocs
	m.Frees = gcFrees
	m.TotalAlloc = gcTotalAlloc
	m.NumGC = uint32(gcNumGC)
	gcHeapUnlock()
}
