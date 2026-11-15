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
//   - the stack: from the current SP up to stackTop, which the shim sets to
//     its own frame address each time it calls into Go, so every Go frame
//     lies below it
//   - callee-saved registers: spilled by tinygo_scanCurrentStack (shim.c)

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

// enterDepth counts nested calls from C into Go. Only the outermost entry
// sets stackTop, so it stays above every Go frame on the stack.
var enterDepth int

func enter(top uintptr) {
	if enterDepth == 0 {
		stackTop = top
	}
	enterDepth++
}

func leave() {
	enterDepth--
	if lineLen > 0 {
		flushLine()
	}
}

// Entry points called from shim.c. Each takes the caller's frame address as
// the GC's stack top.

var kmodExitHook func()

// tinygo_kmod_init sets up the GC heap, runs package initializers and then
// main.main, which acts as the module's init function.
//
//export tinygo_kmod_init
func tinygo_kmod_init(top, heap, heapSize uintptr) {
	enter(top)
	heapStart = heap
	heapEnd = heap + heapSize
	initRand()
	initHeap()
	initAll()
	callMain()
	leave()
}

//export tinygo_kmod_exit
func tinygo_kmod_exit(top uintptr) {
	enter(top)
	if kmodExitHook != nil {
		kmodExitHook()
	}
	leave()
}

// SetModuleExit registers fn to run when the module is unloaded. It's exposed
// to the kernel package via go:linkname.
func kmodSetExit(fn func()) {
	kmodExitHook = fn
}
