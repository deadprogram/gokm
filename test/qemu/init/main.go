// Command init is the /init of the QEMU test initramfs (built with the regular
// Go toolchain, statically). It loads /module.ko, shows it in /proc/modules,
// runs /test if present, unloads the module and powers the VM off. Kernel log
// output goes to the serial console.
//
// If /expect_errno exists, loading is instead expected to fail with that
// errno name (e.g. ENODEV).
package main

import (
	"fmt"
	"os"
	"os/exec"
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
	if err := syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, ""); err != nil {
		fmt.Println("mount /dev:", err)
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
	if want, err := os.ReadFile("/expect_errno"); err == nil {
		got := errnoName(errno)
		fmt.Printf("init: finit_module returned %s, expected %s\n", got, strings.TrimSpace(string(want)))
		return got == strings.TrimSpace(string(want))
	}
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

	if _, err := os.Stat("/test"); err == nil {
		fmt.Println("init: running /test")
		cmd := exec.Command("/test")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Println("init: /test failed:", err)
			return false
		}
	}

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

func errnoName(e syscall.Errno) string {
	names := map[syscall.Errno]string{
		0: "OK", syscall.ENODEV: "ENODEV", syscall.ENOMEM: "ENOMEM",
		syscall.EINVAL: "EINVAL", syscall.EBUSY: "EBUSY", syscall.EIO: "EIO",
		syscall.EEXIST: "EEXIST", syscall.ENOENT: "ENOENT", syscall.EPERM: "EPERM",
	}
	if n, ok := names[e]; ok {
		return n
	}
	return fmt.Sprint(int(e))
}
