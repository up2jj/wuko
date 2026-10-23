// Package yamlstrict decodes YAML the way wuko reads its own configuration: a file holds
// exactly one document, and a mapping key the target struct does not declare is an error
// rather than a silently ignored typo.
package yamlstrict

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// ErrMultipleDocuments reports input that holds more than one YAML document.
var ErrMultipleDocuments = errors.New("multiple YAML documents are not supported")

// Decode decodes the single YAML document in data into target. A second document is
// ErrMultipleDocuments; a second document that fails to parse reports its own syntax error,
// since that is the real problem in the file.
func Decode(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
	case err == nil:
		return ErrMultipleDocuments
	case !errors.Is(err, io.EOF):
		return err
	}
	return nil
}

// Convert decodes an already parsed value, such as an untyped mapping or a yaml.Node, into
// target with the same unknown-field checks as Decode, by encoding it and decoding it again.
func Convert(value, target any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding YAML: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(target)
}
