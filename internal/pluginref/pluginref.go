// Package pluginref implements the plugin-reference cache (ADR 0026 §
// "Plugin acquisition by pointer and pin"): an owner-only, content-addressed,
// offline-verifiable cache of plugin trees fetched from a pinned git
// revision.
//
// Fetch is the one operation in this package — and the only plugin-related
// operation anywhere in tenon — that touches the network. It shells out to
// the system "git" executable: an operator-owned tool dependency this
// package documents rather than vendors, exactly as ADR 0026 requires.
// Resolve and Diff are both purely offline: they read and rehash bytes
// already committed to the cache and never invoke git or dial a network.
//
// Layout under base:
//
//	.lock                    advisory mutation lock
//	<rev>/tree/...           the fetched plugin tree, worktree bytes only (no .git)
//	<rev>/state.json         source, rev, fetch time, and the recorded content digest
//
// The digest folds every tree entry's path, content length, content hash,
// and executable bit into one stable value, the same shape
// internal/agentproject uses for the project source fingerprint, so a
// resolved plugin's cached bytes join that fingerprint on the same terms as
// vendored bytes.
package pluginref

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

// Bounds on one fetched plugin tree (ADR 0013-style safety ceiling, not an
// ordinary-use quota).
const (
	// MaxTreeBytes bounds the total bytes of one fetched plugin tree.
	MaxTreeBytes = 64 * 1024 * 1024
	// MaxTreeFiles bounds the total regular-file count of one fetched plugin
	// tree.
	MaxTreeFiles = 8192
)

// RevPattern is the exact grammar a pinned revision must match: a full,
// lowercase 40-character git commit SHA. A short SHA, a branch, or a tag is
// refused everywhere this package accepts a rev.
var RevPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Error is a typed cache-operation failure carrying a stable dotted code and
// a bounded, credential-free detail, in the same idiom as
// internal/integration's StoreError.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Cache is one owner-only, content-addressed plugin-reference cache rooted
// at a caller-supplied absolute base directory.
type Cache struct {
	base string
}

// NewCache returns a cache rooted at base, an absolute directory owned by
// the invoking user. A relative or empty base makes every operation fail
// rather than write outside the cache.
func NewCache(base string) *Cache { return &Cache{base: base} }

