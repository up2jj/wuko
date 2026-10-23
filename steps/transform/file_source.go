package transform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

var errFileTooLarge = errors.New("file exceeds max_bytes")

// validateFile checks a file source's options. Before templates are rendered, fields that
// still contain one are checked again at run time, when an unresolved template is an error.
func (source source) validateFile(resolved bool) error {
	if source.file == "" {
		return nil
	}
	for _, field := range []struct{ name, value string }{{"file", source.file}, {"format", source.format}, {"max_bytes", source.maxBytes}} {
		if templated(field.value) && resolved {
			return fmt.Errorf("from.%s contains an unresolved template", field.name)
		}
	}
	if !templated(source.file) && !templated(source.format) {
		if _, err := source.fileFormat(); err != nil {
			return err
		}
	}
	if source.maxBytes != "" && !templated(source.maxBytes) {
		if _, err := parseSize(source.maxBytes); err != nil {
			return fmt.Errorf("from.max_bytes: %w", err)
		}
	}
	return nil
}

// fileFormat returns the declared format, or the one the file extension names.
func (source source) fileFormat() (string, error) {
	switch source.format {
	case "json", "jsonl":
		return source.format, nil
	case "":
	default:
		return "", fmt.Errorf("from.format must be json or jsonl")
	}
	switch strings.ToLower(filepath.Ext(source.file)) {
	case ".json":
		return "json", nil
	case ".jsonl", ".ndjson":
		return "jsonl", nil
	}
	return "", fmt.Errorf("from.format is required when from.file does not end in .json, .jsonl or .ndjson")
}

// openFileSource opens the source file and returns a stream of its items. The caller closes
// the file once the pipeline is done with the stream.
func (runner *Runner) openFileSource(ctx context.Context, request step.Request) (*os.File, itemStream, error) {
	source := runner.config.From
	if err := source.validateFile(true); err != nil {
		return nil, nil, err
	}
	filesystem, err := executor.FileSystemFor(request.Executor)
	if err != nil || !executor.IsLocal(filesystem) {
		return nil, nil, fmt.Errorf("from.file streams from the local filesystem and cannot read inside this executor; decode the file instead, or move the step out of the executor scope")
	}
	format, err := source.fileFormat()
	if err != nil {
		return nil, nil, err
	}
	limit := int64(0)
	if source.maxBytes != "" {
		if limit, err = parseSize(source.maxBytes); err != nil {
			return nil, nil, fmt.Errorf("from.max_bytes: %w", err)
		}
	}
	path := source.file
	if !filepath.IsAbs(path) {
		directory := request.RunDir
		if directory == "" {
			if directory, err = os.Getwd(); err != nil {
				return nil, nil, fmt.Errorf("finding run directory: %w", err)
			}
		}
		path = filepath.Join(directory, path)
	}
	path = filepath.Clean(path)
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening transform file %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("inspecting transform file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("transform file %s must be a regular file", path)
	}
	if limit > 0 && info.Size() > limit {
		_ = file.Close()
		return nil, nil, fmt.Errorf("transform file %s exceeds max_bytes %s", path, source.maxBytes)
	}
	var reader io.Reader = file
	if limit > 0 {
		reader = &limitedReader{reader: file, remaining: limit}
	}
	read := jsonArrayItems
	if format == "jsonl" {
		read = jsonLineItems
	}
	items := read(ctx, reader, path)
	return file, func(yield func(any, error) bool) {
		for item, err := range items {
			if errors.Is(err, errFileTooLarge) {
				err = fmt.Errorf("transform file %s exceeds max_bytes %s", path, source.maxBytes)
			}
			if err != nil {
				yield(nil, err)
				return
			}
			// Plain numbers, exactly as decode publishes them.
			if !yield(workflow.RuntimeValue(item), nil) {
				return
			}
		}
	}, nil
}

// jsonArrayItems yields the elements of a top-level JSON array one at a time, holding only
// the element being decoded. Numbers are json.Number, so none rounds through float64.
func jsonArrayItems(ctx context.Context, reader io.Reader, path string) itemStream {
	return func(yield func(any, error) bool) {
		decoder := json.NewDecoder(reader)
		decoder.UseNumber()
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = fmt.Errorf("transform file %s is empty; it must contain a JSON array", path)
			}
			yield(nil, readError(path, decoder, err))
			return
		}
		if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
			yield(nil, fmt.Errorf("transform file %s must contain a JSON array; use decode for other JSON values", path))
			return
		}
		for index := 0; decoder.More(); index++ {
			if err := checkContext(ctx); err != nil {
				yield(nil, err)
				return
			}
			var item any
			if err := decoder.Decode(&item); err != nil {
				yield(nil, readError(path, decoder, fmt.Errorf("item %d: %w", index, err)))
				return
			}
			if !yield(item, nil) {
				return
			}
		}
		if _, err := decoder.Token(); err != nil {
			yield(nil, readError(path, decoder, err))
			return
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			if err == nil {
				err = fmt.Errorf("multiple JSON values are not supported")
			}
			yield(nil, readError(path, decoder, err))
		}
	}
}

// jsonLineItems yields one JSON value per non-blank line.
func jsonLineItems(ctx context.Context, reader io.Reader, path string) itemStream {
	return func(yield func(any, error) bool) {
		lines := bufio.NewReaderSize(reader, 64<<10)
		for number := 1; ; number++ {
			if err := checkContext(ctx); err != nil {
				yield(nil, err)
				return
			}
			line, readErr := lines.ReadBytes('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				if errors.Is(readErr, errFileTooLarge) {
					yield(nil, readErr)
				} else {
					yield(nil, fmt.Errorf("reading transform file %s: %w", path, readErr))
				}
				return
			}
			if text := bytes.TrimSpace(line); len(text) > 0 {
				item, err := decodeLine(text)
				if err != nil {
					yield(nil, fmt.Errorf("reading transform file %s: line %d: %w", path, number, err))
					return
				}
				if !yield(item, nil) {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}
}

func decodeLine(text []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	var item any
	if err := decoder.Decode(&item); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, fmt.Errorf("a line must hold exactly one JSON value")
	}
	return item, nil
}

func readError(path string, decoder *json.Decoder, err error) error {
	if errors.Is(err, errFileTooLarge) {
		return err
	}
	return fmt.Errorf("reading transform file %s near byte %d: %w", path, decoder.InputOffset(), err)
}

// limitedReader fails, rather than stopping quietly, once a file grows past its limit.
type limitedReader struct {
	reader    io.Reader
	remaining int64
}

func (reader *limitedReader) Read(buffer []byte) (int, error) {
	if reader.remaining < 0 {
		return 0, errFileTooLarge
	}
	if int64(len(buffer)) > reader.remaining+1 {
		buffer = buffer[:reader.remaining+1]
	}
	read, err := reader.reader.Read(buffer)
	reader.remaining -= int64(read)
	if reader.remaining < 0 {
		return read, errFileTooLarge
	}
	return read, err
}

// parseSize reads a binary size such as 64KiB, as decode's max_bytes does.
func parseSize(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	for _, unit := range []struct {
		suffix     string
		multiplier int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1},
	} {
		if !strings.HasSuffix(trimmed, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(trimmed, unit.suffix))
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed <= 0 || parsed > math.MaxInt64/unit.multiplier {
			break
		}
		return parsed * unit.multiplier, nil
	}
	return 0, fmt.Errorf("must be a positive size such as 64KiB")
}
