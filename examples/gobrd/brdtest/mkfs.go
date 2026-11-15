package main

// (From govfat's tests.) A minimal mkfs.fat for FAT32 (the VM has no dosfstools): 512-byte
// sectors, 2 FATs, FSInfo at sector 1, backup boot sector at 6, root
// directory in cluster 2.

import (
	"encoding/binary"
	"fmt"
	"os"
)

func mkfs(dev string, secPerClus uint32) error {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	size, err := f.Seek(0, 2)
	if err != nil {
		return err
	}
	const secSize = 512
	const rsvd = 32
	const nfats = 2
	total := uint32(size / secSize)

	// Smallest FAT that covers the clusters that remain.
	var fatSecs, clusters uint32
	for fatSecs = 1; ; fatSecs++ {
		clusters = (total - rsvd - nfats*fatSecs) / secPerClus
		if (clusters+2)*4 <= fatSecs*secSize {
			break
		}
	}
	if clusters < 65525 {
		return fmt.Errorf("too few clusters (%d) for FAT32", clusters)
	}

	boot := make([]byte, secSize)
	copy(boot, []byte{0xeb, 0x58, 0x90})
	copy(boot[3:], "GOVFAT  ")
	le := binary.LittleEndian
	le.PutUint16(boot[11:], secSize)
	boot[13] = byte(secPerClus)
	le.PutUint16(boot[14:], rsvd)
	boot[16] = nfats
	boot[21] = 0xf8 // media
	le.PutUint16(boot[24:], 32)
	le.PutUint16(boot[26:], 64)
	le.PutUint32(boot[32:], total)
	le.PutUint32(boot[36:], fatSecs)
	le.PutUint32(boot[44:], 2) // root cluster
	le.PutUint16(boot[48:], 1) // FSInfo
	le.PutUint16(boot[50:], 6) // backup boot sector
	boot[64] = 0x80
	boot[66] = 0x29
	le.PutUint32(boot[67:], 0x12345678)
	copy(boot[71:], "GOVFAT TEST")
	copy(boot[82:], "FAT32   ")
	boot[510], boot[511] = 0x55, 0xaa

	fsinfo := make([]byte, secSize)
	le.PutUint32(fsinfo[0:], 0x41615252)
	le.PutUint32(fsinfo[484:], 0x61417272)
	le.PutUint32(fsinfo[488:], clusters-1) // the root uses one
	le.PutUint32(fsinfo[492:], 3)
	le.PutUint32(fsinfo[508:], 0xaa550000)

	// Reserved area and FATs zeroed, then the first FAT entries.
	zero := make([]byte, (rsvd+nfats*fatSecs)*secSize)
	if _, err := f.WriteAt(zero, 0); err != nil {
		return err
	}
	for _, at := range []int64{0, 6} {
		f.WriteAt(boot, at*secSize)
		f.WriteAt(fsinfo, (at+1)*secSize)
	}
	fat0 := make([]byte, 12)
	le.PutUint32(fat0[0:], 0x0ffffff8)
	le.PutUint32(fat0[4:], 0x0fffffff)
	le.PutUint32(fat0[8:], 0x0fffffff) // root directory: one cluster
	for i := uint32(0); i < nfats; i++ {
		f.WriteAt(fat0, int64(rsvd+i*fatSecs)*secSize)
	}
	// Root directory cluster zeroed.
	data := int64(rsvd+nfats*fatSecs) * secSize
	if _, err := f.WriteAt(make([]byte, secPerClus*secSize), data); err != nil {
		return err
	}
	return f.Sync()
}
