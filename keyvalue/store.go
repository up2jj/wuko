// Package keyvalue provides JSON-backed named key-value stores.
package keyvalue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/up2jj/wuko/internal/filelock"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const (
	// Local selects the values directory beside the top-level workflow.
	Local = "local"
	// Global selects the values directory in Wuko's platform configuration directory.
	Global = "global"
)

// Entry is one key and value returned by Store.List.
type Entry struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// Store is one named JSON object rooted in a managed values directory.
type Store struct {
	path     string
	lockPath string
}

// Open validates name and returns a store rooted at dir. It does not access the filesystem.
func Open(dir, name string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("store directory is unavailable")
	}
	if !namePattern.MatchString(name) || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid store name %q", name)
	}
	return &Store{
		path:     filepath.Join(dir, name+".json"),
		lockPath: filepath.Join(dir, name+".lock"),
	}, nil
}

// reservedNames are stores Wuko manages for its change snapshots, once outcomes, and
// workflow picker history. Their owners open them through Open, which does not consult
// this list.
var reservedNames = map[string]struct{}{"changed": {}, "once": {}, "picker": {}}

// ErrClaimBusy reports that a non-blocking per-key claim is held elsewhere.
var ErrClaimBusy = errors.New("key-value claim is busy")

// ErrClaimDeadlock reports that waiting for a key would close a cycle: its holder is
// waiting, directly or transitively, for a key the caller already holds. Waiting would
// block both sides forever, so the wait is refused instead.
var ErrClaimDeadlock = errors.New("waiting would deadlock: its holder is waiting for a key this run already holds")

// claimRetryInterval is how long a waiting claim sleeps between attempts. It also
// bounds how long a cycle goes unnoticed once the last edge of it appears.
const claimRetryInterval = 10 * time.Millisecond

// claimWalkLimit bounds a wait-for walk so a chain that is being rewritten under us
// cannot spin. Any real cycle is far shorter than this.
const claimWalkLimit = 64

// claimRecord is the payload a holder publishes inside its own claim lock file so
// other waiters can see who holds the key and what, if anything, that holder is itself
// waiting for. Waiters read it without taking the lock.
type claimRecord struct {
	PID int    `json:"pid"`
	Key string `json:"key"`
	// Wanted names the key this holder is waiting for, and WantedPath locates that
	// key's claim file. The path is what the walk follows, so a cycle is found even
	// when it runs through a different store or scope than the one it started in.
	Wanted     string `json:"wanted,omitempty"`
	WantedPath string `json:"wanted_path,omitempty"`
}

// Claim owns one per-key advisory lock until Release is called.
type Claim struct {
	handle *filelock.Handle
	key    string
	path   string
}

// Release gives up a per-key claim. It is safe to call more than once.
func (claim *Claim) Release() error {
	if claim == nil {
		return nil
	}
	return claim.handle.Release()
}

// Path locates this claim's lock file. Callers collect the paths of the claims they
// hold and pass them to ClaimKey so a wait that would close a cycle is refused.
func (claim *Claim) Path() string {
	if claim == nil {
		return ""
	}
	return claim.path
}

// SetWanted publishes the key this claim's holder is now waiting for, together with that
// key's claim path, or clears both when key is empty. Waiters walk these edges to find a
// cycle before blocking, so a holder that is about to wait must announce it first:
// announcing late risks two holders each missing the other, while announcing early only
// costs a redundant check.
func (claim *Claim) SetWanted(key, claimPath string) error {
	if claim == nil {
		return nil
	}
	payload, err := json.Marshal(claimRecord{
		PID: os.Getpid(), Key: claim.key, Wanted: key, WantedPath: claimPath,
	})
	if err != nil {
		return fmt.Errorf("encoding claim record: %w", err)
	}
	return claim.handle.SetContent(payload)
}

// ClaimPath is where the advisory lock for key lives. The digest keeps the name a fixed
// length and free of anything the filesystem would object to.
func (s *Store) ClaimPath(key string) string {
	return s.path + "." + fmt.Sprintf("%x", sha256.Sum256([]byte(key))) + ".claim.lock"
}

// readClaimRecord reports the record a live holder published for key. It returns false
// whenever that cannot be established: no file, an unparsable or half-written payload,
// or a holder process that no longer exists. Callers treat false as "no edge", so every
// uncertainty makes the deadlock check less eager rather than more.
func readClaimRecord(claimPath string) (claimRecord, bool) {
	data, err := filelock.ReadContent(claimPath)
	if err != nil {
		return claimRecord{}, false
	}
	var record claimRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return claimRecord{}, false
	}
	if record.PID <= 0 || !processAlive(record.PID) {
		return claimRecord{}, false
	}
	return record, true
}

