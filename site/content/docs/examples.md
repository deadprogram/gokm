---
title: "Examples"
description: "Complete kernel modules built with gokm, each with a test program."
weight: 7
---

| Example               | Shows                                                                |
|-----------------------|----------------------------------------------------------------------|
| [`examples/hello`](https://github.com/jespino/gokm/tree/main/examples/hello)      | the basics                                                           |
| [`examples/gcstress`](https://github.com/jespino/gokm/tree/main/examples/gcstress)   | the GC under pressure                                                |
| [`examples/gofs`](https://github.com/jespino/gokm/tree/main/examples/gofs)       | a filesystem (`gokm/fs`)                                             |
| [`examples/devices`](https://github.com/jespino/gokm/tree/main/examples/devices)    | `/dev/gorev` with ioctls, `/dev/gopipe` with poll/blocking I/O, `/proc/gorev` |
| [`examples/gonet`](https://github.com/jespino/gokm/tree/main/examples/gonet)      | an IP device answering ping/UDP echo in atomic context, and an Ethernet one with ARP |
| [`examples/gobrd`](https://github.com/jespino/gokm/tree/main/examples/gobrd)      | a RAM disk, ported from Linux's brd (`gokm/blk`, module parameters)  |
| [`examples/sysinfo`](https://github.com/jespino/gokm/tree/main/examples/sysinfo)    | `gokm/linux`: functions, structs, inline functions, by-value structs, macros, pragmas |

Each has a test program that `make qemu` runs inside a VM against the loaded
module; [`test/fixtures`](https://github.com/jespino/gokm/tree/main/test/fixtures) cover load failures, nested and parked entries,
parallelism, goroutines, heap growth and atomic-context misuse.
