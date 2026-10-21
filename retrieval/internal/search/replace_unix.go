//go:build !windows

package search

import "os"

func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}
