//go:build linux

package tcpprobe

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type linuxSocket struct {
	address netip.Addr
	port    uint16
	inode   string
	uid     uint32
}

func ownerLookupSupported() bool { return true }

func nativeOwnerLookup(ctx context.Context, endpoints []listenerEndpoint) ownerLookupResult {
	return lookupLinuxOwners(ctx, endpoints, "/proc")
}

func lookupLinuxOwners(ctx context.Context, endpoints []listenerEndpoint, root string) ownerLookupResult {
	result := ownerLookupResult{supported: true, complete: true, owners: make(map[listenerEndpoint][]owner)}
	if len(endpoints) == 0 {
		return result
	}

	var sockets []linuxSocket
	for _, name := range []string{"tcp", "tcp6"} {
		parsed, err := readLinuxSocketTable(filepath.Join(root, "net", name), name == "tcp6")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			result.complete = false
			result.detail = joinDetail(result.detail, fmt.Sprintf("reading %s socket table: %v", name, err))
			continue
		}
		sockets = append(sockets, parsed...)
	}
	matched := matchingLinuxSockets(endpoints, sockets)
	if len(matched.endpoints) == 0 {
		return result
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		result.complete = false
		result.detail = joinDetail(result.detail, fmt.Sprintf("listing processes: %v", err))
		return result
	}
	seen := make(map[listenerEndpoint]map[int]struct{}, len(endpoints))
	unreadable := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			result.complete = false
			result.detail = joinDetail(result.detail, err.Error())
			return result
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		// Every process is scanned rather than only those whose own UID matches a socket's.
		// sk_uid is the euid at socket creation, so a server that binds and then drops
		// privileges, or that inherited an activated socket, owns a socket it no longer
		// matches by UID and would otherwise never be found.
		fds, err := os.ReadDir(filepath.Join(root, entry.Name(), "fd"))
		if err != nil {
			switch {
			case errors.Is(err, fs.ErrNotExist):
			case errors.Is(err, fs.ErrPermission):
				unreadable++
			default:
				result.complete = false
			}
			continue
		}
		var owned map[listenerEndpoint]struct{}
		denied := false
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(root, entry.Name(), "fd", fd.Name()))
			if err != nil {
				switch {
				case errors.Is(err, fs.ErrNotExist):
				case errors.Is(err, fs.ErrPermission):
					// A process that changed credentials is no longer dumpable, so its
					// descriptors need CAP_SYS_PTRACE to follow even as root. Counted once
					// per process, since one such server has many descriptors.
					denied = true
				default:
					result.complete = false
				}
				continue
			}
			inode, ok := socketInode(target)
			if !ok {
				continue
			}
			for _, endpoint := range matched.endpoints[inode] {
				if owned == nil {
					owned = make(map[listenerEndpoint]struct{})
				}
				owned[endpoint] = struct{}{}
			}
		}
		if denied {
			unreadable++
		}
		if len(owned) == 0 {
			continue
		}
		process, complete := linuxProcess(root, pid)
		if !complete {
			result.complete = false
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
	result.hint = linuxLookupHint(matched.uids, unreadable)
	if !result.complete && result.detail == "" {
		result.detail = "some process information was inaccessible"
	}
	return result
}

// linuxLookupHint explains why a listener may have gone unexplained, naming the UID that owns
// the socket so the answer to "why can't I see it" is the same as the answer to "who do I
// need to be". It is only folded into detail when a listener actually had no owner.
func linuxLookupHint(uids map[uint32]struct{}, unreadable int) string {
	var hint string
	if unreadable > 0 {
		hint = fmt.Sprintf("%d processes were not readable by this user", unreadable)
	}
	if len(uids) == 0 {
		return hint
	}
	sorted := slices.Sorted(maps.Keys(uids))
	owners := make([]string, 0, len(sorted))
	for _, uid := range sorted {
		owners = append(owners, strconv.FormatUint(uint64(uid), 10))
	}
	return joinDetail(hint, "matching sockets were created by uid "+strings.Join(owners, ", "))
}

