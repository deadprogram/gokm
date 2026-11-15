// Package fs lets Go code implement a Linux filesystem.
//
// Implement Dir and file nodes, register a FileSystem, and it can be mounted
// with `mount -t <name> none /mnt`. gokm compiles the kernel-side glue (VFS
// callbacks, inode/dentry caches, user-memory copies) into the module
// automatically when this package is imported.
//
//	type root struct{ ... }   // implements fs.Dir (+ Creator, Mkdirer, Remover)
//	type file struct{ ... }   // implements fs.Node (+ Reader, Writer, Truncater)
//
//	func main() {
//		err := fs.Register(&fs.FileSystem{
//			Name:  "myfs",
//			Mount: func() (fs.Dir, error) { return newRoot(), nil },
//		})
//		if err != nil {
//			kernel.FailLoad(err)
//		}
//	}
//
// Nodes must be comparable (use pointer types): the framework maps each node
// to an inode number. Methods return kernel.Errno values (kernel.ENOENT,
// kernel.ENOSPC, ...) to report specific errors; other errors become EIO.
// All methods are called holding the module's Go lock, one at a time.
package fs

import (
	"io"
	"unsafe"

	"gokm/kernel"
)

// Mode is a file type and permission bits, with Linux's layout (S_IFDIR...).
type Mode uint32

const (
	ModeType    Mode = 0o170000
	ModeDir     Mode = 0o040000
	ModeRegular Mode = 0o100000
	ModePerm    Mode = 0o7777
)

func (m Mode) IsDir() bool     { return m&ModeType == ModeDir }
func (m Mode) IsRegular() bool { return m&ModeType == ModeRegular }

// Attr are a node's attributes.
type Attr struct {
	Mode Mode  // type and permissions
	Size int64 // size in bytes (regular files)
}

// Node is anything in the filesystem.
type Node interface {
	Attr() Attr
}

// Dir is a directory.
type Dir interface {
	Node
	// Lookup returns the child called name, or (nil, nil) / kernel.ENOENT
	// if there is none.
	Lookup(name string) (Node, error)
	// ReadDir calls yield for each entry, in a stable order, until yield
	// returns false.
	ReadDir(yield func(name string, n Node) bool) error
}

// Optional interfaces. Operations a node doesn't implement fail with EPERM
// (directories) or EINVAL / EBADF (files).
type (
	// Creator creates regular files in a directory.
	Creator interface {
		Create(name string, mode Mode) (Node, error)
	}
	// Mkdirer creates subdirectories.
	Mkdirer interface {
		Mkdir(name string, mode Mode) (Dir, error)
	}
	// Remover removes an entry (a file, or an empty directory: the
	// framework checks emptiness before calling it for rmdir).
	Remover interface {
		Remove(name string) error
	}
	// Reader reads file content at off. Return 0 (with nil or io.EOF) at
	// end of file.
	Reader interface {
		ReadAt(p []byte, off int64) (int, error)
	}
	// Writer writes file content at off, growing the file if needed.
	Writer interface {
		WriteAt(p []byte, off int64) (int, error)
	}
	// Truncater changes a file's size (truncate, O_TRUNC).
	Truncater interface {
		Truncate(size int64) error
	}
	// Unmounter is implemented by a root Dir that wants to know when its
	// mount goes away.
	Unmounter interface {
		Unmount()
	}
)

// FileSystem is a filesystem type.
type FileSystem struct {
	Name string
	// Magic is reported by statfs (f_type). Default: 0x676f6b6d ("gokm").
	Magic uint32
	// Mount is called for each mount and returns its root directory.
	Mount func() (Dir, error)

	ptr    uintptr // kernel struct, 0 when not registered
	handle kernel.Handle
}

// Implemented in the fs glue (gokm/frameworks/fs/fs.c).
//
//export gokm_fs_register
func cRegister(name *byte, nameLen int, magic uint32, handle uintptr, out *uintptr) int

//export gokm_fs_unregister
func cUnregister(ptr uintptr)

//export gokm_fs_emit
func cEmit(ctx uintptr, name *byte, nameLen int, ino uint64, dtype uint32) bool

// Register registers the filesystem type with the kernel. It's unregistered
// automatically at module exit (or earlier with Unregister).
func Register(f *FileSystem) error {
	if f.Name == "" || f.Mount == nil {
		return kernel.EINVAL
	}
	if f.ptr != 0 {
		return kernel.EBUSY
	}
	if f.Magic == 0 {
		f.Magic = 0x676f6b6d
	}
	f.handle = kernel.NewHandle(f)
	name := f.Name + "\x00"
	if rc := cRegister(unsafe.StringData(name), len(f.Name), f.Magic, uintptr(f.handle), &f.ptr); rc != 0 {
		f.handle.Delete()
		f.ptr = 0
		return kernel.Errno(-rc)
	}
	kernel.OnExit(f.Unregister)
	return nil
}

