// SPDX-License-Identifier: GPL-2.0
/*
 * gokm threads: how kernel tasks run Go code concurrently, and how the
 * garbage collector stops them.
 *
 * Entry gate. Any number of tasks can run Go at once. gokm_active counts the
 * tasks currently running Go code. A collection ("stop the world") sets
 * gokm_gc_pending and waits for gokm_active to drop to zero: a task stops
 * counting when it returns to C, parks around a blocking call, or blocks
 * in a Go wait (mutex, channel, sleep), all of which go through here. Tasks
 * entering Go while a collection is pending wait for it to finish
 * (process context) or fail to enter (atomic context: the caller defers
 * the work instead; spinning there could deadlock against the collector).
 *
 * Thread records. Every task with Go frames on its stack has a
 * struct gokm_thread in gokm_threads (hashed by task), living in the frame
 * of its outermost entry, so it's inside the stack range the collector
 * scans (it holds the
 * Go task.Task, whose fields may point into the heap). A task not running
 * Go but with Go frames is *parked*: it recorded its stack pointer and
 * callee-saved registers, and the collector scans from there to the top.
 * Atomic-context entries (softirq, preemption disabled) use a per-CPU
 * record instead; they never park, so the collector never needs to scan
 * them (it waits for them to finish).
 *
 * Goroutines are kthreads. Their blocking waits (futex) park. At module
 * unload, goroutines blocked in waits or sleeps are made to exit.
 */
#define pr_fmt(fmt) KBUILD_MODNAME ": " fmt

#include <linux/module.h>
#include <linux/kernel.h>
#include <linux/sched.h>
#include <linux/sched/task.h>
#include <linux/kthread.h>
#include <linux/wait.h>
#include <linux/spinlock.h>
#include <linux/list.h>
#include <linux/hash.h>
#include <linux/percpu.h>
#include <linux/preempt.h>
#include <linux/hardirq.h>
#include <linux/jiffies.h>
#include <linux/completion.h>
#include <linux/atomic.h>
#include <linux/slab.h>
#include <linux/string.h>
#include <linux/objtool.h>

#include "gokm.h"

/* ---- Entry gate ------------------------------------------------------- */

static atomic_t gokm_active = ATOMIC_INIT(0);
static int gokm_gc_pending;
static DECLARE_WAIT_QUEUE_HEAD(gokm_gate_wq);	/* tasks waiting for a GC to end */
static DECLARE_WAIT_QUEUE_HEAD(gokm_stw_wq);	/* the GC waiting for tasks to stop */

static void gate_dec(void)
{
	smp_mb__before_atomic();
	if (atomic_dec_return(&gokm_active) == 0 && READ_ONCE(gokm_gc_pending))
		wake_up(&gokm_stw_wq);
}

static void gate_enter(struct gokm_thread *t)
{
	for (;;) {
		wait_event(gokm_gate_wq, !READ_ONCE(gokm_gc_pending));
		atomic_inc(&gokm_active);
		smp_mb__after_atomic();
		if (!READ_ONCE(gokm_gc_pending))
			break;
		gate_dec();
	}
	t->active = true;
}

static bool gate_try_enter(void)
{
	if (READ_ONCE(gokm_gc_pending))
		return false;
	atomic_inc(&gokm_active);
	smp_mb__after_atomic();
	if (READ_ONCE(gokm_gc_pending)) {
		gate_dec();
		return false;
	}
	return true;
}

static void gate_exit(struct gokm_thread *t)
{
	t->active = false;
	gate_dec();
}

/* ---- Thread records --------------------------------------------------- */

#define GOKM_THREAD_BITS 8

/* Records hashed by task, so concurrent tasks don't share a lock. */
static struct gokm_thread_bucket {
	spinlock_t lock;
	struct list_head threads;
} gokm_threads[1 << GOKM_THREAD_BITS];
static DEFINE_PER_CPU(struct gokm_thread, gokm_atomic_thread);

static struct gokm_thread_bucket *thread_bucket(struct task_struct *tsk)
{
	return &gokm_threads[hash_ptr(tsk, GOKM_THREAD_BITS)];
}

static struct gokm_thread *find_thread(struct task_struct *tsk)
{
	struct gokm_thread_bucket *b = thread_bucket(tsk);
	struct gokm_thread *t, *found = NULL;
	unsigned long flags;

