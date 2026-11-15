// devices shows two device frameworks in one pure-Go module:
//
//   - /dev/gorev (gokm/chardev): write text to it, read it back reversed.
//   - /proc/gorev (gokm/procfs): usage statistics; write "reset" to clear.
//
// /dev/gorev also answers two ioctls: GOREV_CLEAR (_IO('g', 1)) clears the
// message, GOREV_LEN (_IOR('g', 2, uint32)) returns its length.
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
	if _, err := procfs.Create("gorev", statsFile{}); err != nil {
		dev.Deregister() // exit hooks don't run when loading fails
		kernel.FailLoad(err)
		return
	}
	kernel.Print("created /dev/gorev and /proc/gorev")
}
