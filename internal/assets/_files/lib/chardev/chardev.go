// Package chardev creates character devices (/dev/<name>) backed by Go code.
// They're "misc" devices: the kernel picks the minor number and devtmpfs
// creates the /dev node.
//
//	type dev struct{}
//	func (dev) Open() (chardev.File, error) { return &session{}, nil }
//
//	type session struct{ ... }  // implements chardev.Reader / Writer / Releaser
//
//	err := chardev.Register(&chardev.Device{Name: "mydev", Mode: 0o666, Opener: dev{}})
//
// Each open() gets its own File. Read and Write receive kernel buffers (at
// most 64 KiB per call) and the file offset; the returned count advances it.
// Devices are deregistered automatically at module exit.
//
// Waiting for data (a device that produces data over time):
//
//	func (s *session) ReadFlags(p []byte, off int64, fl chardev.Flags) (int, error) {
//		err := dev.Wait(fl, func() bool { return len(queue) > 0 }) // EAGAIN, EINTR
//		...
//	}
//	func (s *session) Poll() chardev.PollEvents { ... chardev.PollIn ... }
//
//	// producer, e.g. a goroutine:
//	queue = append(queue, data); dev.Wake()
//
// Poll makes the device work with poll, select and epoll; Wake tells
// pollers and Wait-ers to look again.
package chardev

import (
	"io"
	"sync/atomic"
	"unsafe"

	"gokm/kernel"
)

// Opener creates the per-open state of a device.
type Opener interface {
	Open() (File, error)
}

// OpenFunc adapts a function to an Opener.
type OpenFunc func() (File, error)

func (f OpenFunc) Open() (File, error) { return f() }

// File is one open instance of a device; it implements any of the
// interfaces below. Operations it doesn't implement fail with EINVAL.
type File any

type (
	// Reader reads into p from offset off. Return 0 (nil or io.EOF) at end.
	Reader interface {
		Read(p []byte, off int64) (int, error)
	}
	// Writer writes p at offset off.
	Writer interface {
		Write(p []byte, off int64) (int, error)
	}
	// Releaser is called when the last reference to the open file closes.
	Releaser interface {
		Release()
	}
	// FlagsReader and FlagsWriter are Reader and Writer that also get the
	// file's flags (e.g. O_NONBLOCK); used instead of Read/Write when
	// implemented.
	FlagsReader interface {
		ReadFlags(p []byte, off int64, fl Flags) (int, error)
	}
	FlagsWriter interface {
		WriteFlags(p []byte, off int64, fl Flags) (int, error)
	}
	// Poller reports which operations would not block, for poll, select
	// and epoll. Call Device.Wake when that may have changed. Files without
	// Poll are always readable and writable.
	Poller interface {
		Poll() PollEvents
	}
	// Ioctler handles ioctl(2). arg is the raw argument: when it's a user
	// pointer, read/write through it with CopyFromUser/CopyToUser. The int
	// result is what ioctl returns (non-negative).
	Ioctler interface {
		Ioctl(cmd uint32, arg uintptr) (int, error)
	}
)

// Flags are the open file's flags (O_* values of the file's f_flags).
type Flags uint32

// Nonblock reports O_NONBLOCK: don't wait, fail with EAGAIN instead.
func (f Flags) Nonblock() bool { return f&0o4000 != 0 }

// PollEvents are poll(2) events.
type PollEvents uint32

const (
	PollIn  PollEvents = 0x1  // readable
	PollPri PollEvents = 0x2  // urgent data
	PollOut PollEvents = 0x4  // writable
	PollErr PollEvents = 0x8  // error
	PollHup PollEvents = 0x10 // hang up (e.g. end of stream)
)

// Linux ioctl command encoding (_IO, _IOR, _IOW, _IOWR from
// <asm-generic/ioctl.h>): typ is the "magic" byte, nr the command number,
// size the size of the argument struct.
func IO(typ byte, nr uint8) uint32 { return ioc(0, typ, nr, 0) }

// IOR is a command that reads from the device into a user buffer.
func IOR(typ byte, nr uint8, size uintptr) uint32 { return ioc(2, typ, nr, size) }

// IOW is a command that writes a user buffer to the device.
func IOW(typ byte, nr uint8, size uintptr) uint32 { return ioc(1, typ, nr, size) }

// IOWR is a command that does both.
func IOWR(typ byte, nr uint8, size uintptr) uint32 { return ioc(3, typ, nr, size) }

func ioc(dir uint32, typ byte, nr uint8, size uintptr) uint32 {
	return dir<<30 | uint32(size&0x3fff)<<16 | uint32(typ)<<8 | uint32(nr)
}

//export gokm_chardev_copy_from_user
func cCopyFromUser(dst *byte, uaddr uintptr, n int) int

//export gokm_chardev_copy_to_user
func cCopyToUser(uaddr uintptr, src *byte, n int) int

// CopyFromUser copies len(dst) bytes from the user-space address uaddr
// (e.g. an ioctl argument). It returns EFAULT if the memory isn't readable.
// Process context only (it may fault pages in).
func CopyFromUser(dst []byte, uaddr uintptr) error {
	if len(dst) == 0 {
		return nil
	}
	if rc := cCopyFromUser(&dst[0], uaddr, len(dst)); rc != 0 {
		return kernel.Errno(-rc)
	}
	return nil
}

// CopyToUser copies src to the user-space address uaddr.
func CopyToUser(uaddr uintptr, src []byte) error {
	if len(src) == 0 {
		return nil
	}
	if rc := cCopyToUser(uaddr, &src[0], len(src)); rc != 0 {
		return kernel.Errno(-rc)
	}
	return nil
}

