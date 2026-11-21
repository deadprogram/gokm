---
title: "Memory"
description: "The garbage-collected Go heap inside the kernel, and kernel memory."
weight: 5
---

Go code uses a **garbage-collected heap** that grows on demand from
`heap_size` up to `heap_max` and gives memory back when it shrinks. It's
gokm's own mark/sweep collector: precise for heap objects (pointer-free data
like `[]byte` isn't scanned), conservative for stacks. `runtime.GC()` and
`runtime.ReadMemStats` work as usual. A background kthread collects ahead of
time.

**Running out of memory is a fatal error, as in Go** (for `make`, `new`,
`append`, ...), so size `heap_max` for your workload, and allocate large or
input-dependent buffers with `kernel.MakeBytes`, which returns `ENOMEM`
instead. Atomic-context code can't collect or grow the heap; it has a spare
chunk in reserve, refilled by the background collector.

`kernel.AllocBytes` gives off-heap kernel buffers freed with `FreeBytes`
(never put Go pointers in them). **Kernel memory must never hold Go
pointers**: store a `kernel.Handle` instead (frameworks already do).
