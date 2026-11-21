---
title: "gokm/chardev and gokm/procfs"
linkTitle: "chardev and procfs"
description: "Character devices in /dev and files in /proc backed by Go code."
weight: 3
---

API reference: [gokm/chardev](/api/chardev/), [gokm/procfs](/api/procfs/).

## gokm/chardev and gokm/procfs

```go
chardev.Register(&chardev.Device{Name: "gorev", Mode: 0o666,
	Opener: chardev.OpenFunc(func() (chardev.File, error) { return &session{}, nil })})
// session implements Read(p, off) / Write(p, off) / Release() /
// Ioctl(cmd, arg): commands built with chardev.IO/IOR/IOW/IOWR, user
// pointers accessed with chardev.CopyFromUser/CopyToUser

// Data that arrives over time: Poll for poll/select/epoll, Wait to block
// (EAGAIN with O_NONBLOCK, EINTR on signals), Wake when things change.
func (s *session) ReadFlags(p []byte, off int64, fl chardev.Flags) (int, error) {
	if err := dev.Wait(fl, func() bool { return pending() > 0 }); err != nil {
		return 0, err
	}
	...
}
func (s *session) Poll() chardev.PollEvents { ... chardev.PollIn ... }
// producer (a goroutine, a network callback, any context): dev.Wake()

procfs.Create("gorev", procfs.ReadFunc(func() ([]byte, error) {
	return []byte(fmt.Sprintf("writes: %d\n", writes)), nil
}))  // implement Write(p []byte) error too for a writable file
```
