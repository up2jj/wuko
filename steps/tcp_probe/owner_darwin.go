//go:build darwin

package tcpprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinProcInfoListPIDs  = 1
	darwinProcInfoPIDInfo   = 2
	darwinProcInfoPIDFDInfo = 3

	darwinAllPIDs        = 1
	darwinPIDListFDs     = 1
	darwinPIDPathInfo    = 11
	darwinPIDFDSocket    = 3
	darwinFDTypeSocket   = 2
	darwinSocketInfoTCP  = 2
	darwinTCPStateListen = 1

	darwinFDInfoSize       = 8
	darwinSocketFDInfoSize = 792
	darwinMaxPathSize      = 4096

	darwinSocketInfoOffset = 24
	darwinSocketKindOffset = darwinSocketInfoOffset + 232
	darwinTCPInfoOffset    = darwinSocketInfoOffset + 240
	darwinTCPLocalPort     = darwinTCPInfoOffset + 4
	darwinTCPVFlag         = darwinTCPInfoOffset + 24
	darwinTCPLocalAddress  = darwinTCPInfoOffset + 48
	darwinTCPState         = darwinTCPInfoOffset + 80

	darwinIPv4Flag = 1
	darwinIPv6Flag = 2

	// Items that may appear between sizing a proc_info list and reading it, and how many
	// times that headroom doubles before the host is treated as too busy to enumerate.
	darwinProcInfoHeadroom = 32
	darwinProcInfoAttempts = 4
)

type darwinProcAPI interface {
	PIDs() ([]int, error)
	FDs(int) ([]darwinFD, error)
	Socket(int, int) (darwinSocket, error)
	Path(int) (string, error)
}

// nativeDarwinProcAPI reuses one socket buffer across the scan. Socket is called for every
// socket descriptor of every process on the host, so a fresh allocation per call costs
// thousands of them per probe. One lookup owns one value and reads it on a single goroutine,
// and parseDarwinSocket copies out everything it keeps, so the buffer is safe to reuse.
type nativeDarwinProcAPI struct {
	socket []byte
}

type darwinFD struct {
	number int
	kind   uint32
}

type darwinSocket struct {
	address netip.Addr
	port    uint16
	listen  bool
}

func ownerLookupSupported() bool { return true }

func nativeOwnerLookup(ctx context.Context, endpoints []listenerEndpoint) ownerLookupResult {
	return lookupDarwinOwners(ctx, endpoints, &nativeDarwinProcAPI{})
}

func lookupDarwinOwners(ctx context.Context, endpoints []listenerEndpoint, api darwinProcAPI) ownerLookupResult {
	result := ownerLookupResult{supported: true, complete: true, owners: make(map[listenerEndpoint][]owner)}
	if len(endpoints) == 0 {
		return result
	}
	pids, err := api.PIDs()
	if err != nil {
		result.complete = false
		result.detail = fmt.Sprintf("listing processes: %v", err)
		return result
	}
	seen := make(map[listenerEndpoint]map[int]struct{}, len(endpoints))
	unreadable := 0
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			result.complete = false
			result.detail = joinDetail(result.detail, err.Error())
			return result
		}
		fds, err := api.FDs(pid)
		if err != nil {
			switch {
			case darwinTransientProcessError(err):
			case darwinDeniedProcessError(err):
				// Another user's process. Unless a listener ends up unexplained this is not a
				// gap in the answer, and without it every unprivileged probe on a shared host
				// would report an incomplete lookup.
				unreadable++
			default:
				result.complete = false
			}
			continue
		}
		var owned map[listenerEndpoint]struct{}
		for _, fd := range fds {
			if fd.kind != darwinFDTypeSocket {
				continue
			}
			socket, err := api.Socket(pid, fd.number)
			if err != nil {
				if !darwinTransientProcessError(err) {
					result.complete = false
				}
				continue
			}
			if !socket.listen {
				continue
			}
			for _, endpoint := range endpoints {
				if socket.port == endpoint.port && listenerAddressMatches(socket.address, endpoint.address) {
					if owned == nil {
						owned = make(map[listenerEndpoint]struct{})
					}
					owned[endpoint] = struct{}{}
				}
			}
		}
		if len(owned) == 0 {
			continue
		}
		path, pathErr := api.Path(pid)
		if pathErr != nil && !darwinTransientProcessError(pathErr) {
			result.complete = false
		}
		process := owner{PID: pid, Executable: path}
		if path != "" {
			process.Name = filepath.Base(path)
		}
		for endpoint := range owned {
			if seen[endpoint] == nil {
				seen[endpoint] = make(map[int]struct{})
			}
			if _, duplicate := seen[endpoint][pid]; duplicate {
				continue
			}
			seen[endpoint][pid] = struct{}{}
			result.owners[endpoint] = append(result.owners[endpoint], process)
		}
	}
	for endpoint := range result.owners {
		slices.SortFunc(result.owners[endpoint], func(left, right owner) int { return left.PID - right.PID })
	}
	if unreadable > 0 {
		result.hint = fmt.Sprintf("%d processes were not readable by this user", unreadable)
	}
	if !result.complete && result.detail == "" {
		result.detail = "some process information was inaccessible"
	}
	return result
}

