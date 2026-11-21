# gokm — Go Kernel Modules

> [!WARNING]
> gokm is **experimental**. Don't use it in production: the modules it
> builds run inside the kernel, where a bug in the generated code or the Go
> runtime can crash the machine or corrupt data, and the API may change
> without notice.

`gokm` builds Linux kernel modules (x86-64) from plain Go code, using TinyGo.
A project is just Go files: filesystems, character devices, `/proc` files and
network devices are written against Go packages, and gokm supplies all the
kernel-side code. Goroutines, channels, `sync`, `fmt`, maps, `defer`/`recover`
and floating point all work, and Go code runs on all CPUs in parallel.

**New to gokm?** [TUTORIAL.md](TUTORIAL.md) walks you through building,
loading and extending a "hello world" kernel module in a few minutes.

```sh
gokm init hello          # creates hello/go.mod and hello/main.go
gokm build hello         # produces hello/hello.ko
sudo insmod hello/hello.ko && sudo rmmod hello && sudo dmesg | tail -2
# hello: hello from Go!
# hello: goodbye from Go!
```

```go
package main

import "gokm/kernel"

func main() { // runs at insmod
	kernel.Print("hello from Go!")
	kernel.OnExit(func() { kernel.Print("goodbye from Go!") }) // runs at rmmod
}
```

## Install

Requirements: Go, TinyGo (tested with 0.43.0), the kernel headers for the
target kernel (`linux-headers` on Arch; they include `vmlinux` with BTF,
used by `gokm/linux`), `make` and binutils (`ld`, `objcopy`).

```sh
go install .          # or: make  (builds ./gokm)
```

## Commands

```
gokm init [-module path] [dir]
    Creates go.mod and main.go in dir (default "."). The kernel module name is
    the last element of the module path. Also writes a go.work so editors
    resolve gokm's packages (machine-specific: don't commit it).

gokm build [flags] [dir]
    -o file            output path (default <dir>/<name>.ko)
    -name name         kernel module name
    -kver release      kernel to build for (default: uname -r)
    -kdir dir          kernel build dir (default: /lib/modules/<kver>/build)
    -heap-size size    initial size of the Go heap (default 1M; load time: heap_size=)
    -heap-max size     limit the Go heap can grow to (default 256M; load time: heap_max=)
    -license text      MODULE_LICENSE: GPL (default), "GPL v2", "Dual MIT/GPL",
                       "Dual BSD/GPL", ... (GPL-compatible only: gokm's runtime
                       uses GPL-only kernel symbols)
    -description text  MODULE_DESCRIPTION
    -author text       MODULE_AUTHOR
    -stack-limit size  warn about Go call paths deeper than this (default 8K; 0: off)
    -v                 show the commands run, their output and the deepest call paths
```

Everything except the final `.ko` goes into `~/.cache/gokm` (or `$GOKMCACHE`).

## Packages

| Package        | What it's for                                                          |
|----------------|------------------------------------------------------------------------|
| `gokm/kernel`  | logging, module lifecycle, errors, handles, memory, `SpinLock`         |
| `gokm/fs`      | filesystems (mmap and xattrs included): `fs.Register`                  |
| `gokm/chardev` | character devices (`/dev/<name>`), ioctl, poll: `chardev.Register`     |
| `gokm/procfs`  | `/proc` files: `procfs.Create`                                         |
| `gokm/netdev`  | network interfaces (IP like TUN, or Ethernet like TAP): `netdev.Register` |
| `gokm/blk`     | block devices (`/dev/<name>`, partitions included): `blk.Register`     |
| `gokm/linux`   | generated bindings to any kernel function, struct or constant          |

Importing a framework package is all it takes: `gokm build` detects it and
compiles in its kernel glue. One module can use several (see
`examples/devices`: a char device and a `/proc` file sharing state).

### gokm/kernel

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

### gokm/fs

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

### gokm/chardev and gokm/procfs