// Unregister unregisters the filesystem type. Mounted instances keep the
// module loaded, so this only happens once all are unmounted.
func (f *FileSystem) Unregister() {
	if f.ptr == 0 {
		return
	}
	cUnregister(f.ptr)
	f.ptr = 0
	f.handle.Delete()
}

// mount is one mounted instance: it maps nodes to inode numbers.
type mount struct {
	root  Dir
	nodes map[uint64]Node
	inos  map[nodeKey]uint64
	next  uint64
}

// nodeKey identifies a node: the two words of its interface value (type and
// pointer). Hashing interface values directly would pull floating-point code
// (for float-typed dynamic values) into the module, which the kernel can't
// link. For pointer node types, this is pointer identity.
type nodeKey struct{ typ, ptr uintptr }

func keyOf(n Node) nodeKey { return *(*nodeKey)(unsafe.Pointer(&n)) }

const rootIno = 1

func (m *mount) ino(n Node) uint64 {
	k := keyOf(n)
	if ino, ok := m.inos[k]; ok {
		return ino
	}
	ino := m.next
	m.next++
	m.nodes[ino] = n
	m.inos[k] = ino
	return ino
}

func (m *mount) forget(ino uint64) {
	if n, ok := m.nodes[ino]; ok {
		delete(m.nodes, ino)
		delete(m.inos, keyOf(n))
	}
}

func getMount(h uintptr) *mount { return kernel.Handle(h).Value().(*mount) }

func (m *mount) dir(ino uint64) (Dir, int) {
	n, ok := m.nodes[ino]
	if !ok {
		return nil, -int(kernel.ENOENT)
	}
	d, ok := n.(Dir)
	if !ok {
		return nil, -int(kernel.ENOTDIR)
	}
	return d, 0
}

func viewString(p *byte, n int) string { return unsafe.String(p, n) }

func viewBytes(p *byte, n int) []byte {
	if n == 0 {
		return nil
	}
	return unsafe.Slice(p, n)
}

// The functions below are called by the fs glue, between GOKM_ENTER and
// GOKM_LEAVE. Go int is 64 bits (long in C).

//export gokm_fs_go_mount
func goMount(fsHandle uintptr, out *uintptr) (rc int) {
	defer kernel.CatchPanic(&rc)
	f := kernel.Handle(fsHandle).Value().(*FileSystem)
	root, err := f.Mount()
	if err != nil {
		return kernel.ToErrno(err)
	}
	if root == nil {
		return -int(kernel.EINVAL)
	}
	m := &mount{root: root, nodes: map[uint64]Node{}, inos: map[nodeKey]uint64{}, next: rootIno}
	m.ino(root)
	*out = uintptr(kernel.NewHandle(m))
	return 0
}

//export gokm_fs_go_unmount
func goUnmount(h uintptr) {
	defer kernel.CatchPanic[int](nil)
	m := getMount(h)
	if u, ok := m.root.(Unmounter); ok {
		u.Unmount()
	}
	kernel.Handle(h).Delete()
}

//export gokm_fs_go_stat
func goStat(h uintptr, ino uint64, mode *uint32, size *int64) (rc int) {
	defer kernel.CatchPanic(&rc)
	n, ok := getMount(h).nodes[ino]
	if !ok {
		return -int(kernel.ENOENT)
	}
	a := n.Attr()
	if a.Mode&ModeType == 0 {
		if _, isDir := n.(Dir); isDir {
			a.Mode |= ModeDir
		} else {
			a.Mode |= ModeRegular
		}
	}
	*mode = uint32(a.Mode)
	*size = a.Size
	return 0
}

// gokm_fs_go_lookup returns the child's inode number, 0 if it doesn't exist,
// or a negative errno.
//
//export gokm_fs_go_lookup
func goLookup(h uintptr, dirIno uint64, name *byte, nameLen int) (rc int64) {
	defer kernel.CatchPanic(&rc)
	m := getMount(h)
	d, errno := m.dir(dirIno)
	if errno != 0 {
		return int64(errno)
	}
	n, err := d.Lookup(viewString(name, nameLen))
	if err == kernel.ENOENT || (err == nil && n == nil) {
		return 0
	}
	if err != nil {
		return int64(kernel.ToErrno(err))
	}
	return int64(m.ino(n))
}

