// devices shows two device frameworks in one pure-Go module:
//
//   - /dev/gorev (gokm/chardev): write text to it, read it back reversed.
//   - /proc/gorev (gokm/procfs): usage statistics; write "reset" to clear.
//
// /dev/gorev also answers two ioctls: GOREV_CLEAR (_IO('g', 1)) clears the
// message, GOREV_LEN (_IOR('g', 2, uint32)) returns its length.
//
// /dev/gopipe is a pipe (4 KiB): reads wait for data and writes for room,
// unless O_NONBLOCK (EAGAIN), and it works with poll, select and epoll.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"

	"gokm/chardev"
	"gokm/kernel"
	"gokm/procfs"
)

// mu protects stats and message: device and /proc callbacks run
// concurrently on several CPUs.
var (
	mu    sync.Mutex
	stats struct{ opens, writes, bytesIn, bytesOut int }
)

// message is the last text written, reversed.
var message []byte

// session is one open of /dev/gorev. It reads the message as it was when the
// file was opened, so concurrent writers don't mix messages mid-read.
type session struct{ snapshot []byte }

func (*session) Write(p []byte, off int64) (int, error) {
	text := bytes.TrimRight(p, "\n")
	rev := make([]byte, len(text), len(text)+1)
	for i, b := range text {
		rev[len(text)-1-i] = b
	}
	mu.Lock()
	defer mu.Unlock()
	message = append(rev, '\n')
	stats.writes++
	stats.bytesIn += len(p)
	return len(p), nil
}

func (s *session) Read(p []byte, off int64) (int, error) {
	if off >= int64(len(s.snapshot)) {
		return 0, nil
	}
	n := copy(p, s.snapshot[off:])
	mu.Lock()
	stats.bytesOut += n
	mu.Unlock()
	return n, nil
}

var (
	ioctlClear = chardev.IO('g', 1)
	ioctlLen   = chardev.IOR('g', 2, 4)
)

func (*session) Ioctl(cmd uint32, arg uintptr) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	switch cmd {
	case ioctlClear:
		message = nil
		return 0, nil
	case ioctlLen:
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(len(message)))
		return 0, chardev.CopyToUser(arg, b[:])
	}
	return 0, kernel.ENOTTY
}

type statsFile struct{}

func (statsFile) Read() ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	return []byte(fmt.Sprintf("opens: %d\nwrites: %d\nbytes in: %d\nbytes out: %d\n",
		stats.opens, stats.writes, stats.bytesIn, stats.bytesOut)), nil
}

func (statsFile) Write(p []byte) error {
	if string(bytes.TrimSpace(p)) != "reset" {
		return kernel.EINVAL
	}
	mu.Lock()
	defer mu.Unlock()
	stats.opens, stats.writes, stats.bytesIn, stats.bytesOut = 0, 0, 0, 0
	return nil
}

const pipeCap = 4096

var (
	pipeDev *chardev.Device
	pipeMu  sync.Mutex
	pipeBuf []byte // at most pipeCap bytes
)

type pipeFile struct{}

func pipeLen() int {
	pipeMu.Lock()
	defer pipeMu.Unlock()
	return len(pipeBuf)
}

func (pipeFile) ReadFlags(p []byte, off int64, fl chardev.Flags) (int, error) {
	for {
		if err := pipeDev.Wait(fl, func() bool { return pipeLen() > 0 }); err != nil {
			return 0, err
		}
		pipeMu.Lock()
		n := copy(p, pipeBuf)
		pipeBuf = append(pipeBuf[:0], pipeBuf[n:]...)
		pipeMu.Unlock()
		if n > 0 {
			pipeDev.Wake() // room for writers
			return n, nil
		}
	}
}

func (pipeFile) WriteFlags(p []byte, off int64, fl chardev.Flags) (int, error) {
	for {
		if err := pipeDev.Wait(fl, func() bool { return pipeLen() < pipeCap }); err != nil {
			return 0, err
		}
		pipeMu.Lock()
		n := min(len(p), pipeCap-len(pipeBuf))
		pipeBuf = append(pipeBuf, p[:n]...)
		pipeMu.Unlock()
		if n > 0 {
			pipeDev.Wake() // data for readers
			return n, nil
		}
	}
}

func (pipeFile) Poll() chardev.PollEvents {
	var ev chardev.PollEvents
	n := pipeLen()
	if n > 0 {
		ev |= chardev.PollIn
	}
	if n < pipeCap {
		ev |= chardev.PollOut
	}
	return ev
}

func main() {
	dev := &chardev.Device{
		Name: "gorev",
		Mode: 0o666,
		Opener: chardev.OpenFunc(func() (chardev.File, error) {
			mu.Lock()
			defer mu.Unlock()
			stats.opens++
			return &session{snapshot: message}, nil
		}),
	}
	if err := chardev.Register(dev); err != nil {
		kernel.FailLoad(err)
		return
	}
	pipeDev = &chardev.Device{
		Name:   "gopipe",
		Mode:   0o666,
		Opener: chardev.OpenFunc(func() (chardev.File, error) { return pipeFile{}, nil }),
	}
	if err := chardev.Register(pipeDev); err != nil {
		dev.Deregister()
		kernel.FailLoad(err)
		return
	}
	if _, err := procfs.Create("gorev", statsFile{}); err != nil {
		dev.Deregister() // exit hooks don't run when loading fails
		pipeDev.Deregister()
		kernel.FailLoad(err)
		return
	}
	kernel.Print("created /dev/gorev, /dev/gopipe and /proc/gorev")
}
