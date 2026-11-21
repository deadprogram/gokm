---
title: "gokm/fs"
linkTitle: "fs"
description: "Filesystems written in Go, including mmap and extended attributes."
weight: 2
---

API reference: [gokm/fs](/api/fs/).

## gokm/fs

```go
type dir struct{ children map[string]fs.Node; names []string }

func (d *dir) Attr() fs.Attr                                  { return fs.Attr{Mode: fs.ModeDir | 0o755} }
func (d *dir) Lookup(name string) (fs.Node, error)            { ... }
func (d *dir) ReadDir(yield func(string, fs.Node) bool) error { ... }
// optional: Create, Mkdir, Remove, Rename, Symlink; files: ReadAt, WriteAt,
// Truncate; symlinks: Readlink; any node: GetXattr/SetXattr/RemoveXattr/ListXattrs

fs.Register(&fs.FileSystem{Name: "gofs", Mount: func() (fs.Dir, error) { return newDir(), nil }})
```

`examples/gofs` is a complete in-memory filesystem (files, directories,
symlinks, rename, holes, truncation, xattrs, `ENOSPC` when the heap is
full). Its test runs a binary stored in it.

`read`/`write` go straight to `ReadAt`/`WriteAt`. `mmap` (shared or
private, which also makes `exec` work) goes through the kernel's page cache.
The page cache is filled with `ReadAt`, and dirty pages are written back
with `WriteAt` (on `msync`, `fsync`, or when the kernel flushes).
The framework keeps both paths coherent: `read` and `write` flush dirty
mapped pages in their range first, and `write`/`truncate` drop the cached
pages they changed. Extended attributes in the `user.` and `trusted.`
namespaces go to the node's `Xattrer`; the framework handles the
create/replace flags and buffer sizes.

**Block-device filesystems:** set `MountDevice` instead of `Mount` and the
filesystem mounts from a device (`mount -t name /dev/sdb1 /mnt`).
`MountDevice` receives an `*fs.Device` that reads and writes blocks through
the kernel's buffer cache (`ReadBlocks`, `WriteBlocks`, `Sync`,
`SetBlockSize`). The root `Dir` can also implement:
- `Statfser`, for statfs numbers;
- `Syncer`, called on sync/fsync/unmount;
- `Unmounter`.

Nodes can implement `SetTimer` (utimes) and `Forgetter` (the kernel let go
of a removed node: release its storage). `Attr` carries timestamps.
[govfat](https://github.com/jespino/govfat) is a FAT32 driver built this way.
