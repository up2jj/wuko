package workflow

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/process"
)

const githubAPIVersion = "2022-11-28"

// githubStoredTokenTimeout bounds the stored-credential lookup so a stalled credential helper
// cannot hang workflow loading, matching the bound the command action source uses.
const githubStoredTokenTimeout = 30 * time.Second

type githubWukoActionLocator struct {
	owner      string
	repository string
	ref        string
	directory  string
}

func (locator githubWukoActionLocator) String() string {
	repository := locator.owner + "/" + locator.repository
	if locator.ref != "" {
		repository += "@" + locator.ref
	}
	return repository + ":" + locator.directory
}

func parseGitHubWukoActionLocator(value string) (githubWukoActionLocator, error) {
	if strings.TrimSpace(value) != value {
		return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: surrounding whitespace is not allowed", value)
	}
	repository, directory, found := strings.Cut(value, ":")
	if !found || directory == "" {
		return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: expected owner/repo[@ref]:directory", value)
	}
	ownerRepository := repository
	ref := ""
	if before, after, hasRef := strings.Cut(repository, "@"); hasRef {
		ownerRepository, ref = before, after
		if ref == "" || strings.Contains(ref, "@") {
			return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: ref is invalid", value)
		}
	}
	parts := strings.Split(ownerRepository, "/")
	if len(parts) != 2 || !validGitHubPart(parts[0]) || !validGitHubPart(parts[1]) {
		return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: expected owner/repo[@ref]:directory", value)
	}
	if ref != "" && strings.TrimSpace(ref) != ref {
		return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: ref is invalid", value)
	}
	if ref != "" {
		if err := validateRemotePath(ref); err != nil {
			return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: ref is invalid", value)
		}
	}
	if err := validateRemotePath(directory); err != nil {
		return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: directory %w", value, err)
	}
	if name := path.Base(directory); name == "action.yml" || name == "action.yaml" {
		return githubWukoActionLocator{}, fmt.Errorf("invalid GitHub-hosted Wuko action locator %q: path must identify an action directory, not its manifest", value)
	}
	return githubWukoActionLocator{owner: parts[0], repository: parts[1], ref: ref, directory: directory}, nil
}

// looksLikeGitHubWukoActionLocator reports whether a scalar uses value is written in the
// owner/repo[@ref]:directory shape rather than as a local path. Local action paths are relative
// file paths, which do not pair an owner/repo prefix with a colon, so the shape is what separates
// the two forms. A value that starts with ./, ../, / or ~ is always taken as a path so a local
// directory whose name contains a colon stays reachable.
func looksLikeGitHubWukoActionLocator(value string) bool {
	if strings.HasPrefix(value, ".") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") {
		return false
	}
	repository, _, found := strings.Cut(value, ":")
	if !found {
		return false
	}
	if before, _, hasRef := strings.Cut(repository, "@"); hasRef {
		repository = before
	}
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

