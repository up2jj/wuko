package http

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const httpOnlyPrefix = "#HttpOnly_"

type persistentJar struct {
	jar     *cookiejar.Jar
	entries map[string]jarEntry
}

type jarEntry struct {
	domain            string
	includeSubdomains bool
	path              string
	secure            bool
	expires           int64
	name              string
	value             string
	httpOnly          bool
}

func openPersistentJar(ctx context.Context, path string) (_ stdhttp.CookieJar, close func() error, resultErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("creating cookie jar directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("opening cookie jar lock: %w", err)
	}
	if err := lock.Chmod(0o600); err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf("setting cookie jar lock permissions: %w", err)
	}
	if err := acquireJarLock(ctx, lock); err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	locked := true
	defer func() {
		if resultErr == nil {
			return
		}
		if locked {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
		_ = lock.Close()
	}()

	jar, err := newPersistentJar(path)
	if err != nil {
		return nil, nil, err
	}
	close = func() (closeErr error) {
		defer func() {
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); closeErr == nil && err != nil {
				closeErr = fmt.Errorf("unlocking cookie jar: %w", err)
			}
			locked = false
			if err := lock.Close(); closeErr == nil && err != nil {
				closeErr = fmt.Errorf("closing cookie jar lock: %w", err)
			}
		}()
		return jar.write(path)
	}
	return jar, close, nil
}

