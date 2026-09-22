//go:build linux

package tcpprobe

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestParseLinuxSocketTables(t *testing.T) {
	data := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000  501        0 12345\n" +
		"   1: 0100007F:1770 00000000:0000 01 00000000:00000000 00:00000000 00000000  501        0 22222\n"
	sockets, err := parseLinuxSocketTable(strings.NewReader(data), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sockets) != 1 || sockets[0].address.String() != "127.0.0.1" || sockets[0].port != 5432 || sockets[0].inode != "12345" || sockets[0].uid != 501 {
		t.Fatalf("sockets = %#v", sockets)
	}
}

func TestLookupLinuxOwnersFromProcFixture(t *testing.T) {
	root := t.TempDir()
	uid := os.Getuid()
	mustWrite(t, filepath.Join(root, "net", "tcp"), fmt.Sprintf("  sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n0: 0100007F:1538 00000000:0000 0A 0:0 00:0 0 %d 0 12345\n", uid))
	mustWrite(t, filepath.Join(root, "net", "tcp6"), "  sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n")
	process := filepath.Join(root, "42")
	if err := os.MkdirAll(filepath.Join(process, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(process, "comm"), "postgres\n")
	if err := os.Symlink("/usr/bin/postgres", filepath.Join(process, "exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[12345]", filepath.Join(process, "fd", "3")); err != nil {
		t.Fatal(err)
	}
	endpoint := listenerEndpoint{address: netip.MustParseAddr("127.0.0.1"), port: 5432}
	result := lookupLinuxOwners(t.Context(), []listenerEndpoint{endpoint}, root)
	owners := result.owners[endpoint]
	if !result.complete || len(owners) != 1 || owners[0].PID != 42 || owners[0].Name != "postgres" || owners[0].Executable != "/usr/bin/postgres" {
		t.Fatalf("result = %#v", result)
	}
}

// sk_uid is the euid at socket creation, so a server that binds as root and then drops
// privileges owns a socket whose UID no longer matches its own. Ownership is resolved by
// socket inode, and a UID that matches nothing must not hide the process holding the port.
func TestLookupLinuxOwnersFindsProcessThatDroppedPrivileges(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "net", "tcp"), "  sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n0: 0100007F:1538 00000000:0000 0A 0:0 00:0 0 0 0 12345\n")
	process := filepath.Join(root, "42")
	if err := os.MkdirAll(filepath.Join(process, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(process, "comm"), "postgres\n")
	if err := os.Symlink("socket:[12345]", filepath.Join(process, "fd", "3")); err != nil {
		t.Fatal(err)
	}
	endpoint := listenerEndpoint{address: netip.MustParseAddr("127.0.0.1"), port: 5432}
	result := lookupLinuxOwners(t.Context(), []listenerEndpoint{endpoint}, root)
	if owners := result.owners[endpoint]; len(owners) != 1 || owners[0].PID != 42 {
		t.Fatalf("result = %#v", result)
	}
}

// A listener nothing claims must not read as a clean, complete lookup.
func TestLookupLinuxOwnersMarksUnexplainedListenerIncomplete(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "net", "tcp"), "  sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n0: 0100007F:1538 00000000:0000 0A 0:0 00:0 0 0 0 12345\n")
	endpoint := listenerEndpoint{address: netip.MustParseAddr("127.0.0.1"), port: 5432}
	result := lookupLinuxOwners(t.Context(), []listenerEndpoint{endpoint}, root)
	noteMissingOwners(&result, []listenerEndpoint{endpoint})
	if result.complete || !strings.Contains(result.detail, "127.0.0.1:5432") ||
		!strings.Contains(result.detail, "uid 0") {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseLinuxIPv6Address(t *testing.T) {
	address, port, err := parseLinuxAddress("00000000000000000000000001000000:0FA0", true)
	if err != nil {
		t.Fatal(err)
	}
	if address != netip.IPv6Loopback() || port != 4000 {
		t.Fatalf("address = %s, port = %d", address, port)
	}
}

func TestNativeLinuxOwnerLookupFindsCurrentProcess(t *testing.T) {
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

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