	spin_lock_irqsave(&b->lock, flags);
	list_for_each_entry(t, &b->threads, list) {
		if (t->task == tsk) {
			found = t;
			break;
		}
	}
	spin_unlock_irqrestore(&b->lock, flags);
	return found;
}

static void add_thread(struct gokm_thread *t)
{
	struct gokm_thread_bucket *b = thread_bucket(t->task);
	unsigned long flags;

	spin_lock_irqsave(&b->lock, flags);
	list_add(&t->list, &b->threads);
	spin_unlock_irqrestore(&b->lock, flags);
}

static void del_thread(struct gokm_thread *t)
{
	struct gokm_thread_bucket *b = thread_bucket(t->task);
	unsigned long flags;

	spin_lock_irqsave(&b->lock, flags);
	list_del(&t->list);
	spin_unlock_irqrestore(&b->lock, flags);
}

/* The record of the code running right now: an atomic entry on this CPU,
 * or the current task's. */
static struct gokm_thread *current_thread(void)
{
	struct gokm_thread *a = raw_cpu_ptr(&gokm_atomic_thread);

	if (READ_ONCE(a->active) && (!in_task() || a->task == current))
		return a;
	return find_thread(current);
}

void *gokm_current_task(void)
{
	struct gokm_thread *t = current_thread();

	return t ? t->gotask : NULL;
}

unsigned long gokm_current_stack_top(void)
{
	struct gokm_thread *t = current_thread();

	return t ? t->top : 0;
}

/* Whether the code running now may sleep (park, wait, run the collector). */
bool gokm_can_block(void)
{
	struct gokm_thread *a = raw_cpu_ptr(&gokm_atomic_thread);

	if (!in_task() || !preemptible())
		return false;
	return !(READ_ONCE(a->active) && a->task == current);
}

static void init_record(struct gokm_thread *t, unsigned long top)
{
	memset(t, 0, sizeof(*t));
	t->task = current;
	t->top = top;
}

void gokm_enter(struct gokm_entry *e, unsigned long top)
{
	struct gokm_thread *t = current_thread();

	if (t && t->active) {
		e->kind = GOKM_ENTRY_NESTED;	/* Go -> C -> Go */
	} else if (t) {
		might_sleep();
		gate_enter(t);			/* callback while parked */
		e->kind = GOKM_ENTRY_REENTRY;
	} else {
		might_sleep();
		t = &e->thread;
		init_record(t, top);
		gate_enter(t);
		add_thread(t);	/* only while active: the GC never sees it change */
		e->kind = GOKM_ENTRY_OUTER;
	}
	e->t = t;
}

bool gokm_enter_atomic(struct gokm_entry *e, unsigned long top)
{
	struct gokm_thread *t = current_thread();

	e->kind = GOKM_ENTRY_FAILED;
	if (t && t->active) {
		e->kind = GOKM_ENTRY_NESTED;
	} else if (t) {
		if (!gate_try_enter())
			return false;
		t->active = true;
		e->kind = GOKM_ENTRY_REENTRY;
	} else if (!in_task() || !preemptible()) {
		t = this_cpu_ptr(&gokm_atomic_thread);
		if (WARN_ON_ONCE(t->active) || !gate_try_enter())
			return false;
		init_record(t, top);
		t->atomic = true;
		WRITE_ONCE(t->active, true);
		e->kind = GOKM_ENTRY_ATOMIC;
	} else {
		t = &e->thread;
		init_record(t, top);
		if (!gate_try_enter())
			return false;
		t->active = true;
		add_thread(t);
		e->kind = GOKM_ENTRY_OUTER;
	}
	e->t = t;
	return true;
}

void gokm_leave(struct gokm_entry *e)
{
	struct gokm_thread *t = e->t;

	switch (e->kind) {
	case GOKM_ENTRY_OUTER:
		del_thread(t);	/* still active: the GC isn't scanning */
		gate_exit(t);
		break;
	case GOKM_ENTRY_REENTRY:
		gate_exit(t);	/* back to parked */
		break;
	case GOKM_ENTRY_ATOMIC:
		WRITE_ONCE(t->active, false);
		gate_dec();
		break;
	}
}

/* ---- Parking ---------------------------------------------------------- */

#define SPILL_REGS(regs)						\
	asm volatile("movq %%rbx,  0(%0)\n\t"				\
		     "movq %%rbp,  8(%0)\n\t"				\
		     "movq %%r12, 16(%0)\n\t"				\
		     "movq %%r13, 24(%0)\n\t"				\
		     "movq %%r14, 32(%0)\n\t"				\
		     "movq %%r15, 40(%0)\n\t"				\
		     : : "r"(regs) : "memory")

