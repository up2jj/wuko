//go:build darwin

package file

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func renameNoReplace(source, destination string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
}

func exchangePaths(first, second string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, first, unix.AT_FDCWD, second, unix.RENAME_SWAP)
}

func accessTime(info os.FileInfo) time.Time {
	stat := info.Sys().(*syscall.Stat_t)
	return time.Unix(stat.Atimespec.Sec, stat.Atimespec.Nsec)
}
