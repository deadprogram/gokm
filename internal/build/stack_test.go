package build

import (
	"strings"
	"testing"
)

func TestIRCallGraph(t *testing.T) {
	ir := `define internal fastcc i64 @main.fib(i64 %n) unnamed_addr #3 {
entry:
  %a = tail call fastcc i64 @main.fib(i64 %n)
  ret i64 %a
}

define i64 @"gokm_fs_go_lookup"(ptr %p) #3 {
  %r = call i64 @"(*main.dir).Lookup"(ptr %p, ptr undef)
  %x = call i64 %r(ptr %p)
  call void @llvm.memset.p0.i64(ptr %p, i8 0, i64 8, i1 false)
  ret i64 0
}
`
	g := &stackGraph{frame: map[string]uint64{"main.fib": 24, "(*main.dir).Lookup": 64, "gokm_fs_go_lookup": 32},
		calls: map[string][]string{}, indirect: map[string]bool{},
		memo: map[string]*stackEntry{}, visiting: map[string]bool{}, modPath: "m"}
	irCallGraph([]byte(ir), g)
	if strings.Join(g.calls["main.fib"], ",") != "main.fib" {
		t.Errorf("fib calls %v", g.calls["main.fib"])
	}
	if strings.Join(g.calls["gokm_fs_go_lookup"], ",") != "(*main.dir).Lookup" || !g.indirect["gokm_fs_go_lookup"] {
		t.Errorf("lookup calls %v indirect=%v", g.calls["gokm_fs_go_lookup"], g.indirect["gokm_fs_go_lookup"])
	}
	if e := g.deepest("main.fib"); !e.Recursive || e.RecursesIn != "main.fib" {
		t.Errorf("fib not recursive: %+v", e)
	}
	if e := g.deepest("gokm_fs_go_lookup"); e.Bytes != 32+8+64+8 || !e.Indirect {
		t.Errorf("lookup: %+v", e)
	}
}
