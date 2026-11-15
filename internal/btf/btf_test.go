package btf

import (
	"os"
	"testing"
	"time"
)

// TestVmlinux parses the running kernel's BTF, when available.
func TestVmlinux(t *testing.T) {
	const path = "/sys/kernel/btf/vmlinux"
	if _, err := os.Stat(path); err != nil {
		t.Skip("no", path)
	}
	start := time.Now()
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d types in %v", len(s.Types), time.Since(start))
	var inode, sysinfo, siMeminfo *Type
	for _, ty := range s.Types {
		switch {
		case ty.Kind == KindStruct && ty.Name == "inode":
			inode = ty
		case ty.Kind == KindStruct && ty.Name == "sysinfo":
			sysinfo = ty
		case ty.Kind == KindFunc && ty.Name == "si_meminfo":
			siMeminfo = ty
		}
	}
	if inode == nil || sysinfo == nil || siMeminfo == nil {
		t.Fatalf("missing types: inode=%v sysinfo=%v si_meminfo=%v", inode != nil, sysinfo != nil, siMeminfo != nil)
	}
	if sysinfo.Size != 112 {
		t.Errorf("sizeof(struct sysinfo) = %d, want 112", sysinfo.Size)
	}
	proto := s.Type(siMeminfo.Ref)
	if proto.Kind != KindFuncProto || len(proto.Params) != 1 {
		t.Fatalf("si_meminfo proto: %+v", proto)
	}
	if p := s.Skip(proto.Params[0].Type); p.Kind != KindPtr || s.Skip(p.Ref) != sysinfo {
		t.Errorf("si_meminfo param isn't struct sysinfo *")
	}
}
