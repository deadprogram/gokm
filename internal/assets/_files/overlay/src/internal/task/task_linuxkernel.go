//go:build scheduler.threads && linuxkernel

// Goroutines for Go code in a Linux kernel module. This replaces TinyGo's
// task_threads.go (pthreads + signals): each goroutine is a kernel thread,
// the current task is found from the kernel's `current`, waits use the
// futex in kmod/threads.c (which lets the GC run meanwhile), and stopping
// the world is done by the entry gate in kmod/threads.c instead of signals.
//
// Tasks that enter Go from a kernel callback (not started with `go`) get a
// task.Task too: it lives in the C thread record (struct gokm_thread) on
// that task's stack.

package task

import "unsafe"

type threadID uintptr

// Scheduler-specific state.
type state struct {
	// Semaphore to pause/resume the task.
	pauseSem Semaphore
}

//export gokm_current_task
func gokmCurrentTask() unsafe.Pointer

//export gokm_current_stack_top
func gokmCurrentStackTop() uintptr

//export gokm_go_start
func gokmGoStart(fn uintptr, args unsafe.Pointer) int

//export gokm_goexit
func gokmGoexit()

//export gokm_num_goroutines
func gokmNumGoroutines() int

//export gokm_num_cpus
func gokmNumCPUs() int

//export gokm_yield
func gokmYield()

func OnSystemStack() bool {
	return false
}

// Init is called by the threads scheduler's run(), which gokm doesn't use.
func Init(sp uintptr) {}

// Current returns the task of the code running now.
func Current() *Task {
	t := (*Task)(gokmCurrentTask())
	if t == nil {
		runtimeFatal("Go code running outside of a gokm entry")
	}
	return t
}

// NumGoroutine counts goroutines started with `go`, plus the caller.
func NumGoroutine() int {
	return gokmNumGoroutines() + 1
}

// Pause pauses the current task until it is resumed.
func Pause() {
	Current().state.pauseSem.Wait()
}

// Resume resumes the given task (possibly before it pauses).
func (t *Task) Resume() {
	t.state.pauseSem.Post()
}

// start runs fn(args) in a new kernel thread.
func start(fn uintptr, args unsafe.Pointer, stackSize uintptr) {
	if rc := gokmGoStart(fn, args); rc != 0 {
		runtimeFatal("could not start goroutine (kthread_create failed or module unloading)")
	}
}

// Goexit exits the current goroutine.
func Goexit() {
	gokmGoexit()
}

func CoroExit(next *Task) {
	next.Resume()
	gokmGoexit()
}

// Yield lets other kernel tasks run.
func Yield() {
	gokmYield()
}

// StackTop is the highest address of the current task's Go stack.
func StackTop() uintptr {
	return gokmCurrentStackTop()
}

func NumCPU() int {
	return gokmNumCPUs()
}

// GCStopWorldAndScan and GCResumeWorld are used by TinyGo's block-based
// collectors; gokm's collector (runtime/gc_linuxkernel.go) stops the world
// through kmod/threads.c directly.
func GCStopWorldAndScan() {
	runtimeFatal("GCStopWorldAndScan: not used with gokm's collector")
}

func GCResumeWorld() {}

// Scan globals, implemented in the runtime package (gc_stack_threads.go).
func gcScanGlobals()
