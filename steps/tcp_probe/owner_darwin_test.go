//go:build darwin

package tcpprobe

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestParseDarwinSocket(t *testing.T) {
	data := make([]byte, darwinSocketFDInfoSize)
	binary.NativeEndian.PutUint32(data[darwinSocketKindOffset:], darwinSocketInfoTCP)
	binary.BigEndian.PutUint16(data[darwinTCPLocalPort:], 5432)
	data[darwinTCPVFlag] = darwinIPv4Flag
	copy(data[darwinTCPLocalAddress+12:], net.ParseIP("127.0.0.1").To4())
	binary.NativeEndian.PutUint32(data[darwinTCPState:], darwinTCPStateListen)

	socket, err := parseDarwinSocket(data)
	if err != nil {
		t.Fatal(err)
	}
	if socket.address.String() != "127.0.0.1" || socket.port != 5432 || !socket.listen {
		t.Fatalf("socket = %#v", socket)
	}
}

func TestLookupDarwinOwnersMatchesAndSortsProcesses(t *testing.T) {
	endpoint := listenerEndpoint{address: netip.MustParseAddr("127.0.0.1"), port: 4000}
	api := fakeDarwinProcAPI{
		pids: []int{20, 10},
		fds: map[int][]darwinFD{
			20: {{number: 4, kind: darwinFDTypeSocket}},
			10: {{number: 3, kind: darwinFDTypeSocket}},
		},
		sockets: map[[2]int]darwinSocket{
			{20, 4}: {address: netip.IPv4Unspecified(), port: 4000, listen: true},
			{10, 3}: {address: netip.MustParseAddr("127.0.0.1"), port: 4000, listen: true},
		},
		paths: map[int]string{20: "/bin/twenty", 10: "/bin/ten"},
	}
	result := lookupDarwinOwners(t.Context(), []listenerEndpoint{endpoint}, api)
	owners := result.owners[endpoint]
	if !result.complete || len(owners) != 2 || owners[0].PID != 10 || owners[0].Name != "ten" || owners[1].PID != 20 {
		t.Fatalf("result = %#v", result)
	}
}

func TestLookupDarwinOwnersReportsInaccessibleProcessInformation(t *testing.T) {
	endpoint := listenerEndpoint{address: netip.MustParseAddr("127.0.0.1"), port: 4000}
	api := fakeDarwinProcAPI{pids: []int{10}, fdErrors: map[int]error{10: syscall.EIO}}
	result := lookupDarwinOwners(t.Context(), []listenerEndpoint{endpoint}, api)
	if result.complete || result.detail == "" {
		t.Fatalf("result = %#v", result)
	}
}

// A process this user does not own is an expected limit, not a failed scan: on a shared host
// most processes refuse inspection, and reporting that as incomplete would make every
// unprivileged probe look broken even when it found the owner it was asked for.
func TestLookupDarwinOwnersTreatsDeniedProcessesAsABlindSpot(t *testing.T) {
	endpoint := listenerEndpoint{address: netip.MustParseAddr("127.0.0.1"), port: 4000}
	api := fakeDarwinProcAPI{
		pids:     []int{10, 20},
		fdErrors: map[int]error{20: syscall.EPERM},
		fds:      map[int][]darwinFD{10: {{number: 3, kind: darwinFDTypeSocket}}},
		sockets:  map[[2]int]darwinSocket{{10, 3}: {address: netip.MustParseAddr("127.0.0.1"), port: 4000, listen: true}},
		paths:    map[int]string{10: "/bin/ten"},
	}
	result := lookupDarwinOwners(t.Context(), []listenerEndpoint{endpoint}, api)
	if !result.complete || len(result.owners[endpoint]) != 1 || result.detail != "" {
		t.Fatalf("result = %#v", result)
	}
	if result.hint == "" {
		t.Fatalf("denied process left no hint: %#v", result)
	}

	// The same refusal, with the listener now unexplained, has to reach the workflow as an
	// incomplete lookup that says why.
	unexplained := lookupDarwinOwners(t.Context(), []listenerEndpoint{endpoint}, fakeDarwinProcAPI{
		pids: []int{20}, fdErrors: map[int]error{20: syscall.EPERM},
	})
	noteMissingOwners(&unexplained, []listenerEndpoint{endpoint})
	if unexplained.complete || !strings.Contains(unexplained.detail, "127.0.0.1:4000") ||
		!strings.Contains(unexplained.detail, "not readable") {
		t.Fatalf("result = %#v", unexplained)
	}
}

func TestNativeDarwinOwnerLookupFindsCurrentProcess(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skipf("sandbox does not permit loopback listeners: %v", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	endpoint := listenerEndpoint{address: netip.MustParseAddr("127.0.0.1"), port: port}
	result := nativeOwnerLookup(t.Context(), []listenerEndpoint{endpoint})
	for _, process := range result.owners[endpoint] {
		if process.PID != os.Getpid() {
			continue
		}
		if process.Executable == "" || process.Name == "" {
			t.Fatalf("owner = %#v, want executable and name", process)
		}
		return
	}
	t.Fatalf("current PID %d not found: %#v", os.Getpid(), result)
}

type fakeDarwinProcAPI struct {
	pids      []int
	fds       map[int][]darwinFD
	fdErrors  map[int]error
	sockets   map[[2]int]darwinSocket
	sockError map[[2]int]error
	paths     map[int]string
	pathError map[int]error
}

func (api fakeDarwinProcAPI) PIDs() ([]int, error) { return api.pids, nil }

func (api fakeDarwinProcAPI) FDs(pid int) ([]darwinFD, error) {
	return api.fds[pid], api.fdErrors[pid]
}

func (api fakeDarwinProcAPI) Socket(pid, fd int) (darwinSocket, error) {
	key := [2]int{pid, fd}
	return api.sockets[key], api.sockError[key]
}

func (api fakeDarwinProcAPI) Path(pid int) (string, error) {
	return api.paths[pid], api.pathError[pid]
}

var _ darwinProcAPI = fakeDarwinProcAPI{}
