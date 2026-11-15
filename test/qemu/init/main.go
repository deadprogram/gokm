// Command init is the /init of the QEMU test initramfs (built with the regular
// Go toolchain, statically). It loads /module.ko, shows it in /proc/modules,
// unloads it and powers the VM off. Kernel log output goes to the serial
// console.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// finit_module(2) on x86-64; the syscall package doesn't name it.
const sysFinitModule = 313

func main() {
	ok := run()
	if ok {
		fmt.Println("TEST: PASS")
	} else {
		fmt.Println("TEST: FAIL")
	}
	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}

func run() bool {
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		fmt.Println("mount /proc:", err)
	}

	f, err := os.Open("/module.ko")
	if err != nil {
		fmt.Println("open module:", err)
		return false
	}
	// Optional module parameters, e.g. "heap_size=2M" (written by run.sh).
	p, _ := os.ReadFile("/params")
	params := append([]byte(strings.TrimSpace(string(p))), 0)
	fmt.Printf("init: finit_module(/module.ko, %q)\n", params[:len(params)-1])
	_, _, errno := syscall.Syscall(sysFinitModule, f.Fd(),
		uintptr(unsafe.Pointer(&params[0])), 0)
	f.Close()
	if errno != 0 {
		fmt.Println("finit_module:", errno)
		return false
	}

	mods, _ := os.ReadFile("/proc/modules")
	fmt.Printf("init: /proc/modules:\n%s", mods)

	// Ours is the only module in this VM: the first /proc/modules field.
	fields := strings.Fields(string(mods))
	if len(fields) == 0 {
		fmt.Println("module not listed in /proc/modules")
		return false
	}
	modName := fields[0]

	time.Sleep(200 * time.Millisecond)

	name := []byte(modName + "\x00")
	fmt.Printf("init: delete_module(%s)\n", modName)
	_, _, errno = syscall.Syscall(syscall.SYS_DELETE_MODULE,
		uintptr(unsafe.Pointer(&name[0])), syscall.O_NONBLOCK, 0)
	if errno != 0 {
		fmt.Println("delete_module:", errno)
		return false
	}
	return true
}
