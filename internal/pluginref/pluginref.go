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

// withLock acquires the cache's advisory mutation lock for the duration of
// fn, creating the base directory first if needed (a fresh cache has no
// ".lock" file to open). Every method that reads or writes cache state
// serializes through this, matching Fetch's original locked section, so a
// concurrent verify never races a concurrent fetch's rename.
func (c *Cache) withLock(fn func() error) error {
	if err := c.checkBase(); err != nil {
		return err
	}
	if err := os.MkdirAll(c.base, 0o700); err != nil {
		return errorf("pluginref.init", "the plugin reference cache could not be created: %s", boundErr(err))
	}
	unlock, err := lockCache(c.base)
	if err != nil {
		return errorf("pluginref.lock", "the plugin reference cache lock could not be acquired: %s", boundErr(err))
	}
	defer unlock()
	return fn()
}

// sweepStaleTemp removes any leftover staging or clone directory from a
// prior Fetch that crashed or was killed mid-operation. It is called only
// while the cache lock is held, at the start of Fetch, so a resumed
// operator process never accumulates garbage under the cache base.
func (c *Cache) sweepStaleTemp() {
	entries, err := os.ReadDir(c.base)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-fetch-") || strings.HasPrefix(name, ".tmp-clone-") {
			os.RemoveAll(filepath.Join(c.base, name))
		}
	}
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
	var result FetchResult
	err := c.withLock(func() error {
		c.sweepStaleTemp()

		if root, digest, state, err := c.verifyState(rev); err == nil {
			_ = root
			if state.Source == source {
				result = FetchResult{Digest: digest, Cached: true}
				return nil
			}
			// A cache entry exists for this rev, but it was recorded under a
			// different source than the one now requested. The content is
			// keyed only by rev, so this is either a stale local test fixture
			// or a genuine provenance change; either way it is not the
			// cached hit the caller asked for. Fall through and re-fetch,
			// overwriting the recorded source below.
		}

		clone, err := os.MkdirTemp(c.base, ".tmp-clone-*")
		if err != nil {
			return errorf("pluginref.fetch.failed", "a clone directory could not be created: %s", boundErr(err))
		}
		defer os.RemoveAll(clone)

		if err := fetchInto(clone, source, rev); err != nil {
			return err
		}
		head, err := runGit(clone, nil, "rev-parse", "HEAD")
		if err != nil {
			return errorf("pluginref.fetch.failed", "git rev-parse HEAD failed: %s", boundErr(err))
		}
		if err := checkHeadMatchesRev(head, rev); err != nil {
			return err
		}

		tmpTree, err := os.MkdirTemp(c.base, ".tmp-fetch-*")
		if err != nil {
			return errorf("pluginref.fetch.failed", "a staging directory could not be created: %s", boundErr(err))
		}
		committed := false
		defer func() {
			if !committed {
				os.RemoveAll(tmpTree)
			}
		}()
		entries, err := copyTreeInto(clone, tmpTree, filepath.Join(clone, ".git"))
		if err != nil {
			return err
		}
		digest := computeDigest(entries)

		if err := os.MkdirAll(c.revDir(rev), 0o700); err != nil {
			return errorf("pluginref.fetch.failed", "the cache entry directory could not be created: %s", boundErr(err))
		}
		os.RemoveAll(c.treeDir(rev))
		if err := os.Rename(tmpTree, c.treeDir(rev)); err != nil {
			return errorf("pluginref.fetch.failed", "the fetched tree could not be committed: %s", boundErr(err))
		}
		committed = true

		state := State{SchemaVersion: 1, Source: source, Rev: rev, FetchedAt: time.Now().UTC(), Digest: digest}
		if err := c.writeState(rev, state); err != nil {
			return err
		}
		result = FetchResult{Digest: digest, Cached: false}
		return nil
	})
	if err != nil {
		return FetchResult{}, err
	}
	return result, nil
}

// checkHeadMatchesRev fails closed if the checked-out HEAD (raw git output,
// possibly carrying trailing whitespace) is not exactly the pinned rev.
// Split out from Fetch so the mismatch case is directly testable without a
// git server that lies about what it checked out.
func checkHeadMatchesRev(head, rev string) error {
	head = strings.TrimSpace(head)
	if head != rev {
		return errorf("pluginref.fetch.verify", "checked-out HEAD %s does not equal the pinned rev %s", head, rev)
	}
	return nil
}

