---
title: "Limitations"
description: "What gokm doesn't support (yet)."
weight: 10
---

- **Out of memory is fatal** (as in Go); use `kernel.MakeBytes` where it
  must be handled. Atomic-context allocation is limited to free space plus
  the reserve.
- **Stop-the-world GC**: pauses all Go code; tight loops that never allocate
  or block delay it.
- **Small stacks**: Go runs on 16 KB kernel stacks (the caller's, or a
  goroutine kthread's); `gokm build` warns about deep or recursive paths.
- Framework coverage, what's still missing:
  - `gokm/fs`: POSIX ACLs and `security.*` xattrs, file locking beyond
    the VFS's own, `fallocate`, direct I/O, and large folios. Page-cache use
    is limited to mmap.
  - `gokm/chardev`: mmap of device memory and `fasync`.
  - `gokm/netdev`: offloads (checksum, TSO/GRO), multiple queues, and
    ethtool.
  - `gokm/blk`: zoned devices and per-request timeouts.
- `gokm/linux` can't call variadic functions directly (use `//gokm:inline`).
- x86-64 only; GPL-compatible licenses only. TinyGo runtime internals are
  used and may change between TinyGo versions.
