# gokm — Go Kernel Modules

`gokm` builds Linux kernel modules (x86-64) from plain Go code, using TinyGo.
A project is just Go files: filesystems, character devices, `/proc` files and
network devices are written against Go packages, and gokm supplies all the
kernel-side code.

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
    -heap-size size    default size of the garbage-collected Go heap (default 1M);
                       overridable at load time: insmod mod.ko heap_size=8M
    -description text  MODULE_DESCRIPTION
    -author text       MODULE_AUTHOR
    -v                 show the commands run and their output
```

Everything except the final `.ko` goes into `~/.cache/gokm` (or `$GOKMCACHE`).

## Packages

| Package        | What it's for                                                          |
|----------------|------------------------------------------------------------------------|
| `gokm/kernel`  | logging, module lifecycle, errors, handles, off-heap memory            |
| `gokm/fs`      | filesystems: implement `Dir` / file interfaces, `fs.Register`          |
| `gokm/chardev` | character devices (`/dev/<name>`): `chardev.Register`                  |
| `gokm/procfs`  | `/proc` files: `procfs.Create`                                         |
| `gokm/netdev`  | network interfaces (point-to-point IP, like TUN): `netdev.Register`    |
| `gokm/linux`   | generated bindings to any exported kernel function, struct or constant |

Importing a framework package is all it takes: `gokm build` detects it and
compiles in its kernel glue. One module can use several (see
`examples/devices`: a char device and a `/proc` file sharing state).

### gokm/kernel

| API                                     | Purpose                                                   |
|-----------------------------------------|-----------------------------------------------------------|
| `Print(s)`                              | `pr_info`, one log line (prefixed with the module name)   |
| `OnExit(fn)`                            | run `fn` at rmmod (several allowed, run in reverse order) |
| `FailLoad(err)`                         | make insmod fail with `err` once `main` returns           |
| `Errno` (`ENOENT`, `ENOSPC`, ...)       | kernel errors as Go `error` values                        |
| `ToErrno(err)`                          | `error` → negative errno                                  |
| `NewHandle(v)` / `Value()` / `Delete()` | integer handles to Go values, for kernel memory           |
| `AllocBytes(n)` / `FreeBytes(b)`        | off-heap kernel buffers (`kvzalloc`)                      |
| `CatchPanic(&rc)`                       | turn a panic into `-EIO` (frameworks do this for you)     |
| `MonotonicNanos()`                      | `ktime_get_ns()`                                          |

`fmt`, `strconv`, `strings`, `sort`, `errors`, `math`, maps, closures,
interfaces, `defer`/`recover` and floating point all work (floats are
software-emulated: the kernel can't use the FPU).

### gokm/fs

```go
type dir struct{ children map[string]fs.Node; names []string }

func (d *dir) Attr() fs.Attr                                  { return fs.Attr{Mode: fs.ModeDir | 0o755} }
func (d *dir) Lookup(name string) (fs.Node, error)            { return d.children[name], nil }
func (d *dir) ReadDir(yield func(string, fs.Node) bool) error { ... }
func (d *dir) Create(name string, mode fs.Mode) (fs.Node, error) { ... } // optional
// optional: Mkdir, Remove; files: ReadAt, WriteAt, Truncate

fs.Register(&fs.FileSystem{Name: "gofs", Mount: func() (fs.Dir, error) { return newDir(), nil }})
```

`examples/gofs` is a complete in-memory filesystem (files, directories,
holes, truncation, `ENOSPC` when the heap is full) in ~150 lines of Go.

### gokm/chardev and gokm/procfs

```go
chardev.Register(&chardev.Device{Name: "gorev", Mode: 0o666,
	Opener: chardev.OpenFunc(func() (chardev.File, error) { return &session{}, nil })})
// session implements Read(p, off) / Write(p, off) / Release()

procfs.Create("gorev", procfs.ReadFunc(func() ([]byte, error) {
	return []byte(fmt.Sprintf("writes: %d\n", writes)), nil
}))  // implement Write(p []byte) error too for a writable file
```

### gokm/netdev

```go
type echo struct{ iface *netdev.Interface }

func (e *echo) Transmit(pkt []byte) { e.iface.Receive(makeReply(pkt)) } // IPv4/IPv6 packets

e.iface = &netdev.Interface{Name: "goecho%d", Driver: e}
netdev.Register(e.iface)
```

`examples/gonet` is a network device that answers ping and UDP echo.

### gokm/linux: bindings generated from the kernel's BTF

Reference any exported kernel function, struct or constant as `linux.X`
(CamelCase of the C name; constants keep their C name) and `gokm build`
generates exactly those declarations for the target kernel. Struct layouts
come from the kernel's BTF and are checked at compile time; integer macros
are evaluated with the kernel's own headers and config:

```go
import "gokm/linux"

var si linux.Sysinfo   // struct sysinfo, exact layout for this kernel
linux.SiMeminfo(&si)   // exported function: a direct call
mib := si.Totalram * uint64(si.MemUnit) >> 20
_ = linux.HZ           // macro, evaluated against the headers
```

Static inline functions and function-like macros have no symbol to call:
declare them as body-less Go functions with a pragma, and gokm generates the
C wrapper:

```go
//gokm:inline utsname linux/utsname.h      // optional extra headers
func utsname() *linux.NewUtsname

//gokm:blocking msleep                     // releases the Go lock around the call
func msleep(ms uint32)

