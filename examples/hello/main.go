// A Linux kernel module written in Go and compiled with TinyGo.
//
// main() runs as the module's init function (insmod); the function passed to
// kernel.OnExit runs at rmmod.
package main

import (
	"strconv"

	"gokm/kernel"
)

type greeter interface {
	Greet() string
}

type gopher struct{ name string }

func (g *gopher) Greet() string { return "hello from " + g.name + "!" }

var loadedAt int64

func main() {
	loadedAt = kernel.MonotonicNanos()

	// Interfaces, heap allocation, closures, maps and slices all work.
	var g greeter = &gopher{name: "TinyGo"}
	kernel.Print(g.Greet())

	counts := map[string]int{}
	for _, w := range []string{"go", "kernel", "go", "module", "go"} {
		counts[w]++
	}
	kernel.Print("word count: go=" + strconv.Itoa(counts["go"]) +
		" kernel=" + strconv.Itoa(counts["kernel"]))

	var fib []int
	a, b := 0, 1
	for i := 0; i < 15; i++ {
		fib = append(fib, a)
		a, b = b, a+b
	}
	kernel.Print("fib[14] = " + strconv.Itoa(fib[14]))

	kernel.OnExit(func() {
		alive := (kernel.MonotonicNanos() - loadedAt) / 1000000
		kernel.Print("goodbye after " + strconv.FormatInt(alive, 10) + " ms")
	})
}
