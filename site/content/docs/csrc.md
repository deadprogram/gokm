---
title: "C in csrc/"
description: "The escape hatch: C files compiled into the module."
weight: 8
---

For anything not covered, `.c`/`.h` files in a project's `csrc/` directory
are compiled into the module. They include `gokm.h` and call into Go with
`GOKM_ENTER()`/`GOKM_LEAVE()` (and `GOKM_TRY_ENTER_ATOMIC()`,
`GOKM_PARK()`/`GOKM_UNPARK()`; see the comments there). No example needs it.
