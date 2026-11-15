package main

import (
	"runtime"

	"gokm/fs"
	"gokm/kernel"
)

// dir is a directory: entries in creation order.
type dir struct {
	mode     fs.Mode
	names    []string
	children map[string]fs.Node
}

func newDir(mode fs.Mode) *dir {
	return &dir{mode: mode & fs.ModePerm, children: map[string]fs.Node{}}
}

func (d *dir) Attr() fs.Attr { return fs.Attr{Mode: fs.ModeDir | d.mode} }

func (d *dir) Lookup(name string) (fs.Node, error) { return d.children[name], nil }

func (d *dir) ReadDir(yield func(string, fs.Node) bool) error {
	for _, name := range d.names {
		if !yield(name, d.children[name]) {
			break
		}
	}
	return nil
}

func (d *dir) add(name string, n fs.Node) error {
	if len(name) > 255 {
		return kernel.ENAMETOOLONG
	}
	if d.children[name] != nil {
		return kernel.EEXIST
	}
	d.children[name] = n
	d.names = append(d.names, name)
	return nil
}

func (d *dir) Create(name string, mode fs.Mode) (fs.Node, error) {
	f := &file{mode: mode & fs.ModePerm}
	return f, d.add(name, f)
}

func (d *dir) Mkdir(name string, mode fs.Mode) (fs.Dir, error) {
	sub := newDir(mode)
	return sub, d.add(name, sub)
}

func (d *dir) Remove(name string) error {
	if d.children[name] == nil {
		return kernel.ENOENT
	}
	delete(d.children, name)
	for i, n := range d.names {
		if n == name {
			d.names = append(d.names[:i], d.names[i+1:]...)
			break
		}
	}
	return nil
}

// file is a regular file held in a Go byte slice.
type file struct {
	mode fs.Mode
	data []byte
}

func (f *file) Attr() fs.Attr {
	return fs.Attr{Mode: fs.ModeRegular | f.mode, Size: int64(len(f.data))}
}

func (f *file) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, nil
	}
	return copy(p, f.data[off:]), nil
}

func (f *file) WriteAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	if !f.reserve(end) {
		return 0, kernel.ENOSPC
	}
	if l := int64(len(f.data)); end > l {
		f.data = f.data[:end]
		clear(f.data[l:off]) // a hole reads as zeros (no-op when off <= l)
	}
	return copy(f.data[off:], p), nil
}

func (f *file) Truncate(size int64) error {
	if l := int64(len(f.data)); size > l {
		if !f.reserve(size) {
			return kernel.ENOSPC
		}
		f.data = f.data[:size]
		clear(f.data[l:])
		return nil
	}
	f.data = f.data[:size]
	if size < int64(cap(f.data))/4 { // give memory back to the GC heap
		f.data = append([]byte(nil), f.data...)
	}
	return nil
}

// reserve makes cap(f.data) >= size, failing (instead of panicking with out
// of memory) when the GC heap doesn't have room.
func (f *file) reserve(size int64) bool {
	if size <= int64(cap(f.data)) {
		return true
	}
	newCap := max(2*int64(cap(f.data)), size)
	if !heapHasRoom(newCap) {
		newCap = size
		if !heapHasRoom(newCap) {
			return false
		}
	}
	nd := make([]byte, len(f.data), newCap)
	copy(nd, f.data)
	f.data = nd
	return true
}

// heapHasRoom reports whether an allocation of size bytes will likely fit,
// collecting garbage first if needed, with slack for fragmentation.
func heapHasRoom(size int64) bool {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if size*2 < int64(ms.HeapSys)-int64(ms.HeapInuse) {
		return true
	}
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return size*2 < int64(ms.HeapSys)-int64(ms.HeapInuse)
}
