// gcstress exercises the garbage collector: it allocates many times its heap
// size in short-lived garbage while keeping data alive through locals
// (stack/registers), globals, maps and closures, then checks all of it
// survived intact. Build with a small heap to force lots of collections:
//
//	gokm build -heap-size 256K
package main

import (
	"runtime"
	"strconv"

	"gokm/kernel"
)

type node struct {
	next  *node
	value int
	label string
}

// Reachable only through a global.
var registry = map[string][]int{}

func buildList(n int) *node {
	var head *node
	for i := n - 1; i >= 0; i-- {
		head = &node{next: head, value: i, label: "n" + strconv.Itoa(i)}
	}
	return head
}

func checkList(head *node, n int) bool {
	i := 0
	for p := head; p != nil; p = p.next {
		if p.value != i || p.label != "n"+strconv.Itoa(i) {
			return false
		}
		i++
	}
	return i == n
}

// churn allocates `total` bytes of garbage in assorted shapes.
func churn(total int) int {
	sink := 0
	for done := 0; done < total; {
		s := make([]byte, 64+done%4000)
		s[len(s)-1] = byte(done)
		sink += int(s[len(s)-1])
		m := map[int]string{done: strconv.Itoa(done)}
		sink += len(m[done])
		str := "garbage-" + strconv.Itoa(done)
		sink += len(str)
		done += len(s) + len(str) + 64
	}
	return sink
}

func stats(when string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	kernel.Print(when + ": heap " + strconv.FormatUint(m.HeapSys/1024, 10) + " KiB" +
		", in use " + strconv.FormatUint(m.HeapInuse/1024, 10) + " KiB" +
		", GCs " + strconv.FormatUint(uint64(m.NumGC), 10) +
		", mallocs " + strconv.FormatUint(m.Mallocs, 10) +
		", frees " + strconv.FormatUint(m.Frees, 10))
}

func main() {
	stats("start")

	const n = 500
	list := buildList(n) // only referenced from this stack frame
	for i := 0; i < 50; i++ {
		registry["k"+strconv.Itoa(i)] = []int{i, i * i}
	}
	counter := 0
	bump := func() { counter++ } // closure capturing a heap-allocated variable

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	heap := int(m.HeapSys)

	sink := 0
	for round := 0; round < 10; round++ {
		sink += churn(5 * heap)
		bump()
	}

	ok := checkList(list, n) && counter == 10
	for i := 0; i < 50; i++ {
		v := registry["k"+strconv.Itoa(i)]
		if len(v) != 2 || v[0] != i || v[1] != i*i {
			ok = false
		}
	}
	stats("after churning " + strconv.Itoa(50*heap/1024) + " KiB")

	// Off-heap buffer: manual lifetime, doesn't use the GC heap.
	big := kernel.AllocBytes(4 << 20)
	for i := range big {
		big[i] = byte(i)
	}
	sum := 0
	for _, b := range big {
		sum += int(b)
	}
	kernel.FreeBytes(big)
	ok = ok && sum == (4<<20)/256*(255*256/2)
	_ = kernel.AllocBytes(64 << 10) // deliberately leaked: freed at rmmod

	if ok {
		kernel.Print("OK (sink " + strconv.Itoa(sink%1000) + ")")
	} else {
		kernel.Print("CORRUPTION DETECTED")
	}

	kernel.OnExit(func() {
		churn(5 * heap)
		runtime.GC()
		stats("exit")
		if checkList(list, n) {
			kernel.Print("list still intact at exit")
		} else {
			kernel.Print("list corrupted at exit")
		}
	})
}