// fetchInto populates the fresh, empty directory dir with a checkout of
// source at rev, without ever downloading source's full history up front
// (finding: "full clone before bounds"). It first tries a direct-SHA fetch,
// which pulls only the one commit's reachable objects; some git servers
// refuse that (uploadpack.allowReachableSHA1InWant=false, the default
// posture for an arbitrary SHA not advertised as a ref tip on many hosts),
// so on that failure it falls back to a blob-less fetch of the full ref
// history — every server permits this, and it still excludes blob content
// until checkout below materializes exactly the files rev's tree needs.
// Either path fetches strictly less than a full clone with history.
func fetchInto(dir, source, rev string) error {
	cfg := protocolConfig(source)
	if out, err := runGit(dir, cfg, "init", "--quiet"); err != nil {
		return errorf("pluginref.fetch.failed", "git init failed: %s", boundErr(fmt.Errorf("%w: %s", err, out)))
	}
	if out, err := runGit(dir, cfg, "remote", "add", "origin", "--", source); err != nil {
		return errorf("pluginref.fetch.failed", "git remote add failed: %s", boundErr(fmt.Errorf("%w: %s", err, out)))
	}
	if out, err := runGit(dir, cfg, "fetch", "--depth", "1", "origin", rev); err != nil {
		out2, err2 := runGit(dir, cfg, "fetch", "--filter=blob:none", "origin")
		if err2 != nil {
			return errorf("pluginref.fetch.failed",
				"git fetch failed (direct rev fetch: %s; fallback fetch: %s)",
				boundErr(fmt.Errorf("%w: %s", err, out)), boundErr(fmt.Errorf("%w: %s", err2, out2)))
		}
		if out, err := runGit(dir, nil, "checkout", "--detach", rev); err != nil {
			return errorf("pluginref.fetch.failed", "git checkout %s failed: %s", rev, boundErr(fmt.Errorf("%w: %s", err, out)))
		}
		return nil
	}
	if out, err := runGit(dir, nil, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return errorf("pluginref.fetch.failed", "git checkout FETCH_HEAD failed: %s", boundErr(fmt.Errorf("%w: %s", err, out)))
	}
	return nil
}

// protocolConfig pins the git config for one fetchInto invocation so an
// operator's own gitconfig (url.<base>.insteadOf, protocol.allow) can never
// rewrite or reroute the already-validated source URL (ADR 0026 keeps the
// authored grammar closed; this keeps git's own plumbing from reopening it).
// runGit additionally runs with GIT_CONFIG_NOSYSTEM and GIT_CONFIG_GLOBAL=
// /dev/null, so this -c list is the only config git sees for the call.
//
// Production sources are validated https-only before Fetch is ever called,
// so protocol.https.allow=always is the only scheme that ever matters
// there. Only this package's own tests reach Fetch with a local filesystem
// path (the documented test seam: "tests may pass a local path directly,
// since git itself accepts one"); protocol.allow=never would otherwise also
// refuse that local read once pinned below, so the file protocol is opened
// only in that exact case — never for an https source — keeping production
// traffic protocol-file-free.
func protocolConfig(source string) []string {
	cfg := []string{
		"protocol.allow=never",
		"protocol.https.allow=always",
		"protocol.ext.allow=never",
		"core.symlinks=false",
	}
	if !strings.HasPrefix(source, "https://") {
		cfg = append(cfg, "protocol.file.allow=always")
	}
	return cfg
}

// Resolve returns the absolute path to rev's cached, digest-verified plugin
// tree, after checking that the recorded state's Source equals source. It
// satisfies agentproject.PluginCache: Load calls exactly this, and only
// this, to resolve a plugins/<name>.md reference file — never Fetch. It
// performs no network operation.
//
// The source check catches a swap Verify's digest re-hash alone cannot: the
// cache is keyed only by rev, so a rev reused under a different declared
// source would otherwise resolve to content whose provenance no longer
// matches what the reference file claims. `tenon plugin fetch` and
// `tenon plugin status` already re-check this independently via State; this
// makes the same check load-bearing at Load time itself, for every command
// that loads a project, not only the two that inspect plugin references
// directly.
func (c *Cache) Resolve(source, rev string) (string, error) {
	var root string
	err := c.withLock(func() error {
		r, _, state, err := c.verifyState(rev)
		if err != nil {
			return err
		}
		if state.Source != source {
			return errorf("pluginref.source.mismatch",
				"rev %s is cached from a different source (%s) than the one now declared (%s); a rev is content-addressed, not source-addressed, so this is either a stale local cache entry or a genuine provenance change — re-run tenon plugin fetch to confirm and re-pin",
				rev, boundText(state.Source, 256), boundText(source, 256))
		}
		root = r
		return nil
	})
	if err != nil {
		return "", err
	}
	return root, nil
}

