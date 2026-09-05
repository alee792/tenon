package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/manifest"
	"github.com/alee792/tenon/internal/version"
)

// checkResult is the jsonl-mode result summary for a passing check. Agent and
// Fingerprint keep the exact key names the summary has always carried, so
// an existing consumer still parses it; Outcome is additive.
type checkResult struct {
	Outcome     string `json:"outcome"`
	Agent       string `json:"agent"`
	Fingerprint string `json:"fingerprint"`
	// PinsWritten is the path --write-pins wrote the resolved pin set to,
	// omitted entirely when the gate wrote no pins.
	PinsWritten string `json:"pins_written,omitempty"`
}

// gateFailedResult terminates the jsonl stream when the gate rejects the
// project, so a consumer reading objects until end of stream never has to
// infer failure from the absence of a summary.
type gateFailedResult struct {
	Outcome string `json:"outcome"`
	// SourceDigest is a content hash over the authored files that failed, so
	// a rejected candidate is attributable without the consumer hashing the
	// tree itself. It is not a fingerprint and never joins with one: a
	// digest names bytes, a fingerprint names a proven configuration.
	// Omitted only when the agent root itself could not be read.
	SourceDigest string `json:"source_digest,omitempty"`
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
		Kind string `json:"kind"`
		Name string `json:"name"`
		// Transport is the wire transport the server speaks, in ONE
		// vocabulary across both entry sources: "stdio" for a locally
		// spawned process, "streamable-http" for a remote HTTPS endpoint,
		// and "installed" for a server tenon relays through an installed
		// integration package's shim. An authored connection's kind is
		// mapped onto it (remote → streamable-http; stdio and installed
		// pass through) and a plugin-declared server's transport already
		// speaks it, so a consumer never has to know which of the two
		// authored an entry to read this field. Source carries that
		// distinction, as it does for every other catalog kind.
		Transport string `json:"transport"`
		Source    string `json:"source"`
	}
	catalogSubagent struct {
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Effort      string `json:"effort,omitempty"`
	}
)