// DefaultBase resolves the per-OS-user default cache location, a sibling of
// internal/integration's store base: on darwin under the user config
// directory, otherwise under XDG_STATE_HOME or ~/.local/state.
func DefaultBase() (string, error) {
	if runtime.GOOS == "darwin" {
		config, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(config, "tenon", "pluginrefs"), nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "tenon", "pluginrefs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "tenon", "pluginrefs"), nil
}

// State is the recorded fetch record for one pinned revision (schema 1).
type State struct {
	SchemaVersion int       `json:"schema_version"`
	Source        string    `json:"source"`
	Rev           string    `json:"rev"`
	FetchedAt     time.Time `json:"fetched_at"`
	Digest        string    `json:"digest"`
}

// FetchResult reports one completed Fetch.
type FetchResult struct {
	// Digest is "sha256:<hex>" over the fetched tree.
	Digest string
	// Cached reports whether rev was already present and verified, so no
	// network operation ran.
	Cached bool
}

func (c *Cache) checkBase() error {
	if c == nil || c.base == "" || !filepath.IsAbs(c.base) {
		return errorf("pluginref.base.invalid", "the plugin reference cache base must be an absolute directory")
	}
	return nil
}

func (c *Cache) revDir(rev string) string  { return filepath.Join(c.base, rev) }
func (c *Cache) treeDir(rev string) string { return filepath.Join(c.revDir(rev), "tree") }
func (c *Cache) statePath(rev string) string {
	return filepath.Join(c.revDir(rev), "state.json")
}

// Fetch clones source, checks out the exact rev, verifies the checkout's
// HEAD equals rev, then copies the worktree (no .git) into the cache
// atomically, content-addressed by rev. It is the only network operation in
// this package. If rev is already cached and its recorded digest still
// verifies, Fetch does no network work and reports Cached: true.
//
// source is passed to "git clone" verbatim. CLI callers validate it is an
// absolute https URL before ever reaching here (ADR 0026 keeps the authored
// grammar closed); tests may pass a local path directly, since git itself
// accepts one.
func (c *Cache) Fetch(source, rev string) (FetchResult, error) {
	if err := c.checkBase(); err != nil {
		return FetchResult{}, err
	}
	if !RevPattern.MatchString(rev) {
		return FetchResult{}, errorf("pluginref.rev.invalid", "rev must be a full 40-character lowercase hexadecimal git commit SHA; found %q", rev)
	}
	if err := os.MkdirAll(c.base, 0o700); err != nil {
		return FetchResult{}, errorf("pluginref.init", "the plugin reference cache could not be created: %s", boundErr(err))
	}
	unlock, err := lockCache(c.base)
	if err != nil {
		return FetchResult{}, errorf("pluginref.lock", "the plugin reference cache lock could not be acquired: %s", boundErr(err))
	}
	defer unlock()

	if root, digest, err := c.verify(rev); err == nil {
		_ = root
		return FetchResult{Digest: digest, Cached: true}, nil
	}

	clone, err := os.MkdirTemp("", "tenon-pluginref-clone-*")
	if err != nil {
		return FetchResult{}, errorf("pluginref.fetch.failed", "a clone directory could not be created: %s", boundErr(err))
	}
	defer os.RemoveAll(clone)

	if out, err := runGit("", "clone", "--", source, clone); err != nil {
		return FetchResult{}, errorf("pluginref.fetch.failed", "git clone failed: %s", boundErr(fmt.Errorf("%w: %s", err, out)))
	}
	if out, err := runGit(clone, "checkout", "--detach", rev); err != nil {
		return FetchResult{}, errorf("pluginref.fetch.failed", "git checkout %s failed: %s", rev, boundErr(fmt.Errorf("%w: %s", err, out)))
	}
	head, err := runGit(clone, "rev-parse", "HEAD")
	if err != nil {
		return FetchResult{}, errorf("pluginref.fetch.failed", "git rev-parse HEAD failed: %s", boundErr(err))
	}
	head = strings.TrimSpace(head)
	if head != rev {
		return FetchResult{}, errorf("pluginref.fetch.verify", "checked-out HEAD %s does not equal the pinned rev %s", head, rev)
	}

	tmpTree, err := os.MkdirTemp(c.base, ".tmp-fetch-*")
	if err != nil {
		return FetchResult{}, errorf("pluginref.fetch.failed", "a staging directory could not be created: %s", boundErr(err))
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmpTree)
		}
	}()
	entries, err := copyTreeInto(clone, tmpTree, filepath.Join(clone, ".git"))
	if err != nil {
		return FetchResult{}, err
	}
	digest := computeDigest(entries)

	if err := os.MkdirAll(c.revDir(rev), 0o700); err != nil {
		return FetchResult{}, errorf("pluginref.fetch.failed", "the cache entry directory could not be created: %s", boundErr(err))
	}
	os.RemoveAll(c.treeDir(rev))
	if err := os.Rename(tmpTree, c.treeDir(rev)); err != nil {
		return FetchResult{}, errorf("pluginref.fetch.failed", "the fetched tree could not be committed: %s", boundErr(err))
	}
	committed = true

	state := State{SchemaVersion: 1, Source: source, Rev: rev, FetchedAt: time.Now().UTC(), Digest: digest}
	if err := c.writeState(rev, state); err != nil {
		return FetchResult{}, err
	}
	return FetchResult{Digest: digest, Cached: false}, nil
}

// Resolve returns the absolute path to rev's cached, digest-verified plugin
// tree. It satisfies agentproject.PluginCache: Load calls exactly this, and
// only this, to resolve a plugins/<name>.md reference file — never Fetch.
// It performs no network operation.
func (c *Cache) Resolve(rev string) (string, error) {
	root, _, err := c.verify(rev)
	return root, err
}

// Verify is Resolve plus the recorded digest, for callers (status, fetch's
// idempotence check) that want both.
func (c *Cache) Verify(rev string) (root, digest string, err error) {
	return c.verify(rev)
}

func (c *Cache) verify(rev string) (root, digest string, err error) {
	if err := c.checkBase(); err != nil {
		return "", "", err
	}
	if !RevPattern.MatchString(rev) {
		return "", "", errorf("pluginref.rev.invalid", "rev must be a full 40-character lowercase hexadecimal git commit SHA; found %q", rev)
	}
	state, err := c.readState(rev)
	if err != nil {
		return "", "", err
	}
	if state == nil {
		return "", "", errorf("pluginref.not-cached", "no cache entry for rev %s; run tenon plugin fetch", rev)
	}
	entries, err := walkTree(c.treeDir(rev))
	if err != nil {
		return "", "", errorf("pluginref.tree.missing", "the cached tree for rev %s could not be read: %s", rev, boundErr(err))
	}
	got := computeDigest(entries)
	if got != state.Digest {
		return "", "", errorf("pluginref.digest.mismatch",
			"the cached tree for rev %s no longer matches its recorded digest (%s, now %s); it may be corrupted or tampered with, and must be re-fetched",
			rev, state.Digest, got)
	}
	return c.treeDir(rev), got, nil
}

// State returns the recorded fetch record for rev, or nil if rev is not
// cached. It never rehashes; use Verify for an offline integrity check.
func (c *Cache) State(rev string) (*State, error) {
	if err := c.checkBase(); err != nil {
		return nil, err
	}
	return c.readState(rev)
}

