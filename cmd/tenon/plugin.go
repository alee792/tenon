package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/frontmatter"
	"github.com/alee792/tenon/internal/pluginref"
)

const pluginUsage = `usage:
  tenon plugin fetch AGENT [NAME] [--manifest PATH]
  tenon plugin update AGENT NAME --rev REV [--manifest PATH]
  tenon plugin status AGENT [NAME] [--manifest PATH]
`

// resolvePluginCacheBase resolves the operator's plugin-reference cache base
// directory (ADR 0026's per-OS-user default, a sibling of the integration
// store). Every command that loads a project configures the same cache via
// agentproject.ConfigurePluginCache once at startup (main), so a
// plugins/<name>.md reference resolves identically no matter which command
// asks — exactly like resolveIntegrationStoreBase for connections.
func resolvePluginCacheBase() string {
	base, err := pluginref.DefaultBase()
	if err != nil {
		return ""
	}
	return base
}

// configurePluginCache installs the plugin-reference cache every subsequent
// agentproject.Load call in this process consults. Called once from main.
func configurePluginCache() {
	base := resolvePluginCacheBase()
	if base == "" {
		return
	}
	agentproject.ConfigurePluginCache(pluginref.NewCache(base))
}

// runPlugin is the operator CLI for plugin reference files (ADR 0026 "plugin
// acquisition by pointer and pin"). fetch and update are the only commands
// here that touch the network — both shell out to the system git executable
// via internal/pluginref — and status is fully offline.
func runPlugin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "tenon plugin: a subcommand is required (fetch, update, status)\n%s", pluginUsage)
		return 2
	}
	switch args[0] {
	case "fetch":
		return runPluginFetch(args[1:], stdout, stderr)
	case "update":
		return runPluginUpdate(args[1:], stdout, stderr)
	case "status":
		return runPluginStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tenon plugin: unknown subcommand %q\n%s", args[0], pluginUsage)
		return 2
	}
}

// runPluginFetch resolves every plugins/<name>.md reference in AGENT (or
// just NAME, when given) into the plugin cache, independently per reference:
// one failure never stops the others from being attempted and reported. It
// is the one online step ADR 0026 keeps entirely separate from apply.
func runPluginFetch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "optional supplied agent manifest proving an instructions-free root")
	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) < 1 || len(positional) > 2 {
		fmt.Fprintf(stderr, "tenon plugin fetch: usage: tenon plugin fetch AGENT [NAME] [--manifest PATH]\n")
		return 2
	}
	agent := positional[0]
	filterName := ""
	if len(positional) == 2 {
		filterName = positional[1]
	}

	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon plugin fetch:", err)
		return 1
	}
	root, ok := proveAgentRoot(agent, "plugin fetch", expectedFingerprint(supplied), stderr)
	if !ok {
		return 1
	}

	refs, diags, err := agentproject.LoadPluginReferencesForStatus(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon plugin fetch:", err)
		return 1
	}

	base := resolvePluginCacheBase()
	if base == "" {
		fmt.Fprintln(stderr, "tenon plugin fetch: the plugin reference cache location could not be resolved")
		return 1
	}
	cache := pluginref.NewCache(base)

	found := false
	failed := false
	for _, ref := range refs {
		if filterName != "" && ref.Name != filterName {
			continue
		}
		found = true
		result, err := cache.Fetch(ref.Source, ref.Rev)
		if err != nil {
			failed = true
			fmt.Fprintf(stderr, "tenon plugin fetch: %s: %s\n", ref.Name, diagnostics.Bound(err.Error(), 512))
			continue
		}
		state := "fetched"
		if result.Cached {
			state = "already-cached"
		}
		fmt.Fprintf(stdout, "%s: rev %s digest %s (%s)\n", ref.Name, ref.Rev, result.Digest, state)
	}
	if filterName != "" && !found {
		if reported := reportPluginReferenceDiagnostics(root, filterName, diags, stderr); reported {
			return 1
		}
		fmt.Fprintf(stderr, "tenon plugin fetch: no plugin reference named %q was found\n", filterName)
		return 1
	}
	if diags.HasErrors() {
		_ = diags.WriteProse(stderr)
		failed = true
	}
	if failed {
		return 1
	}
	if !found {
		fmt.Fprintln(stdout, "no plugin references declared")
	}
	return 0
}

