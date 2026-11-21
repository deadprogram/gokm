// SPDX-License-Identifier: GPL-2.0

// gobrd is a RAM-backed block device: a Go port of Linux's brd
// (drivers/block/brd.c).
//
//	insmod gobrd.ko rd_nr=2 rd_size=65536    # /dev/gobrd0, /dev/gobrd1, 64 MiB each
//
// As in brd, memory is allocated a page at a time on first write (reads of
// never-written sectors return zeros), stays until the module is unloaded,
// and discards (blkdiscard, fstrim) give back the whole pages they cover.
package main

import (
	"strconv"
	"sync"

	"gokm/blk"
	"gokm/kernel"
)

// Module parameters, as in brd.

//gokm:param rd_nr Maximum number of brd devices
var rdNr = 16

//gokm:param rd_size Size of each RAM disk in kbytes.
var rdSize uint = 4096

const (
	pageSize  = 4096
	pageShift = 12
	nShards   = 64 // page maps, each with its own lock (brd: an xarray)
)

type shard struct {
	mu    sync.Mutex
	pages map[uint64][]byte // page index -> off-heap page (kernel.AllocBytes)
}

// ramDisk is one device's storage (brd_device).
type ramDisk struct {
	shards [nShards]shard
}

func newRAMDisk() *ramDisk {
	r := &ramDisk{}
	for i := range r.shards {
		r.shards[i].pages = map[uint64][]byte{}
	}
	return r
}

func (r *ramDisk) shard(idx uint64) *shard { return &r.shards[idx%nShards] }

// each calls fn for the parts of [off, off+n) within each page, with the
// page's shard locked.
func (r *ramDisk) each(off int64, n int, fn func(s *shard, idx uint64, pageOff, done, n int) error) error {
	for done := 0; done < n; {
		idx := uint64(off) >> pageShift
		pageOff := int(off & (pageSize - 1))
		chunk := min(n-done, pageSize-pageOff)
		s := r.shard(idx)
		s.mu.Lock()
		err := fn(s, idx, pageOff, done, chunk)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		done += chunk
		off += int64(chunk)
	}
	return nil
}

func (r *ramDisk) ReadAt(p []byte, off int64) error {
	return r.each(off, len(p), func(s *shard, idx uint64, pageOff, done, n int) error {
		if page := s.pages[idx]; page != nil {
			copy(p[done:done+n], page[pageOff:])
		} else {
			clear(p[done : done+n]) // never written (brd_lookup_page miss)
		}
		return nil
	})
}

func (r *ramDisk) WriteAt(p []byte, off int64) error {
	return r.each(off, len(p), func(s *shard, idx uint64, pageOff, done, n int) error {
		page := s.pages[idx]
		if page == nil { // brd_insert_page
			if page = kernel.AllocBytes(pageSize); page == nil {
				return kernel.ENOMEM
			}
			s.pages[idx] = page
		}
		copy(page[pageOff:], p[done:done+n])
		return nil
	})
}

// Discard frees the pages entirely inside the range; partial pages at the
// ends keep their data (brd_do_discard).
func (r *ramDisk) Discard(off, n int64) error {
	first := uint64(off+pageSize-1) >> pageShift
	end := uint64(off+n) >> pageShift
	for idx := first; idx < end; idx++ {
		s := r.shard(idx)
		s.mu.Lock()
		if page := s.pages[idx]; page != nil {
			delete(s.pages, idx)
			kernel.FreeBytes(page)
		}
		s.mu.Unlock()
	}
	return nil
}

// free releases all pages (brd_free_pages); the disk is gone already.
func (r *ramDisk) free() {
	for i := range r.shards {
		s := &r.shards[i]
		for idx, page := range s.pages {
			kernel.FreeBytes(page)
			delete(s.pages, idx)
		}
	}
}

func (r *ramDisk) pages() int {
	n := 0
	for i := range r.shards {
		s := &r.shards[i]
		s.mu.Lock()
		n += len(s.pages)
		s.mu.Unlock()
	}
	return n
}

var (
	disks   []*ramDisk
	devices []*blk.Disk
)

// shutdown removes the disks and frees their memory.
func shutdown() int {
	for i := len(devices) - 1; i >= 0; i-- {
		devices[i].Unregister()
	}
	pages := 0
	for _, r := range disks {
		pages += r.pages()
		r.free()
	}
	disks, devices = nil, nil
	return pages
}

func main() {
	if rdNr < 1 || rdSize == 0 || rdSize%4 != 0 {
		kernel.FailLoad(kernel.EINVAL)
		return
	}
	for i := 0; i < rdNr; i++ {
		r := newRAMDisk()
		d := &blk.Disk{
			Name:   "gobrd%d",
			Size:   int64(rdSize) << 10,
			Device: r,
		}
		if err := blk.Register(d); err != nil {
			kernel.Print("creating disk: " + err.Error())
			shutdown()
			kernel.FailLoad(err)
			return
		}
		disks = append(disks, r)
		devices = append(devices, d)
	}
	kernel.OnExit(func() {
		kernel.Print("unloaded, freed " + strconv.Itoa(shutdown()) + " pages")
	})
	kernel.Print("module loaded: " + strconv.Itoa(rdNr) + " disks of " +
		strconv.FormatUint(uint64(rdSize), 10) + " KiB")
}
