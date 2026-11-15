// gonet is a network device written in Go (gokm/netdev): goecho0, a
// point-to-point IPv4 interface whose "remote end" is this driver. It answers
// ICMP echo requests (ping) and UDP datagrams to port 7 (echo), and reports
// counters in /proc/goecho (gokm/procfs).
//
//	sudo insmod gonet.ko
//	sudo ip addr add 10.99.0.1 peer 10.99.0.2 dev goecho0
//	sudo ip link set goecho0 up
//	ping 10.99.0.2
//
// It also creates goeth0, an Ethernet interface (MAC 02:00:00:00:00:01) whose
// other end is a simulated host, 10.99.1.2 at 02:00:00:00:00:02: it answers
// ARP requests and the same echo services, so the kernel's ARP resolution
// runs over a Go driver:
//
//	sudo ip addr add 10.99.1.1/24 dev goeth0
//	sudo ip link set goeth0 up
//	ping 10.99.1.2
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
	if reply := e.reply(pkt); reply != nil {
		if err := e.iface.Receive(reply); err != nil {
			e.dropped.Add(1)
		}
	}
}

// reply returns the echo service's answer to an IPv4 packet, or nil.
func (e *echo) reply(pkt []byte) []byte {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		e.dropped.Add(1) // not IPv4
		return nil
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+8 {
		e.dropped.Add(1)
		return nil
	}
	reply := append([]byte(nil), pkt...) // pkt is only valid during the call
	swap(reply[12:16], reply[16:20])     // src <-> dst (IP checksum unchanged)

	switch reply[9] {
	case protoICMP:
		icmp := reply[ihl:]
		if icmp[0] != 8 { // echo request
			e.dropped.Add(1)
			return nil
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
			return nil
		}
		swap(udp[0:2], udp[2:4]) // ports (UDP checksum unchanged)
		e.udp.Add(1)
	default:
		e.dropped.Add(1)
		return nil
	}
	return reply
}

// The simulated host at the other end of goeth0.
var (
	ethLocalMAC = [6]byte{0x02, 0, 0, 0, 0, 0x01}
	peerMAC     = [6]byte{0x02, 0, 0, 0, 0, 0x02}
	peerIP      = [4]byte{10, 99, 1, 2}
)

const (
	etherIPv4 = 0x0800
	etherARP  = 0x0806
)

// ethPeer drives goeth0: frames the kernel sends reach the simulated host.
type ethPeer struct {
	iface *netdev.Interface
	echo  *echo // shares the echo service (and counters) with goecho0
	arps  atomic.Int64
}

func (p *ethPeer) Transmit(f []byte) {
	if len(f) < 14 {
		p.echo.dropped.Add(1)
		return
	}
	var out []byte
	switch int(f[12])<<8 | int(f[13]) {
	case etherARP:
		out = p.arpReply(f)
	case etherIPv4:
		if [6]byte(f[0:6]) == peerMAC {
			if ip := p.echo.reply(f[14:]); ip != nil {
				out = frame(f[6:12], etherIPv4, ip)
			}
		}
	}
	if out == nil {
		return // not for us (IPv6 neighbor discovery, broadcasts, ...)
	}
	if err := p.iface.Receive(out); err != nil {
		p.echo.dropped.Add(1)
	}
}

// arpReply answers "who has 10.99.1.2?" (RFC 826).
func (p *ethPeer) arpReply(f []byte) []byte {
	arp := f[14:]
	if len(arp) < 28 || arp[1] != 1 || arp[2] != 0x08 || arp[3] != 0 || arp[7] != 1 ||
		[4]byte(arp[24:28]) != peerIP {
		return nil
	}
	reply := make([]byte, 28)
	copy(reply, arp[:6]) // hardware/protocol types and lengths
	reply[7] = 2         // reply
	copy(reply[8:14], peerMAC[:])
	copy(reply[14:18], peerIP[:])
	copy(reply[18:28], arp[8:18]) // the asker's MAC and IP
	p.arps.Add(1)
	return frame(arp[8:14], etherARP, reply)
}

// frame builds an Ethernet frame from the simulated host to dst.
func frame(dst []byte, etherType int, payload []byte) []byte {
	f := make([]byte, 14+len(payload))
	copy(f[0:6], dst)
	copy(f[6:12], peerMAC[:])
	f[12], f[13] = byte(etherType>>8), byte(etherType)
	copy(f[14:], payload)
	return f
}

func macString(m [6]byte) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 0, 17)
	for i, c := range m {
		if i > 0 {
			b = append(b, ':')
		}
		b = append(b, hex[c>>4], hex[c&15])
	}
	return string(b)
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
	// goeth0: Transmit from the workqueue (the default).
	p := &ethPeer{echo: e}
	p.iface = &netdev.Interface{Name: "goeth%d", Driver: p, Ethernet: true, MAC: ethLocalMAC}
	if err := netdev.Register(p.iface); err != nil {
		e.iface.Unregister()
		kernel.FailLoad(err)
		return
	}
	_, err := procfs.Create("goecho", procfs.ReadFunc(func() ([]byte, error) {
		direct, deferred := e.iface.TransmitStats()
		return []byte(fmt.Sprintf("interface: %s\nopened: %d\npings: %d\nudp: %d\ndropped: %d\n"+
			"transmit_direct: %d\ntransmit_deferred: %d\nethernet: %s %s\narp_replies: %d\n",
			e.iface.Name, e.opened.Load(), e.pings.Load(), e.udp.Load(), e.dropped.Load(),
			direct, deferred, p.iface.Name, macString(p.iface.HardwareAddr()), p.arps.Load())), nil
	}))
	if err != nil {
		e.iface.Unregister() // exit hooks don't run when loading fails
		p.iface.Unregister()
		kernel.FailLoad(err)
		return
	}
	kernel.Print("created interfaces " + e.iface.Name + " and " + p.iface.Name)
}
