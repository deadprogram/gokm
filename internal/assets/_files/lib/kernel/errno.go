package kernel

import _ "unsafe" // for go:linkname

// Errno is a kernel error number (positive, like EINVAL). It implements error,
// so Go code can return it, and ToErrno converts any error back into the
// negative errno kernel callbacks return.
type Errno int

const (
	EPERM         Errno = 1
	ENOENT        Errno = 2
	EINTR         Errno = 4
	EIO           Errno = 5
	ENXIO         Errno = 6
	E2BIG         Errno = 7
	EBADF         Errno = 9
	EAGAIN        Errno = 11
	ENOMEM        Errno = 12
	EACCES        Errno = 13
	EFAULT        Errno = 14
	EBUSY         Errno = 16
	EEXIST        Errno = 17
	EXDEV         Errno = 18
	ENODEV        Errno = 19
	ENOTDIR       Errno = 20
	EISDIR        Errno = 21
	EINVAL        Errno = 22
	ENFILE        Errno = 23
	EMFILE        Errno = 24
	ENOTTY        Errno = 25
	EFBIG         Errno = 27
	ENOSPC        Errno = 28
	ESPIPE        Errno = 29
	EROFS         Errno = 30
	EMLINK        Errno = 31
	ERANGE        Errno = 34
	ENAMETOOLONG  Errno = 36
	ENOSYS        Errno = 38
	ENOTEMPTY     Errno = 39
	ENODATA       Errno = 61
	EOVERFLOW     Errno = 75
	EOPNOTSUPP    Errno = 95
	EADDRNOTAVAIL Errno = 99
	ETIMEDOUT     Errno = 110
)

var errnoNames = map[Errno]string{
	EPERM: "EPERM", ENOENT: "ENOENT", EIO: "EIO", ENXIO: "ENXIO", E2BIG: "E2BIG",
	EINTR: "EINTR", EBADF: "EBADF", EAGAIN: "EAGAIN", ENOMEM: "ENOMEM", EACCES: "EACCES",
	EFAULT: "EFAULT", EBUSY: "EBUSY", EEXIST: "EEXIST", EXDEV: "EXDEV",
	ENODEV: "ENODEV", ENOTDIR: "ENOTDIR", EISDIR: "EISDIR", EINVAL: "EINVAL",
	ENFILE: "ENFILE", EMFILE: "EMFILE", ENOTTY: "ENOTTY", EFBIG: "EFBIG",
	ENOSPC: "ENOSPC", ESPIPE: "ESPIPE", EROFS: "EROFS", EMLINK: "EMLINK",
	ERANGE: "ERANGE", ENAMETOOLONG: "ENAMETOOLONG", ENOSYS: "ENOSYS",
	ENOTEMPTY: "ENOTEMPTY", ENODATA: "ENODATA", EOVERFLOW: "EOVERFLOW",
	EOPNOTSUPP: "EOPNOTSUPP", ETIMEDOUT: "ETIMEDOUT",
}

func (e Errno) Error() string {
	if name, ok := errnoNames[e]; ok {
		return name
	}
	return "errno " + itoa(int(e))
}

// ToErrno converts err into a negative errno for returning to the kernel:
// nil is 0, an Errno (possibly wrapped, via Unwrap) is -errno, and any other
// error is -EIO.
func ToErrno(err error) int {
	for err != nil {
		if e, ok := err.(Errno); ok {
			return -int(e)
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	if err == nil {
		return 0
	}
	return -int(EIO)
}

//go:linkname setInitError runtime.kmodSetInitError
func setInitError(errno int)

// FailLoad makes module loading fail with err once main returns (insmod
// reports the error). Undo anything main registered before failing: hooks
// registered with OnExit don't run when loading fails. A nil err cancels a
// previous FailLoad.
func FailLoad(err error) {
	setInitError(-ToErrno(err))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
