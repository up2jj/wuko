//go:build !linux && !darwin

package tcpprobe

import "context"

func ownerLookupSupported() bool { return false }

func nativeOwnerLookup(_ context.Context, _ []listenerEndpoint) ownerLookupResult {
	return ownerLookupResult{
		supported: false,
		complete:  false,
		detail:    "owning-process discovery is unsupported on this platform",
		owners:    make(map[listenerEndpoint][]owner),
	}
}
