package githook

import (
	"testing"

	"github.com/up2jj/wuko/internal/testhome"
)

// TestMain keeps tests away from the developer's home directory and Git configuration.
func TestMain(m *testing.M) {
	testhome.Run(m)
}
