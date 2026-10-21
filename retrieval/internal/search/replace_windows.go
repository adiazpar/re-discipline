//go:build windows

package search

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_RENAME_INFO for FileRenameInfoEx. POSIX replacement retains the old
// file for existing handles and mappings while new opens see the new file.
// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information
type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func replaceFile(src, dst string) error {
	absSource, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	// CreateFile does not add the extended-length prefix that Go's os APIs do.
	if !strings.HasPrefix(absSource, `\\?\`) && !strings.HasPrefix(absSource, `\\.\`) {
		if strings.HasPrefix(absSource, `\\`) {
			absSource = `\\?\UNC\` + absSource[2:]
		} else {
			absSource = `\\?\` + absSource
		}
	}
	source, err := windows.UTF16PtrFromString(absSource)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(source, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return &os.LinkError{Op: "replace", Old: src, New: dst, Err: err}
	}
	defer windows.CloseHandle(h)
	var layout fileRenameInfo
	size := int(unsafe.Offsetof(layout.FileName)) + len(name)*2
	buffer := make([]byte, size)
	info := (*fileRenameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	err = windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, &buffer[0], uint32(size))
	if err == windows.ERROR_INVALID_PARAMETER || err == windows.ERROR_NOT_SUPPORTED || err == windows.ERROR_INVALID_FUNCTION {
		// Older Windows/filesystems can still publish when no reader blocks it.
		return os.Rename(src, dst)
	}
	if err != nil {
		return &os.LinkError{Op: "replace", Old: src, New: dst, Err: err}
	}
	return nil
}