// escapeGitHubPath escapes each segment separately so a ref such as feature/release keeps its
// separators. GitHub resolves refs written as path segments, and a percent-encoded separator is
// not accepted. Segments are validated by parseGitHubWukoActionLocator, so no segment can walk up.
func escapeGitHubPath(value string) string {
	segments := strings.Split(value, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

type githubCredentials struct {
	environment map[string]string
	stored      func(context.Context, map[string]string) string
	once        sync.Once
	tokenValue  string
}

func newGitHubCredentials(environment map[string]string, stored func(context.Context, map[string]string) string) *githubCredentials {
	return &githubCredentials{environment: maps.Clone(environment), stored: stored}
}

func (credentials *githubCredentials) token(ctx context.Context, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if token := strings.TrimSpace(credentials.environment["GH_TOKEN"]); token != "" {
		return token
	}
	if token := strings.TrimSpace(credentials.environment["GITHUB_TOKEN"]); token != "" {
		return token
	}
	credentials.once.Do(func() {
		if credentials.stored != nil {
			credentials.tokenValue = strings.TrimSpace(credentials.stored(ctx, credentials.environment))
		}
	})
	return credentials.tokenValue
}

func githubStoredToken(ctx context.Context, environment map[string]string) string {
	ctx, cancel := context.WithTimeout(ctx, githubStoredTokenTimeout)
	defer cancel()
	result, err := process.Run(ctx, process.Options{
		Command: "gh", Args: []string{"auth", "token", "--hostname", "github.com"},
		Env: environment, CaptureLimit: 4096,
	})
	if err != nil || result.StdoutTruncated {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// maxGitHubRepositoryArchive bounds the repository tarball. The archive covers the whole
// repository rather than the action directory alone, so it is allowed to be larger than an action
// package; the limit applies to both the compressed download and the decompressed stream so a
// crafted archive cannot expand without bound, and only files under the action directory are kept.
const maxGitHubRepositoryArchive = 100 << 20

// fetchGitHubWukoAction downloads the repository tarball once and keeps the action directory from
// it. Reading the directory through the Git tree and blob endpoints instead costs one API request
// per file, which exhausts the 60-request anonymous hourly budget on a package of any size.
func (loader *Loader) fetchGitHubWukoAction(ctx context.Context, locator githubWukoActionLocator, token string) ([]byte, error) {
	response, err := loader.openGitHubTarball(ctx, locator.owner, locator.repository, locator.ref, token, "GitHub-hosted Wuko action archive")
	if err != nil {
		return nil, githubWukoActionFetchError(locator, response, err)
	}
	defer response.Body.Close()
	files, err := readGitHubWukoActionArchive(response.Body, locator)
	if err != nil {
		return nil, err
	}
	return zipActionFiles(files)
}

// openGitHubTarball requests the repository tarball for ref, or the default branch when ref is
// empty. The caller closes the body of a successful response.
func (loader *Loader) openGitHubTarball(ctx context.Context, owner, repository, ref, token, kind string) (*http.Response, error) {
	endpoint := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/tarball"
	if ref != "" {
		endpoint += "/" + escapeGitHubPath(ref)
	}
	remoteURL, err := url.Parse(githubAPIBase + endpoint)
	if err != nil {
		return nil, fmt.Errorf("building GitHub API URL: %w", err)
	}
	return loader.open(ctx, remoteURL, githubHeaders(token, "application/vnd.github+json"), kind)
}

// readGitHubWukoActionArchive keeps the files under the action directory of a repository tarball.
func readGitHubWukoActionArchive(body io.Reader, locator githubWukoActionLocator) (map[string]ActionFile, error) {
	files := make(map[string]ActionFile)
	var total int64
	err := walkGitHubArchiveScope(body, githubArchiveScope{
		repository: locator.String(), directory: locator.directory, label: "GitHub-hosted Wuko action", maxEntries: maxEntries,
	}, func(name string, mode os.FileMode, content io.Reader) error {
		data, err := readArchiveFile(func() (io.ReadCloser, error) { return io.NopCloser(content), nil }, &total)
		if err != nil {
			return err
		}
		files[name] = ActionFile{Data: data, Mode: mode}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("GitHub-hosted Wuko action directory %q contains no files in %s", locator.directory, locator.String())
	}
	return files, nil
}

// githubArchiveScope selects one directory of a repository tarball. An empty directory selects
// the whole repository.
type githubArchiveScope struct {
	repository string
	directory  string
	label      string
	maxEntries int
}

// walkGitHubArchiveScope streams the regular files under scope.directory of a repository tarball
// to visit, with paths relative to that directory and normalized modes. GitHub wraps the repository
// in a single owner-repository-commit root directory, which is stripped before the directory is
// matched. Symlinks, duplicate paths, and more than scope.maxEntries entries are rejected, and the
// compressed and decompressed streams are both bounded by maxGitHubRepositoryArchive.
func walkGitHubArchiveScope(body io.Reader, scope githubArchiveScope, visit func(name string, mode os.FileMode, content io.Reader) error) error {
	overflow := fmt.Errorf("GitHub repository archive for %s exceeds %d-byte limit", scope.repository, maxGitHubRepositoryArchive)
	gz, err := gzip.NewReader(newLimitedReader(body, maxGitHubRepositoryArchive, overflow))
	if err != nil {
		return fmt.Errorf("opening GitHub repository archive for %s: %w", scope.repository, err)
	}
	defer gz.Close()

	reader := tar.NewReader(newLimitedReader(gz, maxGitHubRepositoryArchive, overflow))
	prefix := ""
	if scope.directory != "" {
		prefix = scope.directory + "/"
	}
	seen := make(map[string]struct{})
	root := ""
	entries := 0
	directoryFound := prefix == ""
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading GitHub repository archive for %s: %w", scope.repository, err)
		}
		repositoryPath, err := githubArchivePath(header.Name, &root)
		if err != nil {
			return fmt.Errorf("GitHub repository archive for %s: %w", scope.repository, err)
		}
		if repositoryPath == "" {
			continue
		}
		if repositoryPath == scope.directory {
			if header.Typeflag != tar.TypeDir {
				return fmt.Errorf("%s path %q must identify a directory", scope.label, scope.directory)
			}
			directoryFound = true
			continue
		}
		name, found := strings.CutPrefix(repositoryPath, prefix)
		if !found {
			continue
		}
		directoryFound = true
		entries++
		if entries > scope.maxEntries {
			return fmt.Errorf("%s directory exceeds %d-entry limit", scope.label, scope.maxEntries)
		}
		if err := validateArchivePath(name); err != nil {
			return fmt.Errorf("%s archive: %w", scope.label, err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
		default:
			return fmt.Errorf("%s file %q is a symlink or is not a regular file", scope.label, name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("%s archive contains duplicate path %q", scope.label, name)
		}
		seen[name] = struct{}{}
		if err := visit(name, githubArchiveMode(header.FileInfo().Mode()), reader); err != nil {
			return fmt.Errorf("reading %s file %q: %w", scope.label, name, err)
		}
	}
	if !directoryFound {
		return fmt.Errorf("%s directory %q does not exist in %s", scope.label, scope.directory, scope.repository)
	}
	return nil
}

// githubArchivePath strips the archive's single root directory and returns the repository-relative
// path, or an empty string for the root entry itself. A second root would let two files collide in
// the extracted package, so the first one seen is the only one accepted.
func githubArchivePath(name string, root *string) (string, error) {
	first, rest, _ := strings.Cut(name, "/")
	if *root == "" {
		*root = first
	}
	if first != *root {
		return "", fmt.Errorf("archive contains more than one root directory")
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return "", nil
	}
	if err := validateArchivePath(rest); err != nil {
		return "", err
	}
	return rest, nil
}

// githubArchiveMode normalizes the archive mode to the two modes a Git tree can record, so a
// materialized action never carries setuid, setgid, sticky, or group- and world-writable bits.
func githubArchiveMode(mode os.FileMode) os.FileMode {
	if mode.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// newLimitedReader fails with err once more than limit bytes have been read, so neither an
// oversized download nor a decompression bomb can be read without bound.
func newLimitedReader(reader io.Reader, limit int64, err error) io.Reader {
	return &limitedReader{reader: reader, remaining: limit + 1, err: err}
}

type limitedReader struct {
	reader    io.Reader
	remaining int64
	err       error
}

func (reader *limitedReader) Read(buffer []byte) (int, error) {
	if reader.remaining <= 0 {
		return 0, reader.err
	}
	if int64(len(buffer)) > reader.remaining {
		buffer = buffer[:reader.remaining]
	}
	read, err := reader.reader.Read(buffer)
	reader.remaining -= int64(read)
	if reader.remaining <= 0 && err == nil {
		err = reader.err
	}
	return read, err
}

func githubHeaders(token, accept string) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", accept)
	headers.Set("User-Agent", "wuko")
	headers.Set("X-GitHub-Api-Version", githubAPIVersion)
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	return headers
}

func githubWukoActionFetchError(locator githubWukoActionLocator, response *http.Response, err error) error {
	return githubFetchError("GitHub-hosted Wuko action "+locator.String(),
		"raise it with uses.token, GH_TOKEN/GITHUB_TOKEN, or gh authentication",
		"provide uses.token, set GH_TOKEN/GITHUB_TOKEN, or authenticate gh", response, err)
}

// githubFetchError explains a failed GitHub API request for subject. rateHint and accessHint tell
// the user how to raise the rate limit and how to supply credentials.
func githubFetchError(subject, rateHint, accessHint string, response *http.Response, err error) error {
	status := 0
	header := http.Header{}
	if response != nil {
		status, header = response.StatusCode, response.Header
	}
	// GitHub reports an exhausted rate limit as 403 or 429. Without this the message below would
	// blame credentials for a limit that authenticating only raises.
	if (status == http.StatusForbidden || status == http.StatusTooManyRequests) && header.Get("X-RateLimit-Remaining") == "0" {
		return fmt.Errorf("accessing %s: the GitHub API rate limit is exhausted; retry after it resets, or %s: %w", subject, rateHint, err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
		return fmt.Errorf("accessing %s: repository or ref was not found or is not accessible; %s: %w", subject, accessHint, err)
	}
	return fmt.Errorf("accessing %s: %w", subject, err)
}

func zipActionFiles(files map[string]ActionFile) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		file := files[name]
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(file.Mode)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("packaging GitHub-hosted Wuko action file %q: %w", name, err)
		}
		if _, err := writer.Write(file.Data); err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("packaging GitHub-hosted Wuko action file %q: %w", name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("packaging GitHub-hosted Wuko action: %w", err)
	}
	return buffer.Bytes(), nil
}
