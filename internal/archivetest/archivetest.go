// Package archivetest builds tar, gzip-compressed tar and zip archives in memory for tests of
// code that downloads, unpacks or validates them.
package archivetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"testing"
)

// Entry is one archive member. Mode is written as given, so a test can build archives with
// unusual permissions. Data is written only for regular files.
type Entry struct {
	Name string
	Data []byte
	Mode int64
	// Type is a tar type flag; zero means a regular file.
	Type byte
	// Link is the target of a symbolic or hard link.
	Link string
	// Format forces a tar header format, such as tar.FormatPAX; zero lets the writer choose.
	Format tar.Format
}

func (entry Entry) regular() bool {
	return entry.Type == 0 || entry.Type == tar.TypeReg
}

// Tar returns an uncompressed tar archive holding entries in order.
func Tar(t testing.TB, entries ...Entry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writeTar(t, &buffer, entries)
	return buffer.Bytes()
}

// TarGz returns a gzip-compressed tar archive holding entries in order.
func TarGz(t testing.TB, entries ...Entry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressor := gzip.NewWriter(&buffer)
	writeTar(t, compressor, entries)
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// Zip returns a deflate-compressed zip archive holding entries in order. Only regular files
// are supported.
func Zip(t testing.TB, entries ...Entry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		if !entry.regular() {
			t.Fatalf("zip entry %s: only regular files are supported", entry.Name)
		}
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.SetMode(os.FileMode(entry.Mode))
		member, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeTar(t testing.TB, output io.Writer, entries []Entry) {
	t.Helper()
	writer := tar.NewWriter(output)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.Name, Mode: entry.Mode, Typeflag: entry.Type, Linkname: entry.Link, Format: entry.Format}
		if entry.regular() {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.Data))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if !entry.regular() {
			continue
		}
		if _, err := writer.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}
