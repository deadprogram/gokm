---
title: "gokm/netdev"
linkTitle: "netdev"
description: "Network interfaces whose driver is Go code: IP (like TUN) or Ethernet (like TAP)."
weight: 4
---

API reference: [gokm/netdev](/api/netdev/).

## gokm/netdev

```go
type echo struct{ iface *netdev.Interface }

func (e *echo) Transmit(pkt []byte) { e.iface.Receive(makeReply(pkt)) } // IPv4/IPv6 packets

e.iface = &netdev.Interface{Name: "goecho%d", Driver: e, AtomicTransmit: true}
netdev.Register(e.iface)
```

By default `Transmit` runs from a workqueue (process context). With
`AtomicTransmit` it runs directly in the kernel's transmit path (atomic
context): it must not block (use atomics or `kernel.SpinLock`). While a
garbage collection is in progress packets fall back to the workqueue.
With `Ethernet: true` the interface is an Ethernet device (like TAP):
`Transmit` and `Receive` carry whole frames, it has a MAC address (`MAC`,
random if zero; `HardwareAddr()` reads the current one), and the kernel
runs ARP and IPv6 neighbor discovery over it. `examples/gonet` has both
kinds. `goecho0` answers ping and UDP echo (20000-packet floods included).
`goeth0` simulates a host on an Ethernet link that answers ARP.