void gokm_park(struct gokm_park_state *p)
{
	struct gokm_thread *t = current_thread();

	p->t = NULL;
	if (WARN_ONCE(!t || !t->active, "gokm: GOKM_PARK outside of Go\n"))
		return;
	if (t->atomic || !gokm_can_block()) {
		/* A blocking call in atomic context: turn it into a Go panic. */
		tinygo_kmod_panic_blocking();
		return;
	}
	p->t = t;
	p->saved_sp = t->park_sp;
	memcpy(p->saved_regs, t->regs, sizeof(t->regs));
	SPILL_REGS(t->regs);
	t->park_sp = GOKM_FRAME();
	gate_exit(t);
}
STACK_FRAME_NON_STANDARD(gokm_park);

void gokm_unpark(struct gokm_park_state *p)
{
	struct gokm_thread *t = p->t;

	if (!t)
		return;
	gate_enter(t);
	t->park_sp = p->saved_sp;
	memcpy(t->regs, p->saved_regs, sizeof(t->regs));
}

/* ---- Stopping the world ----------------------------------------------- */

/*
 * Called by the collector (an active task, process context). Returns false
 * if another task was already collecting: we parked until it finished, and
 * the caller should just retry what it was doing.
 */
bool gokm_stop_world(void)
{
	if (cmpxchg(&gokm_gc_pending, 0, 1) != 0) {
		GOKM_PARK();
		GOKM_UNPARK();	/* waits for the other collection to end */
		return false;
	}
	/* Don't count ourselves, then wait for everyone else to leave Go. */
	atomic_dec(&gokm_active);
	smp_mb__after_atomic();
	wait_event(gokm_stw_wq, atomic_read(&gokm_active) == 0);
	return true;
}

void gokm_start_world(void)
{
	atomic_inc(&gokm_active);
	smp_mb__before_atomic();
	WRITE_ONCE(gokm_gc_pending, 0);
	smp_mb();
	wake_up_all(&gokm_gate_wq);
}

/*
 * Mark roots on every stack with Go frames, with the world stopped: this
 * task's (from here, with its callee-saved registers spilled) and every
 * parked task's (from its park point, plus its spilled registers).
 */
void gokm_scan_stacks(void)
{
	struct gokm_thread *t;
	unsigned long regs[6];
	int i;

	SPILL_REGS(regs);
	tinygo_scanstack((unsigned long)regs);

	/* Other tasks can't change the lists: they'd need to be active. */
	for (i = 0; i < ARRAY_SIZE(gokm_threads); i++) {
		list_for_each_entry(t, &gokm_threads[i].threads, list) {
			if (t->active)
				continue;	/* us */
			tinygo_kmod_markrange((unsigned long)t->regs,
					      (unsigned long)(t->regs + ARRAY_SIZE(t->regs)));
			if (t->park_sp && t->park_sp < t->top)
				tinygo_kmod_markrange(t->park_sp, t->top);
		}
	}
	asm volatile("" : : "r"(regs) : "memory");	/* keep regs live */
}
STACK_FRAME_NON_STANDARD(gokm_scan_stacks);

/* ---- Goroutines (kthreads) --------------------------------------------- */

struct gokm_go_start {
	void (*fn)(void *);
	void *args;
	struct completion started;
};

struct gokm_goroutine {
	struct list_head node;
	struct task_struct *task;
};

static LIST_HEAD(gokm_goroutine_list);
static DEFINE_SPINLOCK(gokm_goroutine_lock);
static atomic_t gokm_ngoroutines = ATOMIC_INIT(0);
static bool gokm_stop;
static DECLARE_WAIT_QUEUE_HEAD(gokm_stop_wq);

bool gokm_stopping(void)
{
	return READ_ONCE(gokm_stop);
}

long gokm_num_goroutines(void)
{
	return atomic_read(&gokm_ngoroutines);
}

static void goroutine_done(void)
{
	atomic_dec(&gokm_ngoroutines);
	wake_up_all(&gokm_stop_wq);
}

static int gokm_goroutine_main(void *arg)
{
	struct gokm_go_start *s = arg;
	void (*fn)(void *) = s->fn;
	void *args = s->args;	/* kept alive by this frame from now on */

	GOKM_ENTER();
	__gokm_entry.t->goroutine = true;
	complete(&s->started);	/* s lives in the creator's frame */
	fn(args);
	GOKM_LEAVE();
	goroutine_done();
	return 0;
}