// Verify is Resolve plus the recorded digest, for callers (status, fetch's
// idempotence check) that want both. Both take the cache lock so a verify
// never races a concurrent fetch's rename of the tree directory.
func (c *Cache) Verify(rev string) (root, digest string, err error) {
	lockErr := c.withLock(func() error {
		r, d, e := c.verify(rev)
		root, digest = r, d
		return e
	})
	if lockErr != nil {
		return "", "", lockErr
	}
	return root, digest, nil
}

// verify is verifyState without the recorded state, for callers that only
// need the resolved root and digest. It performs no locking of its own;
// every caller reaching it already holds the cache lock (via withLock).
func (c *Cache) verify(rev string) (root, digest string, err error) {
	root, digest, _, err = c.verifyState(rev)
	return root, digest, err
}

// verifyState resolves rev to its cached tree, re-hashes it, and checks the
// result against the recorded digest, returning the full recorded State
// alongside the root and digest so Fetch can compare its recorded Source
// without a second read. It performs no locking of its own; every caller
// reaching it already holds the cache lock (via withLock, or Fetch's own
// locked section).
func (c *Cache) verifyState(rev string) (root, digest string, state *State, err error) {
	if err := c.checkBase(); err != nil {
		return "", "", nil, err
	}
	if !RevPattern.MatchString(rev) {
		return "", "", nil, errorf("pluginref.rev.invalid", "rev must be a full 40-character lowercase hexadecimal git commit SHA; found %q", rev)
	}
	state, err = c.readState(rev)
	if err != nil {
		return "", "", nil, err
	}
	if state == nil {
		return "", "", nil, errorf("pluginref.not-cached", "no cache entry for rev %s; run tenon plugin fetch", rev)
	}
	entries, err := walkTree(c.treeDir(rev))
	if err != nil {
		return "", "", nil, errorf("pluginref.tree.missing", "the cached tree for rev %s could not be read: %s", rev, boundErr(err))
	}
	got := computeDigest(entries)
	if got != state.Digest {
		return "", "", nil, errorf("pluginref.digest.mismatch",
			"the cached tree for rev %s no longer matches its recorded digest (%s, now %s); it may be corrupted or tampered with, and must be re-fetched",
			rev, state.Digest, got)
	}
	return c.treeDir(rev), got, state, nil
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
	lockErr := c.withLock(func() error {
		oldRoot, _, e := c.verify(oldRev)
		if e != nil {
			return e
		}
		newRoot, _, e := c.verify(newRev)
		if e != nil {
			return e
		}
		oldEntries, e := walkTree(oldRoot)
		if e != nil {
			return e
		}
		newEntries, e := walkTree(newRoot)
		if e != nil {
			return e
		}
		oldByPath := make(map[string]entry, len(oldEntries))
		for _, en := range oldEntries {
			oldByPath[en.path] = en
		}
		newByPath := make(map[string]entry, len(newEntries))
		for _, en := range newEntries {
			newByPath[en.path] = en
		}
		for path, en := range newByPath {
			old, ok := oldByPath[path]
			if !ok {
				added = append(added, path)
				continue
			}
			if old.hash != en.hash || old.executable != en.executable {
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
		return nil
	})
	if lockErr != nil {
		return nil, nil, nil, lockErr
	}
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
	var total int64
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
		if err := validTreePath(rel); err != nil {
			return err
		}
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
		if err := treeBoundsError(len(out)+1, total+info.Size()); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += info.Size()
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

// validTreePath rejects any relative tree path carrying a control character
// (anything below 0x20, plus DEL 0x7f) — most importantly \n and \r. git
// itself permits either byte inside a tracked path; computeDigest's encoding
// is now injective regardless (each path is length-prefixed), but two
// distinct trees differing only in such bytes would otherwise be legal
// content this package fetches and staged, so both walkTree and
// copyTreeInto fail closed on them here, the same posture already taken for
// a symlink or other non-regular entry.
func validTreePath(rel string) error {
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return errorf("pluginref.tree.invalid", "%q contains a control character; a fetched plugin tree may not carry one in a path", rel)
		}
	}
	return nil
}

// treeBoundsError enforces MaxTreeFiles and MaxTreeBytes against a running
// tally, shared by walkTree's read-back and copyTreeInto's incremental
// staging so both fail the same way on an oversized tree. Splitting the rule
// out from the filesystem walk that feeds it also makes it directly
// testable without materializing an eight-thousand-file or 64-megabyte
// fixture tree.
func treeBoundsError(fileCount int, totalBytes int64) error {
	if fileCount > MaxTreeFiles {
		return errorf("pluginref.tree.bounds", "a fetched plugin tree may contain at most %d files", MaxTreeFiles)
	}
	if totalBytes > MaxTreeBytes {
		return errorf("pluginref.tree.bounds", "a fetched plugin tree may contain at most %d bytes", MaxTreeBytes)
	}
	return nil
}

// computeDigest hashes a sorted entry list into one stable value, in the
// same shape internal/agentproject's fingerprint rollup uses: each entry's
// path, content length, content hash, and executable intent. The path field
// is length-prefixed ("<byte-length>:<path>") so the encoding stays
// injective no matter what bytes a path carries — two distinct entry lists
// can never hash the same, even before validTreePath's control-character
// rejection is considered. The "sha256v2" prefix marks this encoding
// version: a state.json digest recorded under the prior "sha256:<hex>"
// encoding will not match a freshly computed "sha256v2:<hex>" digest, so a
// stale pre-upgrade cache entry fails verification cleanly (digest mismatch,
// naming re-fetch) rather than silently trusting a digest computed under a
// different, non-injective rule.
func computeDigest(entries []entry) string {
	entries = slices.Clone(entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		mode := "-"
		if e.executable {
			mode = "x"
		}
		fmt.Fprintf(h, "%d:%s\n%d\n%x\n%s\n", len(e.path), e.path, e.length, e.hash, mode)
	}
	return fmt.Sprintf("sha256v2:%x", h.Sum(nil))
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
		if err := validTreePath(relSlash); err != nil {
			return err
		}
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
		if err := treeBoundsError(len(out)+1, total+info.Size()); err != nil {
			return err
		}
		total += info.Size()
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
// returning its combined output for bounded diagnostics. gitConfig entries
// (each a "key=value" pair) are passed ahead of args as "-c" flags, letting
// callers pin protocol and safety config per invocation (see
// protocolConfig); pass nil for a call that touches no network and needs
// none. GIT_CONFIG_NOSYSTEM and GIT_CONFIG_GLOBAL=/dev/null apply to every
// call regardless, so no operator gitconfig — system or user-global — is
// ever consulted; only the -c flags this package supplies itself, plus any
// config already committed inside the working tree git operates in, govern
// the invocation. It never runs a credential helper interactively and never
// prints a token: any secret in a source URL is the operator's own
// responsibility, exactly as it is for a manual git clone.
func runGit(dir string, gitConfig []string, args ...string) (string, error) {
	full := make([]string, 0, len(gitConfig)*2+len(args))
	for _, kv := range gitConfig {
		full = append(full, "-c", kv)
	}
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func boundErr(err error) string {
	return boundText(err.Error(), 500)
}

// boundText maps every control character (anything below 0x20, plus DEL
// 0x7f — not just \n) to a single space, then truncates to at most max
// bytes on a rune boundary. git relays remote-controlled bytes verbatim in
// its own diagnostic output (a ref name, a server message), and without
// this a \r could overwrite a terminal line, and a byte-offset cut could
// split a multi-byte UTF-8 rune into an invalid tail.
func boundText(s string, max int) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	s = b.String()
	if len(s) <= max {
		return s
	}
	cut := 0
	for i := range s {
		if i > max {
			break
		}
		cut = i
	}
	return s[:cut] + "..."
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
