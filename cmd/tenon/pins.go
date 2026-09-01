package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/integration"
	"github.com/alee792/tenon/internal/manifest"
	"github.com/alee792/tenon/internal/toolruntime"
	"github.com/alee792/tenon/internal/version"
)

// manifestResolverFor builds the production closure Resolver for p on
// harnessName against the operator's integration store base. It is a package
// variable so credential-free tests inject a fake closure — replacing it in a
// test seam — instead of running real harness and toolchain subprocesses. The
// real functions run `<harness> --version`, read the integration store, and
// query the tool toolchains. Because it is a shared package global swapped by
// the test seam, tests that install a fake resolver must not run with
// t.Parallel; production only ever reads it.
var manifestResolverFor = func(p *agentproject.Project, harnessName, storeBase string) manifest.Resolver {
	return manifest.Resolver{
		HarnessVersion:    func(h string) (string, error) { return resolveHarnessVersion(h) },
		ToolRuntimes:      func() (string, string, string, string, error) { return resolveToolRuntimes(p) },
		PackageIdentities: func(h string) ([]manifest.PackageIdentity, error) { return resolvePackageIdentities(p, storeBase) },
	}
}

// readSuppliedManifest reads and parses the supplied pin set at path,
// enforcing the same bound and real-regular-file rule tenon enforces on every
// authored input. An empty path returns (nil, nil): no pins were supplied.
func readSuppliedManifest(path string) (*manifest.Manifest, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("the pin set file could not be read: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the pin set must be a regular file; symlinks are never followed")
	}
	if info.Size() > manifest.MaxManifestBytes {
		return nil, fmt.Errorf("the pin set may contain at most %d bytes; found %d", manifest.MaxManifestBytes, info.Size())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the pin set file could not be read: %w", err)
	}
	return manifest.Parse(raw)
}

// verifyManifestDiag resolves the current closure and verifies the supplied
// pin set against it, recording drift as a diagnostic whose identifier is the
// stable typed-error code so check and apply report identical, machine-
// readable failures. A nil pin set is a no-op. The returned error is reserved
// for an unresolvable closure (an environment failure), never for drift.
func verifyManifestDiag(p *agentproject.Project, harnessName, storeBase string, supplied *manifest.Manifest, diags *diagnostics.List) error {
	if supplied == nil {
		return nil
	}
	current, err := manifest.Resolve(p, harnessName, version.Version, manifestResolverFor(p, harnessName, storeBase))
	if err != nil {
		return err
	}
	if err := manifest.Verify(supplied, current); err != nil {
		var me *manifest.Error
		if errors.As(err, &me) {
			diags.Errorf(me.Code, ".", "%s", me.Message)
		} else {
			diags.Errorf("pins.drift", ".", "%s", err.Error())
		}
	}
	return nil
}

// checkManifest resolves the current closure and verifies the supplied pin set
// against it, returning drift or an unresolvable closure as one error. It gates
// every tenon-owned process open (serve, run, trigger, each schedule
// occurrence): open nothing when it returns non-nil. A nil pin set is a no-op.
func checkManifest(p *agentproject.Project, harnessName, storeBase string, supplied *manifest.Manifest) error {
	if supplied == nil {
		return nil
	}
	current, err := manifest.Resolve(p, harnessName, version.Version, manifestResolverFor(p, harnessName, storeBase))
	if err != nil {
		return err
	}
	return manifest.Verify(supplied, current)
}

// expectedFingerprint returns the supplied pin set's expected source
// fingerprint, or "" when no pins are supplied. It proves an
// instructions-free root through agentproject.LoadWithManifest.
func expectedFingerprint(supplied *manifest.Manifest) string {
	if supplied == nil {
		return ""
	}
	return supplied.SourceFingerprint
}

// manifestIdentity returns the supplied pin set's provenance identity, or ""
// when no pins are supplied.
func manifestIdentity(supplied *manifest.Manifest) string {
	if supplied == nil {
		return ""
	}
	return supplied.Identity()
}

// manifestModel returns the supplied pin set's recorded model for
// harnessName, or "" when no pins were supplied or that harness records no
// model (ADR 0020). apply and check thread the result into
// apply.Target.Model identically, so their generation — and any resulting
// diagnostics — match.
func manifestModel(supplied *manifest.Manifest, harnessName string) string {
	if supplied == nil {
		return ""
	}
	return supplied.Harnesses[harnessName].Model
}

