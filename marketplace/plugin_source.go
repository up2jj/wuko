package marketplace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/workflow"
)

// PluginSourceName is the metadata file recording where an imported plugin release came from.
const PluginSourceName = ".wuko-marketplace-source.json"

// PluginSourceRoot resolves the directory holding imported plugin releases. It must not
// be the local plugin installation root, because an imported release contains no executable and
// would make local plugin discovery fail for that namespace.
func PluginSourceRoot(cwd string) string {
	return filepath.Join(cwd, filepath.FromSlash(workflow.MarketplacePluginSourceDir))
}

// PluginSource is the metadata recording where an imported plugin release came from.
type PluginSource struct {
	Version     int    `json:"version"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
}

// ReadPluginSource strictly decodes the source metadata of an imported plugin release.
func ReadPluginSource(directory string) (PluginSource, error) {
	filename := filepath.Join(directory, PluginSourceName)
	data, err := os.ReadFile(filename)
	if err != nil {
		return PluginSource{}, fmt.Errorf("reading marketplace plugin source metadata: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var source PluginSource
	if err := decoder.Decode(&source); err != nil {
		return PluginSource{}, fmt.Errorf("decoding marketplace plugin source metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return PluginSource{}, fmt.Errorf("decoding marketplace plugin source metadata: multiple JSON values are not supported")
	} else if !errors.Is(err, io.EOF) {
		return PluginSource{}, fmt.Errorf("decoding marketplace plugin source metadata: %w", err)
	}
	if source.Version != 1 || source.Source == "" || strings.TrimSpace(source.Description) != source.Description {
		return PluginSource{}, fmt.Errorf("invalid marketplace plugin source metadata")
	}
	return source, nil
}