// runPluginUpdate fetches --rev, prints a bounded summary diff against the
// currently pinned rev's cached tree, and only on success rewrites the
// reference file's rev atomically. Printing the diff before rewriting is the
// review-at-update ADR 0026 requires; there is no interactive prompt.
func runPluginUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rev := fs.String("rev", "", "the new full 40-character git commit SHA to pin")
	manifestPath := fs.String("manifest", "", "optional supplied agent manifest proving an instructions-free root")
	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 2 {
		fmt.Fprintf(stderr, "tenon plugin update: usage: tenon plugin update AGENT NAME --rev REV [--manifest PATH]\n")
		return 2
	}
	agent, name := positional[0], positional[1]
	if !pluginref.RevPattern.MatchString(*rev) {
		fmt.Fprintln(stderr, "tenon plugin update: --rev must be a full 40-character lowercase hexadecimal git commit SHA")
		return 2
	}

	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon plugin update:", err)
		return 1
	}
	root, ok := proveAgentRoot(agent, "plugin update", expectedFingerprint(supplied), stderr)
	if !ok {
		return 1
	}

	refs, diags, err := agentproject.LoadPluginReferencesForStatus(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon plugin update:", err)
		return 1
	}
	var target *agentproject.PluginReferenceInfo
	for i := range refs {
		if refs[i].Name == name {
			target = &refs[i]
			break
		}
	}
	if target == nil {
		if reported := reportPluginReferenceDiagnostics(root, name, diags, stderr); reported {
			return 1
		}
		fmt.Fprintf(stderr, "tenon plugin update: no plugin reference named %q was found; there is no update for a vendored plugins/%s/ directory\n", name, name)
		return 1
	}

	base := resolvePluginCacheBase()
	if base == "" {
		fmt.Fprintln(stderr, "tenon plugin update: the plugin reference cache location could not be resolved")
		return 1
	}
	cache := pluginref.NewCache(base)

	oldRev := target.Rev
	result, err := cache.Fetch(target.Source, *rev)
	if err != nil {
		fmt.Fprintln(stderr, "tenon plugin update:", diagnostics.Bound(err.Error(), 512))
		return 1
	}
	fmt.Fprintf(stdout, "fetched: %s rev %s digest %s\n", name, *rev, result.Digest)

	if oldRev != *rev {
		if _, _, verifyErr := cache.Verify(oldRev); verifyErr != nil {
			// The pinned old rev was never fetched (or no longer verifies) —
			// most commonly because it was pinned by someone else's fetch,
			// or the cache was pruned. The new rev above already fetched and
			// verified cleanly, so there is no reason to dead-end here: skip
			// the diff and proceed with the rewrite below.
			fmt.Fprintf(stdout, "diff unavailable: currently pinned rev %s is not cached\n", oldRev)
		} else {
			added, removed, changed, err := cache.Diff(oldRev, *rev)
			if err != nil {
				fmt.Fprintln(stderr, "tenon plugin update: the diff against the currently pinned rev could not be computed:", diagnostics.Bound(err.Error(), 512))
				return 1
			}
			fmt.Fprintf(stdout, "diff %s -> %s (component paths only):\n", oldRev, *rev)
			for _, p := range added {
				fmt.Fprintf(stdout, "  + %s\n", p)
			}
			for _, p := range removed {
				fmt.Fprintf(stdout, "  - %s\n", p)
			}
			for _, p := range changed {
				fmt.Fprintf(stdout, "  ~ %s\n", p)
			}
			if len(added)+len(removed)+len(changed) == 0 {
				fmt.Fprintln(stdout, "  (no component paths differ)")
			}
		}
	} else {
		fmt.Fprintln(stdout, "the requested rev is already pinned; nothing to diff")
	}

	if err := rewritePluginReferenceRev(root, target.SourcePath, *rev); err != nil {
		fmt.Fprintln(stderr, "tenon plugin update:", err)
		return 1
	}
	fmt.Fprintf(stdout, "updated: %s now pins rev %s\n", target.SourcePath, *rev)
	fmt.Fprintln(stdout, "run tenon apply for each intended workspace")
	return 0
}

