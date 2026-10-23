package scriptsdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// callEnvelopeBytes bounds the callRequest fields around params, so the Unix
// socket's 10 MiB message limit applies without encoding params a second time.
const callEnvelopeBytes = 64

// handleBash accepts NUL-delimited arguments from Bash's printf builtin. JSON
// conversion and result formatting are shared with the executable client.
func (b *Bridge) handleBash(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageBytes))
	if err != nil {
		writeBash(w, nil, fmt.Errorf("invalid Bash SDK request (limit 10 MiB): %w", err))
		return
	}
	if len(data) == 0 || data[len(data)-1] != 0 {
		writeBash(w, nil, fmt.Errorf("invalid Bash SDK request: arguments must be NUL-terminated"))
		return
	}
	var out bytes.Buffer
	args := strings.Split(string(data[:len(data)-1]), "\x00")
	data = nil // Release the request body before dispatching large values.
	err = runBashCall(args, &out, func(method string, params any) (json.RawMessage, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("encoding SDK request: %w", err)
		}
		if len(raw)+len(method)+callEnvelopeBytes > maxMessageBytes {
			return nil, fmt.Errorf("SDK request exceeds 10 MiB")
		}
		result, err := b.dispatch(r.Context(), method, raw)
		if err != nil {
			failure := callFailure(err)
			return nil, fmt.Errorf("SDK %s: %s", failure.Code, failure.Message)
		}
		// Apply the Unix socket response rules without an encode/decode round trip.
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded)+callEnvelopeBytes > maxMessageBytes {
			return nil, fmt.Errorf("SDK invalid_result: SDK result is not JSON-compatible or exceeds 10 MiB")
		}
		return encoded, nil
	})
	writeBash(w, out.Bytes(), err)
}

// writeBash replies in the NUL-delimited format read by bash.bash. A failure
// discards partial stdout and reports err as the diagnostic.
func writeBash(w http.ResponseWriter, stdout []byte, err error) {
	status, diagnostic := "0", ""
	if err != nil {
		status, diagnostic, stdout = "1", err.Error()+"\n", nil
	}
	length := len(status) + escapedBashLength(stdout) + escapedBashLength([]byte(diagnostic)) + 3
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(length))
	_, _ = io.WriteString(w, status+"\x00")
	// Stream the escaped output instead of building a second full-size body.
	writeBashEscaped(w, stdout)
	_, _ = io.WriteString(w, "\x00")
	writeBashEscaped(w, []byte(diagnostic))
	_, _ = io.WriteString(w, "\x00")
}

// read -d ” preserves newlines. Escape only backslashes and NUL bytes;
// printf %b restores them without evaluating shell source or losing NULs.
func writeBashEscaped(w io.Writer, data []byte) {
	start := 0
	for i, c := range data {
		var escaped string
		switch c {
		case '\\':
			escaped = `\\`
		case 0:
			escaped = `\0000`
		default:
			continue
		}
		_, _ = w.Write(data[start:i])
		_, _ = io.WriteString(w, escaped)
		start = i + 1
	}
	_, _ = w.Write(data[start:])
}

func drainBashRequest(r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxMessageBytes+1))
}

func escapedBashLength(data []byte) int {
	return len(data) + bytes.Count(data, []byte{'\\'}) + 4*bytes.Count(data, []byte{0})
}
