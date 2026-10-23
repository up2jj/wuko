package plugin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLockReportsTheDocumentProblem(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "second document", body: "version: 1\nplugins: []\n---\nversion: 1\n", want: "multiple YAML documents are not supported"},
		{name: "unknown field", body: "version: 1\nextra: true\n", want: "field extra not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), LockfileName)
			mustWrite(t, path, []byte(test.body))
			_, err := readLock(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}
