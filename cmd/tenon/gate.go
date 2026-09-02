package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/manifest"
	"github.com/alee792/tenon/internal/version"
)

// gateInput describes one run of the gate every command that reads an agent
// project opens with: load the project, verify a supplied pin set against the
// current closure, prepare the authored tools, and — for a caller that wants
// it — generate against the same apply.Target apply builds. ADR 0027 makes
// "check and apply fail identically on the same source" a binding contract;
// runGate is what makes that structural rather than a property three copies
// of the sequence happen to share.
//
// The three callers differ only in where they prepare tools and whether the
// gate generates:
//
//   - check prepares against the source with a throwaway cache and generates
//     into the source too, discarding the files: apply's failures, none of
//     apply's writes.
//   - drift prepares the same way (its --workspace may not exist yet, and a
//     tool host cannot start in a directory that is not there) but generates
//     into the workspace it classifies.
//   - apply prepares against the real workspace and its persistent cache,
//     because the closure it builds is the one it is about to apply, and it
//     generates inside apply.ApplyWithTarget, which also writes.
type gateInput struct {
	// command is the name error prefixes and outcome objects carry.
	command string
	// agent is the AGENT directory as the operator typed it.
	agent string
	// driver is the target harness, nil for check's portable gate: without
	// a harness there is no closure to verify and nothing to generate.
	driver apply.Driver
	// supplied is the parsed --pins set, nil when none was supplied.
	supplied *manifest.Manifest
	// loadForPins loads a root that may have no instructions.md, for
	// check --write-pins without --pins: the gate mints the very pin set
	// that later proves that root.
	loadForPins bool
	jsonl       bool
	stdout      io.Writer
	stderr      io.Writer

	// prepWorkspace is the workspace tool preparation runs against; empty
	// means the agent source directory. prepCacheTemp asks for a throwaway
	// cache root instead of the workspace's own persistent one; the caller
	// removes it through gateResult.cleanup.
	prepWorkspace string
	prepCacheTemp bool

	// generate runs the generation pass once tools are prepared, against
	// genWorkspace (empty: the preparation workspace). It requires a driver;
	// a caller that generates for itself afterwards leaves it false.
	generate     bool
	genWorkspace string

	// beforeTools runs after verification and before anything is prepared,
	// for a caller whose own precondition must be decided before the gate
	// pays for tool preparation — drift's "--workspace is a regular file"
	// usage error is the only one. It returns the exit code to stop with,
	// or -1 to continue.
	beforeTools func() int
}

// gateResult is what a passing gate produced. It is also returned for a
// failing gate, so a caller can always defer cleanup on it, but only project
// and diags are meaningful then.
type gateResult struct {
	// project is the loaded project, nil when the load produced none.
	project *agentproject.Project
	// diags accumulates every diagnostic the gate raised. The gate renders
	// it itself only when it fails; a caller that passes renders it where
	// its own output needs it, so warnings and the caller's own diagnostics
	// still arrive in one stream.
	diags *diagnostics.List
	// resolved is the closure verification read, kept so check --write-pins
	// writes exactly what was verified instead of resolving the environment
	// a second time. Nil when no pins were supplied.
	resolved *manifest.Manifest
	// executable is the resolved tenon executable, empty for the portable
	// gate that never needs one.
	executable string
	// files is what generation produced, nil when the gate did not
	// generate. Unsorted, in driver order.
	files []apply.GeneratedFile
	// generated reports whether the generation pass actually ran: it is
	// skipped for a portable gate and for a caller that generates itself.
	generated bool
	// cachePath is the throwaway tool cache the gate created, removed by
	// cleanup.
	cachePath string
}

// cleanup removes the throwaway tool cache, if the gate made one. It is safe
// on a zero gateResult, so every caller can defer it immediately.
func (g gateResult) cleanup() {
	if g.cachePath != "" {
		os.RemoveAll(g.cachePath)
	}
}

