//go:build !windows

package search

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func TryLock(root string) (func(), bool) {
	f, err := os.OpenFile(filepath.Join(root, ".re-discipline", "index.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, true
}