/* Exit the current goroutine from deep inside Go (Goexit, or unload).
 * kthread_exit() isn't exported; this completes a dummy completion first. */
static DECLARE_COMPLETION(gokm_bail_done);

static void __noreturn goroutine_bail(struct gokm_thread *t)
{
	del_thread(t);
	if (t->active)
		gate_exit(t);
	goroutine_done();
	kthread_complete_and_exit(&gokm_bail_done, 0);
}

void __noreturn gokm_goexit(void)
{
	struct gokm_thread *t = current_thread();

	if (!t || !t->goroutine) {
		pr_err("runtime.Goexit outside of a goroutine\n");
		gokm_abort();
	}
	goroutine_bail(t);
}

static void reap_goroutines(void)
{
	struct gokm_goroutine *g, *tmp;
	LIST_HEAD(dead);

	spin_lock(&gokm_goroutine_lock);
	list_for_each_entry_safe(g, tmp, &gokm_goroutine_list, node) {
		if (READ_ONCE(g->task->exit_state))
			list_move(&g->node, &dead);
	}
	spin_unlock(&gokm_goroutine_lock);
	list_for_each_entry_safe(g, tmp, &dead, node) {
		put_task_struct(g->task);
		kfree(g);
	}
}

long gokm_go_start(unsigned long fn, void *args)
{
	struct gokm_go_start s = { .fn = (void *)fn, .args = args };
	struct gokm_goroutine *g;
	struct task_struct *tsk;
	long err = 0;

	if (!gokm_can_block()) {
		tinygo_kmod_panic_blocking();
		return -EWOULDBLOCK;
	}
	if (gokm_stopping())
		return -ESHUTDOWN;
	init_completion(&s.started);

	/* args stays referenced from our caller's (Go) frame while parked. */
	GOKM_PARK();
	reap_goroutines();
	g = kmalloc(sizeof(*g), GFP_KERNEL);
	if (!g) {
		err = -ENOMEM;
		goto out;
	}
	atomic_inc(&gokm_ngoroutines);
	tsk = kthread_create(gokm_goroutine_main, &s, "%s-go", KBUILD_MODNAME);
	if (IS_ERR(tsk)) {
		atomic_dec(&gokm_ngoroutines);
		kfree(g);
		err = PTR_ERR(tsk);
		goto out;
	}
	get_task_struct(tsk);
	g->task = tsk;
	spin_lock(&gokm_goroutine_lock);
	list_add(&g->node, &gokm_goroutine_list);
	spin_unlock(&gokm_goroutine_lock);
	wake_up_process(tsk);
	wait_for_completion(&s.started);
out:
	GOKM_UNPARK();
	return err;
}

/* ---- Futex (Go's mutexes, channels, sleeps wait on it) ----------------- */

#define GOKM_FUTEX_BITS 8

struct gokm_futex_waiter {
	struct list_head node;
	u32 *addr;
	struct task_struct *task;
	bool woken;
};

static struct gokm_futex_bucket {
	spinlock_t lock;
	struct list_head waiters;
} gokm_futex[1 << GOKM_FUTEX_BITS];

static struct gokm_futex_bucket *futex_bucket(u32 *addr)
{
	return &gokm_futex[hash_ptr(addr, GOKM_FUTEX_BITS)];
}

static void futex_wait(u32 *addr, u32 cmp, long timeout)
{
	struct gokm_futex_bucket *b = futex_bucket(addr);
	struct gokm_futex_waiter w = { .addr = addr, .task = current };
	struct gokm_thread *t;

	if (!gokm_can_block()) {
		tinygo_kmod_panic_blocking();
		return;
	}
	t = current_thread();
	if (gokm_stopping() && t && t->goroutine)
		goroutine_bail(t);

	spin_lock_bh(&b->lock);
	if (READ_ONCE(*addr) != cmp) {
		spin_unlock_bh(&b->lock);
		return;
	}
	list_add_tail(&w.node, &b->waiters);
	spin_unlock_bh(&b->lock);

	{
		GOKM_PARK();
		set_current_state(TASK_IDLE);
		if (!READ_ONCE(w.woken) && !gokm_stopping())
			schedule_timeout(timeout);
		__set_current_state(TASK_RUNNING);

		spin_lock_bh(&b->lock);
		if (!w.woken)
			list_del(&w.node);
		spin_unlock_bh(&b->lock);

		if (gokm_stopping() && t && t->goroutine)
			goroutine_bail(t);	/* parked: not counted as active */
		GOKM_UNPARK();
	}
}