// claimCycle walks the wait-for edges starting at the claim file wantPath and reports the
// chain of keys back to a claim in held, meaning the caller would close a cycle by
// waiting. The returned chain names the keys involved, starting with the one wanted.
func claimCycle(wantKey, wantPath string, held map[string]struct{}) ([]string, bool) {
	visited := make(map[string]struct{}, claimWalkLimit)
	chain := []string{}
	key, current := wantKey, wantPath
	for len(chain) < claimWalkLimit {
		if _, mine := held[current]; mine {
			return append(chain, key), true
		}
		if _, seen := visited[current]; seen {
			return nil, false
		}
		visited[current] = struct{}{}
		record, ok := readClaimRecord(current)
		if !ok || record.WantedPath == "" {
			return nil, false
		}
		chain = append(chain, key)
		key, current = record.Wanted, record.WantedPath
	}
	return nil, false
}

// processAlive reports whether pid still names a running process. Signal 0 performs the
// permission and existence checks without delivering anything; EPERM means the process
// exists but belongs to another user.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// OpenWorkflowScoped opens a store a workflow asked for by name. It refuses the names Wuko
// reserves so a workflow cannot read or rewrite Wuko's own state through the generic
// key-value interface. Matching ignores case because reserved and workflow stores share a
// directory, and that directory may sit on a case-insensitive filesystem.
func OpenWorkflowScoped(localDir, globalDir, scope, name string) (*Store, error) {
	if _, reserved := reservedNames[strings.ToLower(name)]; reserved {
		return nil, fmt.Errorf("store name %q is reserved by wuko", name)
	}
	return OpenScoped(localDir, globalDir, scope, name)
}

// OpenScoped selects a root explicitly by scope and opens a named store in it.
func OpenScoped(localDir, globalDir, scope, name string) (*Store, error) {
	var dir string
	switch scope {
	case Local:
		dir = localDir
		if dir == "" {
			return nil, fmt.Errorf("local key-value storage is unavailable for this workflow")
		}
	case Global:
		dir = globalDir
		if dir == "" {
			return nil, fmt.Errorf("global key-value storage is unavailable")
		}
	default:
		return nil, fmt.Errorf("scope must be %q or %q", Local, Global)
	}
	return Open(dir, name)
}

// Get returns a cloned JSON value and whether key exists.
func (s *Store) Get(ctx context.Context, key string) (any, bool, error) {
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	var value any
	var found bool
	err := s.withReadLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		value, found = values[key]
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return clone(value), found, nil
}

// Set stores value under key and returns its normalized JSON representation.
func (s *Store) Set(ctx context.Context, key string, value any) (any, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	normalized, err := Normalize(value)
	if err != nil {
		return nil, fmt.Errorf("value is not JSON-compatible: %w", err)
	}
	err = s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		values[key] = normalized
		return s.write(values)
	})
	if err != nil {
		return nil, err
	}
	return clone(normalized), nil
}

// SetIfDifferent atomically stores value when it differs from the current value. It returns true
// when the key was absent or its value changed. An unchanged value does not rewrite the store.
func (s *Store) SetIfDifferent(ctx context.Context, key string, value any) (bool, error) {
	if err := validateKey(key); err != nil {
		return false, err
	}
	normalized, err := Normalize(value)
	if err != nil {
		return false, fmt.Errorf("value is not JSON-compatible: %w", err)
	}
	changed := false
	err = s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		if previous, found := values[key]; found && reflect.DeepEqual(previous, normalized) {
			return nil
		}
		values[key] = normalized
		changed = true
		return s.write(values)
	})
	return changed, err
}

// Update atomically replaces the value stored under key with the result of mutate, which
// receives the current value and whether key existed. The store lock is held across the
// whole read-modify-write, so concurrent updates compose instead of overwriting one
// another. It reports whether the stored value changed; an unchanged value is not
// rewritten.
func (s *Store) Update(ctx context.Context, key string, mutate func(current any, found bool) (any, error)) (any, bool, error) {
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	var stored any
	changed := false
	err := s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		current, found := values[key]
		next, err := mutate(clone(current), found)
		if err != nil {
			return err
		}
		normalized, err := Normalize(next)
		if err != nil {
			return fmt.Errorf("value is not JSON-compatible: %w", err)
		}
		stored = normalized
		if found && reflect.DeepEqual(current, normalized) {
			return nil
		}
		changed = true
		values[key] = normalized
		return s.write(values)
	})
	if err != nil {
		return nil, false, err
	}
	return clone(stored), changed, nil
}

// Delete removes key and returns its previous value and whether it existed.
func (s *Store) Delete(ctx context.Context, key string) (any, bool, error) {
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	var value any
	var deleted bool
	err := s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		value, deleted = values[key]
		if !deleted {
			return nil
		}
		delete(values, key)
		return s.write(values)
	})
	if err != nil {
		return nil, false, err
	}
	return clone(value), deleted, nil
}

// Clear removes every key and reports how many it removed. A store that is already empty
// is not rewritten.
func (s *Store) Clear(ctx context.Context) (int, error) {
	removed := 0
	err := s.withLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		removed = len(values)
		if removed == 0 {
			return nil
		}
		return s.write(make(map[string]any))
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// List returns all entries ordered by key.
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	var entries []Entry
	err := s.withReadLock(ctx, func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		entries = make([]Entry, 0, len(keys))
		for _, key := range keys {
			entries = append(entries, Entry{Key: key, Value: clone(values[key])})
		}
		return nil
	})
	return entries, err
}

