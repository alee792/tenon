package toolruntime

// The shared Python interpreter cache installs one standalone CPython per
// resolved identity, machine-wide, under the "python" shared-runtime
// namespace (sharedruntime.go), instead of once per agent project.
// ensureSharedPythonInterpreter is prepareClosurePython's replacement for
// calling `uv python install` directly: it resolves or installs the shared
// entry under the python runtime lock, then the caller hardlinks it into
// its own per-agent closure.

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// exactPythonSpecPattern recognizes a fully pinned version (all three
// components, as a .python-version file names exactly) — the only shape
// that can be resolved to a shared-cache hit without invoking uv at all,
// because the exact patch is already known.
var exactPythonSpecPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func exactPythonSpec(spec string) string {
	if exactPythonSpecPattern.MatchString(spec) {
		return spec
	}
	return ""
}

// ensureSharedPythonInterpreter resolves spec to an installed, normalized,
// read-only interpreter directory under the shared Python runtime cache,
// installing it first if this is the first time this machine has resolved
// spec, and returns its identity (for example
// "cpython-3.11.13-linux-x86_64-gnu").
//
// An exact three-part spec (from a project's own .python-version pin) can
// be resolved to a cache hit without invoking uv at all. A floor spec
// (pyproject.toml's requires-python, without an exact pin) cannot take
// that shortcut — which patch uv resolves it to isn't knowable without
// asking — so this still invokes `uv python install` on every call for a
// floor-pinned project; what's shared is the expensive network fetch and
// extraction, paid once machine-wide regardless, not the uv invocation
// itself.
func ensureSharedPythonInterpreter(ctx context.Context, uv, spec string) (string, error) {
	root, err := sharedRuntimeRoot("python")
	if err != nil {
		return "", err
	}
	if exact := exactPythonSpec(spec); exact != "" {
		hit, ok, serr := scanSharedIdentity(root, exact)
		if serr != nil {
			return "", serr
		}
		if ok && markerReady(sharedReadyMarker(root, hit)) {
			return hit, nil
		}
	}

	var identity string
	err = withRuntimeLock(ctx, "python", func() error {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return prepareFailure(Python, "the shared python runtime directory could not be created")
		}
		// Re-check for a ready match now that the lock is held: another
		// process (or an earlier caller resolving the same floor spec)
		// may have already installed and normalized this exact identity
		// while this call waited for the lock. uv must never be pointed
		// at an already-normalized shared entry again — normalization
		// deletes files uv's own idempotency bookkeeping expects to find
		// there, and the entry is read-only besides — so this is not only
		// an optimization, it is what makes a normalized entry safe to
		// resolve a second time at all.
		if found, ok, serr := scanSharedIdentity(root, spec); serr != nil {
			return serr
		} else if ok {
			if markerReady(sharedReadyMarker(root, found)) {
				identity = found
				return nil
			}
			// Found but never marked ready: either uv's own leftover
			// partial install, or a crash between chmodTreeReadOnly
			// securing this entry and markSharedRuntimeReady recording it.
			// Left in place, its read-only permissions make uv's own
			// re-install attempt fail, permanently poisoning this identity
			// for every future prepare on the machine — wipe it first so
			// installation starts clean.
			if err := resetSharedEntry(filepath.Join(root, found)); err != nil {
				return prepareFailure(Python, "an incomplete shared python runtime entry could not be cleared: %v", err)
			}
		}

		env := hostEnv("PYTHONDONTWRITEBYTECODE=1")
		if err := run(ctx, Python, "uv python install", exec.CommandContext(ctx, uv,
			"python", "install", "--no-bin", "--install-dir", root, spec), root, env); err != nil {
			return err
		}
		found, ok, serr := scanSharedIdentity(root, spec)
		if serr != nil {
			return serr
		}
		if !ok {
			return prepareFailure(Python,
				"the shared python runtime carries no interpreter matching %q after install", spec)
		}
		identity = found

		marker := sharedReadyMarker(root, identity)
		interpDir := filepath.Join(root, identity)
		if err := normalizeInterpreterClosure(root, interpDir); err != nil {
			return err
		}
		if err := chmodTreeReadOnly(interpDir); err != nil {
			return prepareFailure(Python, "the shared python runtime could not be secured: %v", err)
		}
		return markSharedRuntimeReady(marker)
	})
	if err != nil {
		return "", err
	}
	return identity, nil
}

