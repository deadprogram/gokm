// Package kernel is a tiny set of bindings to Linux kernel facilities for Go
// code compiled with TinyGo into a kernel module. The C side lives in shim.c.
package kernel

import "unsafe"

//export gokm_print
func gokmPrint(buf *byte, n int)

//export gokm_ktime_ns
func gokmKtimeNs() int64

// Print writes s to the kernel log (printk, KERN_INFO). A trailing newline is
// not needed; each call produces one log line.
func Print(s string) {
	if len(s) == 0 {
		gokmPrint(nil, 0)
		return
	}
	gokmPrint(unsafe.StringData(s), len(s))
}

// MonotonicNanos returns ktime_get_ns().
func MonotonicNanos() int64 {
	return gokmKtimeNs()
}

//go:linkname setExit runtime.kmodSetExit
func setExit(fn func())

// OnExit registers fn to be called when the module is unloaded (rmmod).
func OnExit(fn func()) {
	setExit(fn)
}