// ClaimKey acquires an exclusive claim for key without serializing unrelated keys.
// A waiting claim honors ctx; a non-waiting claim returns ErrClaimBusy on contention.
//
// held names the claim paths the caller already holds, from Claim.Path. A waiting claim
// refuses with ErrClaimDeadlock rather than blocking when the holder of key is waiting,
// directly or transitively, for one of them; holders publish those edges with
// Claim.SetWanted.
func (s *Store) ClaimKey(ctx context.Context, key string, wait bool, held ...string) (*Claim, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating store directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("setting store directory permissions: %w", err)
	}
	path := s.ClaimPath(key)
	if !wait {
		handle, acquired, err := filelock.TryAcquire(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("claiming key %q: %w", key, err)
		}
		if !acquired {
			return nil, fmt.Errorf("claiming key %q: %w", key, ErrClaimBusy)
		}
		return s.publishClaim(handle, key)
	}
	heldPaths := make(map[string]struct{}, len(held))
	for _, name := range held {
		heldPaths[name] = struct{}{}
	}
	// Poll rather than block in flock so the wait-for graph is re-examined between
	// attempts: the edge that closes a cycle is often published after this wait began.
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("claiming key %q: %w", key, err)
		}
		handle, acquired, err := filelock.TryAcquire(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("claiming key %q: %w", key, err)
		}
		if acquired {
			return s.publishClaim(handle, key)
		}
		if chain, cyclic := claimCycle(key, path, heldPaths); cyclic {
			return nil, fmt.Errorf("claiming key %q: %w (cycle: %s)", key, ErrClaimDeadlock, strings.Join(chain, " -> "))
		}
		timer := time.NewTimer(claimRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("claiming key %q: %w", key, ctx.Err())
		case <-timer.C:
		}
	}
}

// publishClaim records the new holder in its own lock file so other waiters can resolve
// it. A claim that cannot publish is released rather than held silently, because an
// unreadable holder is exactly what makes another process wait forever.
func (s *Store) publishClaim(handle *filelock.Handle, key string) (*Claim, error) {
	claim := &Claim{handle: handle, key: key, path: handle.Path()}
	if err := claim.SetWanted("", ""); err != nil {
		return nil, errors.Join(fmt.Errorf("claiming key %q: %w", key, err), claim.Release())
	}
	return claim, nil
}

func (s *Store) withLock(ctx context.Context, operation func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating store directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("setting store directory permissions: %w", err)
	}
	lock, err := filelock.Acquire(ctx, s.lockPath)
	if err != nil {
		return fmt.Errorf("store %s: %w", s.path, err)
	}
	return s.holding(lock, operation)
}

// withReadLock runs a read-only operation without creating anything. A store that was
// never written has no directory to make and no lock file to leave behind, so a workflow
// that only reads leaves the values root untouched. Reading such a store without the lock
// stays correct because writers publish with an atomic rename: a reader sees the complete
// previous file or the complete new one, never a partial write.
func (s *Store) withReadLock(ctx context.Context, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("store %s: %w", s.path, err)
	}
	lock, err := filelock.AcquireExisting(ctx, s.lockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store %s: %w", s.path, err)
	}
	return s.holding(lock, operation)
}

// holding runs operation and releases lock afterwards. A nil lock means the store has no
// lock file to hold, which only the read path allows.
func (s *Store) holding(lock *filelock.Handle, operation func() error) (resultErr error) {
	defer func() {
		if err := lock.Release(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("store %s: %w", s.path, err)
		}
	}()
	return operation()
}

func (s *Store) read() (map[string]any, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading store %s: %w", s.path, err)
	}
	// An empty file is what an interrupted create leaves behind. Read it as the empty
	// store a missing file already reads as, rather than failing every later operation.
	if len(bytes.TrimSpace(data)) == 0 {
		return make(map[string]any), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("decoding store %s: %w", s.path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decoding store %s: multiple JSON values", s.path)
		}
		return nil, fmt.Errorf("decoding store %s: %w", s.path, err)
	}
	if values == nil {
		return nil, fmt.Errorf("decoding store %s: root must be a JSON object", s.path)
	}
	return values, nil
}

func (s *Store) write(values map[string]any) (resultErr error) {
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding store: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(dir, ".wuko-values-*")
	if err != nil {
		return fmt.Errorf("creating temporary store: %w", err)
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		_ = os.Remove(temporaryPath)
		if open {
			err = temporary.Close()
		}
		if resultErr == nil && err != nil {
			resultErr = fmt.Errorf("closing temporary store: %w", err)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("setting store permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("writing temporary store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("syncing temporary store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary store: %w", err)
	}
	open = false
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replacing store %s: %w", s.path, err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening store directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("syncing store directory: %w", err)
	}
	return nil
}

func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	return nil
}

// Normalize converts a value to its lossless encoding/json representation.
func Normalize(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func clone(value any) any {
	cloned, err := Normalize(value)
	if err != nil {
		panic("keyvalue: stored value is not JSON-compatible")
	}
	return cloned
}
