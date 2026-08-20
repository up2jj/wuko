// Package variables loads typed workflow variables from configuration files.
package variables

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

var supportedFormats = map[string]string{
	".json": "json",
	".toml": "toml",
}

// LoadFiles reads variable files relative to baseDir and merges them from left to right.
// Each top-level value is one variable, so later files replace earlier values without
// recursively merging nested objects.
func LoadFiles(ctx context.Context, baseDir string, paths []string) (map[string]any, error) {
	result := make(map[string]any)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, err := loadFile(baseDir, path)
		if err != nil {
			return nil, err
		}
		maps.Copy(result, values)
	}
	return result, nil
}

// ValidatePath verifies that a non-templated file extension is supported. A templated
// extension is checked after the workflow renders it at execution time.
func ValidatePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("variable file path must not be empty")
	}
	ext := filepath.Ext(path)
	if strings.Contains(ext, "{{") || strings.Contains(ext, "}}") {
		return nil
	}
	if _, ok := supportedFormats[strings.ToLower(ext)]; !ok {
		return fmt.Errorf("unsupported variable file extension %q; expected .json or .toml", ext)
	}
	return nil
}

func loadFile(baseDir, path string) (map[string]any, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	resolved, err := resolvePath(baseDir, path)
	if err != nil {
		return nil, err
	}
	format := supportedFormats[strings.ToLower(filepath.Ext(resolved))]

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("reading variable file %s: %w", resolved, err)
	}

	reader := viper.New()
	reader.SetConfigType(format)
	if err := reader.ReadConfig(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("reading variable file %s: %w", resolved, err)
	}
	settings := reader.AllSettings()

	// AllSettings is built from Viper's leaf-key index, so it omits nil values and
	// empty maps. Decode the raw object as well and restore only those missing
	// branches, retaining Viper's normal key normalization and dotted-key behavior
	// for all values it does expose.
	raw, err := decodeRaw(data, format)
	if err != nil {
		return nil, fmt.Errorf("reading variable file %s: %w", resolved, err)
	}
	restoreMissing(settings, raw)
	return settings, nil
}

func decodeRaw(data []byte, format string) (map[string]any, error) {
	decoder, err := viper.NewCodecRegistry().Decoder(format)
	if err != nil {
		return nil, err
	}
	raw := make(map[string]any)
	if err := decoder.Decode(data, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func restoreMissing(settings, raw map[string]any) {
	for key, value := range raw {
		restoreMissingPath(settings, strings.Split(strings.ToLower(key), "."), value)
	}
}

func restoreMissingPath(settings map[string]any, path []string, value any) {
	key := path[0]
	if len(path) == 1 {
		existing, exists := settings[key]
		if !exists {
			if rawMap, ok := value.(map[string]any); ok {
				childMap := make(map[string]any)
				settings[key] = childMap
				restoreMissing(childMap, rawMap)
			} else {
				settings[key] = value
			}
			return
		}
		existingMap, existingOK := existing.(map[string]any)
		rawMap, rawOK := value.(map[string]any)
		if existingOK && rawOK {
			restoreMissing(existingMap, rawMap)
		}
		return
	}

	child, exists := settings[key]
	if exists {
		childMap, ok := child.(map[string]any)
		if !ok {
			return
		}
		restoreMissingPath(childMap, path[1:], value)
		return
	}

	childMap := make(map[string]any)
	settings[key] = childMap
	restoreMissingPath(childMap, path[1:], value)
}

func resolvePath(baseDir, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	resolved, err := filepath.Abs(filepath.Join(baseDir, path))
	if err != nil {
		return "", fmt.Errorf("resolving variable file %s: %w", path, err)
	}
	return resolved, nil
}