// --- production resolvers (real subprocesses / store reads) ---

// versionToken matches a bare semantic-version-like token, e.g. "2.1.240" or
// "0.144.1", so the harness and toolchain version parsers pull the version out
// of a noisy `--version` line.
var versionToken = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// resolveHarnessVersion runs `<harness> --version` and parses the version
// token, falling back to the raw trimmed output. Claude prints
// "2.1.240 (Claude Code)"; Codex prints "codex-cli 0.144.1".
func resolveHarnessVersion(harnessName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, harnessName, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running %s --version: %w", harnessName, err)
	}
	v := parseVersion(string(out))
	if v == "" {
		return "", fmt.Errorf("%s --version produced no recognizable version", harnessName)
	}
	return v, nil
}

// parseVersion returns the first version-like token in s, or the whole trimmed
// string when none is present.
func parseVersion(s string) string {
	s = strings.TrimSpace(s)
	if tok := versionToken.FindString(s); tok != "" {
		return tok
	}
	return s
}

// resolveToolRuntimes returns the Deno, uv, Go, and Python runtime pins for
// the languages the project's tools actually use; a language the project
// does not use is left empty. A used language whose toolchain cannot be
// queried is an error, so the closure never resolves against a runtime
// tenon could not read.
//
// python is the resolved Python version SPECIFICATION the project's own pin
// names (manifest.ToolRuntimes.Python's doc explains why it is not the
// exact installed interpreter identity): manifest resolution runs before
// tool preparation ever runs (a supplied manifest is verified before any
// workspace mutation), so it cannot read what `uv python install` will
// actually resolve to without a second, redundant network fetch of the
// pinned interpreter — `uv` does not cache that download across separate
// --install-dir targets, so resolving the real identity here would double
// the network cost of every apply and check. Once preparation has
// actually run, the staged artifact manifest carries the full identity
// (internal/stage.RuntimeInfo.Interpreters).
func resolveToolRuntimes(p *agentproject.Project) (deno, uv, goVer, python string, err error) {
	used := map[string]bool{}
	for _, t := range p.Tools {
		used[t.Language] = true
	}
	if used[toolruntime.TypeScript] {
		if deno, err = toolchainVersion("deno", "--version"); err != nil {
			return "", "", "", "", err
		}
	}
	if used[toolruntime.Python] {
		if uv, err = toolchainVersion("uv", "--version"); err != nil {
			return "", "", "", "", err
		}
		if python, err = toolruntime.ResolvePythonVersionSpec(p.Root); err != nil {
			return "", "", "", "", err
		}
	}
	if used[toolruntime.Go] {
		if goVer, err = toolchainVersion("go", "version"); err != nil {
			return "", "", "", "", err
		}
	}
	return deno, uv, goVer, python, nil
}

// toolchainVersion runs `<exe> <arg>` and parses a version token from its
// output.
func toolchainVersion(exe, arg string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, arg).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running %s %s: %w", exe, arg, err)
	}
	v := parseVersion(string(out))
	if v == "" {
		return "", fmt.Errorf("%s %s produced no recognizable version", exe, arg)
	}
	return v, nil
}

// resolvePackageIdentities returns the {id, manifest_sha256} identity of every
// integration package the project selects through an installed connection,
// sorted by id and deduplicated. It reads the integration store offline; a
// selected package that cannot be inspected is an error, so the closure never
// resolves with a package identity tenon could not read.
func resolvePackageIdentities(p *agentproject.Project, storeBase string) ([]manifest.PackageIdentity, error) {
	ids := map[string]bool{}
	for _, c := range p.Connections {
		if c.Kind == agentproject.ConnectionKindInstalled {
			ids[c.Package] = true
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if storeBase == "" {
		return nil, fmt.Errorf("the project selects installed packages but no integration store is configured")
	}
	store := integration.NewStore(storeBase)
	out := make([]manifest.PackageIdentity, 0, len(ids))
	for id := range ids {
		installed, err := store.Inspect(id)
		if err != nil {
			return nil, fmt.Errorf("inspecting installed package %q: %w", id, err)
		}
		out = append(out, manifest.PackageIdentity{ID: id, ManifestSHA256: installed.State.ManifestSHA256})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
