//go:build linux

package file

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func renameNoReplace(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

func exchangePaths(first, second string) error {
	return unix.Renameat2(unix.AT_FDCWD, first, unix.AT_FDCWD, second, unix.RENAME_EXCHANGE)
}

func accessTime(info os.FileInfo) time.Time {
	stat := info.Sys().(*syscall.Stat_t)
	return time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
}