// scanSharedIdentity finds the shared runtime cache entry matching spec —
// exactly, if spec is a full three-part pin, or by its first two version
// components, if spec is a floor. Unlike pythonClosureLayout's per-agent
// scan, this does not error on multiple entries: the shared root
// legitimately accumulates one directory per distinct version this machine
// has ever resolved, so an unrelated version already present is not an
// ambiguity, only a non-match.
func scanSharedIdentity(root, spec string) (identity string, ok bool, err error) {
	entries, rerr := os.ReadDir(root)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", false, nil
		}
		return "", false, rerr
	}
	prefix := "cpython-" + spec
	var matches []string
	for _, e := range entries {
		if !e.IsDir() || !pythonInterpreterDirPattern.MatchString(e.Name()) {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix+".") || strings.HasPrefix(e.Name(), prefix+"-") {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	// Deterministic even if this machine somehow carries more than one
	// match for the same spec (never expected in practice: one machine has
	// one platform, and a floor spec's minor version resolves to one patch
	// per uv release) — pick the lexicographically first rather than the
	// first ReadDir happens to return.
	first := matches[0]
	for _, m := range matches[1:] {
		if m < first {
			first = m
		}
	}
	return first, true, nil
}

// normalizeInterpreterClosure removes everything the shared interpreter
// tree does not need to launch: CPython's own convenience symlinks (never
// referenced — launch execs the versioned interpreter binary directly),
// the terminfo and man-page trees, and uv's own install-dir bookkeeping.
// Runs exactly once per shared identity, inside
// ensureSharedPythonInterpreter, against the shared store rather than a
// per-agent closure — see normalizeSiteClosure for the still-per-agent
// half of what normalizePythonClosure used to do in one pass.
//
// The allowed symlink-target root is cpythonRoot alone, narrower than the
// combined cpythonRoot-plus-siteDir set the single prior pass used. This
// assumes `uv pip install --target` never produces a symlink pointing back
// into the interpreter tree — unverified against every uv release, unlike
// the interpreter's own convenience-symlink shape, which prior CI failures
// have already forced this package to characterize precisely (see
// removePythonClosureSymlinks's doc). If that assumption is ever wrong,
// the narrowing fails safe rather than silently: removePythonClosureSymlinks
// refuses a symlink whose target resolves outside its own allowed roots
// rather than deleting or ignoring it (TestRemovePythonClosureSymlinksRefusesATargetOutsideTheClosure
// proves the general mechanism; TestNormalizeSiteClosureFailsClosedOnASymlinkIntoTheInterpreterTree
// proves this specific narrowing does too), so a wrong assumption here
// surfaces as a named preparation failure, not a leaked or lost file.
func normalizeInterpreterClosure(cpythonRoot, interpDir string) error {
	for _, p := range []string{
		filepath.Join(interpDir, "share", "terminfo"),
		filepath.Join(interpDir, "share", "man"),
		filepath.Join(cpythonRoot, ".lock"),
		filepath.Join(cpythonRoot, ".temp"),
		filepath.Join(cpythonRoot, ".gitignore"),
	} {
		if err := os.RemoveAll(p); err != nil {
			return prepareFailure(Python, "the shared python interpreter could not be normalized: %v", err)
		}
	}
	if err := removePythonClosureSymlinks(cpythonRoot, []string{cpythonRoot}); err != nil {
		return prepareFailure(Python, "the shared python interpreter could not be normalized: %v", err)
	}
	if err := assertPythonClosureHasNoSymlinks(cpythonRoot); err != nil {
		return prepareFailure(Python, "the shared python interpreter could not be normalized: %v", err)
	}
	return nil
}

// normalizeSiteClosure removes what a per-agent locked-dependency install
// does not need to launch: any console-script shims a dependency placed
// under its own bin/, and uv's own target-dir bookkeeping. Still runs once
// per agent, every prepare, since site/ stays unshared (issue #38's own
// stated future extension, not part of this change).
func normalizeSiteClosure(siteDir string) error {
	for _, p := range []string{
		filepath.Join(siteDir, "bin"),
		filepath.Join(siteDir, ".lock"),
		filepath.Join(siteDir, ".temp"),
		filepath.Join(siteDir, ".gitignore"),
	} {
		if err := os.RemoveAll(p); err != nil {
			return prepareFailure(Python, "the python closure could not be normalized: %v", err)
		}
	}
	if err := removePythonClosureSymlinks(siteDir, []string{siteDir}); err != nil {
		return prepareFailure(Python, "the python closure could not be normalized: %v", err)
	}
	return assertPythonClosureHasNoSymlinks(siteDir)
}

// pythonBinaryName derives the versioned interpreter binary name (for
// example "python3.11") from a resolved identity, the same derivation
// pythonClosureLayout already performs inline.
func pythonBinaryName(identity string) (string, error) {
	match := pythonInterpreterDirPattern.FindStringSubmatch(identity)
	if match == nil {
		return "", fmt.Errorf("%q is not a recognized python interpreter identity", identity)
	}
	parts := strings.SplitN(match[1], ".", 3)
	return "python" + parts[0] + "." + parts[1], nil
}

// isSysconfigdataFile reports whether name is one of CPython's generated
// _sysconfigdata_*.py modules — the one file a standalone interpreter build
// bakes its own install directory into (BINDIR, BINLIBDEST, and the other
// absolute-path build_time_vars entries all sharing one install-directory
// prefix). Every other file under the interpreter tree computes its paths
// at runtime relative to the running binary.
func isSysconfigdataFile(name string) bool {
	return strings.HasPrefix(name, "_sysconfigdata_") && strings.HasSuffix(name, ".py")
}

// copySysconfigdataFiles copies (never links) every _sysconfigdata_*.py
// file under src into the corresponding path under dst — the exception
// hardlinkTree leaves unpopulated when isSysconfigdataFile is passed as
// its skip predicate, because these files must end up carrying
// per-agent-rewritten content, never a link back to the shared store's own
// copy (see RewritePythonSysconfigData).
//
// Deliberately does not reuse copyRegularFile, which preserves the
// source's own mode: the shared store's copy is intentionally read-only
// (chmodTreeReadOnly), but the per-agent copy must not inherit that —
// Prepare is documented safe to run repeatedly, so a later prepare of the
// same agent needs to overwrite this file again, and it must never end up
// locked by the very permissions that protect the shared, multiply-
// referenced source it was copied from.
func copySysconfigdataFiles(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isSysconfigdataFile(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}

// RewritePythonSysconfigData rewrites, in place, every occurrence of
// oldPath to newPath inside every _sysconfigdata_*.py file found while
// walking walkDir. It is exported because it now has two legitimate
// callers: this package's own prepareClosurePython, immediately after
// copying a shared interpreter's sysconfigdata files into a per-agent
// closure (walkDir and newPath are the same per-agent path there — the
// files already physically live where their content needs to end up
// claiming they live, and oldPath is the shared store's path, the stale
// content baked in by whichever `uv python install` first produced it);
// and internal/stage's staging-time rewrite (walkDir and oldPath are the
// same throwaway preparation path there — the files still physically live
// where their content already, correctly, claims they live, and newPath is
// the closure's future canonical path once staged). Different pairings of
// which parameter equals walkDir, same operation: replace one baked path
// with another inside the one generated module that carries it.
func RewritePythonSysconfigData(walkDir, oldPath, newPath string) error {
	if oldPath == newPath {
		return nil
	}
	old := []byte(oldPath)
	replacement := []byte(newPath)
	return filepath.WalkDir(walkDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || !isSysconfigdataFile(d.Name()) {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !bytes.Contains(content, old) {
			return nil
		}
		rewritten := bytes.ReplaceAll(content, old, replacement)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return os.WriteFile(path, rewritten, info.Mode().Perm())
	})
}
