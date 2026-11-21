---
title: "gokm/blk"
linkTitle: "blk"
description: "Block devices whose storage is Go code."
weight: 5
---

API reference: [gokm/blk](/api/blk/).

## gokm/blk

```go
type ram struct{ ... }

func (r *ram) ReadAt(p []byte, off int64) error  { ... }  // one segment of a request
func (r *ram) WriteAt(p []byte, off int64) error { ... }
// optional: Flush() error (advertises a write cache), Discard(off, n int64) error

blk.Register(&blk.Disk{Name: "gobrd%d", Size: 64 << 20, Device: &ram{}})  // /dev/gobrd0
```

The kernel's block layer (blk-mq) queues and merges I/O. Requests reach Go
in process context, so the `Device` can block and allocate, and several
requests run at once on different CPUs (one hardware queue per CPU by
default: `Queues`, `QueueDepth`). Disks get a dynamic device number and can
be partitioned. `examples/gobrd` ports Linux's RAM disk
(`drivers/block/brd.c`), and its test checks it against brd itself.