// runCheck is the single gate over an agent project. Without --harness it is
// the portable gate: load, prepare tools, report. With --harness it is
// additionally everything apply would do short of writing — a supplied pin
// set verified before any generation, then a generation dry-run against
// the same Target apply builds — so check and apply fail identically on the
// same source. --emit adds the inventories the gate already resolved: the
// per-file fingerprint contributions and the capability catalog, in that
// order whatever order the flag names them, and only once the gate passes.
// --write-pins resolves and writes the current closure once the gate has
// passed, so the pin set a later run verifies is only ever minted by a
// project that passes the gate now.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness: claude or codex; omit (with TENON_HARNESS unset) for the portable gate")
	mode := fs.String("format", "prose", "output rendering: prose or jsonl")
	manifestPath := fs.String("pins", "", "supplied pin set to verify against the current runtime closure; fails closed naming the first drifted pin")
	writePins := fs.String("write-pins", "", "write the resolved pin set for the current closure to FILE once the gate passes; requires --harness")
	model := fs.String("model", "", "optional model to record in the written pin set (advisory: operator-supplied, never resolved automatically, and never verified — the harness owns model selection)")
	emit := fs.String("emit", "", "comma-separated inventories to emit on success: files, catalog")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon check: exactly one AGENT directory is required\n%s", usage)
		return 2
	}
	agent := positional[0]

	// An empty flag defers to TENON_HARNESS; the flag always wins when set.
	// check's harness stays optional either way — empty flag and unset env
	// both mean the portable gate.
	driver, harnessValue, ok := resolveDriver("check", *harnessName, true, stderr)
	if !ok {
		return 2
	}
	jsonl, ok := parseFormat("check", *mode, stderr)
	if !ok {
		return 2
	}
	// Pin verification is harness-specific: the closure a pin set pins is the
	// one generation for a named harness resolves, so there is nothing to
	// verify against without one. Writing pins resolves that same closure, so
	// it carries the same requirement.
	if *manifestPath != "" && driver == nil {
		fmt.Fprintln(stderr, "tenon check: --pins requires --harness")
		return 2
	}
	if *writePins != "" && driver == nil {
		fmt.Fprintln(stderr, "tenon check: --write-pins requires --harness")
		return 2
	}
	// A model is only ever recorded into a pin set being written; there is
	// nothing for it to mean on a verify-only run.
	if *model != "" && *writePins == "" {
		fmt.Fprintln(stderr, "tenon check: --model requires --write-pins")
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
		return failEnv(jsonl, stdout, stderr, "check", err)
	}
	// The gate is the one check, drift, and apply share: load, verify a
	// supplied pin set before any generation, prepare tools exactly as apply
	// prepares them (here against a throwaway cache, so check writes
	// nothing), and dry-run generation against the same Target apply builds.
	// Writing pins without a supplied pin set loads for write, which accepts
	// an instructions-free root: the gate mints the very pin set that later
	// proves that root. Everything downstream is identical either way.
	gate, code := runGate(gateInput{
		command:       "check",
		agent:         agent,
		driver:        driver,
		supplied:      supplied,
		loadForPins:   *writePins != "" && supplied == nil,
		jsonl:         jsonl,
		stdout:        stdout,
		stderr:        stderr,
		prepCacheTemp: true,
		generate:      true,
	})
	defer gate.cleanup()
	if code != 0 {
		return code
	}
	p, diags := gate.project, gate.diags
	render(diags, jsonl, stdout, stderr)

	// The gate has passed, so the closure this resolves is one a passing
	// project actually produces. The bytes for an unchanged closure are
	// byte-identical across runs (no timestamps), so a written pin set is an
	// ordinary versioned file. --model records the operator's advisory choice
	// for the selected harness; the gate never resolves a model itself.
	if *writePins != "" {
		// With --pins, the closure was already resolved and verified above;
		// reusing it is what makes the written pin set the closure that
		// passed rather than a second, unverified read of the environment.
		current := gate.resolved
		if current == nil {
			storeBase := resolveIntegrationStoreBase()
			current, err = manifest.Resolve(p, harnessValue, version.Version, manifestResolverFor(p, harnessValue, storeBase))
			if err != nil {
				return failEnv(jsonl, stdout, stderr, "check", err)
			}
		}
		if *model != "" {
			pins := current.Harnesses[harnessValue]
			pins.Model = *model
			current.Harnesses[harnessValue] = pins
		}
		if err := writeFileAtomic(*writePins, current.Bytes()); err != nil {
			return failEnv(jsonl, stdout, stderr, "check", err)
		}
		if !jsonl {
			fmt.Fprintf(stdout, "wrote pins for agent %s (%s) to %s\n", p.Name, harnessValue, *writePins)
		}
	}

	if emitFiles {
		if err := emitFingerprintFiles(p, jsonl, stdout); err != nil {
			return failEnv(jsonl, stdout, stderr, "check", err)
		}
	}
	if emitCatalog {
		if err := emitCapabilityCatalog(p, jsonl, stdout); err != nil {
			return failEnv(jsonl, stdout, stderr, "check", err)
		}
	}

	if jsonl {
		if err := writeResult(stdout, checkResult{Outcome: "ok", Agent: p.Name, Fingerprint: p.Fingerprint, PinsWritten: *writePins}); err != nil {
			return failEnv(jsonl, stdout, stderr, "check", err)
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
// the plugin servers they may shadow), subagents — so a consumer
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
		transport := connectionTransport(c.Kind)
		if err := emit(
			catalogMCP{Kind: "mcp", Name: c.Name, Transport: transport, Source: c.SourcePath},
			fmt.Sprintf("mcp %s (%s, %s)", c.Name, transport, c.SourcePath),
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
	return nil
}

// connectionTransport maps an authored connection's kind onto the catalog's
// single transport vocabulary. A connection kind names how tenon reaches the
// server; a plugin server's transport names what the server speaks. Those
// were two vocabularies in one field, so a consumer reading "remote" beside
// "streamable-http" could not tell whether it was looking at two transports
// or at one transport named twice. Only "remote" needs translating: it is
// the spec transport ADR 0026 makes tenon's remote default, so it is emitted
// under that transport's own name. stdio and installed already say exactly
// what they are.
func connectionTransport(kind string) string {
	if kind == agentproject.ConnectionKindRemote {
		return "streamable-http"
	}
	return kind
}
