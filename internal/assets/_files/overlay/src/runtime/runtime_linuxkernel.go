//go:build linuxkernel

// Runtime platform support for running TinyGo code inside a Linux kernel
// module. gokm adds this file to its mirror of TinyGo's src/runtime.
//
// The kernel-side half lives in kmod/shim.c: every function below declared
// with //export and no body is implemented there.
//
// Memory: the target uses TinyGo's conservative mark/sweep GC (gc_blocks.go).
// The heap is a single vzalloc() region the shim allocates at module load
// (size: module parameter heap_size) and frees at unload. The GC scans:
//   - globals: compiler-generated root tables (gc_globals_blocks.go)
//   - the stack: from the current SP up to stackTop, the frame address of
//     the C function that first entered Go on this task, so every Go frame
//     lies below it
//   - callee-saved registers: spilled by tinygo_scanCurrentStack (shim.c)
//   - parked tasks (blocked in a kernel call made for Go, Go lock released):
//     their stacks and spilled registers, via tinygo_kmod_markrange

package runtime

const zeroSizeAllocPtr uintptr = 16

// Implemented in shim.c.
//
//export gokm_print
func kmodPrint(buf *byte, n int)

//export gokm_abort
func kmodAbort()

//export gokm_ktime_ns
func kmodKtimeNs() int64

//export gokm_msleep
func kmodMsleep(ms uint32)

// putchar is used by the runtime's println/panic output. Bytes are collected
// into a line buffer and handed to printk one full line at a time.
var lineBuf [256]byte
var lineLen int

func putchar(c byte) {
	if c == '\n' {
		flushLine()
		return
	}
	lineBuf[lineLen] = c
	lineLen++
	if lineLen == len(lineBuf) {
		flushLine()
	}
}

func flushLine() {
	kmodPrint(&lineBuf[0], lineLen)
	lineLen = 0
}

func getchar() byte { return 0 }

func buffered() int { return 0 }

// abort is called on a fatal runtime error (after a panic message has been
// printed). We flush any partial line and let the kernel BUG(). gokm_abort
// is marked noreturn in the IR by gokm's IR fixups.
func abort() {
	if lineLen > 0 {
		flushLine()
	}
	kmodAbort()
}

func exit(code int) {
	abort()
}

func mainReturnExit() {}

//go:linkname procPin sync/atomic.runtime_procPin
func procPin() {}

//go:linkname procUnpin sync/atomic.runtime_procUnpin
func procUnpin() {}

// Time: ticks are nanoseconds of ktime_get_ns() (monotonic).
func ticks() timeUnit { return timeUnit(kmodKtimeNs()) }

func nanosecondsToTicks(ns int64) timeUnit { return timeUnit(ns) }

func ticksToNanoseconds(t timeUnit) int64 { return int64(t) }

func sleepTicks(d timeUnit) {
	ms := int64(d) / 1000000
	if ms <= 0 {
		ms = 1
	}
	kmodMsleep(uint32(ms))
}

// GC heap and stack bounds (normally provided by the linker on baremetal
// targets; gokm excludes baremetal_memory.go for this target).
var heapStart, heapEnd uintptr
var stackTop uintptr

// The heap can't grow: it's one fixed vzalloc() region. When it's full and a
// collection doesn't free enough, allocation fails with "out of memory".
func growHeap() bool { return false }

// Hooks for shim.c's Go lock (see gokm_enter/gokm_park there). Several
// tasks can have Go frames on their stacks, but only the one holding the Go
// lock runs Go code; the shim sets stackTop to that task's stack top each time
// a task takes the lock.

//export tinygo_kmod_set_stack_top
func kmodSetStackTop(top uintptr) {
	stackTop = top
}

//export tinygo_kmod_flush
func kmodFlush() {
	if lineLen > 0 {
		flushLine()
	}
}

// tinygo_kmod_markrange lets the shim mark roots outside the running stack:
// the stacks and saved registers of parked tasks.
//
//export tinygo_kmod_markrange
func kmodMarkRange(start, end uintptr) {
	if start < end {
		markRoots(start, end)
	}
}

// Entry points called from shim.c, between gokm_enter and gokm_leave.

var kmodExitHooks []func()

// kmodInitErr is the (positive) errno module init fails with, set through
// kernel.FailLoad.
var kmodInitErr int

// tinygo_kmod_init sets up the GC heap, runs package initializers and then
// main.main, which acts as the module's init function. It returns 0 or a
// negative errno for insmod.
//
//export tinygo_kmod_init
func tinygo_kmod_init(heap, heapSize uintptr) int {
	heapStart = heap
	heapEnd = heap + heapSize
	initRand()
	initHeap()
	initAll()
	callMain()
	return -kmodInitErr
}

//export tinygo_kmod_exit
func tinygo_kmod_exit() {
	// Last registered runs first (like defer).
	for i := len(kmodExitHooks) - 1; i >= 0; i-- {
		kmodExitHooks[i]()
	}
	kmodExitHooks = nil
}

// kmodAddExit registers fn to run when the module is unloaded. It's exposed
// to the kernel package via go:linkname.
func kmodAddExit(fn func()) {
	kmodExitHooks = append(kmodExitHooks, fn)
}

// kmodSetInitError makes module init fail with the given positive errno once
// main returns. Exposed to the kernel package via go:linkname.
func kmodSetInitError(errno int) {
	kmodInitErr = errno
}
