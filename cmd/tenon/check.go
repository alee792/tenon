package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/version"
)

// checkResult is the jsonl-mode result summary for a passing check. Agent and
// Fingerprint keep the exact key names the summary has always carried, so
// an existing consumer still parses it; Outcome is additive.
type checkResult struct {
	Outcome     string `json:"outcome"`
	Agent       string `json:"agent"`
	Fingerprint string `json:"fingerprint"`
}

// gateFailedResult terminates the jsonl stream when the gate rejects the
// project, so a consumer reading objects until end of stream never has to
// infer failure from the absence of a summary.
type gateFailedResult struct {
	Outcome string `json:"outcome"`
}

// Catalog entries are the resolved capability inventory, one object per
// entry. Each kind carries only the fields that kind has; tool schemas live
// in file contents and are not extracted here.
type (
	catalogSkill struct {
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}
	catalogTool struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Language string `json:"language"`
		Source   string `json:"source"`
	}
	catalogMCP struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Transport string `json:"transport"`
		Source    string `json:"source"`
	}
	catalogSubagent struct {
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Effort      string `json:"effort,omitempty"`
	}
	catalogSchedule struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Cron   string `json:"cron"`
		Source string `json:"source"`
	}
)

// runCheck is the single gate over an agent project. Without --harness it is
// the portable gate: load, prepare tools, report. With --harness it is
// additionally everything apply would do short of writing — a supplied
// manifest verified before any generation, then a generation dry-run against
// the same Target apply builds — so check and apply fail identically on the
// same source. --emit adds the inventories the gate already resolved: the
// per-file fingerprint contributions and the capability catalog, in that
// order whatever order the flag names them, and only once the gate passes.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness: claude or codex (omit for the portable gate)")
	mode := fs.String("diagnostics", "prose", "diagnostic rendering: prose or jsonl")
	manifestPath := fs.String("manifest", "", "optional supplied agent manifest to verify; requires --harness")
	emit := fs.String("emit", "", "comma-separated inventories to emit on success: files, catalog")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon check: exactly one AGENT directory is required\n%s", usage)
		return 2
	}
	agent := positional[0]

	var driver apply.Driver
	switch *harnessName {
	case "":
	case "claude":
		driver = claude.Driver{}
	case "codex":
		driver = codex.Driver{}
	default:
		fmt.Fprintln(stderr, "tenon check: --harness must be exactly claude or codex")
		return 2
	}
	jsonl := false
	switch *mode {
	case "prose":
	case "jsonl":
		jsonl = true
	default:
		fmt.Fprintln(stderr, "tenon check: --diagnostics must be prose or jsonl")
		return 2
	}
	// Manifest verification is harness-specific: the closure it pins is the
	// one generation for a named harness resolves, so there is nothing to
	// verify against without one.
	if *manifestPath != "" && driver == nil {
		fmt.Fprintln(stderr, "tenon check: --manifest requires --harness")
		return 2
	}
	emitFiles, emitCatalog := false, false
	for _, name := range strings.Split(*emit, ",") {
		switch strings.TrimSpace(name) {
		case "":
		case "files":
			emitFiles = true
		case "catalog":
			emitCatalog = true
		default:
			fmt.Fprintf(stderr, "tenon check: --emit values must be files or catalog; found %q\n", name)
			return 2
		}
	}

	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon check:", err)
		return 1
	}
	p, diags, err := agentproject.LoadWithManifest(agent, expectedFingerprint(supplied))
	if err != nil {
		fmt.Fprintln(stderr, "tenon check:", err)
		return 1
	}
	// A supplied manifest reports its closure drift before any generation, so
	// check and apply fail identically.
	if p != nil && !diags.HasErrors() && supplied != nil {
		if err := verifyManifestDiag(p, driver.Harness(), resolveIntegrationStoreBase(), supplied, diags); err != nil {
			fmt.Fprintln(stderr, "tenon check:", err)
			return 1
		}
	}
	if p != nil && !diags.HasErrors() {
		workspace, err := filepath.Abs(agent)
		if err != nil {
			fmt.Fprintln(stderr, "tenon check:", err)
			return 1
		}
		// Tool preparation is the same work apply does, in the same order,
		// against a throwaway cache that is deleted afterwards: check reports
		// apply's tool failures while writing nothing to the workspace.
		cache := ""
		if len(p.Tools) > 0 {
			cache, err = os.MkdirTemp("", "tenon-tools-")
			if err != nil {
				fmt.Fprintln(stderr, "tenon check:", err)
				return 1
			}
			defer os.RemoveAll(cache)
		}
		prepared := prepareTools(p, workspace, cache, diags)
		if prepared && driver != nil {
			// Check resolves exactly what apply would — the same executable
			// and apply's default workspace — so generation and its warnings
			// are identical; the files themselves are discarded.
			executable, err := resolveExecutable()
			if err != nil {
				fmt.Fprintln(stderr, "tenon check:", err)
				return 1
			}
			_ = driver.Generate(p, apply.Target{
				Workspace:        workspace,
				Executable:       executable,
				IntegrationStore: resolveIntegrationStoreBase(),
				TenonVersion:     version.Version,
				Model:            manifestModel(supplied, driver.Harness()),
			}, diags)
		}
	}
	render(diags, jsonl, stdout, stderr)
	if p == nil || diags.HasErrors() {
		if jsonl {
			if err := writeResult(stdout, gateFailedResult{Outcome: "gate_failed"}); err != nil {
				fmt.Fprintln(stderr, "tenon check:", err)
			}
		}
		return 1
	}

	if emitFiles {
		if err := emitFingerprintFiles(p, jsonl, stdout); err != nil {
			fmt.Fprintln(stderr, "tenon check:", err)
			return 1
		}
	}
	if emitCatalog {
		if err := emitCapabilityCatalog(p, jsonl, stdout); err != nil {
			fmt.Fprintln(stderr, "tenon check:", err)
			return 1
		}
	}

	if jsonl {
		if err := writeResult(stdout, checkResult{Outcome: "ok", Agent: p.Name, Fingerprint: p.Fingerprint}); err != nil {
			fmt.Fprintln(stderr, "tenon check:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "ok: agent %s (fingerprint %s)\n", p.Name, p.Fingerprint)
	return 0
}

// emitFingerprintFiles renders every authored file feeding the fingerprint —
// its path, its own content hash, and its executable bit — sorted the same
// way the rollup sorts them. It never recomputes a hash: Load already built
// the per-file list, and this only renders what Load returned.
func emitFingerprintFiles(p *agentproject.Project, jsonl bool, stdout io.Writer) error {
	if jsonl {
		for _, e := range p.FingerprintEntries {
			if err := writeResult(stdout, e); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range p.FingerprintEntries {
		bit := "-"
		if e.Executable {
			bit = "x"
		}
		if _, err := fmt.Fprintf(stdout, "%s %s %s\n", e.Path, e.Hash, bit); err != nil {
			return err
		}
	}
	return nil
}

// emitCapabilityCatalog renders the resolved capability inventory in one
// fixed order — skills, tools, MCP servers (authored connections first, then
// the plugin servers they may shadow), subagents, schedules — so a consumer
// diffing two catalogs sees authored changes, not ordering noise. Every entry
// is what Load already resolved: plugin-merged skills are indistinguishable
// from root skills except by their source path, exactly as generation sees
// them.
func emitCapabilityCatalog(p *agentproject.Project, jsonl bool, stdout io.Writer) error {
	emit := func(v any, prose string) error {
		if jsonl {
			return writeResult(stdout, v)
		}
		_, err := fmt.Fprintln(stdout, prose)
		return err
	}
	for _, s := range p.Skills {
		if err := emit(
			catalogSkill{Kind: "skill", Name: s.Name, Description: s.Description, Source: s.SourcePath},
			fmt.Sprintf("skill %s (%s): %s", s.Name, s.SourcePath, s.Description),
		); err != nil {
			return err
		}
	}
	for _, t := range p.Tools {
		if err := emit(
			catalogTool{Kind: "tool", Name: t.Name, Language: t.Language, Source: t.SourcePath},
			fmt.Sprintf("tool %s (%s, %s)", t.Name, t.Language, t.SourcePath),
		); err != nil {
			return err
		}
	}
	for _, c := range p.Connections {
		if err := emit(
			catalogMCP{Kind: "mcp", Name: c.Name, Transport: c.Kind, Source: c.SourcePath},
			fmt.Sprintf("mcp %s (%s, %s)", c.Name, c.Kind, c.SourcePath),
		); err != nil {
			return err
		}
	}
	for _, s := range p.PluginServers {
		if err := emit(
			catalogMCP{Kind: "mcp", Name: s.Name, Transport: s.Transport, Source: s.SourcePath},
			fmt.Sprintf("mcp %s (%s, %s)", s.Name, s.Transport, s.SourcePath),
		); err != nil {
			return err
		}
	}
	for _, s := range p.Subagents {
		line := fmt.Sprintf("subagent %s: %s", s.Name, s.Description)
		if s.Effort != "" {
			line = fmt.Sprintf("subagent %s (effort %s): %s", s.Name, s.Effort, s.Description)
		}
		if err := emit(
			catalogSubagent{Kind: "subagent", Name: s.Name, Description: s.Description, Effort: s.Effort},
			line,
		); err != nil {
			return err
		}
	}
	for _, s := range p.Schedules {
		if err := emit(
			catalogSchedule{Kind: "schedule", Name: s.Name, Cron: s.Cron, Source: s.SourcePath},
			fmt.Sprintf("schedule %s (cron %q, %s)", s.Name, s.Cron, s.SourcePath),
		); err != nil {
			return err
		}
	}
	return nil
}
