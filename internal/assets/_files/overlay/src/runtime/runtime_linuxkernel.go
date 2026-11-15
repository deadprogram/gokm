//go:build linuxkernel

// Runtime platform support for running TinyGo code inside a Linux kernel
// module. gokm adds this file to its mirror of TinyGo's src/runtime.
//
// The kernel-side half lives in kmod/shim.c: every function below declared
// with //export and no body is implemented there.
//
// Scheduling: TinyGo's threads scheduler, with goroutines as kernel threads
// (internal/task/task_linuxkernel.go, kmod/threads.c).
// Memory: gokm's own collector over growable kernel memory
// (gc_linuxkernel.go, kmod/heap.c).

package runtime

import (
	"internal/task"
	"unsafe"
)

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

// putchar is used by the runtime's println/panic output (serialized by the
// runtime's print lock). Bytes are collected into a line buffer and handed
// to printk one full line at a time.
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

// stackTop is only used by the threads scheduler's run(), which gokm
// doesn't call (stacks are tracked per task in kmod/threads.c).
var stackTop uintptr

//export tinygo_kmod_task_size
func kmodTaskSize() uintptr {
	return unsafe.Sizeof(task.Task{})
}

// tinygo_kmod_panic_blocking is called by the C runtime when Go code tries
// to block (sleep, wait on a mutex or channel, park) in atomic context.
//
//export tinygo_kmod_panic_blocking
func kmodPanicBlocking() {
	runtimePanic(plainError("blocking operation in atomic context (Go mutex, channel, sleep or blocking kernel call)"))
}

//export gokm_goexit
func kmodGoexit()

// Entry points called from shim.c, between gokm_enter and gokm_leave.

var (
	kmodExitHooks     []func()
	kmodExitHooksLock task.Mutex
)

// kmodInitErr is the (positive) errno module init fails with, set through
// kernel.FailLoad.
var kmodInitErr int

// tinygo_kmod_init sets up the GC heap, runs package initializers and then
// main.main, which acts as the module's init function. It returns 0 or a
// negative errno for insmod.
//
//export tinygo_kmod_init
func tinygo_kmod_init(heapSize, heapMax uintptr) int {
	if !gcInit(heapSize, heapMax) {
		return -12 // ENOMEM
	}
	initRand()
	initAll()
	callMain()
	return -kmodInitErr
}

//export tinygo_kmod_exit
func tinygo_kmod_exit() {
	// Last registered runs first (like defer).
	for {
		kmodExitHooksLock.Lock()
		n := len(kmodExitHooks)
		if n == 0 {
			kmodExitHooksLock.Unlock()
			return
		}
		fn := kmodExitHooks[n-1]
		kmodExitHooks = kmodExitHooks[:n-1]
		kmodExitHooksLock.Unlock()
		fn()
	}
}

// kmodAddExit registers fn to run when the module is unloaded. It's exposed
// to the kernel package via go:linkname.
func kmodAddExit(fn func()) {
	kmodExitHooksLock.Lock()
	kmodExitHooks = append(kmodExitHooks, fn)
	kmodExitHooksLock.Unlock()
}

// kmodSetInitError makes module init fail with the given positive errno once
// main returns. Exposed to the kernel package via go:linkname.
func kmodSetInitError(errno int) {
	kmodInitErr = errno
}
