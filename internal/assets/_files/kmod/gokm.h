/* SPDX-License-Identifier: GPL-2.0 */
/*
 * gokm C interface. Project C files (in csrc/) include this to call into Go.
 *
 * Calling Go from C
 * -----------------
 * Export a Go function with //export and declare its prototype in C:
 *
 *     //export myfs_lookup                        (Go)
 *     func myfsLookup(dir uint64, name *byte, n int) uint64 { ... }
 *
 *     u64 myfs_lookup(u64 dir, const char *name, long n);   (C)
 *
 * Go types map to C as: int/int64/uintptr -> long/s64/unsigned long,
 * int32/uint32 -> s32/u32, *T/unsafe.Pointer -> pointer, bool -> bool.
 * Strings, slices, maps and interfaces can't cross; pass pointer + length.
 *
 * Every call into Go must be wrapped in GOKM_ENTER()/GOKM_LEAVE(), in the
 * same C function and scope:
 *
 *     GOKM_ENTER();
 *     ino = myfs_lookup(dir, name, len);
 *     GOKM_LEAVE();
 *
 * This takes the module's Go lock (Go code is single-threaded) and tells the
 * garbage collector where this stack's Go frames start. Rules:
 *   - Process context only: GOKM_ENTER() can sleep. Never use it in
 *     interrupt/softirq context, RCU callbacks or with a spinlock held.
 *   - Lock order: take kernel locks before GOKM_ENTER(), never after.
 *   - Pointers into Go memory returned by Go are only valid until
 *     GOKM_LEAVE(); after that the GC may free them.
 *   - Kernel memory that must refer to Go objects should store a
 *     kernel.Handle (an integer), not a Go pointer.
 *
 * Blocking calls made on behalf of Go
 * -----------------------------------
 * C code called *from* Go that makes a kernel call which may block for long,
 * or wait for another task that needs to run Go (unregistering a device,
 * flushing work, sleeping), must release the Go lock around it:
 *
 *     GOKM_PARK();
 *     unregister_netdev(dev);   (may call back into Go: that's fine)
 *     GOKM_UNPARK();
 *
 * Don't touch Go memory between PARK and UNPARK.
 *
 * Calling C from Go
 * -----------------
 * Declare a body-less Go function with //export naming the C function:
 *
 *     //export myfs_register
 *     func myfsRegister() int
 */
#ifndef GOKM_H
#define GOKM_H

#include <linux/types.h>
#include <linux/compiler.h>
#include <linux/list.h>

struct task_struct;

/* Per-task Go state, see shim.c. */
struct gokm_thread {
	struct list_head list;
	struct task_struct *task;
	unsigned long top;	/* stack top: frame of the outermost GOKM_ENTER */
	unsigned long park_sp;	/* lowest Go-relevant address while parked */
	unsigned long regs[6];	/* callee-saved registers spilled at park */
	bool running;		/* holds the Go lock */
};

enum { GOKM_ENTRY_OUTER, GOKM_ENTRY_NESTED, GOKM_ENTRY_REENTRY };

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
void gokm_leave(struct gokm_entry *e);
void gokm_park(struct gokm_park_state *p);
void gokm_unpark(struct gokm_park_state *p);

#define GOKM_ENTER()							\
	struct gokm_entry __gokm_entry;					\
	gokm_enter(&__gokm_entry, (unsigned long)__builtin_frame_address(0))
#define GOKM_LEAVE() gokm_leave(&__gokm_entry)

#define GOKM_PARK()							\
	struct gokm_park_state __gokm_park;				\
	gokm_park(&__gokm_park)
#define GOKM_UNPARK() gokm_unpark(&__gokm_park)

/* ---- Internal: shim.c <-> Go runtime --------------------------------- */

/* Implemented in Go (overlay/src/runtime/runtime_linuxkernel.go). */
void tinygo_kmod_set_stack_top(unsigned long stack_top);
void tinygo_kmod_flush(void);
void tinygo_kmod_markrange(unsigned long start, unsigned long end);
int tinygo_kmod_init(unsigned long heap, unsigned long heap_size);
void tinygo_kmod_exit(void);

/* Implemented in Go (TinyGo's gc_stack_raw.go). */
void tinygo_scanstack(unsigned long sp);

/* Implemented in shim.c, called from Go. */
void tinygo_scanCurrentStack(void);
void *gokm_alloc(size_t size);
void gokm_free(void *ptr);
void gokm_print(const char *buf, long len);
void __noreturn gokm_abort(void);
s64 gokm_ktime_ns(void);
void gokm_msleep(unsigned int ms);
unsigned long gokm_irq_save(void);
void gokm_irq_restore(unsigned long flags);

#endif