// runGate runs the shared gate and returns its result and the exit code the
// caller should return: 0 to continue, anything else to stop with that code.
// Every failure is rendered and terminated here — the diagnostics, then the
// gate_failed outcome object carrying the source digest — so all three
// commands fail on the same source in the same bytes, by construction.
func runGate(in gateInput) (gateResult, int) {
	res := gateResult{}
	fail := func() int {
		render(res.diags, in.jsonl, in.stdout, in.stderr)
		writeGateFailed(in.jsonl, in.stdout, in.stderr, in.command, sourceDigest(in.agent, res.project))
		return 1
	}

	var err error
	if in.loadForPins {
		res.project, res.diags, err = agentproject.LoadForManifestWrite(in.agent)
	} else {
		res.project, res.diags, err = agentproject.LoadWithManifest(in.agent, expectedFingerprint(in.supplied))
	}
	if err != nil {
		return res, failEnv(in.jsonl, in.stdout, in.stderr, in.command, err)
	}
	if res.project == nil || res.diags.HasErrors() {
		return res, fail()
	}

	// A supplied pin set reports its closure drift before any generation and
	// before any workspace mutation, so check, drift, and apply all fail on
	// a drifted pin having written nothing.
	if in.supplied != nil {
		res.resolved, err = verifyManifestDiag(res.project, in.driver.Harness(), resolveIntegrationStoreBase(), in.supplied, res.diags)
		if err != nil {
			return res, failEnv(in.jsonl, in.stdout, in.stderr, in.command, err)
		}
		if res.diags.HasErrors() {
			return res, fail()
		}
	}

	if in.beforeTools != nil {
		if code := in.beforeTools(); code >= 0 {
			return res, code
		}
	}

	prepWorkspace := in.prepWorkspace
	if prepWorkspace == "" {
		prepWorkspace, err = filepath.Abs(in.agent)
		if err != nil {
			return res, failEnv(in.jsonl, in.stdout, in.stderr, in.command, err)
		}
	}
	if in.driver != nil {
		res.executable, err = resolveExecutable()
		if err != nil {
			return res, failEnv(in.jsonl, in.stdout, in.stderr, in.command, err)
		}
	}
	if in.prepCacheTemp && len(res.project.Tools) > 0 {
		res.cachePath, err = os.MkdirTemp("", "tenon-tools-")
		if err != nil {
			return res, failEnv(in.jsonl, in.stdout, in.stderr, in.command, err)
		}
	}
	// Tools are prepared and inspected before anything is generated or
	// written: a project whose tools cannot be built is not half-applied,
	// and the same failure is what check reports without writing at all.
	if !prepareTools(res.project, prepWorkspace, res.cachePath, res.diags) {
		return res, fail()
	}

	if in.generate && in.driver != nil {
		genWorkspace := in.genWorkspace
		if genWorkspace == "" {
			genWorkspace = prepWorkspace
		}
		// The Target is the one apply builds, down to a supplied pin set's
		// advisory model, so generation and its warnings are identical
		// wherever the files themselves end up.
		res.files = in.driver.Generate(res.project, apply.Target{
			Workspace:        genWorkspace,
			Executable:       res.executable,
			IntegrationStore: resolveIntegrationStoreBase(),
			TenonVersion:     version.Version,
			Model:            manifestModel(in.supplied, in.driver.Harness()),
		}, res.diags)
		res.generated = true
		if res.diags.HasErrors() {
			return res, fail()
		}
	}
	return res, 0
}

// parseFormat reads the --format flag every command that renders output
// shares. It is one switch in one place so the vocabulary, and the usage
// error naming it, cannot drift between commands.
func parseFormat(cmd, mode string, stderr io.Writer) (jsonl, ok bool) {
	switch mode {
	case "prose":
		return false, true
	case "jsonl":
		return true, true
	default:
		fmt.Fprintf(stderr, "tenon %s: --format must be prose or jsonl\n", cmd)
		return false, false
	}
}

// resolveDriver maps --harness (falling back to TENON_HARNESS) onto the
// driver that harness names. allowEmpty is check's portable gate: no harness
// named, no driver, and that is not an error. An invalid value is reported
// as coming from whichever of the two actually supplied it.
func resolveDriver(cmd, flagValue string, allowEmpty bool, stderr io.Writer) (driver apply.Driver, harnessValue string, ok bool) {
	harnessValue, fromEnv := resolveHarness(flagValue)
	switch harnessValue {
	case "":
		if allowEmpty {
			return nil, "", true
		}
	case "claude":
		return claude.Driver{}, harnessValue, true
	case "codex":
		return codex.Driver{}, harnessValue, true
	}
	fmt.Fprint(stderr, harnessFlagError(cmd, harnessValue, fromEnv))
	return nil, "", false
}
