---
title: "Module parameters"
description: "Package-level variables that become module parameters with //gokm:param."
weight: 6
---

## Module parameters

```go
//gokm:param rd_size Size of each RAM disk in kbytes.
var rdSize uint = 4096
```

A package-level variable in package `main` becomes a module parameter:
`insmod gobrd.ko rd_size=65536`, `modinfo`, and read-only in
`/sys/module/<name>/parameters/`. The initializer (a constant expression)
is the default. The loaded value is in the variable when `main` and the
`init` functions run. Supported types: `int`, `int64`, `int32`, `int16`,
`uint`, `uint64`, `uint32`, `uint16`, `byte`, `bool` and `string`.