// Device is a character device.
type Device struct {
	Name   string
	Mode   uint32 // /dev node permissions; 0 means 0600
	Opener Opener

	ptr    atomic.Uintptr
	handle kernel.Handle
}

// Implemented in the chardev glue (gokm/frameworks/chardev/chardev.c).
//
//export gokm_chardev_register
func cRegister(name *byte, mode uint32, handle uintptr, out *uintptr) int

//export gokm_chardev_deregister
func cDeregister(ptr uintptr)

//export gokm_chardev_wake
func cWake(ptr uintptr)

//export gokm_chardev_events
func cEvents(ptr uintptr) uint32

//export gokm_chardev_wait
func cWait(ptr uintptr, seen uint32) int

// Wake makes pollers re-check Poll and wakes Wait calls. It can be called
// from any context, atomic ones included.
func (d *Device) Wake() {
	if p := d.ptr.Load(); p != 0 {
		cWake(p)
	}
}

// Wait blocks until ready() returns true, re-checking it after each Wake.
// With fl.Nonblock() it returns EAGAIN instead of blocking. A signal
// interrupts it with EINTR, and a deregistered device returns ENODEV. Call
// it from file operations (Read, Write, Ioctl), with no locks held that
// Wake's caller needs; ready runs without any lock of its own.
func (d *Device) Wait(fl Flags, ready func() bool) error {
	for {
		p := d.ptr.Load()
		if p == 0 {
			return kernel.ENODEV
		}
		seen := cEvents(p)
		if ready() {
			return nil
		}
		if fl.Nonblock() {
			return kernel.EAGAIN
		}
		if cWait(p, seen) != 0 {
			return kernel.EINTR
		}
	}
}

// Register creates /dev/<Name>.
func Register(d *Device) error {
	if d.Name == "" || d.Opener == nil {
		return kernel.EINVAL
	}
	if d.ptr.Load() != 0 {
		return kernel.EBUSY
	}
	if d.Mode == 0 {
		d.Mode = 0o600
	}
	d.handle = kernel.NewHandle(d)
	name := d.Name + "\x00"
	var ptr uintptr
	if rc := cRegister(unsafe.StringData(name), d.Mode, uintptr(d.handle), &ptr); rc != 0 {
		d.handle.Delete()
		return kernel.Errno(-rc)
	}
	d.ptr.Store(ptr)
	kernel.OnExit(d.Deregister)
	return nil
}

// Deregister removes the device. Files still open keep working until
// closed (they also keep the module loaded).
func (d *Device) Deregister() {
	p := d.ptr.Swap(0)
	if p == 0 {
		return
	}
	cDeregister(p)
	d.handle.Delete()
}

func view(p *byte, n int) []byte {
	if n == 0 {
		return nil
	}
	return unsafe.Slice(p, n)
}

// Called by the glue between GOKM_ENTER and GOKM_LEAVE.

//export gokm_chardev_go_open
func goOpen(h uintptr, out *uintptr) (rc int) {
	rc = -int(kernel.EIO)
	defer kernel.CatchPanic[int](nil)
	d := kernel.Handle(h).Value().(*Device)
	f, err := d.Opener.Open()
	if err != nil {
		return kernel.ToErrno(err)
	}
	*out = uintptr(kernel.NewHandle(&f))
	return 0
}

func file(h uintptr) File { return *kernel.Handle(h).Value().(*File) }

//export gokm_chardev_go_read
func goRead(h uintptr, buf *byte, count int, off int64, flags uint32) (rc int64) {
	rc = -int64(kernel.EIO)
	defer kernel.CatchPanic[int64](nil)
	var n int
	var err error
	switch r := file(h).(type) {
	case FlagsReader:
		n, err = r.ReadFlags(view(buf, count), off, Flags(flags))
	case Reader:
		n, err = r.Read(view(buf, count), off)
	default:
		return -int64(kernel.EINVAL)
	}
	if err != nil && err != io.EOF && n == 0 {
		return int64(kernel.ToErrno(err))
	}
	return int64(n)
}

//export gokm_chardev_go_write
func goWrite(h uintptr, buf *byte, count int, off int64, flags uint32) (rc int64) {
	rc = -int64(kernel.EIO)
	defer kernel.CatchPanic[int64](nil)
	var n int
	var err error
	switch w := file(h).(type) {
	case FlagsWriter:
		n, err = w.WriteFlags(view(buf, count), off, Flags(flags))
	case Writer:
		n, err = w.Write(view(buf, count), off)
	default:
		return -int64(kernel.EINVAL)
	}
	if err != nil && n == 0 {
		return int64(kernel.ToErrno(err))
	}
	return int64(n)
}

//export gokm_chardev_go_ioctl
func goIoctl(h uintptr, cmd uint32, arg uintptr) (rc int64) {
	rc = -int64(kernel.EIO)
	defer kernel.CatchPanic[int64](nil)
	io, ok := file(h).(Ioctler)
	if !ok {
		return -int64(kernel.ENOTTY)
	}
	n, err := io.Ioctl(cmd, arg)
	if err != nil {
		return int64(kernel.ToErrno(err))
	}
	return int64(n)
}

//export gokm_chardev_go_poll
func goPoll(h uintptr) (rc int32) {
	rc = -int32(kernel.EIO)
	defer kernel.CatchPanic[int32](nil)
	if p, ok := file(h).(Poller); ok {
		return int32(p.Poll())
	}
	return int32(PollIn | PollOut)
}

//export gokm_chardev_go_release
func goRelease(h uintptr) {
	defer kernel.CatchPanic[int](nil)
	if r, ok := file(h).(Releaser); ok {
		r.Release()
	}
	kernel.Handle(h).Delete()
}