//export gokm_fs_go_create
func goCreate(h uintptr, dirIno uint64, name *byte, nameLen int, mode uint32) (rc int64) {
	defer kernel.CatchPanic(&rc)
	m := getMount(h)
	d, errno := m.dir(dirIno)
	if errno != 0 {
		return int64(errno)
	}
	nm := string(viewBytes(name, nameLen)) // copy: the kernel buffer is transient
	var n Node
	var err error
	if Mode(mode).IsDir() {
		mk, ok := d.(Mkdirer)
		if !ok {
			return -int64(kernel.EPERM)
		}
		var nd Dir
		nd, err = mk.Mkdir(nm, Mode(mode))
		if nd != nil {
			n = nd
		}
	} else {
		cr, ok := d.(Creator)
		if !ok {
			return -int64(kernel.EPERM)
		}
		n, err = cr.Create(nm, Mode(mode))
	}
	if err != nil {
		return int64(kernel.ToErrno(err))
	}
	if n == nil {
		return -int64(kernel.EIO)
	}
	return int64(m.ino(n))
}

//export gokm_fs_go_remove
func goRemove(h uintptr, dirIno uint64, name *byte, nameLen int, isDir bool) (rc int) {
	defer kernel.CatchPanic(&rc)
	m := getMount(h)
	d, errno := m.dir(dirIno)
	if errno != 0 {
		return errno
	}
	rm, ok := d.(Remover)
	if !ok {
		return -int(kernel.EPERM)
	}
	nm := viewString(name, nameLen)
	if isDir {
		child, err := d.Lookup(nm)
		if err != nil || child == nil {
			return -int(kernel.ENOENT)
		}
		cd, ok := child.(Dir)
		if !ok {
			return -int(kernel.ENOTDIR)
		}
		empty := true
		cd.ReadDir(func(string, Node) bool { empty = false; return false })
		if !empty {
			return -int(kernel.ENOTEMPTY)
		}
	}
	return kernel.ToErrno(rm.Remove(nm))
}

// gokm_fs_go_evict: the kernel dropped the inode of an unlinked node.
//
//export gokm_fs_go_evict
func goEvict(h uintptr, ino uint64) {
	defer kernel.CatchPanic[int](nil)
	getMount(h).forget(ino)
}

//export gokm_fs_go_read
func goRead(h uintptr, ino uint64, buf *byte, count int, off int64) (rc int64) {
	defer kernel.CatchPanic(&rc)
	n, ok := getMount(h).nodes[ino]
	if !ok {
		return -int64(kernel.ENOENT)
	}
	r, ok := n.(Reader)
	if !ok {
		return -int64(kernel.EINVAL)
	}
	got, err := r.ReadAt(viewBytes(buf, count), off)
	if err != nil && err != io.EOF && got == 0 {
		return int64(kernel.ToErrno(err))
	}
	return int64(got)
}

//export gokm_fs_go_write
func goWrite(h uintptr, ino uint64, buf *byte, count int, off int64) (rc int64) {
	defer kernel.CatchPanic(&rc)
	n, ok := getMount(h).nodes[ino]
	if !ok {
		return -int64(kernel.ENOENT)
	}
	w, ok := n.(Writer)
	if !ok {
		return -int64(kernel.EBADF)
	}
	wrote, err := w.WriteAt(viewBytes(buf, count), off)
	if err != nil && wrote == 0 {
		return int64(kernel.ToErrno(err))
	}
	return int64(wrote)
}

//export gokm_fs_go_truncate
func goTruncate(h uintptr, ino uint64, size int64) (rc int) {
	defer kernel.CatchPanic(&rc)
	n, ok := getMount(h).nodes[ino]
	if !ok {
		return -int(kernel.ENOENT)
	}
	t, ok := n.(Truncater)
	if !ok {
		return -int(kernel.EPERM)
	}
	return kernel.ToErrno(t.Truncate(size))
}

// gokm_fs_go_iterate emits directory entries from index start onwards,
// through gokm_fs_emit (which advances the kernel's position).
//
//export gokm_fs_go_iterate
func goIterate(h uintptr, dirIno uint64, start int64, ctx uintptr) (rc int) {
	defer kernel.CatchPanic(&rc)
	m := getMount(h)
	d, errno := m.dir(dirIno)
	if errno != 0 {
		return errno
	}
	var i int64
	err := d.ReadDir(func(name string, n Node) bool {
		if i++; i <= start {
			return true
		}
		if n == nil {
			return true // skip: entries need a node (and inode number)
		}
		mode := n.Attr().Mode
		if mode&ModeType == 0 {
			if _, isDir := n.(Dir); isDir {
				mode |= ModeDir
			} else {
				mode |= ModeRegular
			}
		}
		return cEmit(ctx, unsafe.StringData(name), len(name), m.ino(n), uint32(mode&ModeType)>>12)
	})
	return kernel.ToErrno(err)
}
