---
title: "Command line"
description: "gokm init and build, and their flags."
weight: 3
---

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
