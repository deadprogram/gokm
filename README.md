# gokm — Go Kernel Modules

`gokm` builds Linux kernel modules (x86-64) from plain Go code, using TinyGo.

```sh
gokm init hello          # creates hello/go.mod and hello/main.go
gokm build hello         # produces hello/hello.ko
sudo insmod hello/hello.ko && sudo rmmod hello && sudo dmesg | tail -2
# hello: hello from Go!
# hello: goodbye from Go!
```

A project is just Go files:

```go
package main

import "gokm/kernel"

func main() { // runs at insmod
	kernel.Print("hello from Go!")

	kernel.OnExit(func() { // runs at rmmod
		kernel.Print("goodbye from Go!")
	})
}
```

## Install

Requirements: Go, TinyGo (tested with 0.43.0), the kernel headers for the
target kernel (`linux-headers` on Arch), `make` and binutils (`objcopy`).

```sh
go install .          # or: make  (builds ./gokm)
```

## Commands

```
gokm init [-module path] [dir]
    Creates go.mod and main.go in dir (default "."). The module path defaults
    to the directory name, and the kernel module name is its last element
    (with "-" and "." turned into "_"). It also writes a go.work so editors and
    gopls can resolve "gokm/kernel". That file is machine-specific; don't
    commit it.

gokm build [flags] [dir]
    -o file            output path (default <dir>/<name>.ko)
    -name name         kernel module name
    -kver release      kernel to build for (default: uname -r)
    -kdir dir          kernel build dir (default: /lib/modules/<kver>/build)
    -description text  MODULE_DESCRIPTION
    -author text       MODULE_AUTHOR
    -heap-size size    default size of the garbage-collected Go heap (default 1M);
                       can be overridden at load time: insmod mod.ko heap_size=8M
    -v                 show the commands run and their output
```

Everything except the final `.ko` goes into `~/.cache/gokm` (override with
`$GOKMCACHE`). The project directory stays clean.

## The `gokm/kernel` package

| Function                 | Kernel side                 |
|--------------------------|-----------------------------|
| `Print(s string)`        | `pr_info`, one log line     |
| `OnExit(fn func())`      | runs `fn` at module exit    |
| `MonotonicNanos() int64` | `ktime_get_ns()`            |
| `AllocBytes(n int) []byte` | off-heap buffer (`kvzalloc`) |
| `FreeBytes(b []byte)`    | releases an `AllocBytes` buffer |

Most of the language works: structs, interfaces, closures, slices, maps,
strings, and packages such as `strconv`, `strings`, `sort` and `errors`.

## Memory

Go code in a module uses a **garbage-collected heap**: `new`, `make`,
`append`, maps, closures and strings work as in normal Go, and memory nobody
references any more is reclaimed. `runtime.GC()` and `runtime.ReadMemStats`
work as usual.

- The collector is TinyGo's conservative mark/sweep GC. It scans globals,
  the Go part of the kernel stack and CPU registers, and treats any word that
  looks like a heap pointer as one.
- The heap is a **fixed-size** `vzalloc` region, allocated at insmod and
  freed at rmmod. It defaults to 1M; change it with `gokm build -heap-size 4M`
  or at load time with `insmod mod.ko heap_size=4M` (see `modinfo -p`).
  Collections run when an allocation doesn't fit. If the heap is still full
  after collecting, the module panics with `out of memory`, so size the heap
  for your live data. TinyGo rounds allocations up to 32-byte blocks, plus
  some metadata.

For big or long-lived pointer-free data, there are **manually managed
off-heap buffers**. They don't use GC heap space and aren't scanned:

```go
buf := kernel.AllocBytes(1 << 20) // zeroed, nil on failure
defer kernel.FreeBytes(buf)
```

Off-heap buffers must never hold Go pointers. The GC doesn't look inside
them, so an object referenced only from a buffer would be collected. Freeing a
buffer twice, or freeing something AllocBytes didn't return, logs a warning
instead of corrupting memory. Buffers still allocated at rmmod are freed.

## How it works

`gokm` embeds everything it needs (`internal/assets/_files`) and extracts it
into the cache:

```
your *.go ──tinygo build──▶ go.ll ──IR fixups──▶ tinygo clang ──▶ go.o
                                                                    │
           C shim (init/exit, printk, kvzalloc heap) + go.o ──kbuild──▶ name.ko
```

- **`target.json`**: TinyGo target for code inside the kernel. Kernel code
  model, static relocations, soft-float (no SSE/x87), retpolines, no
  goroutines, conservative GC.
- **`overlay/`**: a runtime platform layer added to a symlink mirror of
  TinyGo's source tree. `putchar` goes to printk, time comes from ktime,
  `abort` calls `BUG()`, `interrupt.Disable` uses `local_irq_save`, and the GC
  heap and stack bounds come from the shim. `baremetal_memory.go`, which
  expects them from a linker script, is excluded from this target.
- **IR fixups** (`internal/build/fixup.go`): kernel ABI details TinyGo can't
  express: `noredzone`, return thunks (RETHUNK), `endbr64` for IBT, no unwind
  tables, and `noreturn` abort hooks for objtool.
- **`kmod/shim.c`**: module boilerplate, the GC heap (`heap_size` parameter),
  and the `gokm_*` services Go calls. Each call into Go passes the C frame
  address as the GC's stack top. `tinygo_scanCurrentStack` spills
  callee-saved registers so the GC sees pointers held only in registers.
- The object is stripped of debug info (pahole can't turn Go type names like
  `[]byte` into BTF) and all of its symbols except the two entry points are
  made local.

## Limitations

- **Fixed-size GC heap.** It can't grow after load, and running out of
  memory panics.
- **No goroutines.** Go runs on the caller's ~16 KB kernel stack.
- **No floating point.** Using it fails at modpost with `__adddf3 undefined`
  or similar, which also rules out `fmt`.
- **Process context only.** Go code runs only from init/exit, and the runtime
  (including the GC) isn't reentrant across CPUs. Calling Go from other
  contexts would need a lock around every entry.
- **Panics** print via printk and then `BUG()`.
- x86-64 only. The module license is always GPL.
- It relies on TinyGo runtime internals (`go:linkname`, the GC's platform
  hooks) that can change between TinyGo versions.

## Developing gokm

```sh
make test                   # go vet + unit tests
make qemu                   # build examples/hello and examples/gcstress (256K heap),
                            # boot /boot/vmlinuz-linux in QEMU, load + unload them
```
