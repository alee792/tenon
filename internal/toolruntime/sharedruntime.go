package toolruntime

// The shared runtime cache holds one machine-wide copy of each pinned
// language runtime tenon installs into an authored-tool closure — the
// standalone CPython interpreter and the deno executable — so agent
// projects that happen to resolve the same version don't each pay the same
// install or copy cost and disk footprint. Every per-agent closure still
// ends up with real regular files at exactly the paths pythonClosureLayout,
// hostCommand, and verifyCache already expect: this cache changes how
// those files get there, never their final shape, so ADR 0021's "closure
// is self-contained, no PATH lookup, no absolute path into the machine
// that prepared it" contract holds for the served artifact exactly as
// before.
//
// Population is copy-once, link-many: a runtime installs or copies into
// the shared store exactly once per machine, normalized and marked ready
// under a namespace-scoped advisory lock, then every per-agent closure
// gets it via hardlink, falling back to a real copy across a filesystem
// boundary. Because a hardlink shares one inode with its source, the
// shared store's own files are made read-only once populated: nothing on
// tenon's own paths ever opens a closure runtime file for writing, and this
// closes that class of cross-agent corruption by construction rather than
// convention.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// runtimeLockPollInterval is how often acquireRuntimeLock retries a
// contended shared-runtime-cache lock.
const runtimeLockPollInterval = 100 * time.Millisecond

// SharedRuntimeRoots returns every machine-wide shared runtime cache root
// this package may install into (issue #38) — the one location outside an
// agent's own closure, source, and workspace that a prepared closure is
// now allowed to be built from. Exported so staging's own build-machine-
// path leak scan (ADR 0021) can name these as needle roots alongside the
// throwaway prepare and workspace roots it already scans: an un-rewritten
// _sysconfigdata_*.py (see RewritePythonSysconfigData) would otherwise bake
// in an operator's home directory path that scan has no way to recognize.
func SharedRuntimeRoots() ([]string, error) {
	var roots []string
	for _, kind := range []string{"python", "deno"} {
		root, err := sharedRuntimeRoot(kind)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

// sharedRuntimeRoot is the machine-wide directory one runtime kind's shared
// cache lives under (os.UserCacheDir()/tenon/runtimes/...).
func sharedRuntimeRoot(kind string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolving the user cache directory: %w", err)
	}
	return filepath.Join(cache, "tenon", "runtimes", kind), nil
}

// withRuntimeLock runs fn while holding the exclusive, blocking-but-
// ctx-cancellable advisory lock for one shared-runtime-cache namespace, so
// two concurrent tenon processes never race installing or normalizing the
// same kind of runtime. Locks are scoped per kind, not per identity: the
// identity a floor-pinned Python spec resolves to isn't known until after
// the install call that needs locking, so a per-identity lock name can't
// be computed up front, and installs are a one-time-per-version-per-machine
// cost, so the modest cross-version serialization this trades away is an
// acceptable simplification over that chicken-and-egg problem.
func withRuntimeLock(ctx context.Context, kind string, fn func() error) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("resolving the user cache directory: %w", err)
	}
	path := filepath.Join(cache, "tenon", "locks", "runtime-"+kind+".lock")
	release, err := acquireRuntimeLock(ctx, path)
	if err != nil {
		return fmt.Errorf("locking the shared %s runtime cache: %w", kind, err)
	}
	defer release()
	return fn()
}

// sharedReadyMarker is the sibling file whose presence is the sole
// cache-hit signal for one shared runtime cache entry. It lives beside its
// identity directory, never inside it, so hardlinkTree walking the
// identity directory never has to know about or skip it.
func sharedReadyMarker(root, identity string) string {
	return filepath.Join(root, identity+".ready")
}

// markerReady reports whether a shared runtime cache entry has been fully
// installed, normalized, and secured. Presence is checked directly rather
// than inferred from permissions, so a change to how entries are secured
// can never be mistaken for readiness.
func markerReady(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// markSharedRuntimeReady writes path's readiness marker atomically (write
// to a temporary sibling, then rename), so a crash between installing or
// normalizing a shared entry and marking it ready is never mistaken for a
// completed one.
func markSharedRuntimeReady(path string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, nil, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resetSharedEntry wipes a shared runtime cache entry directory that
// exists but was never marked ready — either uv's own leftover partial
// install, or the debris of a crash between chmodTreeReadOnly securing an
// entry and markSharedRuntimeReady recording it — so installation always
// starts from a clean, writable slate rather than building on untrusted,
// possibly read-only state (a build tool re-pointed at an already
// chmodTreeReadOnly'd directory that was never marked ready is exactly how
// normalizeInterpreterClosure and writeCacheFile were found to fail:
// os.RemoveAll and a fresh os.WriteFile both need write permission the
// prior, half-finished attempt already stripped). A path that does not
// exist is a silent no-op, so callers may call this unconditionally before
// a fresh install rather than gating it on an existence check themselves.
func resetSharedEntry(dir string) error {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best effort; RemoveAll below reports the real failure
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		_ = os.Chmod(path, info.Mode().Perm()|0o700) // best effort; keep walking regardless
		return nil
	})
	return os.RemoveAll(dir)
}

// chmodTreeReadOnly strips every write bit under root, recursively —
// defense in depth against a multiply-hardlinked shared-cache file ever
// being mutated in place. Because a hardlink shares one inode with its
// source, an in-place write to one copy would be visible through every
// agent's closure that references it, unlike a real copy; nothing on
// tenon's own paths opens a closure runtime file for writing, and this
// closes that class of bug by construction.
func chmodTreeReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return os.Chmod(path, info.Mode().Perm()&^0o222)
	})
}

// linkFn is os.Link by default; tests override it to force hardlinkFile's
// EXDEV fallback path without needing two real filesystems.
var linkFn = os.Link

// hardlinkFile links dst to src's content, falling back to a real copy
// (preserving src's mode, via copyRegularFile) when src and dst cross a
// filesystem boundary — a shared cache under the user's home directory and
// a workspace on a different volume or network mount is a realistic local
// layout, not just a portability nicety.
//
// dst is removed first, ignoring a not-exist error, so a repeat prepare of
// an already-populated closure — an unchanged agent's second `tenon
// apply`, concretely, since Config.CacheDir() is deterministic and Prepare
// runs unconditionally — overwrites rather than failing on os.Link's
// EEXIST. Removing dst needs only write permission on its parent
// directory, never on dst itself, so this succeeds even though dst may be
// a hardlink sharing an inode with the shared store's own read-only copy.
func hardlinkFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := linkFn(src, dst); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return copyRegularFile(src, dst)
		}
		return err
	}
	return nil
}

// hardlinkTree recreates src's regular-file tree at dst, hardlinking every
// file except one whose name skip reports true for — the caller is
// responsible for populating those itself (used for a Python closure's
// _sysconfigdata_*.py files, which must carry per-agent-rewritten content
// rather than the shared store's own path, never a link to it). Mirrors
// copyGoModuleSource's style: refuses symlinks and non-regular entries
// outright, since the shared store is normalized symlink-free before
// anything ever reads from it, and a surviving symlink here means that
// guarantee broke somewhere upstream.
func hardlinkTree(src, dst string, skip func(name string) bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if rel == "." {
			return os.MkdirAll(target, 0o700)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("the shared runtime cache carries a symlink at %q; refusing to link a closure to it", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("the shared runtime cache carries a non-regular entry at %q", rel)
		}
		if skip != nil && skip(d.Name()) {
			return nil
		}
		return hardlinkFile(path, target)
	})
}