static void futex_wake(u32 *addr, int n)
{
	struct gokm_futex_bucket *b = futex_bucket(addr);
	struct gokm_futex_waiter *w, *tmp;

	spin_lock_bh(&b->lock);
	list_for_each_entry_safe(w, tmp, &b->waiters, node) {
		struct task_struct *tsk;

		if (w->addr != addr)
			continue;
		list_del(&w->node);
		tsk = w->task;
		get_task_struct(tsk);
		WRITE_ONCE(w->woken, true);	/* w may vanish after this */
		wake_up_process(tsk);
		put_task_struct(tsk);
		if (--n == 0)
			break;
	}
	spin_unlock_bh(&b->lock);
}

void tinygo_futex_wait(u32 *addr, u32 cmp)
{
	futex_wait(addr, cmp, MAX_SCHEDULE_TIMEOUT);
}

void tinygo_futex_wait_timeout(u32 *addr, u32 cmp, u64 timeout_ns)
{
	futex_wait(addr, cmp, max_t(long, nsecs_to_jiffies(timeout_ns), 1));
}

void tinygo_futex_wake(u32 *addr)
{
	futex_wake(addr, 1);
}

void tinygo_futex_wake_all(u32 *addr)
{
	futex_wake(addr, INT_MAX);
}

/* ---- Sleeping -------------------------------------------------------- */

/* time.Sleep: other tasks run Go (and the GC) meanwhile. Unload interrupts
 * a goroutine's sleep and ends it. */
void gokm_msleep(unsigned int ms)
{
	struct gokm_thread *t;

	if (!gokm_can_block()) {
		tinygo_kmod_panic_blocking();
		return;
	}
	t = current_thread();
	{
		GOKM_PARK();
		wait_event_idle_timeout(gokm_stop_wq, gokm_stopping(),
					max_t(long, msecs_to_jiffies(ms), 1));
		if (gokm_stopping() && t && t->goroutine)
			goroutine_bail(t);
		GOKM_UNPARK();
	}
}

/* ---- Init / unload --------------------------------------------------- */

void gokm_threads_init(void)
{
	int i;

	for (i = 0; i < ARRAY_SIZE(gokm_futex); i++) {
		spin_lock_init(&gokm_futex[i].lock);
		INIT_LIST_HEAD(&gokm_futex[i].waiters);
	}
	for (i = 0; i < ARRAY_SIZE(gokm_threads); i++) {
		spin_lock_init(&gokm_threads[i].lock);
		INIT_LIST_HEAD(&gokm_threads[i].threads);
	}
}

/*
 * Make goroutines exit and wait for them (module unload, after the exit
 * hooks). Goroutines blocked in waits or sleeps exit right away; ones
 * still computing are waited for.
 */
void gokm_threads_stop(void)
{
	struct gokm_goroutine *g, *tmp;
	int i;

	WRITE_ONCE(gokm_stop, true);
	smp_mb();
	for (i = 0; i < ARRAY_SIZE(gokm_futex); i++) {
		struct gokm_futex_waiter *w, *wt;

		spin_lock_bh(&gokm_futex[i].lock);
		list_for_each_entry_safe(w, wt, &gokm_futex[i].waiters, node) {
			struct task_struct *tsk = w->task;

			list_del(&w->node);
			get_task_struct(tsk);
			WRITE_ONCE(w->woken, true);
			wake_up_process(tsk);
			put_task_struct(tsk);
		}
		spin_unlock_bh(&gokm_futex[i].lock);
	}
	wake_up_all(&gokm_stop_wq);

	while (!wait_event_timeout(gokm_stop_wq, atomic_read(&gokm_ngoroutines) == 0, 5 * HZ))
		pr_warn("unload: waiting for %d goroutine(s) to return\n",
			atomic_read(&gokm_ngoroutines));

	/* Wait until each kthread is fully gone (it may still be returning). */
	list_for_each_entry_safe(g, tmp, &gokm_goroutine_list, node) {
		kthread_stop(g->task);
		put_task_struct(g->task);
		list_del(&g->node);
		kfree(g);
	}
}