// linuxSocketMatches maps a socket inode to the endpoints it serves. uids records who created
// those sockets; it no longer selects which processes to scan, and survives only to name the
// user in a diagnostic when a listener goes unexplained.
type linuxSocketMatches struct {
	endpoints map[string][]listenerEndpoint
	uids      map[uint32]struct{}
}

func matchingLinuxSockets(endpoints []listenerEndpoint, sockets []linuxSocket) linuxSocketMatches {
	matches := linuxSocketMatches{
		endpoints: make(map[string][]listenerEndpoint),
		uids:      make(map[uint32]struct{}),
	}
	for _, socket := range sockets {
		for _, endpoint := range endpoints {
			if socket.port != endpoint.port || !listenerAddressMatches(socket.address, endpoint.address) {
				continue
			}
			matches.endpoints[socket.inode] = append(matches.endpoints[socket.inode], endpoint)
			matches.uids[socket.uid] = struct{}{}
		}
	}
	return matches
}

// readLinuxSocketTable streams one procfs socket table. The table lists every TCP socket on
// the host and can reach hundreds of thousands of lines, while only the listening rows are
// kept, so it is scanned line by line instead of being read and split as a whole.
func readLinuxSocketTable(path string, ipv6 bool) ([]linuxSocket, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseLinuxSocketTable(file, ipv6)
}

func parseLinuxSocketTable(reader io.Reader, ipv6 bool) ([]linuxSocket, error) {
	var result []linuxSocket
	scanner := bufio.NewScanner(reader)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		if lineNumber == 1 {
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 10 {
			return nil, fmt.Errorf("line %d has %d fields", lineNumber, len(fields))
		}
		if fields[3] != "0A" {
			continue
		}
		address, port, err := parseLinuxAddress(fields[1], ipv6)
		if err != nil {
			return nil, fmt.Errorf("line %d local address: %w", lineNumber, err)
		}
		uid, err := strconv.ParseUint(fields[7], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("line %d uid: %w", lineNumber, err)
		}
		result = append(result, linuxSocket{address: address, port: port, inode: fields[9], uid: uint32(uid)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func parseLinuxAddress(value string, ipv6 bool) (netip.Addr, uint16, error) {
	host, portText, found := strings.Cut(value, ":")
	if !found {
		return netip.Addr{}, 0, fmt.Errorf("missing port separator")
	}
	port, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid port: %w", err)
	}
	encoded, err := hex.DecodeString(host)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid address: %w", err)
	}
	if (!ipv6 && len(encoded) != 4) || (ipv6 && len(encoded) != 16) {
		return netip.Addr{}, 0, fmt.Errorf("invalid address length %d", len(encoded))
	}
	for offset := 0; offset < len(encoded); offset += 4 {
		slices.Reverse(encoded[offset : offset+4])
	}
	address, ok := netip.AddrFromSlice(encoded)
	if !ok {
		return netip.Addr{}, 0, fmt.Errorf("invalid address bytes")
	}
	return address.Unmap(), uint16(port), nil
}

func listenerAddressMatches(listener, connected netip.Addr) bool {
	listener, connected = listener.Unmap(), connected.Unmap()
	if listener.IsUnspecified() {
		return true
	}
	return listener == connected
}

func socketInode(target string) (string, bool) {
	if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"), true
}

func linuxProcess(root string, pid int) (owner, bool) {
	process := owner{PID: pid}
	complete := true
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "comm"))
	if err == nil {
		process.Name = strings.TrimSpace(string(data))
	} else if !errors.Is(err, fs.ErrNotExist) {
		complete = false
	}
	executable, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe"))
	if err == nil {
		process.Executable = strings.TrimSuffix(executable, " (deleted)")
	} else if !errors.Is(err, fs.ErrNotExist) {
		complete = false
	}
	return process, complete
}
