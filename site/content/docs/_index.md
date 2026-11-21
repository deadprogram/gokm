---
title: "Documentation"
description: "Everything about building Linux kernel modules in Go with gokm."
weight: 1
cards: false
---

`gokm` builds Linux kernel modules (x86-64) from plain Go code, using TinyGo.
A project is just Go files: filesystems, character devices, `/proc` files and
network devices are written against Go packages, and gokm supplies all the
kernel-side code. Goroutines, channels, `sync`, `fmt`, maps, `defer`/`recover`
and floating point all work, and Go code runs on all CPUs in parallel.

> [!WARNING]
> gokm is **experimental**. Don't use it in production: the modules it
> builds run inside the kernel, where a bug in the generated code or the Go
> runtime can crash the machine or corrupt data, and the API may change
> without notice.

## Start here

- [Installation](/docs/install/): requirements and installing the `gokm` command.
- [Tutorial](/docs/tutorial/): a "hello world" module in ten minutes.

## Guides

- [Command line](/docs/commands/): `gokm init` and `gokm build`.
- [Packages](/docs/packages/): filesystems, devices, `/proc` files, network interfaces, block devices, module parameters and kernel bindings.
- [Memory](/docs/memory/): the Go heap inside the kernel.
- [Concurrency](/docs/concurrency/): parallel Go, goroutines and atomic context.
- [Examples](/docs/examples/): complete modules to learn from.
- [C in csrc/](/docs/csrc/): the escape hatch.

## Reference

- [API reference](/api/): every exported identifier of the Go packages.
- [How it works](/docs/internals/): the build pipeline and the runtime.
- [Limitations](/docs/limitations/): what isn't supported.
- [Development](/docs/development/): building gokm itself and running its tests.
