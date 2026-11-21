---
title: "gokm/kernel"
linkTitle: "kernel"
description: "Logging, the module lifecycle, errors, handles, memory and locks."
weight: 1
---

API reference: [gokm/kernel](/api/kernel/).

## gokm/kernel

| API                                     | Purpose                                                       |
|-----------------------------------------|---------------------------------------------------------------|
| `Print(s)`                              | `pr_info`, one log line (prefixed with the module name)       |
| `OnExit(fn)`                            | run `fn` at rmmod (several allowed, run in reverse order)     |
| `FailLoad(err)`                         | make insmod fail with `err` once `main` returns               |
| `Errno` (`ENOENT`, `ENOSPC`, ...)       | kernel errors as Go `error` values; `ToErrno(err)` converts back |
| `NewHandle(v)` / `Value()` / `Delete()` | integer handles to Go values, for kernel memory               |
| `MakeBytes(n)`                          | `make([]byte, n)` that returns `ENOMEM` instead of failing fatally |
| `AllocBytes(n)` / `FreeBytes(b)`        | off-heap kernel buffers (`kvzalloc`)                          |
| `SpinLock`                              | a lock usable in any context, including atomic ones           |
| `Atomic()`                              | whether the calling code may not block                        |
| `CatchPanic(&rc)`                       | turn a panic into `-EIO` (frameworks do this for you)         |
| `MonotonicNanos()`                      | `ktime_get_ns()`                                              |
