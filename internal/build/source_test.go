package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareSourcePragmas(t *testing.T) {
	proj, dst := t.TempDir(), filepath.Join(t.TempDir(), "src")
	src := `package main

import (
	"gokm/kernel"
	k "gokm/linux"
)

//gokm:include linux/foo.h

// sleep waits.
//
//gokm:blocking msleep linux/delay.h
func sleep(ms uint32)

func main() {
	var si k.Sysinfo
	k.SiMeminfo(&si)
	kernel.Print("x")
}
`
	os.WriteFile(filepath.Join(proj, "go.mod"), []byte("module m\n"), 0o644)
	os.WriteFile(filepath.Join(proj, "main.go"), []byte(src), 0o644)
	os.MkdirAll(filepath.Join(proj, "csrc"), 0o755)
	os.WriteFile(filepath.Join(proj, "csrc", "x.c"), nil, 0o644)

	scan, err := prepareSource(proj, dst)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(scan.names, ",") != "SiMeminfo,Sysinfo" {
		t.Errorf("names = %v", scan.names)
	}
	if len(scan.pragmas) != 1 || scan.pragmas[0].Kind != "blocking" || scan.pragmas[0].CName != "msleep" ||
		scan.pragmas[0].Symbol != "gokm_blocking_msleep" || scan.pragmas[0].Linux != "k" {
		t.Errorf("pragmas = %+v", scan.pragmas)
	}
	if strings.Join(scan.headers, ",") != "linux/foo.h,linux/delay.h" {
		t.Errorf("headers = %v", scan.headers)
	}
	out, err := os.ReadFile(filepath.Join(dst, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "//export gokm_blocking_msleep\nfunc sleep(ms uint32)") {
		t.Errorf("pragma not rewritten:\n%s", out)
	}
	if strings.Count(string(out), "\n") != strings.Count(src, "\n") {
		t.Error("rewriting changed the number of lines")
	}
	if _, err := os.Stat(filepath.Join(dst, "csrc")); err == nil {
		t.Error("csrc/ copied into the Go source tree")
	}
}
