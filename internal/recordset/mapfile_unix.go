//go:build unix

package recordset

import (
	"os"
	"syscall"
)

// mapFile maps path read-only. The data stays valid until release is called,
// also when the file is unlinked in the meantime.
func mapFile(path string) (data []byte, release func() error, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, func() error { return nil }, nil
	}
	if int64(int(size)) != size {
		return readFile(path) // larger than the address space can map at once
	}
	data, err = syscall.Mmap(int(file.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return readFile(path)
	}
	return data, func() error { return syscall.Munmap(data) }, nil
}
