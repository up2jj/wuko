//go:build darwin

package http

import (
	"golang.org/x/sys/unix"
)

func renameDownloadNoReplace(source, destination string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
}
