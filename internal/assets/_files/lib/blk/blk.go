// Package blk creates block devices (/dev/<name>) whose storage is Go code.
//
//	type ram struct{ ... }
//	func (r *ram) ReadAt(p []byte, off int64) error  { ... }
//	func (r *ram) WriteAt(p []byte, off int64) error { ... }
//
//	disk := &blk.Disk{Name: "myram%d", Size: 64 << 20, Device: &ram{}}
//	err := blk.Register(disk)  // /dev/myram0; partitions work too
//
// The kernel's block layer (blk-mq) queues the I/O; requests are handed to
// Go in process context (from the block layer's worker threads), one data
// segment at a time: ReadAt/WriteAt get kernel memory valid only during the
// call, at offsets that are multiples of the logical block size. They can
// block and allocate. Several requests can be in flight at once, on
// different CPUs: the Device must be safe for concurrent use.
//
// Disks are removed automatically at module exit.
package blk

import (
	"strings"
	"unsafe"

	"gokm/kernel"
)

// Device stores the data of a disk.
type Device interface {
	ReadAt(p []byte, off int64) error
	WriteAt(p []byte, off int64) error
}

// Optional Device interfaces.
type (
	// Flusher makes previous writes durable (the disk then advertises a
	// volatile write cache: the kernel sends flushes and FUA writes).
	Flusher interface {
		Flush() error
	}
	// Discarder drops the data of a range (TRIM, BLKDISCARD, fstrim);
	// discarded ranges may read back as anything (brd: zeros).
	Discarder interface {
		Discard(off, n int64) error
	}
)

// Disk describes a block device.
type Disk struct {
	// Name is the device name under /dev; a "%d" is replaced with a
	// number counting up from 0 for each disk the module creates.
	Name string
	// Size in bytes (a multiple of LogicalBlockSize).
	Size int64
	// LogicalBlockSize is the addressing unit (default 512);
	// PhysicalBlockSize the preferred I/O size (default 4096).
	LogicalBlockSize  int
	PhysicalBlockSize int
	// MaxIO limits the bytes per request (default 256 KiB).
	MaxIO    int
	ReadOnly bool
	// Rotational tells the kernel it's a spinning disk (default: an SSD).
	Rotational bool
	Device     Device

	ptr    uintptr
	handle kernel.Handle
}

// Implemented in the blk glue (gokm/frameworks/blk/blk.c).
//
//export gokm_blk_create
func cCreate(name *byte, size int64, lbs, pbs, maxIO int, flags uint32, handle uintptr,
	out *uintptr, outName *byte) int

//export gokm_blk_destroy
func cDestroy(ptr uintptr)

const (
	flagReadOnly = 1 << iota
	flagRotational
	flagFlush
	flagDiscard
)

// Register creates and adds the disk. Name is updated with the kernel's
// name for it (e.g. "gobrd0").
func Register(d *Disk) error {
	if d.Name == "" || d.Device == nil || len(d.Name) >= 30 {
		return kernel.EINVAL
	}
	if d.ptr != 0 {
		return kernel.EBUSY
	}
	if d.LogicalBlockSize == 0 {
		d.LogicalBlockSize = 512
	}
	if d.PhysicalBlockSize == 0 {
		d.PhysicalBlockSize = 4096
	}
	if d.PhysicalBlockSize < d.LogicalBlockSize {
		d.PhysicalBlockSize = d.LogicalBlockSize
	}
	if d.MaxIO == 0 {
		d.MaxIO = 256 << 10
	}
	if d.Size <= 0 || d.Size%int64(d.LogicalBlockSize) != 0 {
		return kernel.EINVAL
	}
	var flags uint32
	if d.ReadOnly {
		flags |= flagReadOnly
	}
	if d.Rotational {
		flags |= flagRotational
	}
	if _, ok := d.Device.(Flusher); ok {
		flags |= flagFlush
	}
	if _, ok := d.Device.(Discarder); ok {
		flags |= flagDiscard
	}
	d.handle = kernel.NewHandle(d)
	name := d.Name + "\x00"
	var assigned [32]byte
	if rc := cCreate(unsafe.StringData(name), d.Size, d.LogicalBlockSize, d.PhysicalBlockSize,
		d.MaxIO, flags, uintptr(d.handle), &d.ptr, &assigned[0]); rc != 0 {
		d.handle.Delete()
		d.ptr = 0
		return kernel.Errno(-rc)
	}
	d.Name = strings.TrimRight(string(assigned[:]), "\x00")
	kernel.OnExit(d.Unregister)
	return nil
}

// Unregister removes the disk, waiting for in-flight I/O.
func (d *Disk) Unregister() {
	if d.ptr == 0 {
		return
	}
	cDestroy(d.ptr)
	d.ptr = 0
	d.handle.Delete()
}

func disk(h uintptr) *Disk { return kernel.Handle(h).Value().(*Disk) }

// Called by the glue between GOKM_ENTER and GOKM_LEAVE.

//export gokm_blk_go_rw
func goRW(h uintptr, write bool, buf *byte, n int, off int64) (rc int) {
	defer kernel.CatchPanic(&rc)
	d := disk(h)
	p := unsafe.Slice(buf, n)
	var err error
	if write {
		err = d.Device.WriteAt(p, off)
	} else {
		err = d.Device.ReadAt(p, off)
	}
	return kernel.ToErrno(err)
}

//export gokm_blk_go_flush
func goFlush(h uintptr) (rc int) {
	defer kernel.CatchPanic(&rc)
	if f, ok := disk(h).Device.(Flusher); ok {
		return kernel.ToErrno(f.Flush())
	}
	return 0
}

//export gokm_blk_go_discard
func goDiscard(h uintptr, off, n int64) (rc int) {
	defer kernel.CatchPanic(&rc)
	if dc, ok := disk(h).Device.(Discarder); ok {
		return kernel.ToErrno(dc.Discard(off, n))
	}
	return -int(kernel.EOPNOTSUPP)
}