// Diff reports the component paths added, removed, or changed between two
// cached, already-fetched revisions' trees. It is a bounded summary — paths
// only, never content — for `tenon plugin update`'s pre-rewrite review.
func (c *Cache) Diff(oldRev, newRev string) (added, removed, changed []string, err error) {
	oldRoot, _, err := c.verify(oldRev)
	if err != nil {
		return nil, nil, nil, err
	}
	newRoot, _, err := c.verify(newRev)
	if err != nil {
		return nil, nil, nil, err
	}
	oldEntries, err := walkTree(oldRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	newEntries, err := walkTree(newRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	oldByPath := make(map[string]entry, len(oldEntries))
	for _, e := range oldEntries {
		oldByPath[e.path] = e
	}
	newByPath := make(map[string]entry, len(newEntries))
	for _, e := range newEntries {
		newByPath[e.path] = e
	}
	for path, e := range newByPath {
		old, ok := oldByPath[path]
		if !ok {
			added = append(added, path)
			continue
		}
		if old.hash != e.hash || old.executable != e.executable {
			changed = append(changed, path)
		}
	}
	for path := range oldByPath {
		if _, ok := newByPath[path]; !ok {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed, nil
}

func (c *Cache) readState(rev string) (*State, error) {
	raw, err := os.ReadFile(c.statePath(rev))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errorf("pluginref.read", "the cache state for rev %s could not be read: %s", rev, boundErr(err))
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, errorf("pluginref.read", "the cache state for rev %s is not valid JSON: %s", rev, boundErr(err))
	}
	if state.SchemaVersion != 1 {
		return nil, errorf("pluginref.read", "unsupported cache state schema %d for rev %s", state.SchemaVersion, rev)
	}
	return &state, nil
}

func (c *Cache) writeState(rev string, state State) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errorf("pluginref.write", "the cache state could not be encoded: %s", boundErr(err))
	}
	return writeFileAtomic(c.statePath(rev), append(content, '\n'), 0o600)
}

// entry is one fetched tree entry contributing to the content digest.
type entry struct {
	path       string
	hash       [32]byte
	length     int64
	executable bool
}

// walkTree walks root and returns every regular file as an entry, relative
// paths using "/" separators, sorted by path. A symlink, device, or other
// non-regular, non-directory entry fails closed: a fetched tree may carry
// only ordinary files and directories, the same posture staging and plugin
// loading already take.
func walkTree(root string) ([]entry, error) {
	var out []entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errorf("pluginref.tree.invalid", "%s is a symlink; a fetched plugin tree may contain only regular files and directories", rel)
		}
		if d.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return errorf("pluginref.tree.invalid", "%s is not a regular file", rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, entry{
			path:       rel,
			hash:       sha256.Sum256(data),
			length:     info.Size(),
			executable: info.Mode().Perm()&0o111 != 0,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// computeDigest hashes a sorted entry list into one stable value, in the
// same shape internal/agentproject's fingerprint rollup uses: each entry's
// path, content length, content hash, and executable intent.
func computeDigest(entries []entry) string {
	entries = slices.Clone(entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		mode := "-"
		if e.executable {
			mode = "x"
		}
		fmt.Fprintf(h, "%s\n%d\n%x\n%s\n", e.path, e.length, e.hash, mode)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// copyTreeInto copies src into dst byte-for-byte, excluding the exclude path
// (the source's .git directory), preserving the executable bit, rejecting
// any symlink or special file, and enforcing the total file-count and
// byte-size bounds incrementally so an oversized tree fails before it is
// fully staged. It returns every copied regular file as a digest entry.
func copyTreeInto(src, dst, exclude string) ([]entry, error) {
	var out []entry
	var total int64
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == exclude {
			return filepath.SkipDir
		}
		if path == src {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errorf("pluginref.tree.invalid", "%s is a symlink; a fetched plugin tree may contain only regular files and directories", relSlash)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return errorf("pluginref.tree.invalid", "%s is not a regular file", relSlash)
		}
		if len(out) >= MaxTreeFiles {
			return errorf("pluginref.tree.bounds", "a fetched plugin tree may contain at most %d files", MaxTreeFiles)
		}
		total += info.Size()
		if total > MaxTreeBytes {
			return errorf("pluginref.tree.bounds", "a fetched plugin tree may contain at most %d bytes", MaxTreeBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		executable := info.Mode().Perm()&0o111 != 0
		if executable {
			mode = 0o755
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
		out = append(out, entry{path: relSlash, hash: sha256.Sum256(data), length: info.Size(), executable: executable})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// runGit invokes the system git executable with args, in dir when non-empty,
// returning its combined output for bounded diagnostics. It never runs a
// credential helper interactively and never prints a token: any secret in a
// source URL is the operator's own responsibility, exactly as it is for a
// manual git clone.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func boundErr(err error) string {
	s := err.Error()
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tenon-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
