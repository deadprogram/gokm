---
title: "Packages"
description: "The Go packages kernel modules are written against."
weight: 4
---

| Package        | What it's for                                                          |
|----------------|------------------------------------------------------------------------|
| [`gokm/kernel`](/docs/packages/kernel/)  | logging, module lifecycle, errors, handles, memory, `SpinLock`         |
| [`gokm/fs`](/docs/packages/fs/)      | filesystems (mmap and xattrs included): `fs.Register`                  |
| [`gokm/chardev`](/docs/packages/chardev-procfs/) | character devices (`/dev/<name>`), ioctl, poll: `chardev.Register`     |
| [`gokm/procfs`](/docs/packages/chardev-procfs/)  | `/proc` files: `procfs.Create`                                         |
| [`gokm/netdev`](/docs/packages/netdev/)  | network interfaces (IP like TUN, or Ethernet like TAP): `netdev.Register` |
| [`gokm/blk`](/docs/packages/blk/)     | block devices (`/dev/<name>`, partitions included): `blk.Register`     |
| [`gokm/linux`](/docs/packages/linux/)   | generated bindings to any kernel function, struct or constant          |

Importing a framework package is all it takes: `gokm build` detects it and
compiles in its kernel glue. One module can use several (see
[`examples/devices`](https://github.com/jespino/gokm/tree/main/examples/devices): a char device and a `/proc` file sharing state).
