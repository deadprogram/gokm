// Command parkedtest drives test/fixtures/parked inside the QEMU VM.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	type result struct {
		out string
		err error
		at  time.Duration
	}
	start := time.Now()
	holdDone := make(chan result, 1)
	go func() {
		b, err := os.ReadFile("/proc/parked_hold")
		holdDone <- result{string(b), err, time.Since(start)}
	}()

	time.Sleep(50 * time.Millisecond)
	churns := 0
	var churnEnd time.Duration
	for i := 0; i < 5; i++ {
		if _, err := os.ReadFile("/proc/parked_churn"); err != nil {
			fmt.Println("parkedtest: churn failed:", err)
			os.Exit(1)
		}
		churns++
		churnEnd = time.Since(start)
	}
	hold := <-holdDone
	fmt.Printf("parkedtest: %d churns finished at %v; hold finished at %v: %q\n",
		churns, churnEnd.Round(time.Millisecond), hold.at.Round(time.Millisecond), hold.out)

	_, perr := os.ReadFile("/proc/parked_panic")
	_, after := os.ReadFile("/proc/parked_churn")
	fmt.Printf("parkedtest: panicking handler: %v; module still works: %v\n", perr, after == nil)

	switch {
	case perr == nil || !strings.Contains(perr.Error(), "input/output error") || after != nil:
		fmt.Println("parkedtest: FAIL: panic in handler wasn't turned into EIO")
	case hold.err != nil:
		fmt.Println("parkedtest: FAIL hold:", hold.err)
	case !strings.HasPrefix(hold.out, "ok") || strings.HasPrefix(hold.out, "ok gcs-while-parked=0\n"):
		fmt.Println("parkedtest: FAIL: data corrupted or no GC ran while parked")
	case churnEnd >= hold.at:
		fmt.Println("parkedtest: FAIL: churn didn't run while hold was parked (lock not released)")
	default:
		fmt.Println("parkedtest: PASS")
		return
	}
	os.Exit(1)
}
