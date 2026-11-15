// SPDX-License-Identifier: GPL-2.0
/*
 * gokm heap: kernel side of the Go garbage collector
 * (overlay/src/runtime/gc_linuxkernel.go).
 *
 * The Go heap is a set of vzalloc() chunks the collector adds as the heap
 * grows (up to heap_max) and returns when they become empty. Allocation is
 * serialized by a BH-safe spinlock, so atomic-context Go code can allocate
 * too (from free space only: growing and collecting need process context).
 * A background kthread runs collections ahead of time, so atomic contexts
 * rarely find the heap full.
 */
#define pr_fmt(fmt) KBUILD_MODNAME ": " fmt

#include <linux/module.h>
#include <linux/kernel.h>
#include <linux/vmalloc.h>
#include <linux/kthread.h>
#include <linux/sched.h>
#include <linux/spinlock.h>
#include <linux/list.h>
#include <linux/slab.h>

#include "gokm.h"

struct gokm_chunk {
	struct list_head node;
	void *mem;
};

static LIST_HEAD(gokm_chunks);
static DEFINE_MUTEX(gokm_chunks_lock);
static DEFINE_SPINLOCK(gokm_heap_spinlock);

unsigned long gokm_chunk_alloc(unsigned long size)
{
	struct gokm_chunk *c;

	if (!gokm_can_block())
		return 0;
	c = kmalloc(sizeof(*c), GFP_KERNEL);
	if (!c)
		return 0;
	c->mem = vzalloc(size);
	if (!c->mem) {
		kfree(c);
		return 0;
	}
	mutex_lock(&gokm_chunks_lock);
	list_add(&c->node, &gokm_chunks);
	mutex_unlock(&gokm_chunks_lock);
	return (unsigned long)c->mem;
}

void gokm_chunk_free(unsigned long mem)
{
	struct gokm_chunk *c, *found = NULL;

	mutex_lock(&gokm_chunks_lock);
	list_for_each_entry(c, &gokm_chunks, node) {
		if ((unsigned long)c->mem == mem) {
			found = c;
			list_del(&c->node);
			break;
		}
	}
	mutex_unlock(&gokm_chunks_lock);
	if (WARN_ON_ONCE(!found))
		return;
	vfree(found->mem);
	kfree(found);
}

/* Unload: release whatever the heap still holds. */
void gokm_chunks_free_all(void)
{
	struct gokm_chunk *c, *tmp;

	mutex_lock(&gokm_chunks_lock);
	list_for_each_entry_safe(c, tmp, &gokm_chunks, node) {
		list_del(&c->node);
		vfree(c->mem);
		kfree(c);
	}
	mutex_unlock(&gokm_chunks_lock);
}

void gokm_heap_lock(void)
{
	spin_lock_bh(&gokm_heap_spinlock);
}

void gokm_heap_unlock(void)
{
	spin_unlock_bh(&gokm_heap_spinlock);
}

/* ---- Background collector ---------------------------------------------- */

static struct task_struct *gokm_gc_task;
static bool gokm_gc_kicked;

static int gokm_gc_worker(void *unused)
{
	while (!kthread_should_stop()) {
		set_current_state(TASK_IDLE);
		if (!READ_ONCE(gokm_gc_kicked) && !kthread_should_stop())
			schedule();
		__set_current_state(TASK_RUNNING);
		if (kthread_should_stop())
			break;
		WRITE_ONCE(gokm_gc_kicked, false);
		{
			GOKM_ENTER();
			tinygo_kmod_gc_background();
			GOKM_LEAVE();
		}
	}
	return 0;
}

/* Ask the background collector to run (any context). */
void gokm_gc_kick(void)
{
	struct task_struct *tsk = READ_ONCE(gokm_gc_task);

	if (tsk && !READ_ONCE(gokm_gc_kicked)) {
		WRITE_ONCE(gokm_gc_kicked, true);
		wake_up_process(tsk);
	}
}

int gokm_gc_worker_start(void)
{
	struct task_struct *tsk;

	tsk = kthread_run(gokm_gc_worker, NULL, "%s-gc", KBUILD_MODNAME);
	if (IS_ERR(tsk))
		return PTR_ERR(tsk);
	WRITE_ONCE(gokm_gc_task, tsk);
	return 0;
}

void gokm_gc_worker_stop(void)
{
	struct task_struct *tsk = gokm_gc_task;

	if (tsk) {
		WRITE_ONCE(gokm_gc_task, NULL);
		kthread_stop(tsk);
	}
}
