// bench measures runtime costs: defer, allocation, collection, and the
// C to Go callback path (driven by benchtest inside the QEMU VM).
package main

import (
	"fmt"
	"runtime"
	"sync"

	"gokm/kernel"
	"gokm/procfs"
)

var sink int

//go:noinline
func withDefer(i int) (rc int) {
	rc = -int(kernel.EIO)
	defer kernel.CatchPanic[int](nil)
	return i & 1
}

//go:noinline
func withDeferPtr(i int) (rc int) {
	defer kernel.CatchPanic(&rc)
	return i & 1
}

type node struct {
	next *node
	val  int
}

func nsPerOp(start int64, n int) float64 {
	return float64(kernel.MonotonicNanos()-start) / float64(n)
}

func benchDefer(name string, f func(int) int) string {
	const n = 1_000_000
	start := kernel.MonotonicNanos()
	for i := 0; i < n; i++ {
		sink += f(i)
	}
	return fmt.Sprintf("%s %.1f ns/op", name, nsPerOp(start, n))
}

func benchDeferParallel(name string, f func(int) int) string {
	const n = 500_000
	p := runtime.NumCPU()
	var wg sync.WaitGroup
	start := kernel.MonotonicNanos()
	for g := 0; g < p; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := 0
			for i := 0; i < n; i++ {
				s += f(i)
			}
			sink += s
		}()
	}
	wg.Wait()
	return fmt.Sprintf("%s x%d goroutines %.1f ns/op per goroutine", name, p, nsPerOp(start, n))
}

func benchAlloc() string {
	const n = 1_000_000
	var ring [1024]*node
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := kernel.MonotonicNanos()
	for i := 0; i < n; i++ {
		ring[i%len(ring)] = &node{next: ring[(i+1)%len(ring)], val: i}
	}
	r := nsPerOp(start, n)
	runtime.ReadMemStats(&after)
	sink += ring[0].val
	return fmt.Sprintf("alloc %.1f ns/op (%d GCs)", r, after.NumGC-before.NumGC)
}

func benchGC() string {
	var head *node
	for i := 0; i < 200_000; i++ {
		head = &node{next: head, val: i}
	}
	bufs := make([][]byte, 2000)
	for i := range bufs {
		bufs[i] = make([]byte, 512)
	}
	runtime.GC()
	const n = 10
	start := kernel.MonotonicNanos()
	for i := 0; i < n; i++ {
		runtime.GC()
	}
	r := nsPerOp(start, n) / 1e6
	sink += head.val + len(bufs[0])
	return fmt.Sprintf("gc %.2f ms/op (200k live objects)", r)
}

func main() {
	kernel.Print("bench: " + benchDefer("defer", withDefer))
	kernel.Print("bench: " + benchDefer("defer &rc", withDeferPtr))
	kernel.Print("bench: " + benchDeferParallel("defer", withDefer))
	kernel.Print("bench: " + benchDeferParallel("defer &rc", withDeferPtr))
	kernel.Print("bench: " + benchAlloc())
	kernel.Print("bench: " + benchGC())
	if _, err := procfs.Create("bench_call", procfs.ReadFunc(func() ([]byte, error) {
		return []byte("x"), nil
	})); err != nil {
		kernel.FailLoad(err)
	}
}
