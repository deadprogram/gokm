// Package netdev creates network interfaces whose driver is Go code.
//
// The interfaces are point-to-point IP devices (no link-layer header, no
// ARP), like a TUN device: Transmit gets each IPv4/IPv6 packet the kernel
// routes out of the interface, and Receive injects packets into the kernel
// as if they arrived on it.
//
//	type driver struct{ iface *netdev.Interface }
//
//	func (d *driver) Transmit(pkt []byte) {
//		reply := handle(pkt)     // pkt is only valid during the call
//		d.iface.Receive(reply)
//	}
//
//	d := &driver{}
//	d.iface = &netdev.Interface{Name: "go%d", Driver: d}
//	err := netdev.Register(d.iface)
//
// By default Transmit runs in process context, from a workqueue: packets are
// queued (up to 1000; more are dropped and counted as tx_dropped). With
// AtomicTransmit it runs directly in the kernel's transmit path (atomic
// context, BH disabled): much lower latency, but Transmit must not block
// (no sync.Mutex, channels, time.Sleep or blocking calls: use atomics or
// kernel.SpinLock), and an allocation can fail with an "out of memory"
// panic if the heap is full (the packet is then dropped). While a garbage
// collection is in progress packets take the workqueue path instead.
// Interfaces are unregistered automatically at module exit.
package netdev

import (
	"unsafe"

	"gokm/kernel"
)

// Driver handles packets sent through the interface.
type Driver interface {
	// Transmit receives an outgoing IP packet. pkt is kernel memory valid
	// only during the call: copy what you keep.
	Transmit(pkt []byte)
}

// Optional Driver interfaces, called when the interface is brought up and
// down (ip link set ... up/down).
type (
	Opener  interface{ Open() error }
	Stopper interface{ Stop() error }
)

// Interface is a network interface.
type Interface struct {
	// Name is the interface name; a "%d" in it is replaced with the first
	// free number (e.g. "go%d" -> go0).
	Name   string
	Driver Driver
	// MTU defaults to 1500.
	MTU int
	// AtomicTransmit runs Transmit directly in the transmit path; see the
	// package documentation.
	AtomicTransmit bool

	ptr    uintptr
	handle kernel.Handle
}

// Implemented in the netdev glue (gokm/frameworks/netdev/netdev.c).
//
//export gokm_netdev_create
func cCreate(name *byte, mtu int, atomicXmit bool, handle uintptr, out *uintptr, outName *byte) int

//export gokm_netdev_stats
func cStats(ptr uintptr, direct, deferred *uint64)

//export gokm_netdev_destroy
func cDestroy(ptr uintptr)

//export gokm_netdev_rx
func cRx(ptr uintptr, pkt *byte, n int) int

// Register creates and registers the interface. Name is updated with the
// name the kernel assigned.
func Register(i *Interface) error {
	if i.Name == "" || i.Driver == nil || len(i.Name) >= 16 {
		return kernel.EINVAL
	}
	if i.ptr != 0 {
		return kernel.EBUSY
	}
	if i.MTU == 0 {
		i.MTU = 1500
	}
	i.handle = kernel.NewHandle(i)
	name := i.Name + "\x00"
	var assigned [16]byte
	if rc := cCreate(unsafe.StringData(name), i.MTU, i.AtomicTransmit, uintptr(i.handle), &i.ptr, &assigned[0]); rc != 0 {
		i.handle.Delete()
		i.ptr = 0
		return kernel.Errno(-rc)
	}
	for n, c := range assigned {
		if c == 0 {
			i.Name = string(assigned[:n])
			break
		}
	}
	kernel.OnExit(i.Unregister)
	return nil
}

// Receive injects an IPv4 or IPv6 packet into the kernel as if it arrived
// on the interface. The data is copied.
func (i *Interface) Receive(pkt []byte) error {
	if i.ptr == 0 {
		return kernel.ENODEV
	}
	if len(pkt) == 0 {
		return kernel.EINVAL
	}
	return errOf(cRx(i.ptr, &pkt[0], len(pkt)))
}

// TransmitStats returns how many packets Transmit got directly in the
// transmit path (AtomicTransmit) and how many through the workqueue.
func (i *Interface) TransmitStats() (direct, deferred uint64) {
	if i.ptr != 0 {
		cStats(i.ptr, &direct, &deferred)
	}
	return
}

// Unregister removes the interface.
func (i *Interface) Unregister() {
	if i.ptr == 0 {
		return
	}
	cDestroy(i.ptr)
	i.ptr = 0
	i.handle.Delete()
}

func errOf(rc int) error {
	if rc == 0 {
		return nil
	}
	return kernel.Errno(-rc)
}

func iface(h uintptr) *Interface { return kernel.Handle(h).Value().(*Interface) }

// Called by the glue between GOKM_ENTER and GOKM_LEAVE.

//export gokm_netdev_go_xmit
func goXmit(h uintptr, pkt *byte, n int) {
	defer kernel.CatchPanic[int](nil)
	iface(h).Driver.Transmit(unsafe.Slice(pkt, n))
}

//export gokm_netdev_go_open
func goOpen(h uintptr) (rc int) {
	defer kernel.CatchPanic(&rc)
	if o, ok := iface(h).Driver.(Opener); ok {
		return kernel.ToErrno(o.Open())
	}
	return 0
}

//export gokm_netdev_go_stop
func goStop(h uintptr) (rc int) {
	defer kernel.CatchPanic(&rc)
	if s, ok := iface(h).Driver.(Stopper); ok {
		return kernel.ToErrno(s.Stop())
	}
	return 0
}
