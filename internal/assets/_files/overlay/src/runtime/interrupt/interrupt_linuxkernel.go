//go:build linuxkernel

package interrupt

// State is the saved RFLAGS value from local_irq_save().
type State uintptr

//export gokm_irq_save
func irqSave() uintptr

//export gokm_irq_restore
func irqRestore(flags uintptr)

// Disable disables interrupts on the local CPU (local_irq_save) and returns
// the previous state.
func Disable() (state State) {
	return State(irqSave())
}

// Restore restores the interrupt state returned by Disable.
func Restore(state State) {
	irqRestore(uintptr(state))
}

// In reports whether we're running in interrupt context. Go code in this
// module is only ever called from process context.
func In() bool {
	return false
}
