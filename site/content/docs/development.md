---
title: "Development"
description: "Building gokm itself and running its unit and QEMU tests."
weight: 11
---

```sh
make            # build ./gokm
make test       # go vet + unit tests (BTF parsing and binding generation use the running kernel's BTF)
make qemu       # build every example and fixture and run each in a QEMU VM with its test program
make site-serve # preview this website at http://localhost:1313/gokm/
```

`make qemu` boots each module in its own QEMU VM (`KERNEL=` picks the
kernel image, default `/boot/vmlinuz-linux`, and `KVER` must match it), runs
the example's test program against it and requires `TEST: PASS` plus the
example's expected lines in the log, kept in `_qemu/<name>.log`.

## This website

The site lives in `site/`: a [Hugo](https://gohugo.io/) site whose API
reference pages are generated from the packages' doc comments by
`site/apidoc`. `make site` builds it into `site/public/`, and a GitHub
Actions workflow publishes it to GitHub Pages on every push to `main`.
