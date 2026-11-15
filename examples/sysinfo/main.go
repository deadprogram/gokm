// sysinfo reads kernel information directly through gokm/linux, the kernel
// bindings gokm generates from the target kernel's BTF, and serves it in
// /proc/gosysinfo. Writing a number of milliseconds to the file sleeps that
// long in the kernel (a blocking call: other tasks keep running Go code).
package main

import (
	"bytes"
	"fmt"

	"gokm/kernel"
	"gokm/linux"
	"gokm/procfs"
)

// Static inline functions and macros have no symbol to call: gokm generates
// a C wrapper with the signature declared here.

//gokm:inline utsname linux/utsname.h
func utsname() *linux.NewUtsname

//gokm:inline num_online_cpus
func numOnlineCPUs() uint32

//gokm:blocking msleep linux/delay.h
func msleep(ms uint32)

func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

type info struct{}

func (info) Read() ([]byte, error) {
	var si linux.Sysinfo // struct sysinfo, laid out exactly as in this kernel
	linux.SiMeminfo(&si) // exported kernel function

	u := utsname()
	unit := uint64(si.MemUnit)
	return []byte(fmt.Sprintf(
		"release: %s\nmachine: %s\ncpus: %d\nram_total_mib: %d\nram_free_mib: %d\n"+
			"uptime_s: %d\nhz: %d\npage_size: %d\n",
		cstring(u.Release[:]), cstring(u.Machine[:]), numOnlineCPUs(),
		si.Totalram*unit>>20, si.Freeram*unit>>20,
		linux.KtimeGetSeconds(), linux.HZ, linux.PAGE_SIZE)), nil
}

// Write parses a number of milliseconds with the kernel's own kstrtouint
// and sleeps that long.
func (info) Write(p []byte) error {
	s := append(bytes.TrimSpace(p), 0)
	var ms uint32
	if rc := linux.Kstrtouint(&s[0], 10, &ms); rc != 0 {
		return kernel.Errno(-rc)
	}
	msleep(ms)
	return nil
}

func main() {
	if _, err := procfs.Create("gosysinfo", info{}); err != nil {
		kernel.FailLoad(err)
	}
}