//gokm:include linux/netdevice.h           // extra headers for wrappers/macros
```

Use `//gokm:blocking` for anything that sleeps or can wait on another task
that calls into this module. Pointers to referenced structs are typed
(`*linux.Inode`), other pointers are `unsafe.Pointer`, `char` is `byte`,
function pointers are `uintptr`. Variadic functions (like `printk`) and
by-value struct arguments aren't callable directly: wrap them with
`//gokm:inline`. See `examples/sysinfo`.

## Memory

Go code uses a **garbage-collected heap**: TinyGo's conservative mark/sweep
GC over a fixed-size `vzalloc` region allocated at insmod (default 1 MiB;
`gokm build -heap-size`, or `insmod mod.ko heap_size=4M`). Collections run
when an allocation doesn't fit; if the heap is still full the module panics
with `out of memory`, so size it for your live data (`runtime.ReadMemStats`
works). For big or long-lived pointer-free data, `kernel.AllocBytes` gives
off-heap buffers that never hold Go pointers and are freed with `FreeBytes`
(leftovers are freed at rmmod).

**Kernel memory must never hold Go pointers** (the GC doesn't scan it): store
a `kernel.Handle` instead. Frameworks already do this.

## Concurrency model

- The Go runtime is single-threaded, so **Go code runs under one module-wide
  lock** (like Python's GIL). Callbacks from different CPUs run one at a time;
  Go → kernel → Go on the same thread nests fine.
- **Blocking calls release the lock** ("parking"): `time.Sleep`,
  `//gokm:blocking` functions and framework calls like unregistering a
  device. Other threads run Go meanwhile; the GC scans parked threads' stacks.
- **Go only runs in process context.** Frameworks move work out of atomic
  context for you (`gokm/netdev` transmits from a workqueue).
- **Panics in framework callbacks become `EIO`** (logged), not a kernel crash.
  A panic elsewhere (e.g. in `main`) still `BUG()`s.

## Examples

| Example             | Shows                                                               |
|---------------------|---------------------------------------------------------------------|
| `examples/hello`    | the basics                                                          |
| `examples/gcstress` | the GC under pressure (50× heap size of garbage)                    |
| `examples/gofs`     | a filesystem (`gokm/fs`)                                            |
| `examples/devices`  | `/dev/gorev` + `/proc/gorev` (`gokm/chardev`, `gokm/procfs`)        |
| `examples/gonet`    | a network device answering ping/UDP echo (`gokm/netdev`)            |
| `examples/sysinfo`  | `gokm/linux` bindings, `//gokm:inline`, `//gokm:blocking`, macros   |

Each has a test program (`fstest`, `devtest`, ...) that `make qemu` runs inside
a VM against the loaded module.

## Escape hatch: C in `csrc/`

For anything not covered, `.c`/`.h` files in a project's `csrc/` directory
are compiled into the module. They include `gokm.h` and call into Go with
`GOKM_ENTER()`/`GOKM_LEAVE()` (see the comments there). No example needs it.

## How it works

```
your *.go ──(copy; pragmas → //export)──▶ tinygo build ──▶ go.ll ──IR fixups──▶ tinygo clang ──▶ go.o
gokm/linux (generated from BTF) ─────────┘                                        + compiler-rt
shim.c + framework glue + generated wrappers + go.o ──kbuild──▶ name.ko
```

- **`target.json`**: TinyGo target for kernel code (kernel code model, static
  relocations, soft-float, retpolines, no goroutines, conservative GC).
- **Runtime overlay** (`internal/assets/_files/overlay`): the platform layer
  (printk output, ktime, `BUG()` on fatal errors, GC heap/stack hooks).
- **IR fixups** (`internal/build/fixup.go`): `noredzone`, return thunks, IBT
  `endbr64`, no unwind tables.
- **compiler-rt** builtins (soft-float, 128-bit math) compiled once with the
  kernel flags; only the objects a module needs are linked in.
- **`shim.c`**: module init/exit, the GC heap, the Go lock (per-thread records,
  parking, scanning parked stacks), an IBT-safe `longjmp` for `recover()`.
- **Frameworks** (`_files/lib/<pkg>` + `_files/frameworks/<pkg>`): the Go API
  and its kernel glue.
- **`internal/btf`, `internal/bindgen`**: BTF parser and `gokm/linux` generator.

## Limitations

- **Fixed-size GC heap**: it can't grow after load; out of memory panics.
- **One CPU in Go at a time** (the Go lock); no goroutines. Heavy packet rates
  or highly parallel workloads are serialized.
- **Process context only** for Go; frameworks defer from atomic context, raw
  `csrc/` code must not enter Go from interrupts, softirqs or RCU callbacks.
- **Small stacks**: Go runs on the caller's ~16 KB kernel stack.
- Framework coverage is a first cut: `gokm/fs` has no rename, symlinks,
  xattrs or mmap; `gokm/chardev` has no ioctl/poll; `gokm/netdev` is
  point-to-point IP only (no Ethernet/ARP).
- `gokm/linux` can't call variadic functions or pass structs by value (use
  `//gokm:inline`); bitfields and unions appear as byte arrays.
- x86-64 only; the module license is always GPL. TinyGo runtime internals
  are used and may change between TinyGo versions.

## Developing gokm

```sh
make test     # go vet + unit tests (BTF parsing and binding generation use the running kernel's BTF)
make qemu     # build every example and fixture and run each in a QEMU VM with its test program
```
