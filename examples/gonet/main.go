// gonet is a network device written in Go (gokm/netdev): goecho0, a
// point-to-point IPv4 interface whose "remote end" is this driver. It answers
// ICMP echo requests (ping) and UDP datagrams to port 7 (echo), and reports
// counters in /proc/goecho (gokm/procfs).
//
//	sudo insmod gonet.ko
//	sudo ip addr add 10.99.0.1 peer 10.99.0.2 dev goecho0
//	sudo ip link set goecho0 up
//	ping 10.99.0.2
package main

import (
	"fmt"
	"sync/atomic"

	"gokm/kernel"
	"gokm/netdev"
	"gokm/procfs"
)

const (
	protoICMP = 1
	protoUDP  = 17
	echoPort  = 7
)

// Counters are atomic: Transmit runs concurrently with /proc reads (and,
// with AtomicTransmit, in softirq context where a mutex can't be used).
type echo struct {
	iface                       *netdev.Interface
	pings, udp, dropped, opened atomic.Int64
}

func (e *echo) Open() error { e.opened.Add(1); return nil }

// Transmit gets every packet the kernel sends through goecho0 and answers
// the ones addressed to the echo services.
func (e *echo) Transmit(pkt []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		e.dropped.Add(1) // not IPv4
		return
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+8 {
		e.dropped.Add(1)
		return
	}
	reply := append([]byte(nil), pkt...) // pkt is only valid during the call
	swap(reply[12:16], reply[16:20])     // src <-> dst (IP checksum unchanged)

	switch reply[9] {
	case protoICMP:
		icmp := reply[ihl:]
		if icmp[0] != 8 { // echo request
			e.dropped.Add(1)
			return
		}
		icmp[0] = 0 // echo reply
		icmp[2], icmp[3] = 0, 0
		sum := checksum(icmp)
		icmp[2], icmp[3] = byte(sum>>8), byte(sum)
		e.pings.Add(1)
	case protoUDP:
		udp := reply[ihl:]
		if dport := int(udp[2])<<8 | int(udp[3]); dport != echoPort {
			e.dropped.Add(1)
			return
		}
		swap(udp[0:2], udp[2:4]) // ports (UDP checksum unchanged)
		e.udp.Add(1)
	default:
		e.dropped.Add(1)
		return
	}
	if err := e.iface.Receive(reply); err != nil {
		e.dropped.Add(1)
	}
}

func swap(a, b []byte) {
	for i := range a {
		a[i], b[i] = b[i], a[i]
	}
}

// checksum is the Internet checksum (RFC 1071).
func checksum(b []byte) uint16 {
	var sum uint32
	for ; len(b) >= 2; b = b[2:] {
		sum += uint32(b[0])<<8 | uint32(b[1])
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	return ^uint16(sum)
}

func main() {
	e := &echo{}
	// AtomicTransmit: answer packets right in the kernel's transmit path
	// (Transmit only uses atomics, never blocks).
	e.iface = &netdev.Interface{Name: "goecho%d", Driver: e, AtomicTransmit: true}
	if err := netdev.Register(e.iface); err != nil {
		kernel.FailLoad(err)
		return
	}
	_, err := procfs.Create("goecho", procfs.ReadFunc(func() ([]byte, error) {
		direct, deferred := e.iface.TransmitStats()
		return []byte(fmt.Sprintf("interface: %s\nopened: %d\npings: %d\nudp: %d\ndropped: %d\n"+
			"transmit_direct: %d\ntransmit_deferred: %d\n",
			e.iface.Name, e.opened.Load(), e.pings.Load(), e.udp.Load(), e.dropped.Load(),
			direct, deferred)), nil
	}))
	if err != nil {
		e.iface.Unregister() // exit hooks don't run when loading fails
		kernel.FailLoad(err)
		return
	}
	kernel.Print("created interface " + e.iface.Name)
}
