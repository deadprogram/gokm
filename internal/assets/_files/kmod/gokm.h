/* SPDX-License-Identifier: GPL-2.0 */
/* Interface between shim.c and the TinyGo-compiled object (go.o). */
#ifndef GOKM_H
#define GOKM_H

#include <linux/types.h>
#include <linux/compiler.h>

/* Implemented in Go (overlay/src/runtime/runtime_linuxkernel.go). */
void tinygo_kmod_init(unsigned long stack_top, unsigned long heap, unsigned long heap_size);
void tinygo_kmod_exit(unsigned long stack_top);

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
