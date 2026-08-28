package toolruntime

// The shared deno runtime cache copies one deno executable per content
// identity, machine-wide, under the "deno" shared-runtime namespace
// (sharedruntime.go), instead of once per agent project (PR #39 / issue
// #16 introduced the per-agent copy this now shares). Unlike Python's
// interpreter, a compiled deno binary computes its own paths at runtime
// relative to itself, so there is no baked-install-path rewrite analogous
// to Python's sysconfigdata — a plain hardlink of the single file is the
// whole per-agent integration.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// denoIdentityChars mirrors hostsDigest's own truncation convention
// (hostCacheKeyChars): enough of the content digest to separate distinct
// deno builds, short enough to keep the shared-store path readable.
const denoIdentityChars = hostCacheKeyChars

// ensureSharedDeno resolves deno (an already-PATH-resolved deno
// executable) to a copied, read-only entry under the shared deno runtime
// cache, keyed by the content hash of its own bytes rather than a reported
// version string — so a rebuilt or patched deno at the same nominal
// version never collides with a stale shared copy — and returns that
// entry's identity and its path.
func ensureSharedDeno(ctx context.Context, deno string) (identity, sharedPath string, err error) {
	data, err := readClosureExecutable(deno)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	identity = "deno-" + hex.EncodeToString(sum[:])[:denoIdentityChars]

	root, err := sharedRuntimeRoot("deno")
	if err != nil {
		return "", "", err
	}
	dst := filepath.Join(root, identity, "deno")
	marker := sharedReadyMarker(root, identity)
	if markerReady(marker) {
		return identity, dst, nil
	}

	err = withRuntimeLock(ctx, "deno", func() error {
		if markerReady(marker) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return prepareFailure(TypeScript, "the shared deno runtime directory could not be created")
		}
		if err := writeCacheFile(dst, data, 0o755); err != nil {
			return err
		}
		if err := chmodTreeReadOnly(filepath.Dir(dst)); err != nil {
			return prepareFailure(TypeScript, "the shared deno runtime could not be secured: %v", err)
		}
		return markSharedRuntimeReady(marker)
	})
	if err != nil {
		return "", "", err
	}
	return identity, dst, nil
}

// readClosureExecutable resolves source (a build tool commonly found on
// PATH as a symlink, e.g. a Homebrew install), verifies it is a bounded,
// regular, executable file, and returns its bytes.
func readClosureExecutable(source string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, prepareFailure(TypeScript, "the deno executable could not be resolved")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Size() > maxClosureExecutableBytes {
		return nil, prepareFailure(TypeScript, "the deno executable must be a bounded regular executable file")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, prepareFailure(TypeScript, "the deno executable could not be read")
	}
	return data, nil
}
