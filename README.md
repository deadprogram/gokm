# gokm — Go Kernel Modules

`gokm` builds Linux kernel modules (x86-64) from plain Go code, using TinyGo.
A project is just Go files: filesystems, character devices, `/proc` files and
network devices are written against Go packages, and gokm supplies all the
kernel-side code. Goroutines, channels, `sync`, `fmt`, maps, `defer`/`recover`
and floating point all work, and Go code runs on all CPUs in parallel.

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
| `gokm/fs`      | filesystems: implement `Dir` / file interfaces, `fs.Register`          |
| `gokm/chardev` | character devices (`/dev/<name>`), with ioctl: `chardev.Register`      |
| `gokm/procfs`  | `/proc` files: `procfs.Create`                                         |
| `gokm/netdev`  | network interfaces (point-to-point IP, like TUN): `netdev.Register`    |
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
// Truncate; symlinks: Readlink

fs.Register(&fs.FileSystem{Name: "gofs", Mount: func() (fs.Dir, error) { return newDir(), nil }})
```

`examples/gofs` is a complete in-memory filesystem (files, directories,
symlinks, rename, holes, truncation, `ENOSPC` when the heap is full).

### gokm/chardev and gokm/procfs

```go
chardev.Register(&chardev.Device{Name: "gorev", Mode: 0o666,
	Opener: chardev.OpenFunc(func() (chardev.File, error) { return &session{}, nil })})
// session implements Read(p, off) / Write(p, off) / Release() /
// Ioctl(cmd, arg): commands built with chardev.IO/IOR/IOW/IOWR, user
// pointers accessed with chardev.CopyFromUser/CopyToUser

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
`examples/gonet` is a network device answering ping and UDP echo (20000-packet
floods included).

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
| `examples/devices`    | `/dev/gorev` with ioctls + `/proc/gorev` (`gokm/chardev`, `gokm/procfs`) |
| `examples/gonet`      | a network device answering ping/UDP echo in atomic context          |
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
  `endbr64`, no unwind tables.
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
- Framework coverage: `gokm/fs` has no mmap or xattrs, `gokm/chardev` no
  poll, `gokm/netdev` is point-to-point IP only (no Ethernet/ARP).
- `gokm/linux` can't call variadic functions directly (use `//gokm:inline`).
- x86-64 only; GPL-compatible licenses only. TinyGo runtime internals are
  used and may change between TinyGo versions.

## Developing gokm

```sh
make test     # go vet + unit tests (BTF parsing and binding generation use the running kernel's BTF)
make qemu     # build every example and fixture and run each in a QEMU VM with its test program
```
