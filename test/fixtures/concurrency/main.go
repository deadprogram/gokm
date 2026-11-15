// concurrency exercises parallel Go code, goroutines and the growable heap.
// Its /proc files are driven by conctest inside the QEMU VM (-smp 2).
package main

import (
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gokm/kernel"
	"gokm/procfs"
)

// ---- parallelism: CPU-bound handler, no allocation ----

func spin() ([]byte, error) {
	start := kernel.MonotonicNanos()
	x := uint64(1)
	for kernel.MonotonicNanos()-start < 300_000_000 { // 300 ms of pure computation
		for i := 0; i < 1000; i++ {
			x = x*6364136223846793005 + 1442695040888963407
		}
	}
	return []byte(fmt.Sprintf("%d %d\n", (kernel.MonotonicNanos()-start)/1e6, x%10)), nil
}

// ---- goroutines, at load time ----

func goroutineReport() string {
	// 8 goroutines x 5000 increments under a mutex.
	var mu sync.Mutex
	var wg sync.WaitGroup
	counter := 0
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				mu.Lock()
				counter++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// A pipeline: generator -> squarer -> summer, over unbuffered channels.
	nums := make(chan int)
	squares := make(chan int)
	go func() {
		for i := 1; i <= 100; i++ {
			nums <- i
		}
		close(nums)
	}()
	go func() {
		for n := range nums {
			squares <- n * n
		}
		close(squares)
	}()
	sum := 0
	for s := range squares {
		sum += s
	}

	// select with a timer.
	never := make(chan int)
	timedOut := false
	select {
	case <-never:
	case <-time.After(20 * time.Millisecond):
		timedOut = true
	}
	return fmt.Sprintf("counter=%d sum=%d timeout=%v", counter, sum, timedOut)
}

var (
	ticks  atomic.Int64
	stopCh = make(chan struct{})
)

func ticker() {
	for {
		select {
		case <-stopCh:
			return
		default:
		}
		ticks.Add(1)
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- heap growth ----

var growSet [][]byte

func grow() ([]byte, error) {
	var before, peak runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 6*1024; i++ { // 6 MiB live, in 1 KiB pieces
		b := make([]byte, 1024)
		b[0], b[1023] = byte(i), byte(i>>8)
		growSet = append(growSet, b)
	}
	ok := true
	for i, b := range growSet {
		if b[0] != byte(i) || b[1023] != byte(i>>8) {
			ok = false
		}
	}
	runtime.ReadMemStats(&peak)
	growSet = nil
	return []byte(fmt.Sprintf("before=%d peak=%d intact=%v\n",
		before.HeapSys>>10, peak.HeapSys>>10, ok)), nil
}

// shrink collects (from a fresh stack, so nothing of grow's is left on it)
// and reports the heap size: empty chunks go back to the kernel.
func shrink() ([]byte, error) {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return []byte(fmt.Sprintf("%d\n", ms.HeapSys>>10)), nil
}

// ---- concurrent allocation: data on this task's stack survives others' GCs ----

type node struct {
	next *node
	val  int
	name string
}

func allocate() ([]byte, error) {
	var head *node
	for i := 0; i < 500; i++ {
		head = &node{next: head, val: i, name: "n" + strconv.Itoa(i)}
	}
	var ring [16][]byte
	sum := 0
	for i := 0; i < 20000; i++ { // ~20 MiB of garbage
		b := make([]byte, 1024)
		b[i%1024] = byte(i)
		ring[i%16] = b
		if r := ring[(i+7)%16]; r != nil {
			sum += int(r[(i+7)%1024])
		}
	}
	i := 499
	for n := head; n != nil; n = n.next {
		if n.val != i || n.name != "n"+strconv.Itoa(i) {
			return []byte("corrupt\n"), nil
		}
		i--
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return []byte(fmt.Sprintf("ok gcs=%d sum=%d\n", ms.NumGC, sum%10)), nil
}

// atomicMisuse sleeps while holding a SpinLock (atomic context): that must
// be a recoverable panic, not a deadlock or kernel BUG.
func atomicMisuse() ([]byte, error) {
	var l kernel.SpinLock
	atomicSeen := false
	msg := "no panic"
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		l.Lock()
		defer l.Unlock()
		atomicSeen = kernel.Atomic()
		time.Sleep(time.Millisecond)
	}()
	return []byte(fmt.Sprintf("atomic=%v outside=%v panic=%q\n", atomicSeen, kernel.Atomic(), msg)), nil
}

func main() {
	report := goroutineReport()
	kernel.Print("goroutines: " + report)

	go ticker()
	leaked := make(chan int)
	go func() { <-leaked }() // blocks forever: unload must still work

	files := map[string]func() ([]byte, error){
		"conc_spin":     spin,
		"conc_grow":     grow,
		"conc_shrink":   shrink,
		"conc_atomic":   atomicMisuse,
		"conc_alloc":    allocate,
		"conc_report":   func() ([]byte, error) { return []byte(report + "\n"), nil },
		"conc_ticks":    func() ([]byte, error) { return []byte(strconv.FormatInt(ticks.Load(), 10) + "\n"), nil },
		"conc_routines": func() ([]byte, error) { return []byte(strconv.Itoa(runtime.NumGoroutine()) + "\n"), nil },
	}
	for name, fn := range files {
		if _, err := procfs.Create(name, procfs.ReadFunc(fn)); err != nil {
			kernel.FailLoad(err)
			return
		}
	}
	kernel.OnExit(func() {
		close(stopCh)
		kernel.Print("exit: stopped ticker after " + strconv.FormatInt(ticks.Load(), 10) + " ticks")
	})
}
