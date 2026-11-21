---
title: "Concurrency"
description: "Parallel Go, goroutines as kernel threads, stop-the-world GC and atomic context."
weight: 6
---

- **Go runs in parallel.** Kernel callbacks run Go on whatever CPU they
  arrive on, concurrently, like HTTP handlers: protect shared state with
  `sync.Mutex` (or atomics/`kernel.SpinLock` in atomic context).
- **Goroutines** are kernel threads: `go`, channels, `select`, `sync`,
  timers and `time.Sleep` work. At rmmod, after the `OnExit` hooks,
  goroutines blocked in waits or sleeps are ended; ones still computing are
  waited for (stop them from an `OnExit` hook).
- **Garbage collection stops the world**: it waits until every task running
  Go reaches a transition (returns to the kernel, blocks, sleeps, or calls
  a `//gokm:blocking` function). A tight loop without those delays it.
- **Atomic context** (softirq, spinlocks held): Go can run there through
  frameworks that offer it (`netdev.AtomicTransmit`). Blocking there (mutex,
  channel, sleep) is a recoverable panic, not a deadlock.
- **Panics in framework callbacks become `EIO`** (logged).
