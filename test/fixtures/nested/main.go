// nested checks re-entering Go from C on the same thread (Go -> C -> Go), with
// the inner call forcing garbage collections. The outer Go frame holds data
// referenced only from its stack, which sits above the C frames: if the GC
// didn't scan it (wrong stack top), the data would be freed and overwritten.
package main

import (
	"runtime"
	"strconv"

	"gokm/kernel"
)

// Implemented in csrc/nested.c: calls nested_go_callback depth times, each
// time via GOKM_ENTER/GOKM_LEAVE.
//
//export nested_call_back
func nestedCallBack(depth int32) int32

var ring [8][]byte // keeps the garbage observable so it isn't optimized away

//export nested_go_callback
func nestedGoCallback(depth int32) int32 {
	sum := 0
	for i := 0; i < 2000; i++ { // ~2 MiB of garbage per level (256K heap)
		b := make([]byte, 1024)
		b[i%1024] = byte(i)
		ring[i%len(ring)] = b
		if r := ring[(i+3)%len(ring)]; len(r) > 0 {
			sum += int(r[0])
		}
	}
	if sum < 0 {
		return -1
	}
	if depth > 1 {
		return nestedCallBack(depth-1) + 1
	}
	return 1
}

type item struct {
	n    int
	text string
}

func main() {
	var items []*item // only referenced from this frame
	for i := 0; i < 200; i++ {
		items = append(items, &item{n: i, text: "item-" + strconv.Itoa(i)})
	}

	levels := nestedCallBack(3)

	ok := levels == 3
	for i, it := range items {
		if it.n != i || it.text != "item-"+strconv.Itoa(i) {
			ok = false
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ok {
		kernel.Print("OK (" + strconv.Itoa(int(levels)) + " levels, " +
			strconv.FormatUint(uint64(ms.NumGC), 10) + " GCs)")
	} else {
		kernel.Print("CORRUPTION DETECTED")
	}
}
