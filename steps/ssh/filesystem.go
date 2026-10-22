package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/process"
)

const missingFileStatus = 72
const existingFileStatus = 73

// Check ancestor search permissions before reporting absence: test -e alone
// would misclassify an inaccessible path as missing. Missing parents are absent too.
const missingFileScript = `missing() {
  parent=$(dirname -- "$1")
  while [ "$parent" != / ]; do
    if [ -d "$parent" ]; then [ -x "$parent" ] || return 1; return 72; fi
    [ ! -e "$parent" ] || return 1
    parent=$(dirname -- "$parent")
  done
  [ -x / ] || return 1
  return 72
}
`

func (s *session) filePath(value string) (string, error) {
	if err := s.checkOpen(); err != nil {
		return "", err
	}
	if value == "" || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("file path must be non-empty and contain no NUL")
	}
	return s.mapPath(value), nil
}

func fileError(operation, name string, result process.Result, err error) error {
	if result.ExitCode == missingFileStatus {
		return &fs.PathError{Op: operation, Path: name, Err: fs.ErrNotExist}
	}
	if result.ExitCode == existingFileStatus {
		return &fs.PathError{Op: operation, Path: name, Err: fs.ErrExist}
	}
	return fmt.Errorf("%s remote file %s: %w", operation, name, err)
}

func (s *session) Stat(ctx context.Context, name string) (executor.FileInfo, error) {
	target, err := s.filePath(name)
	if err != nil {
		return executor.FileInfo{}, err
	}
	result, err := s.internal(ctx, missingFileScript+`if stat -c '%s %f %Y' -- "$1"; then exit 0; fi
if [ ! -e "$1" ] && [ ! -L "$1" ]; then
  missing "$1"; exit $?
fi
exit 1`, target)
	if err != nil {
		return executor.FileInfo{}, fileError("stat", name, result, err)
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) != 3 {
		return executor.FileInfo{}, fmt.Errorf("invalid remote file metadata")
	}
	size, sizeErr := strconv.ParseInt(fields[0], 10, 64)
	mode, modeErr := strconv.ParseUint(fields[1], 16, 32)
	modified, timeErr := strconv.ParseInt(fields[2], 10, 64)
	if err := errors.Join(sizeErr, modeErr, timeErr); err != nil {
		return executor.FileInfo{}, fmt.Errorf("invalid remote file metadata: %w", err)
	}
	return executor.FileInfo{Size: size, Mode: fileMode(uint32(mode)), ModTime: time.Unix(modified, 0)}, nil
}

// Unix stat type/permission bits differ from Go's portable FileMode type bits.
func fileMode(mode uint32) fs.FileMode {
	result := fs.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		result |= fs.ModeSetuid
	}
	if mode&0o2000 != 0 {
		result |= fs.ModeSetgid
	}
	if mode&0o1000 != 0 {
		result |= fs.ModeSticky
	}
	switch mode & 0o170000 {
	case 0o040000:
		result |= fs.ModeDir
	case 0o120000:
		result |= fs.ModeSymlink
	case 0o010000:
		result |= fs.ModeNamedPipe
	case 0o140000:
		result |= fs.ModeSocket
	case 0o060000:
		result |= fs.ModeDevice
	case 0o020000:
		result |= fs.ModeDevice | fs.ModeCharDevice
	}
	return result
}

func (s *session) ReadFile(ctx context.Context, name string, maximum int64) ([]byte, error) {
	target, err := s.filePath(name)
	if err != nil {
		return nil, err
	}
	limit := ""
	if maximum > 0 && maximum < math.MaxInt64 {
		limit = strconv.FormatInt(maximum+1, 10)
	}
	// Stream straight into one buffer; a captured string would be copied again into the result.
	var data bytes.Buffer
	result, err := s.script(ctx, missingFileScript+`if [ ! -f "$1" ]; then
		if [ ! -e "$1" ]; then missing "$1"; exit $?; fi
  echo 'not a regular file' >&2; exit 1
fi
if [ -n "$2" ]; then exec head -c "$2" -- "$1"; else exec cat -- "$1"; fi`, []string{target, limit}, process.Options{
		Stdout: &data, StdoutPolicy: process.OutputInherit, StderrPolicy: process.OutputCapture, CaptureLimit: 64 << 10,
	})
	if err != nil {
		return nil, fileError("read", name, result, err)
	}
	return data.Bytes(), nil
}

func (s *session) WriteFile(ctx context.Context, name string, data []byte, options executor.WriteOptions) error {
	target, err := s.filePath(name)
	if err != nil {
		return err
	}
	result, err := s.script(ctx, missingFileScript+installFileScript, []string{target, path.Dir(target), fmt.Sprintf("%04o", options.Mode.Perm()), strconv.FormatBool(options.Replace), strconv.Itoa(len(data))}, process.Options{
		Stdin: bytes.NewReader(data), StdoutPolicy: process.OutputDiscard, StderrPolicy: process.OutputCapture, CaptureLimit: 64 << 10,
	})
	if err != nil {
		return fileError("write", name, result, err)
	}
	return nil
}

const installFileScript = `set -eu
target=$1
directory=$2
mode=$3
replace=$4
expected=$5
umask 077
if [ ! -d "$directory" ]; then
  if [ ! -e "$directory" ]; then missing "$target"; exit $?; fi
  echo 'parent is not a directory' >&2; exit 1
fi
temporary=$(mktemp "$directory/.wuko-write.XXXXXXXXXX")
trap 'rm -f -- "$temporary"' 0
trap 'exit 143' 1 2 15
cat > "$temporary"
[ "$(stat -c %s -- "$temporary")" = "$expected" ] || { echo 'incomplete remote file upload' >&2; exit 1; }
chmod "$mode" -- "$temporary"
# Match the local filesystem: the contents are durable before they are installed.
sync -- "$temporary"
if [ "$replace" = true ]; then
  mv -fT -- "$temporary" "$target"
else
  if ! ln -T -- "$temporary" "$target"; then
    if [ -e "$target" ] || [ -L "$target" ]; then exit 73; fi
    exit 1
  fi
  rm -- "$temporary"
fi
`

func (s *session) MkdirAll(ctx context.Context, name string, mode fs.FileMode) error {
	target, err := s.filePath(name)
	if err != nil {
		return err
	}
	_, err = s.internal(ctx, `set -eu
mode=$2
make_directory() {
  [ ! -d "$1" ] || return 0
  make_directory "$(dirname -- "$1")"
  mkdir -m "$mode" -- "$1" || [ -d "$1" ]
}
make_directory "$1"
chmod "$mode" -- "$1"`, target, fmt.Sprintf("%04o", mode.Perm()))
	if err != nil {
		return fmt.Errorf("creating remote directory %s: %w", name, err)
	}
	return nil
}

var _ executor.FileSystem = (*session)(nil)
