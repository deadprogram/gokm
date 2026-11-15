// Command nettest exercises goecho0 inside the QEMU VM: configures the
// interface with ioctls (no iproute2 in the VM), then pings it and sends UDP
// echo traffic, including from many goroutines at once.
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var failed bool

func check(name string, err error) {
	if err != nil {
		fmt.Printf("nettest: FAIL %s: %v\n", name, err)
		failed = true
		return
	}
	fmt.Printf("nettest: ok   %s\n", name)
}

const (
	ifname = "goecho0"
	local  = "10.99.0.1"
	remote = "10.99.0.2"
)

type ifreqAddr struct {
	Name [16]byte
	Addr syscall.RawSockaddrInet4
	_    [8]byte
}

type ifreqFlags struct {
	Name  [16]byte
	Flags uint16
	_     [22]byte
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func setAddr(fd int, req uintptr, ip string) error {
	var r ifreqAddr
	copy(r.Name[:], ifname)
	r.Addr.Family = syscall.AF_INET
	copy(r.Addr.Addr[:], net.ParseIP(ip).To4())
	return ioctl(fd, req, unsafe.Pointer(&r))
}

func configure() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := setAddr(fd, syscall.SIOCSIFADDR, local); err != nil {
		return fmt.Errorf("SIOCSIFADDR: %w", err)
	}
	if err := setAddr(fd, syscall.SIOCSIFDSTADDR, remote); err != nil {
		return fmt.Errorf("SIOCSIFDSTADDR: %w", err)
	}
	var f ifreqFlags
	copy(f.Name[:], ifname)
	if err := ioctl(fd, syscall.SIOCGIFFLAGS, unsafe.Pointer(&f)); err != nil {
		return fmt.Errorf("SIOCGIFFLAGS: %w", err)
	}
	f.Flags |= syscall.IFF_UP
	if err := ioctl(fd, syscall.SIOCSIFFLAGS, unsafe.Pointer(&f)); err != nil {
		return fmt.Errorf("SIOCSIFFLAGS: %w", err)
	}
	return nil
}

func ping(seq uint16) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_ICMP)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	tv := syscall.Timeval{Sec: 2}
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	msg := make([]byte, 8+32)
	msg[0] = 8 // echo request
	binary.BigEndian.PutUint16(msg[4:], 0x1234)
	binary.BigEndian.PutUint16(msg[6:], seq)
	copy(msg[8:], "gokm ping payload")
	binary.BigEndian.PutUint16(msg[2:], checksum(msg))

	dst := &syscall.SockaddrInet4{}
	copy(dst.Addr[:], net.ParseIP(remote).To4())
	if err := syscall.Sendto(fd, msg, 0, dst); err != nil {
		return err
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return fmt.Errorf("no reply: %w", err)
		}
		ihl := int(buf[0]&0x0f) * 4
		icmp := buf[ihl:n]
		if icmp[0] == 0 && binary.BigEndian.Uint16(icmp[4:]) == 0x1234 &&
			binary.BigEndian.Uint16(icmp[6:]) == seq {
			if checksum(icmp) != 0 {
				return fmt.Errorf("bad ICMP checksum in reply")
			}
			if string(icmp[8:8+17]) != "gokm ping payload" {
				return fmt.Errorf("payload %q", icmp[8:])
			}
			return nil
		}
	}
}

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

func udpEcho(msg string) error {
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(remote), Port: 7})
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		return err
	}
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	if string(buf[:n]) != msg {
		return fmt.Errorf("echo %q, want %q", buf[:n], msg)
	}
	return nil
}

func main() {
	check("configure "+ifname, configure())
	check("ping x3", func() error {
		for i := uint16(1); i <= 3; i++ {
			if err := ping(i); err != nil {
				return err
			}
		}
		return nil
	}())
	check("UDP echo", udpEcho("hello from userspace"))
	check("UDP echo 1400 bytes", udpEcho(strings.Repeat("x", 1400)))
	check("UDP echo: 16 goroutines x 50", func() error {
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for g := 0; g < 16; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					if err := udpEcho(fmt.Sprintf("g%d-i%d", g, i)); err != nil {
						errs <- err
						return
					}
				}
			}(g)
		}
		wg.Wait()
		close(errs)
		return <-errs
	}())
	check("UDP flood: 20000 datagrams", func() error {
		c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(remote), Port: 7})
		if err != nil {
			return err
		}
		defer c.Close()
		c.SetReadBuffer(8 << 20)
		got := make(chan int)
		go func() {
			n := 0
			buf := make([]byte, 2048)
			for {
				c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				if _, err := c.Read(buf); err != nil {
					break
				}
				n++
			}
			got <- n
		}()
		start := time.Now()
		payload := []byte(strings.Repeat("f", 200))
		for i := 0; i < 20000; i++ {
			c.Write(payload)
		}
		n := <-got
		fmt.Printf("nettest:      %d/20000 replies (sent in %v)\n", n, time.Since(start).Round(time.Millisecond))
		if n < 10000 {
			return fmt.Errorf("only %d replies", n)
		}
		return nil
	}())
	check("/proc/goecho counters", func() error {
		b, err := os.ReadFile("/proc/goecho")
		if err != nil {
			return err
		}
		fmt.Printf("nettest:      %s\n", strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", ", "))
		if !strings.Contains(string(b), "pings: 3\n") || !strings.Contains(string(b), "udp: 20802\n") ||
			strings.Contains(string(b), "transmit_direct: 0\n") {
			return fmt.Errorf("unexpected counters")
		}
		return nil
	}())
	if failed {
		os.Exit(1)
	}
}
