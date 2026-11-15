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
package chardev

import (
	"io"
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
	// Ioctler handles ioctl(2). arg is the raw argument: when it's a user
	// pointer, read/write through it with CopyFromUser/CopyToUser. The int
	// result is what ioctl returns (non-negative).
	Ioctler interface {
		Ioctl(cmd uint32, arg uintptr) (int, error)
	}
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

	ptr    uintptr
	handle kernel.Handle
}

// Implemented in the chardev glue (gokm/frameworks/chardev/chardev.c).
//
//export gokm_chardev_register
func cRegister(name *byte, mode uint32, handle uintptr, out *uintptr) int

//export gokm_chardev_deregister
func cDeregister(ptr uintptr)

// Register creates /dev/<Name>.
func Register(d *Device) error {
	if d.Name == "" || d.Opener == nil {
		return kernel.EINVAL
	}
	if d.ptr != 0 {
		return kernel.EBUSY
	}
	if d.Mode == 0 {
		d.Mode = 0o600
	}
	d.handle = kernel.NewHandle(d)
	name := d.Name + "\x00"
	if rc := cRegister(unsafe.StringData(name), d.Mode, uintptr(d.handle), &d.ptr); rc != 0 {
		d.handle.Delete()
		d.ptr = 0
		return kernel.Errno(-rc)
	}
	kernel.OnExit(d.Deregister)
	return nil
}

// Deregister removes the device. Files still open keep working until
// closed (they also keep the module loaded).
func (d *Device) Deregister() {
	if d.ptr == 0 {
		return
	}
	cDeregister(d.ptr)
	d.ptr = 0
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
	defer kernel.CatchPanic(&rc)
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
func goRead(h uintptr, buf *byte, count int, off int64) (rc int64) {
	defer kernel.CatchPanic(&rc)
	r, ok := file(h).(Reader)
	if !ok {
		return -int64(kernel.EINVAL)
	}
	n, err := r.Read(view(buf, count), off)
	if err != nil && err != io.EOF && n == 0 {
		return int64(kernel.ToErrno(err))
	}
	return int64(n)
}

//export gokm_chardev_go_write
func goWrite(h uintptr, buf *byte, count int, off int64) (rc int64) {
	defer kernel.CatchPanic(&rc)
	w, ok := file(h).(Writer)
	if !ok {
		return -int64(kernel.EINVAL)
	}
	n, err := w.Write(view(buf, count), off)
	if err != nil && n == 0 {
		return int64(kernel.ToErrno(err))
	}
	return int64(n)
}

//export gokm_chardev_go_ioctl
func goIoctl(h uintptr, cmd uint32, arg uintptr) (rc int64) {
	defer kernel.CatchPanic(&rc)
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

//export gokm_chardev_go_release
func goRelease(h uintptr) {
	defer kernel.CatchPanic[int](nil)
	if r, ok := file(h).(Releaser); ok {
		r.Release()
	}
	kernel.Handle(h).Delete()
}