```go
chardev.Register(&chardev.Device{Name: "gorev", Mode: 0o666,
	Opener: chardev.OpenFunc(func() (chardev.File, error) { return &session{}, nil })})
// session implements Read(p, off) / Write(p, off) / Release() /
// Ioctl(cmd, arg): commands built with chardev.IO/IOR/IOW/IOWR, user
// pointers accessed with chardev.CopyFromUser/CopyToUser

// Data that arrives over time: Poll for poll/select/epoll, Wait to block
// (EAGAIN with O_NONBLOCK, EINTR on signals), Wake when things change.
func (s *session) ReadFlags(p []byte, off int64, fl chardev.Flags) (int, error) {
	if err := dev.Wait(fl, func() bool { return pending() > 0 }); err != nil {
		return 0, err
	}
	...
}
func (s *session) Poll() chardev.PollEvents { ... chardev.PollIn ... }
// producer (a goroutine, a network callback, any context): dev.Wake()

procfs.Create("gorev", procfs.ReadFunc(func() ([]byte, error) {
	return []byte(fmt.Sprintf("writes: %d\n", writes)), nil
}))  // implement Write(p []byte) error too for a writable file
```

### gokm/netdev

```go
type echo struct{ iface *netdev.Interface }

func (e *echo) Transmit(pkt []byte) { e.iface.Receive(makeReply(pkt)) } // IPv4/IPv6 packets

e.iface = &netdev.Interface{Name: "goecho%d", Driver: e, AtomicTransmit: true}
netdev.Register(e.iface)
```

By default `Transmit` runs from a workqueue (process context). With
`AtomicTransmit` it runs directly in the kernel's transmit path (atomic
context): it must not block (use atomics or `kernel.SpinLock`). While a
garbage collection is in progress packets fall back to the workqueue.
With `Ethernet: true` the interface is an Ethernet device (like TAP):
`Transmit` and `Receive` carry whole frames, it has a MAC address (`MAC`,
random if zero; `HardwareAddr()` reads the current one), and the kernel
runs ARP and IPv6 neighbor discovery over it. `examples/gonet` has both
kinds. `goecho0` answers ping and UDP echo (20000-packet floods included).
`goeth0` simulates a host on an Ethernet link that answers ARP.

### gokm/blk

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

### Module parameters

```go
//gokm:param rd_size Size of each RAM disk in kbytes.
var rdSize uint = 4096
```

A package-level variable in package `main` becomes a module parameter:
`insmod gobrd.ko rd_size=65536`, `modinfo`, and read-only in
`/sys/module/<name>/parameters/`. The initializer (a constant expression)
is the default. The loaded value is in the variable when `main` and the
`init` functions run. Supported types: `int`, `int64`, `int32`, `int16`,
`uint`, `uint64`, `uint32`, `uint16`, `byte`, `bool` and `string`.

### gokm/linux: bindings generated from the kernel's BTF

Reference any kernel function, struct or constant as `linux.X` (CamelCase of
the C name; constants keep their C name) and `gokm build` generates exactly
those declarations for the target kernel:

```go
import "gokm/linux"

var si linux.Sysinfo                  // struct sysinfo, exact layout for this kernel
linux.SiMeminfo(&si)                  // exported function: a direct call
u := linux.Utsname()                  // static inline function: wrapper generated,
release := u.Release                  //   its signature read from the headers' DWARF
ts := linux.NsToTimespec64(1500000000) // struct returned by value
_ = linux.HZ                          // macro, evaluated against the headers
```

- **Structs** have every field at its BTF offset (size checked at compile
  time); bitfields get `Name()`/`SetName(v)` methods, union members and other
  overlapping members get `Name() *T` accessors. Structs used by referenced
  functions are generated too.
- **Exported functions** are direct calls; ones taking or returning structs
  by value go through a generated C wrapper (with a by-value Go API).
- **Static inline functions** are found automatically (gokm compiles a probe
  taking their address and reads the signature from DWARF).