// runPluginStatus reports every plugin reference's declared source and pin,
// plus its offline resolution health against the cache, entirely offline.
func runPluginStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "optional supplied agent manifest proving an instructions-free root")
	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) < 1 || len(positional) > 2 {
		fmt.Fprintf(stderr, "tenon plugin status: usage: tenon plugin status AGENT [NAME] [--manifest PATH]\n")
		return 2
	}
	agent := positional[0]
	filterName := ""
	if len(positional) == 2 {
		filterName = positional[1]
	}

	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon plugin status:", err)
		return 1
	}
	if _, ok := proveAgentRoot(agent, "plugin status", expectedFingerprint(supplied), stderr); !ok {
		return 1
	}

	refs, diags, err := agentproject.LoadPluginReferencesForStatus(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon plugin status:", err)
		return 1
	}

	base := resolvePluginCacheBase()
	var cache *pluginref.Cache
	if base != "" {
		cache = pluginref.NewCache(base)
	}

	found := false
	failed := false
	for _, ref := range refs {
		if filterName != "" && ref.Name != filterName {
			continue
		}
		found = true
		if cache == nil {
			fmt.Fprintf(stdout, "%s: source %s rev %s: no plugin cache configured\n", ref.Name, ref.Source, ref.Rev)
			failed = true
			continue
		}
		_, digest, err := cache.Verify(ref.Rev)
		if err != nil {
			fmt.Fprintf(stdout, "%s: source %s rev %s: unresolved: %s\n", ref.Name, ref.Source, ref.Rev, diagnostics.Bound(err.Error(), 256))
			failed = true
			continue
		}
		// Verify checks the tree's digest, not its provenance: a cache
		// entry's recorded source is compared here, explicitly, against
		// the declared reference — redundantly with Cache.Resolve's own
		// source check, which also runs at Load time.
		if state, stateErr := cache.State(ref.Rev); stateErr == nil && state != nil && state.Source != ref.Source {
			fmt.Fprintf(stdout, "%s: source %s rev %s: unresolved: the cached tree for this rev was fetched from a different source (%s); re-run tenon plugin fetch\n",
				ref.Name, ref.Source, ref.Rev, diagnostics.Bound(state.Source, 256))
			failed = true
			continue
		}
		fmt.Fprintf(stdout, "%s: source %s rev %s digest %s: resolved\n", ref.Name, ref.Source, ref.Rev, digest)
	}
	if filterName != "" && !found {
		fmt.Fprintf(stderr, "tenon plugin status: no plugin reference named %q was found\n", filterName)
		return 1
	}
	if !found && filterName == "" {
		fmt.Fprintln(stdout, "no plugin references declared")
	}
	for _, d := range diags.All() {
		fmt.Fprintln(stderr, d.String())
		if d.Severity == diagnostics.Error {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// reportPluginReferenceDiagnostics checks whether plugins/<name>.md exists
// on disk before any "no plugin reference ... was found" headline is
// printed. A malformed-but-present reference file must lead with its own
// parse diagnostic (LoadPluginReferencesForStatus records one whenever
// parsing fails, keyed by that exact source path) rather than be reported
// as though no reference by that name exists at all — those are different
// failures an operator needs to tell apart. It returns whether it printed
// anything, so the caller skips its own not-found line in that case.
func reportPluginReferenceDiagnostics(root, name string, diags *diagnostics.List, stderr io.Writer) bool {
	if root == "" {
		return false
	}
	full := filepath.Join(root, "plugins", name+".md")
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	sourcePath := "plugins/" + name + ".md"
	printed := false
	for _, d := range diags.All() {
		if d.Path == sourcePath {
			fmt.Fprintln(stderr, d.String())
			printed = true
		}
	}
	if !printed {
		fmt.Fprintf(stderr, "tenon plugin: %s exists but could not be parsed as a plugin reference\n", sourcePath)
	}
	return true
}

// rewritePluginReferenceRev rewrites only the "rev" field of an already-
// validated plugin reference file, atomically, preserving every other byte
// (the source line, the body) exactly. It is called only after a successful
// fetch and diff print, never before (ADR 0026's review-at-update).
func rewritePluginReferenceRev(root, sourcePath, newRev string) error {
	full := filepath.Join(root, filepath.FromSlash(sourcePath))
	raw, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("the reference file could not be read: %w", err)
	}
	rewritten, err := replaceFrontmatterRev(raw, newRev)
	if err != nil {
		return err
	}
	return writeFileAtomic(full, rewritten)
}

// revLinePattern matches the first top-level "rev:" frontmatter line, so
// replaceFrontmatterRev can rewrite exactly its value and nothing else. The
// value class is [^\r\n]*, not the "."-based ".*$" this used to read: "."
// excludes \n but not \r, so on a CRLF-authored file ".*$" would consume the
// line's trailing \r along with the value, and the rewrite would silently
// drop it (turning CRLF into a bare LF on every rewritten line-ending
// style). Excluding \r explicitly from the value class preserves it.
var revLinePattern = regexp.MustCompile(`(?m)^rev:[ \t]*[^\r\n]*`)

// replaceFrontmatterRev rewrites raw's "rev" frontmatter field to newRev,
// preserving every other byte — the source field, key order, comments the
// frontmatter parser ignores, and the body — exactly.
func replaceFrontmatterRev(raw []byte, newRev string) ([]byte, error) {
	_, bodyStart, err := frontmatter.Split(raw)
	if err != nil {
		return nil, fmt.Errorf("the reference file frontmatter could not be parsed: %w", err)
	}
	head := raw[:bodyStart]
	tail := raw[bodyStart:]
	loc := revLinePattern.FindIndex(head)
	if loc == nil {
		return nil, fmt.Errorf("the reference file frontmatter carries no rev field to rewrite")
	}
	out := make([]byte, 0, len(raw)+len(newRev))
	out = append(out, head[:loc[0]]...)
	out = append(out, []byte("rev: "+newRev)...)
	out = append(out, head[loc[1]:]...)
	out = append(out, tail...)
	return out, nil
}
