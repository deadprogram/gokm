package kernel

import "runtime"

// CatchPanic turns a panic in the current function into an error result
// instead of a kernel BUG(). Use it directly with defer in functions the
// kernel calls:
//
//	func handler() (rc int) {
//		defer kernel.CatchPanic(&rc)
//		...
//	}
//
// On panic it logs the panic value and sets *rc to -EIO (rc may be nil for
// functions without a result). Framework packages already do this around
// every callback into user code.
//
// Taking &rc makes TinyGo allocate rc on the heap at every call. On hot
// paths, set the result first and pass nil, as a recovered panic returns
// the current value of rc:
//
//	func handler() (rc int) {
//		rc = -int(kernel.EIO)
//		defer kernel.CatchPanic[int](nil)
//		...
//	}
func CatchPanic[T ~int | ~int32 | ~int64](rc *T) {
	r := recover()
	if r == nil {
		return
	}
	Print("panic recovered, returning EIO: " + panicString(r))
	if rc != nil {
		*rc = T(-EIO)
	}
}

func panicString(r any) string {
	switch v := r.(type) {
	case string:
		return v
	case runtime.Error:
		return v.Error()
	case error:
		return v.Error()
	case interface{ String() string }:
		return v.String()
	default:
		return "(non-string panic value)"
	}
}
