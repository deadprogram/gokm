//go:build linuxkernel

package interrupt

// Go code in a gokm module never runs in hard-interrupt context, so there is
// nothing to disable: TinyGo uses Disable/Restore to guard data shared with
// Go interrupt handlers (e.g. around channel locks). Disabling hardware
// interrupts there would only prevent the code inside from blocking.
// (Data shared with atomic-context Go code needs kernel.SpinLock.)

// State is unused.
type State uintptr

// Disable does nothing.
func Disable() (state State) { return 0 }

// Restore does nothing.
func Restore(state State) {}

// In reports whether we're in an interrupt handler: never.
func In() bool { return false }
