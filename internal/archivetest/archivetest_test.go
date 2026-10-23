package archivetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestTarGzRoundTripsEntries(t *testing.T) {
	t.Parallel()
	data := TarGz(t,
		Entry{Name: "dir/", Mode: 0o755, Type: tar.TypeDir},
		Entry{Name: "dir/file", Mode: 0o600, Data: []byte("body")},
		Entry{Name: "dir/link", Mode: 0o777, Type: tar.TypeSymlink, Link: "file"},
	)
	decompressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(decompressed)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		if header.Name == "dir/file" {
			body, _ := io.ReadAll(reader)
			if string(body) != "body" || header.Mode != 0o600 {
				t.Fatalf("file = %q mode %o", body, header.Mode)
			}
		}
		if header.Name == "dir/link" && header.Linkname != "file" {
			t.Fatalf("link target = %q", header.Linkname)
		}
	}
	if len(names) != 3 {
		t.Fatalf("entries = %v", names)
	}
}

func TestZipRoundTripsModes(t *testing.T) {
	t.Parallel()
	data := Zip(t, Entry{Name: "run.sh", Mode: 0o755, Data: []byte("#!/bin/sh\n")})
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 || reader.File[0].Mode().Perm() != 0o755 {
		t.Fatalf("files = %#v", reader.File)
	}
}
