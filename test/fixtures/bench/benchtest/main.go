// Command benchtest times reads of /proc/bench_call inside the QEMU VM.
package main

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
)

func reads(n int) error {
	for i := 0; i < n; i++ {
		if _, err := os.ReadFile("/proc/bench_call"); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	const n = 20000
	if err := reads(1000); err != nil {
		fmt.Println("benchtest: FAIL", err)
		os.Exit(1)
	}
	for _, p := range []int{1, runtime.NumCPU()} {
		var wg sync.WaitGroup
		start := time.Now()
		for g := 0; g < p; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				reads(n)
			}()
		}
		wg.Wait()
		fmt.Printf("benchtest: procfs read x%d %.0f ns/op\n", p, float64(time.Since(start).Nanoseconds())/n)
	}
	fmt.Println("benchtest: PASS")
}
