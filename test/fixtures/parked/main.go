// parked checks the Go lock is released while a task sleeps (parks) and that
// the GC scans parked tasks' stacks:
//
//   - reading /proc/parked_hold builds data referenced only from that
//     task's stack, sleeps 500 ms (time.Sleep parks: the Go lock is released)
//     and then verifies the data;
//   - reading /proc/parked_churn meanwhile, from other tasks, allocates
//     garbage forcing many collections. If the GC didn't scan the parked
//     stack, the held data would be freed and overwritten.
package main

import (
	"runtime"
	"strconv"
	"time"

	"gokm/kernel"
	"gokm/procfs"
)

type node struct {
	next  *node
	value int
	label string
}

var ring [16][]byte

func hold() ([]byte, error) {
	var head *node
	for i := 0; i < 300; i++ {
		head = &node{next: head, value: i, label: "v" + strconv.Itoa(i)}
	}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	time.Sleep(500 * time.Millisecond) // parks: other tasks run Go meanwhile

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	i := 299
	for n := head; n != nil; n = n.next {
		if n.value != i || n.label != "v"+strconv.Itoa(i) {
			return []byte("corrupt\n"), nil
		}
		i--
	}
	if i != -1 {
		return []byte("corrupt (short)\n"), nil
	}
	return []byte("ok gcs-while-parked=" + strconv.Itoa(int(after.NumGC-before.NumGC)) + "\n"), nil
}

func churn() ([]byte, error) {
	sum := 0
	for i := 0; i < 3000; i++ { // ~3 MiB of garbage through a 256K heap
		b := make([]byte, 1024)
		b[i%1024] = byte(i)
		ring[i%len(ring)] = b
		if r := ring[(i+5)%len(ring)]; len(r) > 0 {
			sum += int(r[0])
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return []byte(strconv.Itoa(int(ms.NumGC)) + " " + strconv.Itoa(sum%7) + "\n"), nil
}

func main() {
	if _, err := procfs.Create("parked_hold", procfs.ReadFunc(hold)); err != nil {
		kernel.FailLoad(err)
		return
	}
	if _, err := procfs.Create("parked_churn", procfs.ReadFunc(churn)); err != nil {
		kernel.FailLoad(err)
		return
	}
	// A handler that panics: the framework recovers and the read fails
	// with EIO instead of crashing the kernel.
	if _, err := procfs.Create("parked_panic", procfs.ReadFunc(func() ([]byte, error) {
		var m map[string]int
		m["boom"]++ // assignment to entry in nil map
		return nil, nil
	})); err != nil {
		kernel.FailLoad(err)
		return
	}
}