- **Function-like macros** (e.g. `num_online_cpus()`) have no signature to
  find: declare them with a pragma and gokm generates the wrapper:

```go
//gokm:inline num_online_cpus linux/cpumask.h  // optional extra headers
func numOnlineCPUs() uint32

//gokm:blocking msleep                         // also lets the GC run meanwhile
func msleep(ms uint32)

//gokm:include linux/netdevice.h               // extra headers for wrappers/macros
```

Use `//gokm:blocking` for anything that may sleep. Variadic functions (like
`printk`) can't be called directly: wrap them with `//gokm:inline`.

## Memory

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

## Concurrency

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

## Examples

| Example               | Shows                                                                |
|-----------------------|----------------------------------------------------------------------|
| `examples/hello`      | the basics                                                           |
| `examples/gcstress`   | the GC under pressure                                                |
| `examples/gofs`       | a filesystem (`gokm/fs`)                                             |
| `examples/devices`    | `/dev/gorev` with ioctls, `/dev/gopipe` with poll/blocking I/O, `/proc/gorev` |
| `examples/gonet`      | an IP device answering ping/UDP echo in atomic context, and an Ethernet one with ARP |
| `examples/gobrd`      | a RAM disk, ported from Linux's brd (`gokm/blk`, module parameters)  |
| `examples/sysinfo`    | `gokm/linux`: functions, structs, inline functions, by-value structs, macros, pragmas |

Each has a test program that `make qemu` runs inside a VM against the loaded
module; `test/fixtures` cover load failures, nested and parked entries,
parallelism, goroutines, heap growth and atomic-context misuse.

## Escape hatch: C in `csrc/`

For anything not covered, `.c`/`.h` files in a project's `csrc/` directory
are compiled into the module. They include `gokm.h` and call into Go with
`GOKM_ENTER()`/`GOKM_LEAVE()` (and `GOKM_TRY_ENTER_ATOMIC()`,
`GOKM_PARK()`/`GOKM_UNPARK()`; see the comments there). No example needs it.

## How it works

```
your *.go ──(copy; pragmas → //export)──▶ tinygo build ──▶ go.ll ──IR fixups──▶ tinygo clang ──▶ go.o
gokm/linux (generated from BTF/DWARF) ───┘                                        + compiler-rt
runtime C (shim, threads, heap) + framework glue + generated wrappers + go.o ──kbuild──▶ name.ko
```

- **`target.json`**: TinyGo target for kernel code (kernel code model, static
  relocations, soft-float, retpolines, threads scheduler, custom GC).
- **Runtime overlay** (`internal/assets/_files/overlay`): the platform layer,
  goroutines as kernel threads (`internal/task`), and the collector
  (`runtime/gc_linuxkernel.go`).
- **C runtime** (`internal/assets/_files/kmod`): `threads.c` (entry gate,
  thread records, parking, stop-the-world, futex, goroutines), `heap.c`
  (heap chunks, background collector), `shim.c` (module init/exit,
  services, IBT-safe `longjmp` for `recover()`).
- **IR fixups** (`internal/build/fixup.go`): `noredzone`, return thunks, IBT
  `endbr64`, no unwind tables, direct (non-GOT) access to C globals.
- **compiler-rt** builtins (soft-float, 128-bit math) compiled once with the
  kernel flags; only what a module needs is linked in.
- **Frameworks** (`_files/lib/<pkg>` + `_files/frameworks/<pkg>`).
- **`internal/btf`, `internal/bindgen`**: BTF parser and `gokm/linux` generator.
- **Stack analysis** (`internal/build/stack.go`): per-function frame sizes
  from LLVM, call graph from the IR, deepest paths from every entry point.

## Limitations

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

## Developing gokm

```sh
make test     # go vet + unit tests (BTF parsing and binding generation use the running kernel's BTF)
make qemu     # build every example and fixture and run each in a QEMU VM with its test program
```
