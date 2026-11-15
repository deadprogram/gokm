package main

// mmap (through the page cache), running binaries from gofs, and extended
// attributes.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31) + seed
	}
	return b
}

func msync(b []byte) error {
	_, _, e := syscall.Syscall(syscall.SYS_MSYNC, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), syscall.MS_SYNC)
	if e != 0 {
		return e
	}
	return nil
}

func mapFile(path string, size, prot, flags int) (*os.File, []byte, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	m, err := syscall.Mmap(int(f.Fd()), 0, size, prot, flags)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, m, nil
}

func preadAll(f *os.File, n int) ([]byte, error) {
	b := make([]byte, n)
	got, err := f.ReadAt(b, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return b[:got], nil
}

func mmapTests() {
	const size = 3*4096 + 100
	path := p("mapped")

	check("mmap: read a file", func() error {
		want := pattern(size, 1)
		if err := os.WriteFile(path, want, 0o644); err != nil {
			return err
		}
		f, m, err := mapFile(path, 4*4096, syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			return err
		}
		defer f.Close()
		defer syscall.Munmap(m)
		if !bytes.Equal(m[:size], want) {
			return fmt.Errorf("mapped content differs")
		}
		if !bytes.Equal(m[size:], make([]byte, 4*4096-size)) {
			return fmt.Errorf("bytes past EOF in the last page aren't zero")
		}
		return nil
	}())

	check("mmap: shared writes reach read() and survive munmap", func() error {
		f, m, err := mapFile(path, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if err != nil {
			return err
		}
		defer f.Close()
		mapped := true
		defer func() {
			if mapped {
				syscall.Munmap(m)
			}
		}()
		copy(m[100:], "written through the mapping")
		copy(m[2*4096+10:], "page three")
		// No msync: read() writes back dirty pages of its range first.
		got, err := preadAll(f, size)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, m) {
			return fmt.Errorf("read() doesn't see the mapped writes")
		}
		copy(m[4000:], "spans two pages")
		if err := msync(m); err != nil {
			return err
		}
		snapshot := append([]byte(nil), m...)
		syscall.Munmap(m)
		mapped = false
		os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0)
		got, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, snapshot) {
			return fmt.Errorf("file content after munmap differs")
		}
		return nil
	}())

	check("mmap: write() updates an existing mapping", func() error {
		f, m, err := mapFile(path, size, syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			return err
		}
		defer f.Close()
		defer syscall.Munmap(m)
		_ = m[0] // fault the pages in
		_ = m[4096]
		if _, err := f.WriteAt([]byte("NEW DATA FROM WRITE"), 4090); err != nil {
			return err
		}
		if string(m[4090:4090+19]) != "NEW DATA FROM WRITE" {
			return fmt.Errorf("mapping shows %q", m[4090:4090+19])
		}
		return nil
	}())

	check("mmap: private writes stay private", func() error {
		before, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, m, err := mapFile(path, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE)
		if err != nil {
			return err
		}
		copy(m, "private scribble")
		syscall.Munmap(m)
		f.Close()
		after, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, after) {
			return fmt.Errorf("a MAP_PRIVATE write reached the file")
		}
		return nil
	}())

	check("mmap: grow with ftruncate, fill through the mapping", func() error {
		f, err := os.OpenFile(p("grown"), os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		const n = 64 << 10
		if err := f.Truncate(n); err != nil {
			return err
		}
		m, err := syscall.Mmap(int(f.Fd()), 0, n, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if err != nil {
			return err
		}
		want := pattern(n, 7)
		copy(m, want)
		syscall.Munmap(m)
		if err := f.Sync(); err != nil { // fsync writes back the pages
			return err
		}
		return expectContent(p("grown"), want)
	}())

	check("run a binary stored in gofs", func() error {
		self, err := os.ReadFile("/proc/self/exe")
		if err != nil {
			return err
		}
		bin := p("fstest-copy")
		if err := os.WriteFile(bin, self, 0o755); err != nil {
			return err
		}
		out, err := exec.Command(bin, "exec-probe").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		if strings.TrimSpace(string(out)) != "exec-probe: running from "+bin {
			return fmt.Errorf("output %q", out)
		}
		fmt.Printf("fstest:      ran a %d KiB binary from gofs\n", len(self)>>10)
		return os.Remove(bin)
	}())

	for _, f := range []string{path, p("grown")} {
		os.Remove(f)
	}
}

func xattrTests() {
	check("xattrs: set, get, list, flags, remove (file and dir)", func() error {
		file := p("xattr-file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			return err
		}
		defer os.Remove(file)
		dir := p("xattr-dir")
		if err := os.Mkdir(dir, 0o755); err != nil {
			return err
		}
		defer os.Remove(dir)
		for _, path := range []string{file, dir} {
			if err := syscall.Setxattr(path, "user.color", []byte("blue"), 0); err != nil {
				return fmt.Errorf("setxattr: %w", err)
			}
			if err := syscall.Setxattr(path, "trusted.secret", []byte{0, 1, 2}, 0); err != nil {
				return fmt.Errorf("setxattr trusted: %w", err)
			}
			if n, err := syscall.Getxattr(path, "user.color", nil); n != 4 || err != nil {
				return fmt.Errorf("size query = %d, %v", n, err)
			}
			buf := make([]byte, 64)
			n, err := syscall.Getxattr(path, "user.color", buf)
			if err != nil || string(buf[:n]) != "blue" {
				return fmt.Errorf("getxattr = %q, %v", buf[:n], err)
			}
			if _, err := syscall.Getxattr(path, "user.color", buf[:2]); err != syscall.ERANGE {
				return fmt.Errorf("small buffer: %v", err)
			}
			if err := syscall.Setxattr(path, "user.color", []byte("red"), 1); err != syscall.EEXIST {
				return fmt.Errorf("XATTR_CREATE on existing: %v", err)
			}
			if err := syscall.Setxattr(path, "user.size", []byte("L"), 2); err != syscall.ENODATA {
				return fmt.Errorf("XATTR_REPLACE on missing: %v", err)
			}
			if err := syscall.Setxattr(path, "user.color", []byte("green"), 2); err != nil {
				return fmt.Errorf("XATTR_REPLACE: %v", err)
			}
			n, err = syscall.Listxattr(path, buf)
			if err != nil {
				return fmt.Errorf("listxattr: %w", err)
			}
			names := strings.Split(strings.TrimSuffix(string(buf[:n]), "\x00"), "\x00")
			sort.Strings(names)
			if strings.Join(names, ",") != "trusted.secret,user.color" {
				return fmt.Errorf("listxattr = %q", names)
			}
			if err := syscall.Removexattr(path, "user.color"); err != nil {
				return fmt.Errorf("removexattr: %w", err)
			}
			if _, err := syscall.Getxattr(path, "user.color", buf); err != syscall.ENODATA {
				return fmt.Errorf("get after remove: %v", err)
			}
			if err := syscall.Removexattr(path, "user.color"); err != syscall.ENODATA {
				return fmt.Errorf("remove missing: %v", err)
			}
			if err := syscall.Setxattr(path, "security.label", []byte("x"), 0); err != syscall.EOPNOTSUPP {
				return fmt.Errorf("security.* = %v, want EOPNOTSUPP", err)
			}
		}
		return nil
	}())
}
