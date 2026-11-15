package build

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

var (
	fnAttrGroupRe = regexp.MustCompile(`^attributes #\d+ = \{ (.*"target-cpu".*)$`)
	uwtableRe     = regexp.MustCompile(` ?\buwtable(\((sync|async)\))?`)
	noreturnDecl  = regexp.MustCompile(`^(declare void @gokm_(abort|out_of_memory)\([^)]*\)) local_unnamed_addr #`)
)

// fixupIR patches TinyGo's LLVM IR for the x86-64 kernel ABI, covering what a
// TinyGo target JSON can't express:
//
//   - noredzone: interrupts and NMIs push onto the current kernel stack, so
//     code must never use the 128-byte red zone.
//   - fn_ret_thunk_extern: CONFIG_MITIGATION_RETHUNK turns `ret` into
//     `jmp __x86_return_thunk`.
//   - uwtable is dropped: no .eh_frame unwind tables (the kernel uses ORC).
//   - cf-protection-branch: CONFIG_X86_KERNEL_IBT, emit endbr64 at the start
//     of functions that can be reached by an indirect call.
//   - noreturn on gokm_abort and gokm_out_of_memory (they BUG()), so objtool's
//     control-flow analysis agrees with the C side.
func fixupIR(in []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(in) + 1024)
	sc := bufio.NewScanner(bytes.NewReader(in))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	const flagID = "!9000001"
	sawFlags := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case fnAttrGroupRe.MatchString(line):
			line = uwtableRe.ReplaceAllString(line, "")
			line = strings.Replace(line, "{ ", "{ noredzone fn_ret_thunk_extern ", 1)
		case noreturnDecl.MatchString(line):
			line = noreturnDecl.ReplaceAllString(line, "$1 local_unnamed_addr noreturn #")
		case strings.HasPrefix(line, "!llvm.module.flags = !{"):
			line = strings.Replace(line, "!{", "!{"+flagID+", ", 1)
			sawFlags = true
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if !sawFlags {
		out.WriteString("!llvm.module.flags = !{" + flagID + "}\n")
	}
	out.WriteString(flagID + ` = !{i32 8, !"cf-protection-branch", i32 1}` + "\n")
	return out.Bytes()
}
