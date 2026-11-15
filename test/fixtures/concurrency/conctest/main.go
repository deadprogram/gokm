// Command conctest drives test/fixtures/concurrency inside the QEMU VM.
package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var failed bool

func check(name string, err error) {
	if err != nil {
		fmt.Printf("conctest: FAIL %s: %v\n", name, err)
		failed = true
		return
	}
	fmt.Printf("conctest: ok   %s\n", name)
}

func read(name string) string {
	b, err := os.ReadFile("/proc/" + name)
	if err != nil {
		return "error: " + err.Error()
	}
	return strings.TrimSpace(string(b))
}

func parallel(name string, n int) ([]string, time.Duration) {
	out := make([]string, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); out[i] = read(name) }(i)
	}
	wg.Wait()
	return out, time.Since(start)
}

func main() {
	check("goroutines: mutex, pipeline, select+timer", func() error {
		if r := read("conc_report"); r != "counter=40000 sum=338350 timeout=true" {
			return fmt.Errorf("report %q", r)
		}
		return nil
	}())

	check("Go runs on 2 CPUs at once", func() error {
		_, one := parallel("conc_spin", 1)
		outs, two := parallel("conc_spin", 2)
		fmt.Printf("conctest:      1 spinner: %v, 2 concurrent spinners: %v (%v)\n",
			one.Round(time.Millisecond), two.Round(time.Millisecond), outs)
		if two > one*3/2 {
			return fmt.Errorf("2 spinners took %v vs %v for one: serialized", two, one)
		}
		return nil
	}())

	check("background goroutine ticks", func() error {
		a := read("conc_ticks")
		time.Sleep(200 * time.Millisecond)
		b := read("conc_ticks")
		fmt.Printf("conctest:      ticks %s -> %s, goroutines: %s\n", a, b, read("conc_routines"))
		if a == b {
			return fmt.Errorf("ticker isn't running")
		}
		return nil
	}())

	check("heap grows and shrinks", func() error {
		r := read("conc_grow")
		var before, peak, after int
		var intact string
		fmt.Sscanf(r, "before=%d peak=%d intact=%s", &before, &peak, &intact)
		fmt.Sscanf(read("conc_shrink"), "%d", &after)
		fmt.Printf("conctest:      heap KiB: before=%d with 6 MiB live=%d after release=%d\n", before, peak, after)
		if intact != "true" || peak < 6*1024 || after >= peak/2 {
			return fmt.Errorf("unexpected: %s, after=%d", r, after)
		}
		return nil
	}())

	check("4 concurrent allocators, live data survives GCs", func() error {
		outs, d := parallel("conc_alloc", 4)
		fmt.Printf("conctest:      %v in %v\n", outs, d.Round(time.Millisecond))
		for _, o := range outs {
			if !strings.HasPrefix(o, "ok") {
				return fmt.Errorf("allocator: %s", o)
			}
		}
		return nil
	}())

	check("blocking in atomic context is a recoverable panic", func() error {
		r := read("conc_atomic")
		fmt.Printf("conctest:      %s\n", r)
		if !strings.HasPrefix(r, "atomic=true outside=false panic=") || !strings.Contains(r, "blocking operation in atomic context") {
			return fmt.Errorf("unexpected: %s", r)
		}
		return nil
	}())

	if failed {
		os.Exit(1)
	}
}
