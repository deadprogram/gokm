package build

import (
	"strings"
	"testing"
)

func TestFixupIR(t *testing.T) {
	in := `@gokm_param_x = external local_unnamed_addr global i64, align 8
declare void @gokm_abort() local_unnamed_addr #4
declare void @gokm_print(ptr, i64) local_unnamed_addr #4
attributes #0 = { mustprogress nounwind willreturn memory(none) }
attributes #3 = { nounwind uwtable(sync) "target-cpu"="x86-64" "target-features"="-sse" }
attributes #4 = { "target-cpu"="x86-64" }
!llvm.module.flags = !{!1, !2}
`
	out := string(fixupIR([]byte(in)))

	for _, want := range []string{
		"@gokm_param_x = external dso_local local_unnamed_addr global i64, align 8\n",
		"declare void @gokm_abort() local_unnamed_addr noreturn #4\n",
		"declare void @gokm_print(ptr, i64) local_unnamed_addr #4\n",
		// intrinsic attribute groups (no target-cpu) are left alone
		"attributes #0 = { mustprogress nounwind willreturn memory(none) }\n",
		`attributes #3 = { noredzone fn_ret_thunk_extern nounwind "target-cpu"="x86-64" "target-features"="-sse" }` + "\n",
		`attributes #4 = { noredzone fn_ret_thunk_extern "target-cpu"="x86-64" }` + "\n",
		"!llvm.module.flags = !{!9000001, !1, !2}\n",
		`!9000001 = !{i32 8, !"cf-protection-branch", i32 1}` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "uwtable") {
		t.Errorf("uwtable not removed:\n%s", out)
	}
}

func TestModuleNameFromPath(t *testing.T) {
	for in, want := range map[string]string{
		"hello":                   "hello",
		"github.com/me/my-driver": "my_driver",
		"example.com/foo/bar.v2":  "bar_v2",
	} {
		if got := ModuleNameFromPath(in); got != want {
			t.Errorf("ModuleNameFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}
