// Command devtest exercises /dev/gorev and /proc/gorev inside the QEMU VM
// (test/qemu/run.sh with TEST_PROG=examples/devices/devtest).
package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

var failed bool

func check(name string, err error) {
	if err != nil {
		fmt.Printf("devtest: FAIL %s: %v\n", name, err)
		failed = true
		return
	}
	fmt.Printf("devtest: ok   %s\n", name)
}

func main() {
	check("/dev/gorev exists, mode 0666", func() error {
		fi, err := os.Stat("/dev/gorev")
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeCharDevice == 0 || fi.Mode().Perm() != 0o666 {
			return fmt.Errorf("mode %v", fi.Mode())
		}
		return nil
	}())

	check("write then read reversed", func() error {
		if err := os.WriteFile("/dev/gorev", []byte("hello kernel\n"), 0); err != nil {
			return err
		}
		got, err := os.ReadFile("/dev/gorev")
		if err != nil {
			return err
		}
		if string(got) != "lenrek olleh\n" {
			return fmt.Errorf("got %q", got)
		}
		return nil
	}())

	check("20 concurrent writers/readers", func() error {
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					if err := os.WriteFile("/dev/gorev", []byte(fmt.Sprintf("msg-%d-%d", i, j)), 0); err != nil {
						errs <- err
						return
					}
					got, err := os.ReadFile("/dev/gorev")
					if err != nil || !strings.HasPrefix(string(got), "") || !strings.HasSuffix(string(got), "-gsm\n") || strings.Count(string(got), "\n") != 1 {
						errs <- fmt.Errorf("read %q %v", got, err)
						return
					}
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		return <-errs
	}())

	check("/proc/gorev stats", func() error {
		got, err := os.ReadFile("/proc/gorev")
		if err != nil {
			return err
		}
		fmt.Printf("devtest:      %s\n", strings.ReplaceAll(strings.TrimSpace(string(got)), "\n", ", "))
		if !strings.Contains(string(got), "writes: 1001\n") {
			return fmt.Errorf("unexpected stats %q", got)
		}
		return nil
	}())

	check("/proc/gorev reset", func() error {
		if err := os.WriteFile("/proc/gorev", []byte("reset\n"), 0); err != nil {
			return err
		}
		if err := os.WriteFile("/proc/gorev", []byte("bogus\n"), 0); err == nil {
			return fmt.Errorf("bogus write accepted")
		}
		got, _ := os.ReadFile("/proc/gorev")
		if !strings.HasPrefix(string(got), "opens: 0\nwrites: 0\n") {
			return fmt.Errorf("after reset %q", got)
		}
		return nil
	}())

	if failed {
		os.Exit(1)
	}
}
