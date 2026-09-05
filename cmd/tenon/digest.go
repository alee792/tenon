package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
)

// sourceDigestDomain is the domain-separation prefix every source digest is
// hashed under. It exists so a digest and a fingerprint of byte-identical
// content differ: a fingerprint names a configuration the gate proved, a
// digest names bytes that failed it, and no source can ever produce the same
// value under both names. The two are not told apart by inspecting a bare
// string — both render as sha256:<64 hex> — but by the field the value
// arrives in; the domain prefix is what guarantees the values themselves
// cannot coincide.
const sourceDigestDomain = "tenon-source-digest\n"

// digestSourceNames are the top-level names the fallback walk digests: the
// authored inputs the loader itself reads, and nothing else. It mirrors
// internal/agentproject — instructions.md and the component directories
// loaded by loadSkills, loadPlugins, loadSubagents, loadTools,
// loadHarnessFiles, and loadConnections (mcpAuthoredDir) — so the two stay
// in sync; a name added there belongs here too.
//
// It is an allowlist rather than a list of exclusions on purpose. A digest
// that hashed everything it was not told to skip would fold in .git/,
// node_modules/, .venv/, dist/, and editor droppings, and .git alone mutates
// on every fetch and checkout — which would destroy the determinism the
// digest exists to provide.
var digestSourceNames = map[string]bool{
	"instructions.md": true,
	"skills":          true,
	"tools":           true,
	"subagents":       true,
	"plugins":         true,
	"mcp":             true,
	// The removed schedules/ surface (ADR 0029) and the pre-#49 name for
	// mcp/. The loader reads each only to fail closed (schedules.removed,
	// mcp.migration.connections-dir), so its bytes are exactly the bytes that
	// caused that failure; a digest that skipped them would name two sources
	// that differ only there as one.
	"schedules":   true,
	"connections": true,
	"harnesses":   true,
}

// digestDependencyFiles are the native tool dependency files at the agent
// root that the fingerprint inventories, mirroring
// internal/agentproject.toolDependencySpecs (typescript: deno.json,
// deno.lock; python: pyproject.toml, uv.lock; go: go.mod, go.sum). The walk
// digests each when present without asking which languages the tools use:
// it runs on sources that did not load far enough to have an answer.
var digestDependencyFiles = map[string]bool{
	"deno.json":      true,
	"deno.lock":      true,
	"pyproject.toml": true,
	"uv.lock":        true,
	"go.mod":         true,
	"go.sum":         true,
}

// digestEntry is one authored file's contribution to a source digest: its
// relative path, its content hash, and its executable bit, which the
// fingerprint also covers because the bit is authored intent and a tool that
// gained or lost it is a different source.
type digestEntry struct {
	path       string
	hash       string
	executable bool
}

// sourceDigest is a content hash over the authored files of the agent source
// at root. It is emitted beside a gate_failed outcome so a rejected
// candidate is still attributable, and it is explicitly NOT a fingerprint:
// per ADR 0025 a fingerprint is minted only by a passing gate, and a digest
// of a source that does not load carries none of that proof. Callers must
// never present one as the other.
//
// The digest is computed from the loader's own inventory when there is one —
// p.FingerprintEntries, which the loader populates for every source it got
// far enough to inventory — and otherwise by walking the authored files
// under root. Both are deterministic for a given tree; the two are not
// required to agree with each other, because a source that fails before
// inventory and one that fails after are different states of the world and
// neither's digest is ever compared to the other's. The returned string is
// empty only when root itself cannot be read, in which case the caller omits
// the field.
func sourceDigest(root string, p *agentproject.Project) string {
	var entries []digestEntry
	if p != nil && len(p.FingerprintEntries) > 0 {
		for _, e := range p.FingerprintEntries {
			entries = append(entries, digestEntry{path: e.Path, hash: e.Hash, executable: e.Executable})
		}
	} else {
		walked, ok := walkSourceFiles(root)
		if !ok {
			return ""
		}
		entries = walked
	}
	h := sha256.New()
	_, _ = io.WriteString(h, digestPreimage(entries))
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// digestPreimage is the exact material a source digest hashes: the domain
// prefix, then every entry's path, content hash, and executable intent in a
// total order. It is a named function so the domain separation is testable
// as the property it is, rather than only observable through the two hashes
// it keeps apart.
func digestPreimage(entries []digestEntry) string {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b digestEntry) int {
		if c := strings.Compare(a.path, b.path); c != 0 {
			return c
		}
		if c := strings.Compare(a.hash, b.hash); c != 0 {
			return c
		}
		if a.executable == b.executable {
			return 0
		}
		if !a.executable {
			return -1
		}
		return 1
	})
	var b strings.Builder
	b.WriteString(sourceDigestDomain)
	for _, e := range entries {
		mode := "-"
		if e.executable {
			mode = "x"
		}
		fmt.Fprintf(&b, "%s\n%s\n%s\n", e.path, e.hash, mode)
	}
	return b.String()
}

// walkSourceFiles lists every authored regular file under root as a digest
// entry, taking only the names in digestSourceNames and
// digestDependencyFiles: everything else at the root — tenon's own records,
// the output a fresh apply generates into the default workspace, .git/,
// vendored dependency trees, editor droppings — is not authored source and
// must not move a digest. A file that cannot be read is recorded under a
// fixed marker rather than skipped, so the digest of an unreadable tree is
// still defined and still stable across runs. It reports false only when
// root itself cannot be walked at all.
func walkSourceFiles(root string) ([]digestEntry, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, false
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, false
	}
	var entries []digestEntry
	walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == abs {
				return err
			}
			return nil
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		top := strings.SplitN(rel, "/", 2)[0]
		if !digestSourceNames[top] && !(top == rel && digestDependencyFiles[top]) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		entry := digestEntry{path: rel, hash: "unreadable"}
		if info, statErr := d.Info(); statErr == nil {
			entry.executable = info.Mode().Perm()&0o111 != 0
		}
		content, readErr := os.ReadFile(path)
		if readErr == nil {
			entry.hash = fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		}
		entries = append(entries, entry)
		return nil
	})
	if walkErr != nil {
		return nil, false
	}
	return entries, true
}
