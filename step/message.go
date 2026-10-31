package step

import "github.com/up2jj/wuko/useroutput"

// PrintMessage sends explicit output through the host, or to stderr for direct runners.
// It reports whether a destination was available.
func (request Request) PrintMessage(message useroutput.Message) bool {
	if request.UI != nil {
		request.UI(message)
		return true
	}
	if request.Stderr != nil {
		useroutput.Write(request.Stderr, message, false)
		return true
	}
	return false
}
