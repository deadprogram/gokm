---
title: "gokm"
heroTitle: 'Linux kernel modules in <span class="accent">Go</span>'
lead: "gokm builds Linux kernel modules from plain Go code, using TinyGo. Write filesystems, character devices, `/proc` files, network interfaces and block devices against Go packages: gokm supplies all the kernel-side code."
warning: "gokm is experimental. Don't use it in production: the modules it builds run inside the kernel, where a bug in the generated code or the Go runtime can crash the machine or corrupt data, and the API may change without notice."
actions:
  - label: "Get started"
    url: "/docs/tutorial/"
    primary: true
  - label: "Documentation"
    url: "/docs/"
  - label: "GitHub"
    url: "https://github.com/jespino/gokm"
features:
  - title: "Real Go"
    text: "Goroutines, channels, `select`, `sync`, timers, `fmt`, maps, `defer`/`recover` and floating point all work inside the kernel."
  - title: "Parallel"
    text: "Kernel callbacks run Go on whatever CPU they arrive on, concurrently. Goroutines are kernel threads."
  - title: "Device frameworks"
    text: "Filesystems with mmap and xattrs, character devices with ioctl and poll, `/proc` files, network interfaces and block devices."
  - title: "Any kernel API"
    text: "`gokm/linux` generates bindings to any kernel function, struct or constant from the running kernel's BTF."
  - title: "Garbage collected"
    text: "A growable, garbage-collected heap with a background collector. Kernel memory never holds Go pointers."
  - title: "Built for the kernel"
    text: "Retpolines, IBT, soft-float, the kernel code model and stack-depth analysis: modules are built with kbuild, like any other."
---

## Hello, world

```go
package main

import "gokm/kernel"

func main() { // runs at insmod
	kernel.Print("hello from Go!")
	kernel.OnExit(func() { kernel.Print("goodbye from Go!") }) // runs at rmmod
}
```

```sh
gokm init hello          # creates hello/go.mod and hello/main.go
gokm build hello         # produces hello/hello.ko
sudo insmod hello/hello.ko && sudo rmmod hello && sudo dmesg | tail -2
# hello: hello from Go!
# hello: goodbye from Go!
```

The [tutorial](/docs/tutorial/) walks through it step by step.

## A sibling project

[gopgext](https://jespino.github.io/gopgext/) uses the same approach to
build PostgreSQL extensions from Go: a custom TinyGo target and runtime
layer for the host, a C runtime compiled by the host's own build system
(kbuild here, PGXS there), and generated glue.
