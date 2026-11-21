---
title: "How it works"
description: "The build pipeline, the TinyGo target, the runtime overlay and the C runtime."
weight: 9
---

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
