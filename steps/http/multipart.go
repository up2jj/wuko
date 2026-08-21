package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type multipartUpload struct {
	field       string
	path        string
	filename    string
	contentType string
	size        int64
}

type multipartRequestBody struct {
	ctx           context.Context
	form          map[string]string
	uploads       []multipartUpload
	boundary      string
	contentType   string
	contentLength int64
}

func prepareMultipartBody(ctx context.Context, workflowDir string, form map[string]string, files []FileConfig) (*multipartRequestBody, error) {
	uploads := make([]multipartUpload, 0, len(files))
	for i, configured := range files {
		path, err := resolvePath(workflowDir, configured.Path)
		if err != nil {
			return nil, fmt.Errorf("resolving files[%d] path: %w", i, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspecting upload file %q: %w", configured.Path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("upload file %q is not a regular file", configured.Path)
		}
		filename := filepath.Base(path)
		if strings.ContainsAny(filename, "\r\n") {
			return nil, fmt.Errorf("upload filename %q must not contain newlines", filename)
		}
		contentType := mime.TypeByExtension(filepath.Ext(filename))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		uploads = append(uploads, multipartUpload{
			field: configured.Field, path: path, filename: filename,
			contentType: contentType, size: info.Size(),
		})
	}

	writer := multipart.NewWriter(io.Discard)
	body := &multipartRequestBody{
		ctx: ctx, form: form, uploads: uploads, boundary: writer.Boundary(),
		contentType: writer.FormDataContentType(),
	}
	length, err := body.measure()
	if err != nil {
		return nil, fmt.Errorf("measuring multipart request body: %w", err)
	}
	body.contentLength = length
	return body, nil
}

func (body *multipartRequestBody) ContentLength() int64 { return body.contentLength }

func (body *multipartRequestBody) ContentType() string { return body.contentType }

func (body *multipartRequestBody) Open() (io.ReadCloser, error) {
	if err := body.ctx.Err(); err != nil {
		return nil, err
	}
	for _, upload := range body.uploads {
		file, err := openUpload(upload)
		if err != nil {
			return nil, err
		}
		if err := closeUpload(file, upload.path); err != nil {
			return nil, err
		}
	}

	reader, writer := io.Pipe()
	stopCancellation := context.AfterFunc(body.ctx, func() {
		writer.CloseWithError(body.ctx.Err())
	})
	go func() {
		defer stopCancellation()
		err := body.write(writer, func(index int, part io.Writer) error {
			return writeUpload(part, body.uploads[index])
		})
		writer.CloseWithError(err)
	}()
	return reader, nil
}

func openUpload(upload multipartUpload) (*os.File, error) {
	file, err := os.Open(upload.path)
	if err != nil {
		return nil, fmt.Errorf("opening upload file %q: %w", upload.path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspecting upload file %q: %w", upload.path, err), closeUpload(file, upload.path))
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("upload file %q is not a regular file", upload.path), closeUpload(file, upload.path))
	}
	if info.Size() != upload.size {
		return nil, errors.Join(fmt.Errorf("upload file %q changed size before it could be read", upload.path), closeUpload(file, upload.path))
	}
	return file, nil
}

func writeUpload(part io.Writer, upload multipartUpload) (resultErr error) {
	file, err := openUpload(upload)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, closeUpload(file, upload.path))
	}()
	if _, err := io.CopyN(part, file, upload.size); err != nil {
		return fmt.Errorf("reading upload file %q: %w", upload.path, err)
	}
	var extra [1]byte
	if count, err := file.Read(extra[:]); err != io.EOF {
		if err != nil {
			return fmt.Errorf("checking upload file %q size: %w", upload.path, err)
		}
		if count != 0 {
			return fmt.Errorf("upload file %q changed size while it was being read", upload.path)
		}
	}
	return nil
}

func closeUpload(file *os.File, path string) error {
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing upload file %q: %w", path, err)
	}
	return nil
}

func (body *multipartRequestBody) measure() (int64, error) {
	counter := &countingWriter{}
	err := body.write(counter, func(index int, _ io.Writer) error {
		return counter.Add(body.uploads[index].size)
	})
	return counter.Count(), err
}

func (body *multipartRequestBody) write(destination io.Writer, writeFile func(int, io.Writer) error) error {
	writer := multipart.NewWriter(destination)
	if err := writer.SetBoundary(body.boundary); err != nil {
		return fmt.Errorf("setting multipart boundary: %w", err)
	}
	keys := make([]string, 0, len(body.form))
	for key := range body.form {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		part, err := writer.CreateFormField(key)
		if err != nil {
			return fmt.Errorf("creating form field %q: %w", key, err)
		}
		if _, err := io.WriteString(part, body.form[key]); err != nil {
			return fmt.Errorf("writing form field %q: %w", key, err)
		}
	}
	for i, upload := range body.uploads {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(
			`form-data; name="%s"; filename="%s"`, escapeMultipartName(upload.field), escapeMultipartName(upload.filename),
		))
		header.Set("Content-Type", upload.contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return fmt.Errorf("creating upload part for %q: %w", upload.path, err)
		}
		if err := writeFile(i, part); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("closing multipart body: %w", err)
	}
	return nil
}

func escapeMultipartName(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, `\"`).Replace(value)
}

type countingWriter struct{ count int64 }

func (writer *countingWriter) Write(data []byte) (int, error) {
	if err := writer.Add(int64(len(data))); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (writer *countingWriter) Add(size int64) error {
	if size < 0 || writer.count > math.MaxInt64-size {
		return fmt.Errorf("multipart request body is too large")
	}
	writer.count += size
	return nil
}

func (writer *countingWriter) Count() int64 { return writer.count }