func (*nativeDarwinProcAPI) PIDs() ([]int, error) {
	data, err := darwinVariableProcInfo(darwinProcInfoListPIDs, darwinAllPIDs, 0, 0, 4)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(data)/4)
	for offset := 0; offset+4 <= len(data); offset += 4 {
		pid := int(int32(binary.NativeEndian.Uint32(data[offset : offset+4])))
		if pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func (*nativeDarwinProcAPI) FDs(pid int) ([]darwinFD, error) {
	data, err := darwinVariableProcInfo(darwinProcInfoPIDInfo, pid, darwinPIDListFDs, 0, darwinFDInfoSize)
	if err != nil {
		return nil, err
	}
	fds := make([]darwinFD, 0, len(data)/darwinFDInfoSize)
	for offset := 0; offset+darwinFDInfoSize <= len(data); offset += darwinFDInfoSize {
		fds = append(fds, darwinFD{
			number: int(int32(binary.NativeEndian.Uint32(data[offset : offset+4]))),
			kind:   binary.NativeEndian.Uint32(data[offset+4 : offset+8]),
		})
	}
	return fds, nil
}

func (api *nativeDarwinProcAPI) Socket(pid, fd int) (darwinSocket, error) {
	if len(api.socket) != darwinSocketFDInfoSize {
		api.socket = make([]byte, darwinSocketFDInfoSize)
	}
	written, err := darwinProcInfo(darwinProcInfoPIDFDInfo, pid, darwinPIDFDSocket, uint64(fd), api.socket)
	if err != nil {
		return darwinSocket{}, err
	}
	if written < darwinSocketFDInfoSize {
		return darwinSocket{}, fmt.Errorf("socket info returned %d bytes, want %d", written, darwinSocketFDInfoSize)
	}
	return parseDarwinSocket(api.socket)
}

// Path reads a process executable path. PROC_PIDPATHINFO copies a NUL-terminated path into
// the caller buffer and leaves the syscall result at zero, so the length comes from the
// terminator rather than from the returned byte count.
func (*nativeDarwinProcAPI) Path(pid int) (string, error) {
	data := make([]byte, darwinMaxPathSize)
	written, err := darwinProcInfo(darwinProcInfoPIDInfo, pid, darwinPIDPathInfo, 0, data)
	if err != nil {
		return "", err
	}
	if written > 0 && written < len(data) {
		data = data[:written]
	}
	index := slices.Index(data, byte(0))
	if index < 0 {
		return "", fmt.Errorf("process path is not terminated")
	}
	return string(data[:index]), nil
}

func parseDarwinSocket(data []byte) (darwinSocket, error) {
	if len(data) < darwinSocketFDInfoSize {
		return darwinSocket{}, fmt.Errorf("socket info has %d bytes, want %d", len(data), darwinSocketFDInfoSize)
	}
	if int32(binary.NativeEndian.Uint32(data[darwinSocketKindOffset:darwinSocketKindOffset+4])) != darwinSocketInfoTCP {
		return darwinSocket{}, nil
	}
	result := darwinSocket{
		port:   binary.BigEndian.Uint16(data[darwinTCPLocalPort : darwinTCPLocalPort+2]),
		listen: int32(binary.NativeEndian.Uint32(data[darwinTCPState:darwinTCPState+4])) == darwinTCPStateListen,
	}
	switch data[darwinTCPVFlag] {
	case darwinIPv4Flag:
		result.address = netip.AddrFrom4([4]byte(data[darwinTCPLocalAddress+12 : darwinTCPLocalAddress+16])).Unmap()
	case darwinIPv6Flag:
		result.address = netip.AddrFrom16([16]byte(data[darwinTCPLocalAddress : darwinTCPLocalAddress+16])).Unmap()
	default:
		return darwinSocket{}, nil
	}
	return result, nil
}

// darwinVariableProcInfo reads a list whose length is only known from a sizing call. proc_info
// truncates to the buffer it is given and reports no overflow, so a completely full buffer is
// indistinguishable from an exact fit. Processes and descriptors keep appearing between the
// two calls, so the buffer carries headroom and grows until the kernel returns less than it
// was offered, which is the only proof that nothing was dropped.
func darwinVariableProcInfo(call, pid, flavor int, argument uint64, itemSize int) ([]byte, error) {
	size, err := darwinProcInfo(call, pid, flavor, argument, nil)
	if err != nil {
		return nil, err
	}
	if size <= 0 {
		return nil, nil
	}
	headroom := itemSize * darwinProcInfoHeadroom
	for attempt := 0; attempt < darwinProcInfoAttempts; attempt++ {
		data := make([]byte, size+headroom)
		written, err := darwinProcInfo(call, pid, flavor, argument, data)
		if err != nil {
			return nil, err
		}
		if written < 0 || written > len(data) {
			return nil, fmt.Errorf("proc_info returned invalid size %d", written)
		}
		if written < len(data) {
			return data[:written], nil
		}
		headroom *= 2
	}
	return nil, fmt.Errorf("proc_info result kept filling a %d byte buffer", size+headroom/2)
}

func darwinProcInfo(call, pid, flavor int, argument uint64, data []byte) (int, error) {
	var pointer unsafe.Pointer
	if len(data) > 0 {
		pointer = unsafe.Pointer(&data[0])
	}
	result, _, errno := unix.Syscall6(
		unix.SYS_PROC_INFO,
		uintptr(call), uintptr(pid), uintptr(flavor), uintptr(argument),
		uintptr(pointer), uintptr(len(data)),
	)
	runtime.KeepAlive(data)
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}

func darwinTransientProcessError(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT)
}

// darwinDeniedProcessError reports the refusal proc_info returns for a process this user does
// not own, which is an expected limit rather than a failed scan.
func darwinDeniedProcessError(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

func listenerAddressMatches(listener, connected netip.Addr) bool {
	listener, connected = listener.Unmap(), connected.Unmap()
	return listener.IsUnspecified() || listener == connected
}
