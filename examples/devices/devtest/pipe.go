package main

// /dev/gopipe: blocking and non-blocking I/O, poll, epoll, and a blocked
// reader that must be killable. Files are opened with syscall.Open: os.Open
// would register the (now pollable) device with Go's netpoller and make it
// non-blocking behind our back.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

const pipeDev = "/dev/gopipe"

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

const (
	pollIn  = 0x1
	pollOut = 0x4
)

func poll(fd int, events int16, timeoutMs int) (int16, error) {
	p := pollFd{fd: int32(fd), events: events}
	n, _, e := syscall.Syscall(syscall.SYS_POLL, uintptr(unsafe.Pointer(&p)), 1, uintptr(timeoutMs))
	if e != 0 {
		return 0, e
	}
	if n == 0 {
		return 0, nil
	}
	return p.revents, nil
}

func blockRead() {
	fd, err := syscall.Open(pipeDev, syscall.O_RDONLY, 0)
	if err != nil {
		fmt.Println("block-read:", err)
		os.Exit(2)
	}
	var b [16]byte
	n, err := syscall.Read(fd, b[:])
	fmt.Println("block-read returned", n, err)
	os.Exit(3)
}

func pipeTests() {
	open := func(flags int) (int, error) { return syscall.Open(pipeDev, flags, 0) }

	check("pipe: empty: no POLLIN, POLLOUT, O_NONBLOCK read is EAGAIN", func() error {
		fd, err := open(syscall.O_RDWR | syscall.O_NONBLOCK)
		if err != nil {
			return err
		}
		defer syscall.Close(fd)
		if ev, err := poll(fd, pollIn|pollOut, 0); err != nil || ev != pollOut {
			return fmt.Errorf("poll = %#x, %v", ev, err)
		}
		var b [8]byte
		if _, err := syscall.Read(fd, b[:]); err != syscall.EAGAIN {
			return fmt.Errorf("read: %v", err)
		}
		return nil
	}())

	check("pipe: blocking read waits for a writer", func() error {
		rfd, err := open(syscall.O_RDONLY)
		if err != nil {
			return err
		}
		defer syscall.Close(rfd)
		type result struct {
			data []byte
			err  error
			at   time.Duration
		}
		start := time.Now()
		done := make(chan result)
		go func() {
			b := make([]byte, 64)
			n, err := syscall.Read(rfd, b)
			if n < 0 {
				n = 0
			}
			done <- result{b[:n], err, time.Since(start)}
		}()
		time.Sleep(150 * time.Millisecond)
		wfd, err := open(syscall.O_WRONLY)
		if err != nil {
			return err
		}
		defer syscall.Close(wfd)
		if _, err := syscall.Write(wfd, []byte("hello pipe")); err != nil {
			return err
		}
		select {
		case r := <-done:
			if r.err != nil || string(r.data) != "hello pipe" || r.at < 150*time.Millisecond {
				return fmt.Errorf("read %q, %v after %v", r.data, r.err, r.at)
			}
		case <-time.After(3 * time.Second):
			return fmt.Errorf("reader never woke up")
		}
		return nil
	}())

	check("pipe: poll wakes on write, epoll too", func() error {
		rfd, err := open(syscall.O_RDONLY | syscall.O_NONBLOCK)
		if err != nil {
			return err
		}
		defer syscall.Close(rfd)
		wfd, err := open(syscall.O_WRONLY)
		if err != nil {
			return err
		}
		defer syscall.Close(wfd)

		go func() {
			time.Sleep(100 * time.Millisecond)
			syscall.Write(wfd, []byte("x"))
		}()
		start := time.Now()
		ev, err := poll(rfd, pollIn, 3000)
		if err != nil || ev&pollIn == 0 || time.Since(start) < 100*time.Millisecond {
			return fmt.Errorf("poll = %#x, %v after %v", ev, err, time.Since(start))
		}
		var b [8]byte
		if n, err := syscall.Read(rfd, b[:]); n != 1 || err != nil {
			return fmt.Errorf("read %d, %v", n, err)
		}

		ep, err := syscall.EpollCreate1(0)
		if err != nil {
			return err
		}
		defer syscall.Close(ep)
		if err := syscall.EpollCtl(ep, syscall.EPOLL_CTL_ADD, rfd,
			&syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(rfd)}); err != nil {
			return err
		}
		evs := make([]syscall.EpollEvent, 1)
		if n, _ := syscall.EpollWait(ep, evs, 0); n != 0 {
			return fmt.Errorf("epoll: ready while empty")
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			syscall.Write(wfd, []byte("yz"))
		}()
		n, err := syscall.EpollWait(ep, evs, 3000)
		if err != nil || n != 1 || evs[0].Fd != int32(rfd) || evs[0].Events&syscall.EPOLLIN == 0 {
			return fmt.Errorf("epoll_wait = %d %+v, %v", n, evs[0], err)
		}
		if n, _ := syscall.Read(rfd, b[:]); n != 2 {
			return fmt.Errorf("read %d after epoll", n)
		}
		return nil
	}())

	check("pipe: full: no POLLOUT, EAGAIN; reading makes room; writer unblocks", func() error {
		fd, err := open(syscall.O_RDWR | syscall.O_NONBLOCK)
		if err != nil {
			return err
		}
		defer syscall.Close(fd)
		chunk := bytes.Repeat([]byte("0123456789"), 100)
		total := 0
		for {
			n, err := syscall.Write(fd, chunk)
			if err == syscall.EAGAIN {
				break
			}
			if err != nil {
				return err
			}
			total += n
		}
		if total != 4096 {
			return fmt.Errorf("pipe took %d bytes, want 4096", total)
		}
		if ev, _ := poll(fd, pollIn|pollOut, 0); ev != pollIn {
			return fmt.Errorf("full pipe poll = %#x", ev)
		}
		// A blocking writer waits until a reader drains.
		wfd, err := open(syscall.O_WRONLY)
		if err != nil {
			return err
		}
		defer syscall.Close(wfd)
		wrote := make(chan error)
		go func() {
			_, err := syscall.Write(wfd, []byte("tail"))
			wrote <- err
		}()
		select {
		case <-wrote:
			return fmt.Errorf("write to a full pipe didn't block")
		case <-time.After(150 * time.Millisecond):
		}
		got := make([]byte, 0, 4100)
		b := make([]byte, 1000)
		for len(got) < 4100 {
			n, err := syscall.Read(fd, b)
			if err == syscall.EAGAIN {
				if _, err := poll(fd, pollIn, 3000); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			got = append(got, b[:n]...)
		}
		if err := <-wrote; err != nil {
			return err
		}
		want := append(bytes.Repeat(chunk, 5)[:4096], "tail"...)
		if !bytes.Equal(got, want) {
			return fmt.Errorf("data out of order")
		}
		return nil
	}())

	check("pipe: a blocked reader can be killed", func() error {
		cmd := exec.Command("/proc/self/exe", "block-read")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		select {
		case err := <-exited:
			return fmt.Errorf("reader exited without data: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() {
				return fmt.Errorf("reader exited with %v", err)
			}
		case <-time.After(3 * time.Second):
			return fmt.Errorf("reader didn't die")
		}
		return nil
	}())
}
