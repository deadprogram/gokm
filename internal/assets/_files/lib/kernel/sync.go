package kernel

import "sync/atomic"

//export gokm_bh_disable
func bhDisable()

//export gokm_bh_enable
func bhEnable()

//export gokm_cpu_relax
func cpuRelax()

//export gokm_can_block
func canBlock() bool

// Atomic reports whether the calling code runs in atomic context (softirq,
// preemption disabled, a SpinLock held): there it must not block (no
// sync.Mutex, channels, time.Sleep, blocking kernel calls), and allocation
// can fail with an "out of memory" panic when the heap is full.
func Atomic() bool { return !canBlock() }

// SpinLock is a mutual-exclusion lock usable from any context, including
// atomic ones (e.g. a netdev Driver with AtomicTransmit). It disables
// softirqs on the local CPU while held, so code holding it is in atomic
// context: keep critical sections short and never block in them.
//
// Process-context-only code should use sync.Mutex instead.
type SpinLock struct {
	state atomic.Uint32
}

// Lock acquires the lock, spinning while another CPU holds it.
func (l *SpinLock) Lock() {
	bhDisable()
	for !l.state.CompareAndSwap(0, 1) {
		for l.state.Load() != 0 {
			cpuRelax()
		}
	}
}

// Unlock releases the lock.
func (l *SpinLock) Unlock() {
	l.state.Store(0)
	bhEnable()
}

// TryLock acquires the lock if it's free.
func (l *SpinLock) TryLock() bool {
	bhDisable()
	if l.state.CompareAndSwap(0, 1) {
		return true
	}
	bhEnable()
	return false
}
