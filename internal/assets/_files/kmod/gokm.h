/* SPDX-License-Identifier: GPL-2.0 */
/*
 * gokm C interface. Framework glue and project C files (csrc/) include this
 * to call into Go.
 *
 * Calling Go from C
 * -----------------
 * Export a Go function with //export and declare its prototype in C. Go
 * int/int64/uintptr are long/s64/unsigned long, int32/uint32 are s32/u32,
 * pointers are pointers, bool is bool. Pass strings/slices as pointer+length.
 *
 * Wrap every call into Go in GOKM_ENTER()/GOKM_LEAVE() (same function and
 * scope). Several tasks can run Go at the same time (on different CPUs):
 * there is no global lock, but Go code has to reach a "transition" (return
 * to C, a blocking call, an allocation) for the garbage collector to run.
 *   - GOKM_ENTER() may sleep (it waits for a collection in progress): process
 *     context only.
 *   - In atomic context (softirq, spinlock held, ...) use the try variant:
 *
 *         GOKM_DECLARE_ENTRY();
 *         if (GOKM_TRY_ENTER_ATOMIC()) {
 *                 go_fast_path(...);   (must not block)
 *                 GOKM_LEAVE();
 *         } else {
 *                 defer the work to process context
 *         }
 *
 *   - Pointers into Go memory returned by Go stay valid until GOKM_LEAVE()
 *     (no collection can run while this task is in Go).
 *   - Kernel memory must not keep Go pointers: use a kernel.Handle.
 *
 * Blocking calls made on behalf of Go
 * -----------------------------------
 * C code called *from* Go that may block (sleep, wait for another task,
 * unregister a device, ...) must let the collector run meanwhile:
 *
 *     GOKM_PARK();
 *     unregister_netdev(dev);   (may call back into Go: that's fine)
 *     GOKM_UNPARK();
 *
 * Don't touch Go memory between PARK and UNPARK.
 *
 * Calling C from Go
 * -----------------
 * Declare a body-less Go function with //export naming the C function.
 */
#ifndef GOKM_H
#define GOKM_H

#include <linux/types.h>
#include <linux/compiler.h>
#include <linux/list.h>

struct task_struct;

/* Room for a Go task.Task (checked against the Go side at load). */
#define GOKM_TASK_BYTES 256

/*
 * Per-task Go state. It lives in the frame of the task's outermost
 * GOKM_ENTER (or in per-CPU memory for atomic entries), so it's within the
 * stack range the collector scans.
 */
struct gokm_thread {
	struct list_head list;		/* gokm_threads */
	struct task_struct *task;
	unsigned long top;		/* stack top: frame of the outermost entry */
	unsigned long park_sp;		/* lowest Go-relevant address while parked */
	unsigned long regs[6];		/* callee-saved registers spilled at park */
	bool active;			/* running Go (counted by the entry gate) */
	bool goroutine;			/* a kthread started by a go statement */
	bool atomic;			/* per-CPU record for atomic entries */
	u8 gotask[GOKM_TASK_BYTES] __aligned(16);	/* Go's task.Task */
};

enum {
	GOKM_ENTRY_OUTER,	/* first entry on this task */
	GOKM_ENTRY_NESTED,	/* Go -> C -> Go */
	GOKM_ENTRY_REENTRY,	/* callback while parked */
	GOKM_ENTRY_ATOMIC,	/* atomic-context entry (per-CPU record) */
	GOKM_ENTRY_FAILED,	/* atomic try-entry that didn't enter */
};

struct gokm_entry {
	struct gokm_thread thread;	/* used by the outermost entry */
	struct gokm_thread *t;
	int kind;
};

struct gokm_park_state {
	struct gokm_thread *t;
	unsigned long saved_sp;
	unsigned long saved_regs[6];
};

void gokm_enter(struct gokm_entry *e, unsigned long stack_top);
bool gokm_enter_atomic(struct gokm_entry *e, unsigned long stack_top);
void gokm_leave(struct gokm_entry *e);
void gokm_park(struct gokm_park_state *p);
void gokm_unpark(struct gokm_park_state *p);

#define GOKM_FRAME() ((unsigned long)__builtin_frame_address(0))

#define GOKM_DECLARE_ENTRY() struct gokm_entry __gokm_entry
#define GOKM_ENTER()							\
	GOKM_DECLARE_ENTRY();						\
	gokm_enter(&__gokm_entry, GOKM_FRAME())
#define GOKM_TRY_ENTER_ATOMIC() gokm_enter_atomic(&__gokm_entry, GOKM_FRAME())
#define GOKM_LEAVE() gokm_leave(&__gokm_entry)

#define GOKM_PARK()							\
	struct gokm_park_state __gokm_park;				\
	gokm_park(&__gokm_park)
#define GOKM_UNPARK() gokm_unpark(&__gokm_park)

/* ---- Internal: C runtime <-> Go runtime ------------------------------ */

/* Implemented in Go (overlay/src/runtime, overlay/src/internal/task). */
int tinygo_kmod_init(unsigned long heap_size, unsigned long heap_max);
void tinygo_kmod_exit(void);
void tinygo_kmod_markrange(unsigned long start, unsigned long end);
void tinygo_kmod_gc_background(void);
unsigned long tinygo_kmod_task_size(void);
void __noreturn tinygo_kmod_panic_blocking(void);	/* a Go panic */
void tinygo_scanstack(unsigned long sp);	/* TinyGo's gc_stack_threads.go */

/* threads.c */
void *gokm_current_task(void);
unsigned long gokm_current_stack_top(void);
bool gokm_can_block(void);
bool gokm_stop_world(void);
void gokm_start_world(void);
void gokm_scan_stacks(void);
long gokm_go_start(unsigned long fn, void *args);
void gokm_goexit(void);
long gokm_num_goroutines(void);
void gokm_threads_init(void);
void gokm_threads_stop(void);
bool gokm_stopping(void);
void tinygo_futex_wait(u32 *addr, u32 cmp);
void tinygo_futex_wait_timeout(u32 *addr, u32 cmp, u64 timeout_ns);
void tinygo_futex_wake(u32 *addr);
void tinygo_futex_wake_all(u32 *addr);

/* heap.c */
unsigned long gokm_chunk_alloc(unsigned long size);
void gokm_chunk_free(unsigned long chunk);
void gokm_heap_lock(void);
void gokm_heap_unlock(void);
void gokm_gc_kick(void);
int gokm_gc_worker_start(void);
void gokm_gc_worker_stop(void);
void gokm_chunks_free_all(void);
void tinygo_scanCurrentStack(void);

/* shim.c */
void *gokm_alloc(size_t size);
void gokm_free(void *ptr);
void gokm_print(const char *buf, long len);
void __noreturn gokm_abort(void);
s64 gokm_ktime_ns(void);
void gokm_msleep(unsigned int ms);
unsigned long gokm_irq_save(void);
void gokm_irq_restore(unsigned long flags);
void gokm_bh_disable(void);
void gokm_bh_enable(void);
void gokm_cpu_relax(void);
long gokm_num_cpus(void);
void gokm_yield(void);

#endif
