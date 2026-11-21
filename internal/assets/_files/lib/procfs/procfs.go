// Package procfs creates files in /proc backed by Go code.
//
//	f, err := procfs.Create("mystats", procfs.ReadFunc(func() ([]byte, error) {
//		return []byte("hits: " + strconv.Itoa(hits) + "\n"), nil
//	}))
//
// Read is called once per open() and its result is what that open file
// reads (so readers always see a consistent snapshot). Handlers can run
// concurrently on several CPUs: protect shared state. If the handler also
// implements Writer, the file is writable and each write() is passed to it.
// Files are removed automatically at module exit (or earlier with Remove).
package procfs

import (
	"unsafe"

	"gokm/kernel"
)

// Handler produces the file's content.
type Handler interface {
	Read() ([]byte, error)
}

// Writer is optionally implemented by handlers of writable files. Each
// write(2) call becomes one Write (at most 64 KiB).
type Writer interface {
	Write(p []byte) error
}

// ReadFunc adapts a function to a read-only Handler.
type ReadFunc func() ([]byte, error)

func (f ReadFunc) Read() ([]byte, error) { return f() }

// File is a /proc entry.
type File struct {
	name    string
	handler Handler
	ptr     uintptr
	handle  kernel.Handle
}

// Implemented in the procfs glue (gokm/frameworks/procfs/procfs.c).
//
//export gokm_procfs_create
func cCreate(name *byte, mode uint32, handle uintptr, out *uintptr) int

//export gokm_procfs_remove
func cRemove(ptr uintptr)

// Create creates /proc/<name>: mode 0444, or 0644 if h implements Writer.
func Create(name string, h Handler) (*File, error) {
	if name == "" || h == nil {
		return nil, kernel.EINVAL
	}
	mode := uint32(0o444)
	if _, ok := h.(Writer); ok {
		mode = 0o644
	}
	f := &File{name: name, handler: h}
	f.handle = kernel.NewHandle(f)
	cname := name + "\x00"
	if rc := cCreate(unsafe.StringData(cname), mode, uintptr(f.handle), &f.ptr); rc != 0 {
		f.handle.Delete()
		return nil, kernel.Errno(-rc)
	}
	kernel.OnExit(f.Remove)
	return f, nil
}

// Name returns the file name (under /proc).
func (f *File) Name() string { return f.name }

// Remove removes the file, waiting for in-progress operations on it.
func (f *File) Remove() {
	if f.ptr == 0 {
		return
	}
	cRemove(f.ptr)
	f.ptr = 0
	f.handle.Delete()
}

// Called by the glue between GOKM_ENTER and GOKM_LEAVE. *buf points into Go
// memory, valid until GOKM_LEAVE (no collection runs while the task is in
// Go).
//
//export gokm_procfs_go_open
func goOpen(h uintptr, buf **byte, n *int) (rc int) {
	rc = -int(kernel.EIO)
	defer kernel.CatchPanic[int](nil)
	f := kernel.Handle(h).Value().(*File)
	data, err := f.handler.Read()
	if err != nil {
		return kernel.ToErrno(err)
	}
	*n = len(data)
	if len(data) > 0 {
		*buf = &data[0]
	}
	return 0
}

//export gokm_procfs_go_write
func goWrite(h uintptr, buf *byte, n int) (rc int) {
	rc = -int(kernel.EIO)
	defer kernel.CatchPanic[int](nil)
	f := kernel.Handle(h).Value().(*File)
	w, ok := f.handler.(Writer)
	if !ok {
		return -int(kernel.EPERM)
	}
	var p []byte
	if n > 0 {
		p = unsafe.Slice(buf, n)
	}
	return kernel.ToErrno(w.Write(p))
}
