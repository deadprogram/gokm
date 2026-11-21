---
title: "gokm/linux"
linkTitle: "linux"
description: "Bindings to any kernel function, struct or constant, generated from the kernel's BTF."
weight: 7
---

## gokm/linux: bindings generated from the kernel's BTF

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