func acquireJarLock(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("locking cookie jar: %w", err)
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("locking cookie jar: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("locking cookie jar: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func newPersistentJar(path string) (*persistentJar, error) {
	base, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	jar := &persistentJar{jar: base, entries: make(map[string]jarEntry)}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return jar, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading cookie jar %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if strings.TrimSpace(text) == "" || (strings.HasPrefix(text, "#") && !strings.HasPrefix(text, httpOnlyPrefix)) {
			continue
		}
		entry, err := parseJarEntry(text)
		if err != nil {
			return nil, fmt.Errorf("reading cookie jar %s line %d: %w", path, line, err)
		}
		if entry.expires > 0 && entry.expires <= time.Now().Unix() {
			continue
		}
		jar.add(entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading cookie jar %s: %w", path, err)
	}
	return jar, nil
}

func parseJarEntry(line string) (jarEntry, error) {
	entry := jarEntry{}
	if strings.HasPrefix(line, httpOnlyPrefix) {
		entry.httpOnly = true
		line = strings.TrimPrefix(line, httpOnlyPrefix)
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 7 {
		return entry, fmt.Errorf("expected 7 tab-separated fields")
	}
	entry.domain = strings.ToLower(strings.TrimPrefix(fields[0], "."))
	if entry.domain == "" {
		return entry, fmt.Errorf("cookie domain must not be empty")
	}
	var err error
	entry.includeSubdomains, err = parseJarBool(fields[1])
	if err != nil {
		return entry, fmt.Errorf("include-subdomains field: %w", err)
	}
	entry.path = fields[2]
	if !strings.HasPrefix(entry.path, "/") {
		return entry, fmt.Errorf("cookie path must begin with /")
	}
	entry.secure, err = parseJarBool(fields[3])
	if err != nil {
		return entry, fmt.Errorf("secure field: %w", err)
	}
	entry.expires, err = strconv.ParseInt(fields[4], 10, 64)
	if err != nil || entry.expires < 0 {
		return entry, fmt.Errorf("invalid expiry %q", fields[4])
	}
	entry.name, entry.value = fields[5], fields[6]
	if err := (&stdhttp.Cookie{Name: entry.name, Value: entry.value}).Valid(); err != nil {
		return entry, err
	}
	return entry, nil
}

func parseJarBool(value string) (bool, error) {
	switch strings.ToUpper(value) {
	case "TRUE":
		return true, nil
	case "FALSE":
		return false, nil
	default:
		return false, fmt.Errorf("expected TRUE or FALSE")
	}
}

func (jar *persistentJar) Cookies(value *url.URL) []*stdhttp.Cookie {
	return jar.jar.Cookies(value)
}

func (jar *persistentJar) SetCookies(value *url.URL, cookies []*stdhttp.Cookie) {
	jar.jar.SetCookies(value, cookies)
	for _, cookie := range cookies {
		entry, ok := entryFromCookie(value, cookie)
		if !ok {
			continue
		}
		key := entry.key()
		if cookie.MaxAge < 0 || (entry.expires > 0 && entry.expires <= time.Now().Unix()) {
			delete(jar.entries, key)
			continue
		}
		jar.entries[key] = entry
	}
}

func entryFromCookie(value *url.URL, cookie *stdhttp.Cookie) (jarEntry, bool) {
	host := strings.ToLower(value.Hostname())
	domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
	includeSubdomains := domain != ""
	if domain == "" {
		domain = host
	} else if !domainMatches(host, domain) {
		return jarEntry{}, false
	}
	path := cookie.Path
	if path == "" || path[0] != '/' {
		path = defaultCookiePath(value.Path)
	}
	expires := int64(0)
	if cookie.MaxAge > 0 {
		expires = time.Now().Add(time.Duration(cookie.MaxAge) * time.Second).Unix()
	} else if !cookie.Expires.IsZero() {
		expires = cookie.Expires.Unix()
	}
	entry := jarEntry{
		domain: domain, includeSubdomains: includeSubdomains, path: path,
		secure: cookie.Secure, expires: expires, name: cookie.Name,
		value: cookie.Value, httpOnly: cookie.HttpOnly,
	}
	if err := (&stdhttp.Cookie{Name: entry.name, Value: entry.value}).Valid(); err != nil {
		return jarEntry{}, false
	}
	return entry, true
}

func domainMatches(host, domain string) bool {
	if host == domain {
		return true
	}
	if net.ParseIP(host) != nil {
		return false
	}
	return strings.HasSuffix(host, "."+domain)
}

func defaultCookiePath(requestPath string) string {
	if requestPath == "" || requestPath[0] != '/' || requestPath == "/" {
		return "/"
	}
	index := strings.LastIndex(requestPath, "/")
	if index == 0 {
		return "/"
	}
	return requestPath[:index]
}

func (jar *persistentJar) add(entry jarEntry) {
	jar.entries[entry.key()] = entry
	host := entry.domain
	scheme := "http"
	if entry.secure {
		scheme = "https"
	}
	cookie := &stdhttp.Cookie{
		Name: entry.name, Value: entry.value, Path: entry.path,
		Secure: entry.secure, HttpOnly: entry.httpOnly,
	}
	if entry.includeSubdomains {
		cookie.Domain = entry.domain
	}
	if entry.expires > 0 {
		cookie.Expires = time.Unix(entry.expires, 0)
	}
	jar.jar.SetCookies(&url.URL{Scheme: scheme, Host: host, Path: entry.path}, []*stdhttp.Cookie{cookie})
}

func (entry jarEntry) key() string {
	return entry.domain + "\x00" + entry.path + "\x00" + entry.name
}

func (entry jarEntry) line() string {
	domain := entry.domain
	if entry.includeSubdomains {
		domain = "." + domain
	}
	if entry.httpOnly {
		domain = httpOnlyPrefix + domain
	}
	return strings.Join([]string{
		domain, jarBool(entry.includeSubdomains), entry.path, jarBool(entry.secure),
		strconv.FormatInt(entry.expires, 10), entry.name, entry.value,
	}, "\t")
}

func jarBool(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}

func (jar *persistentJar) write(path string) (resultErr error) {
	lines := make([]string, 0, len(jar.entries))
	now := time.Now().Unix()
	for _, entry := range jar.entries {
		if entry.expires == 0 || entry.expires > now {
			lines = append(lines, entry.line())
		}
	}
	slices.Sort(lines)
	content := "# Netscape HTTP Cookie File\n# This file was generated by Wuko.\n"
	if len(lines) > 0 {
		content += strings.Join(lines, "\n") + "\n"
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".wuko-cookie-*")
	if err != nil {
		return fmt.Errorf("creating temporary cookie jar: %w", err)
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		_ = os.Remove(temporaryPath)
		if open {
			err := temporary.Close()
			if resultErr == nil && err != nil {
				resultErr = fmt.Errorf("closing temporary cookie jar: %w", err)
			}
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("setting cookie jar permissions: %w", err)
	}
	if _, err := temporary.WriteString(content); err != nil {
		return fmt.Errorf("writing temporary cookie jar: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing temporary cookie jar: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary cookie jar: %w", err)
	}
	open = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replacing cookie jar %s: %w", path, err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening cookie jar directory: %w", err)
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return fmt.Errorf("syncing cookie jar directory: %w", err)
	}
	return nil
}
