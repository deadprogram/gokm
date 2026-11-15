// failload checks that kernel.FailLoad makes insmod fail with the given errno
// (the QEMU test expects ENODEV) and that the exit hook doesn't run.
package main

import "gokm/kernel"

func main() {
	kernel.OnExit(func() { kernel.Print("BUG: exit hook ran after failed load") })
	kernel.Print("failing load with ENODEV")
	kernel.FailLoad(kernel.ENODEV)
}
