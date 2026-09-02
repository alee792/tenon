package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
)

// sourceDigestDomain is the domain-separation prefix every source digest is
// hashed under. It exists so a digest and a fingerprint of byte-identical
// content can never collide: a fingerprint names a configuration the gate
// proved, a digest names bytes that failed it, and the two must be
// distinguishable by value alone and not only by the field they arrive in.
const sourceDigestDomain = "tenon-source-digest\n"

// digestExcluded are the top-level names a fresh apply generates into a
// workspace, plus tenon's own record directory. The default workspace is the
// agent directory itself, so a source that has been applied in place carries
// generated output beside its authored files; a digest that included them
// would change when nothing authored changed. Authored harness files live
// under harness/ in the source and are not affected.
var digestExcluded = map[string]bool{
	".tenon":    true,
	".claude":   true,
	".codex":    true,
	".mcp.json": true,
	"CLAUDE.md": true,
	"AGENTS.md": true,
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
// far enough to inventory — and otherwise by walking regular files under
// root. Both are deterministic for a given tree; the two are not required to
// agree with each other, because a source that fails before inventory and
// one that fails after are different states of the world and neither's
// digest is ever compared to the other's. The returned string is empty only
// when root itself cannot be read, in which case the caller omits the field.
func sourceDigest(root string, p *agentproject.Project) string {
	var entries [][2]string
	if p != nil && len(p.FingerprintEntries) > 0 {
		for _, e := range p.FingerprintEntries {
			entries = append(entries, [2]string{e.Path, e.Hash})
		}
	} else {
		walked, ok := walkSourceFiles(root)
		if !ok {
			return ""
		}
		entries = walked
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i][0] != entries[j][0] {
			return entries[i][0] < entries[j][0]
		}
		return entries[i][1] < entries[j][1]
	})
	h := sha256.New()
	_, _ = io.WriteString(h, sourceDigestDomain)
	for _, e := range entries {
		fmt.Fprintf(h, "%s\n%s\n", e[0], e[1])
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// walkSourceFiles lists every regular file under root as a (relative path,
// content hash) pair, excluding generated output. A file that cannot be read
// is recorded under a fixed marker rather than skipped, so the digest of an
// unreadable tree is still defined and still stable across runs. It reports
// false only when root itself cannot be walked at all.
func walkSourceFiles(root string) ([][2]string, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, false
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, false
	}
	var entries [][2]string
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
		if digestExcluded[strings.SplitN(rel, "/", 2)[0]] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			entries = append(entries, [2]string{rel, "unreadable"})
			return nil
		}
		entries = append(entries, [2]string{rel, fmt.Sprintf("sha256:%x", sha256.Sum256(content))})
		return nil
	})
	if walkErr != nil {
		return nil, false
	}
	return entries, true
}
