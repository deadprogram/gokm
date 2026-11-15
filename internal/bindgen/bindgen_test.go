package bindgen

import (
	"os"
	"strings"
	"testing"

	"gokm/internal/btf"
)

func TestGoName(t *testing.T) {
	for c, want := range map[string]string{
		"si_meminfo":        "SiMeminfo",
		"super_block":       "SuperBlock",
		"i_mode":            "IMode",
		"__kmalloc":         "Kmalloc",
		"new_utsname":       "NewUtsname",
		"ktime_get_seconds": "KtimeGetSeconds",
		"_1":                "X1",
	} {
		if got := GoName(c); got != want {
			t.Errorf("GoName(%q) = %q, want %q", c, got, want)
		}
	}
}

// TestGenerateRunningKernel generates bindings from the running kernel's
// BTF (when available) and checks the layout of a well-known struct.
func TestGenerateRunningKernel(t *testing.T) {
	spec, err := btf.Load("/sys/kernel/btf/vmlinux")
	if err != nil {
		t.Skip("no kernel BTF:", err)
	}
	exports := map[string]string{"si_meminfo": "", "kstrtouint": ""}
	g := New(spec, exports)
	names := []string{"Sysinfo", "SiMeminfo", "Kstrtouint", "I_NEW", "HZ"}
	if m := g.Macros(names); len(m) != 1 || m[0] != "HZ" {
		t.Fatalf("Macros = %v, want [HZ]", m)
	}
	out, err := g.Generate(names, map[string]Const{"HZ": {Value: 1000, Signed: true}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	src := string(out.Go)
	for _, want := range []string{
		"type Sysinfo struct {",
		"Totalram  uint64",
		"// totalram, offset 32",
		"MemUnit   uint32 // mem_unit, offset 104",
		"var _ [112 - unsafe.Sizeof(Sysinfo{})]byte",
		"//export si_meminfo\nfunc SiMeminfo(val *Sysinfo)",
		"func Kstrtouint(s *byte, base uint32, res *uint32) int32",
		"HZ    = 1000",
		"I_NEW = 1",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated code lacks %q", want)
		}
	}
	if t.Failed() {
		os.WriteFile(t.TempDir()+"/linux.go", out.Go, 0o644)
		t.Log(src)
	}
}
