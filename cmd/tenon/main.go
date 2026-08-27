// Command tenon compiles filesystem-authored agent projects into native
// configuration for coding-agent harnesses.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/dispatch"
	"github.com/alee792/tenon/internal/dispatchstate"
	"github.com/alee792/tenon/internal/friction"
	"github.com/alee792/tenon/internal/harness"
	claudeharness "github.com/alee792/tenon/internal/harness/claude"
	codexharness "github.com/alee792/tenon/internal/harness/codex"
	"github.com/alee792/tenon/internal/integration"
	"github.com/alee792/tenon/internal/mcp"
	"github.com/alee792/tenon/internal/schedule"
	"github.com/alee792/tenon/internal/toolruntime"
	"github.com/alee792/tenon/internal/version"
)

// prepareBudget bounds one tool preparation: installing locked dependencies
// and building a Go host is slow, but it is not unbounded.
const prepareBudget = 5 * time.Minute

const usage = `usage:
  tenon apply AGENT --harness <claude|codex> [--workspace DIR] [--manifest PATH] [--diagnostics <prose|jsonl>] [--discard-local]
  tenon validate AGENT --harness <claude|codex> [--manifest PATH] [--diagnostics <prose|jsonl>]
  tenon drift AGENT --workspace DIR --harness <claude|codex> [--diagnostics <prose|jsonl>]
  tenon fingerprint show AGENT [--diagnostics <prose|jsonl>]
  tenon manifest write AGENT --harness <claude|codex> [--output PATH] [--manifest PATH] [--model VALUE]
  tenon mcp serve AGENT --harness <claude|codex> [--workspace DIR] [--manifest PATH]
  tenon run AGENT --workspace DIR --harness <claude|codex> [--conversation ID] [--input jsonl] [--manifest PATH] [--timeout DUR] [--turn-timeout DUR]
  tenon schedule trigger AGENT NAME --workspace DIR --harness <claude|codex> --input-id ID [--manifest PATH] [--turn-timeout DUR] [--timeout DUR]
  tenon schedule run AGENT --workspace DIR --harness <claude|codex> [--manifest PATH] [--turn-timeout DUR] [--max-active-turns N]
  tenon stage AGENT --harness <claude|codex> --output DIR
  tenon stage verify --artifact PATH [--prefix DIR]
  tenon connection add AGENT NAME --url HTTPS_URL [--context TEXT]
  tenon connection status AGENT [NAME]
  tenon connection remove AGENT NAME
  tenon integration install SOURCE --trust operator
  tenon integration inspect|verify|list|enable|disable|remove [ID]
  tenon integration update ID SOURCE --trust operator
  tenon version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "apply":
		return runApply(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "drift":
		return runDrift(args[1:], stdout, stderr)
	case "fingerprint":
		if len(args) < 2 || args[1] != "show" {
			fmt.Fprintf(stderr, "tenon fingerprint: the only subcommand is show\n%s", usage)
			return 2
		}
		return runFingerprintShow(args[2:], stdout, stderr)
	case "manifest":
		return runManifest(args[1:], stdout, stderr)
	case "mcp":
		if len(args) < 2 || args[1] != "serve" {
			fmt.Fprintf(stderr, "tenon mcp: the only subcommand is serve\n%s", usage)
			return 2
		}
		return runMCPServe(args[2:], stdin, stdout, stderr)
	case "run":
		return runRun(args[1:], stdin, stdout, stderr)
	case "schedule":
		return runSchedule(args[1:], stdout, stderr)
	case "stage":
		return runStage(args[1:], stdout, stderr)
	case "connection":
		return runConnection(args[1:], stdout, stderr)
	case "integration":
		return runIntegration(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tenon: unknown command %q\n%s", args[0], usage)
		return 2
	}
}

// runVersion reports the version stamped into this binary. An agent manifest
// pins that exact string, so this is the authoritative way to read what a
// given tenon will record and verify against.
func runVersion(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "tenon version: takes no arguments\n%s", usage)
		return 2
	}
	fmt.Fprintln(stdout, version.Version)
	return 0
}

// commonFlags parses the shared AGENT positional and flag set. It returns
// the agent path, the selected driver, and the diagnostics mode. When
// withDiscardLocal is true (apply only), it also accepts --discard-local and
// returns whether it was set; every other caller gets discardLocal=false
// unconditionally, since the flag is not registered on their FlagSet.
func commonFlags(name string, args []string, stderr io.Writer, withWorkspace, withDiscardLocal bool) (agent string, workspace string, driver apply.Driver, jsonl bool, manifestPath string, discardLocal bool, ok bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	harness := fs.String("harness", "", "target harness: claude or codex")
	mode := fs.String("diagnostics", "prose", "diagnostic rendering: prose or jsonl")
	manifest := fs.String("manifest", "", "optional supplied agent manifest to verify")
	var ws *string
	if withWorkspace {
		ws = fs.String("workspace", "", "workspace directory (defaults to the agent directory)")
	}
	var discard *bool
	if withDiscardLocal {
		discard = fs.Bool("discard-local", false, "overwrite tenon-owned files modified since the previous apply; hand-authored files are still refused")
	}

	// Accept the positional agent root before or after flags.
	positional := []string{}
	rest := args
	for len(rest) > 0 {
		if rest[0] != "" && rest[0][0] != '-' {
			positional = append(positional, rest[0])
			rest = rest[1:]
			continue
		}
		if err := fs.Parse(rest); err != nil {
			return "", "", nil, false, "", false, false
		}
		next := fs.Args()
		if len(next) == len(rest) {
			fmt.Fprintf(stderr, "tenon %s: unexpected argument %q\n", name, rest[0])
			return "", "", nil, false, "", false, false
		}
		rest = next
	}
	if len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon %s: exactly one AGENT directory is required\n%s", name, usage)
		return "", "", nil, false, "", false, false
	}
	agent = positional[0]

	switch *harness {
	case "claude":
		driver = claude.Driver{}
	case "codex":
		driver = codex.Driver{}
	default:
		fmt.Fprintf(stderr, "tenon %s: --harness must be exactly claude or codex\n", name)
		return "", "", nil, false, "", false, false
	}
	switch *mode {
	case "prose":
	case "jsonl":
		jsonl = true
	default:
		fmt.Fprintf(stderr, "tenon %s: --diagnostics must be prose or jsonl\n", name)
		return "", "", nil, false, "", false, false
	}
	workspace = agent
	if withWorkspace && *ws != "" {
		workspace = *ws
	}
	if withDiscardLocal {
		discardLocal = *discard
	}
	return agent, workspace, driver, jsonl, *manifest, discardLocal, true
}

func render(diags *diagnostics.List, jsonl bool, stdout, stderr io.Writer) {
	if jsonl {
		_ = diags.WriteJSONL(stdout)
		return
	}
	_ = diags.WriteProse(stderr)
}

// validateResult is the jsonl-mode result summary for a successful validate.
type validateResult struct {
	Agent       string `json:"agent"`
	Fingerprint string `json:"fingerprint"`
}

// applyResult is the jsonl-mode result summary for a successful apply. Field
// names follow apply.Record's existing json tags (snake_case). ManagedTools
// names only the tools exposed through tenon's managed MCP boundary — native
// harness tools are never included and always remain unmanaged, regardless
// of this list's contents.
type applyResult struct {
	Agent        string   `json:"agent"`
	Harness      string   `json:"harness"`
	Workspace    string   `json:"workspace"`
	Fingerprint  string   `json:"fingerprint"`
	Written      []string `json:"written"`
	Removed      []string `json:"removed"`
	ManagedTools []string `json:"managed_tools"`
}

// writeResult emits one jsonl-mode result summary as a single JSON object
// followed by a newline, matching WriteJSONL's per-line encoding. The
// caller must report a returned error rather than discard it: a broken
// pipe or write failure here means the promised result was never sent.
func writeResult(stdout io.Writer, v any) error {
	return json.NewEncoder(stdout).Encode(v)
}

// resolveExecutable returns the absolute, symlink-free path of the running
// tenon binary. Generated managed-server configuration launches tenon from
// it, so an unresolvable or non-regular executable is an environment failure
// rather than an authored contract violation.
func resolveExecutable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("the tenon executable could not be located: %w", err)
	}
	abs, err := filepath.Abs(self)
	if err != nil {
		return "", fmt.Errorf("the tenon executable path could not be resolved: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("the tenon executable path could not be resolved: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("the tenon executable must be a regular file: %s", resolved)
	}
	return resolved, nil
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	agent, _, driver, jsonl, manifestPath, _, ok := commonFlags("validate", args, stderr, false, false)
	if !ok {
		return 2
	}
	supplied, err := readSuppliedManifest(manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon validate:", err)
		return 1
	}
	p, diags, err := agentproject.LoadWithManifest(agent, expectedFingerprint(supplied))
	if err != nil {
		fmt.Fprintln(stderr, "tenon validate:", err)
		return 1
	}
	// When a manifest is supplied, validate reports the same closure drift apply
	// would, before any generation, so validate and apply fail identically.
	if p != nil && !diags.HasErrors() && supplied != nil {
		if err := verifyManifestDiag(p, driver.Harness(), resolveIntegrationStoreBase(), supplied, diags); err != nil {
			fmt.Fprintln(stderr, "tenon validate:", err)
			return 1
		}
	}
	if p != nil && !diags.HasErrors() {
		// Validate resolves exactly what apply would — the same executable
		// and apply's default workspace — so generation and its warnings are
		// identical; the files themselves are discarded.
		executable, err := resolveExecutable()
		if err != nil {
			fmt.Fprintln(stderr, "tenon validate:", err)
			return 1
		}
		workspace, err := filepath.Abs(agent)
		if err != nil {
			fmt.Fprintln(stderr, "tenon validate:", err)
			return 1
		}
		// Tool preparation is the same work apply does, in the same order,
		// against a throwaway cache that is deleted afterwards: validate
		// reports apply's tool failures while writing nothing to the
		// workspace.
		cache := ""
		if len(p.Tools) > 0 {
			cache, err = os.MkdirTemp("", "tenon-tools-")
			if err != nil {
				fmt.Fprintln(stderr, "tenon validate:", err)
				return 1
			}
			defer os.RemoveAll(cache)
		}
		if prepareTools(p, workspace, cache, diags) {
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
		return 1
	}
	if jsonl {
		if err := writeResult(stdout, validateResult{Agent: p.Name, Fingerprint: p.Fingerprint}); err != nil {
			fmt.Fprintln(stderr, "tenon validate:", err)
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "valid: agent %s (fingerprint %s)\n", p.Name, p.Fingerprint)
	}
	return 0
}

func runApply(args []string, stdout, stderr io.Writer) int {
	agent, workspace, driver, jsonl, manifestPath, discardLocal, ok := commonFlags("apply", args, stderr, true, true)
	if !ok {
		return 2
	}
	supplied, err := readSuppliedManifest(manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon apply:", err)
		return 1
	}
	p, diags, err := agentproject.LoadWithManifest(agent, expectedFingerprint(supplied))
	if err != nil {
		fmt.Fprintln(stderr, "tenon apply:", err)
		return 1
	}
	if p == nil || diags.HasErrors() {
		render(diags, jsonl, stdout, stderr)
		return 1
	}
	storeBase := resolveIntegrationStoreBase()
	// A supplied manifest is verified BEFORE any workspace mutation — before
	// tools are prepared and before generation — so drift writes nothing: no
	// .tenon, no generated files.
	if supplied != nil {
		if err := verifyManifestDiag(p, driver.Harness(), storeBase, supplied, diags); err != nil {
			fmt.Fprintln(stderr, "tenon apply:", err)
			return 1
		}
		if diags.HasErrors() {
			render(diags, jsonl, stdout, stderr)
			return 1
		}
	}
	executable, err := resolveExecutable()
	if err != nil {
		fmt.Fprintln(stderr, "tenon apply:", err)
		return 1
	}
	// Tools are prepared and inspected once, before anything in the
	// workspace is mutated: a project whose tools cannot be built is not
	// half-applied.
	if !prepareTools(p, workspace, "", diags) {
		render(diags, jsonl, stdout, stderr)
		return 1
	}

	result, applyDiags, err := apply.ApplyWithTarget(p, apply.Target{
		Workspace:        workspace,
		Executable:       executable,
		IntegrationStore: storeBase,
		TenonVersion:     version.Version,
		ManifestIdentity: manifestIdentity(supplied),
		Model:            manifestModel(supplied, driver.Harness()),
		DiscardLocal:     discardLocal,
	}, driver)
	for _, d := range applyDiags.All() {
		diags.Add(d)
	}
	render(diags, jsonl, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "tenon apply:", err)
		return 1
	}
	if result == nil || diags.HasErrors() {
		return 1
	}
	if jsonl {
		res := applyResult{
			Agent:        p.Name,
			Harness:      driver.Harness(),
			Workspace:    workspace,
			Fingerprint:  result.Fingerprint,
			Written:      result.Written,
			Removed:      result.Removed,
			ManagedTools: managedTools(p),
		}
		if err := writeResult(stdout, res); err != nil {
			fmt.Fprintln(stderr, "tenon apply:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "applied: agent %s for %s in %s (fingerprint %s)\n",
		p.Name, driver.Harness(), workspace, result.Fingerprint)
	for _, f := range result.Written {
		fmt.Fprintf(stdout, "  wrote %s\n", f)
	}
	for _, f := range result.Removed {
		fmt.Fprintf(stdout, "  removed %s\n", f)
	}
	fmt.Fprintf(stdout, "managed tools: %s via MCP; native harness tools remain unmanaged\n",
		strings.Join(managedTools(p), ", "))
	fmt.Fprintf(stdout, "start %s normally in %s\n", driver.Harness(), workspace)
	return 0
}

// fingerprintRollupJSON is the jsonl rendering of the final rolled-up
// fingerprint line.
type fingerprintRollupJSON struct {
	Fingerprint string `json:"fingerprint"`
}

// runFingerprintShow prints every authored file that feeds AGENT's
// fingerprint — its path, its own content hash, and its executable bit —
// sorted the same way the rollup sorts them, then the rolled-up fingerprint
// itself. It never recomputes a hash: agentproject.Load already built the
// per-file list, and this only renders what Load returned. Tool preparation
// runs first, exactly as validate and apply require it, so a project whose
// tools cannot be built never reports a fingerprint as though it were clean.
func runFingerprintShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fingerprint show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	diagMode := fs.String("diagnostics", "prose", "diagnostic rendering: prose or jsonl")
	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon fingerprint show: usage: tenon fingerprint show AGENT [--diagnostics <prose|jsonl>]\n")
		return 2
	}
	agent := positional[0]
	jsonl := false
	switch *diagMode {
	case "prose":
	case "jsonl":
		jsonl = true
	default:
		fmt.Fprintf(stderr, "tenon fingerprint show: --diagnostics must be prose or jsonl\n")
		return 2
	}

	p, diags, err := agentproject.Load(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon fingerprint show:", err)
		return 1
	}
	if p != nil && !diags.HasErrors() {
		workspace, err := filepath.Abs(agent)
		if err != nil {
			fmt.Fprintln(stderr, "tenon fingerprint show:", err)
			return 1
		}
		cache := ""
		if len(p.Tools) > 0 {
			cache, err = os.MkdirTemp("", "tenon-tools-")
			if err != nil {
				fmt.Fprintln(stderr, "tenon fingerprint show:", err)
				return 1
			}
			defer os.RemoveAll(cache)
		}
		prepareTools(p, workspace, cache, diags)
	}
	render(diags, jsonl, stdout, stderr)
	if p == nil || diags.HasErrors() {
		return 1
	}

	if jsonl {
		for _, e := range p.FingerprintEntries {
			if err := writeResult(stdout, e); err != nil {
				fmt.Fprintln(stderr, "tenon fingerprint show:", err)
				return 1
			}
		}
		if err := writeResult(stdout, fingerprintRollupJSON{Fingerprint: p.Fingerprint}); err != nil {
			fmt.Fprintln(stderr, "tenon fingerprint show:", err)
			return 1
		}
		return 0
	}

	for _, e := range p.FingerprintEntries {
		bit := "-"
		if e.Executable {
			bit = "x"
		}
		fmt.Fprintf(stdout, "%s %s %s\n", e.Path, e.Hash, bit)
	}
	fmt.Fprintf(stdout, "fingerprint: %s\n", p.Fingerprint)
	return 0
}

// managedTools names the tools the managed boundary will expose for the
// project: the built-ins first, then every authored tool.
func managedTools(p *agentproject.Project) []string {
	tools := []string{"echo"}
	if p.Instructions != nil && p.Instructions.FrictionNotes {
		tools = append(tools, "record-friction")
	}
	for _, tool := range p.Tools {
		tools = append(tools, tool.Name)
	}
	return tools
}

// toolConfig describes the project's tool runtime. cacheRoot is empty for the
// workspace cache apply writes and serving reads, and a throwaway directory
// for validate.
func toolConfig(p *agentproject.Project, workspace, cacheRoot string) (toolruntime.Config, error) {
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return toolruntime.Config{}, fmt.Errorf("resolving workspace: %w", err)
	}
	return toolruntime.Config{
		Source:      p.Root,
		Workspace:   ws,
		Fingerprint: p.Fingerprint,
		Tools:       p.Tools,
		CacheRoot:   cacheRoot,
	}, nil
}

// prepareTools prepares and inspects the project's authored tools, reporting
// every failure as a diagnostic. A project without tools prepares nothing, so
// apply and validate behave exactly as before for it.
func prepareTools(p *agentproject.Project, workspace, cacheRoot string, diags *diagnostics.List) bool {
	if len(p.Tools) == 0 {
		return true
	}
	cfg, err := toolConfig(p, workspace, cacheRoot)
	if err != nil {
		diags.Errorf("tool.prepare.failed", "tools", "%s", diagnostics.Bound(err.Error(), 512))
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), prepareBudget)
	defer cancel()
	if err := toolruntime.Prepare(ctx, cfg); err != nil {
		reportToolFailure(err, diags)
		return false
	}
	return true
}

// reportToolFailure renders one preparation or inspection failure as a stable
// diagnostic. The message names the language and the step; it never carries a
// toolchain's raw output.
func reportToolFailure(err error, diags *diagnostics.List) {
	var failure *toolruntime.Failure
	id := "tool.prepare.failed"
	if errors.As(err, &failure) && failure.Phase == "inspect" {
		id = "tool.inspect.failed"
	}
	diags.Errorf(id, "tools", "%s", diagnostics.Bound(err.Error(), 512))
}

// toolCaller binds the open tool runtime to the managed boundary, applying
// tenon's own per-call deadline: the boundary never waits on authored code
// indefinitely, and an overrun takes the language host down with it.
type toolCaller struct {
	runtime *toolruntime.Runtime
}

func (c toolCaller) Call(name string, arguments json.RawMessage) (json.RawMessage, error) {
	return c.runtime.Call(name, arguments, toolruntime.CallDeadline)
}

// runMCPServe serves the managed tool boundary on stdin/stdout with audit on
// stderr. It fails closed unless the workspace still carries exactly the
// generated setup this agent source applied: a harness starting a stale
// managed server would otherwise serve an agent nobody applied.
func runMCPServe(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	agent, workspace, driver, _, manifestPath, _, ok := commonFlags("mcp serve", args, stderr, true, false)
	if !ok {
		return 2
	}
	supplied, err := readSuppliedManifest(manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon mcp serve:", err)
		return 1
	}
	p, diags, err := agentproject.LoadWithManifest(agent, expectedFingerprint(supplied))
	if err != nil {
		fmt.Fprintln(stderr, "tenon mcp serve:", err)
		return 1
	}
	// Stdout carries the protocol stream, so diagnostics only ever render as
	// prose on stderr here regardless of the diagnostics mode. Warnings are
	// not repeated: apply already reported them for this exact fingerprint,
	// which Verify then proves is the one applied.
	if p == nil || diags.HasErrors() {
		_ = diags.WriteProse(stderr)
		fmt.Fprintln(stderr, "tenon mcp serve: the agent project is invalid; run tenon apply")
		return 1
	}
	if err := apply.Verify(p, workspace, driver.Harness()); err != nil {
		fmt.Fprintln(stderr, "tenon mcp serve:", err)
		return 1
	}
	// A supplied manifest gates this process open: on drift, open nothing.
	if err := checkManifest(p, driver.Harness(), resolveIntegrationStoreBase(), supplied); err != nil {
		fmt.Fprintln(stderr, "tenon mcp serve:", err)
		return 1
	}

	cfg := mcp.Config{
		Agent:             p.Name,
		SourceFingerprint: p.Fingerprint,
		FrictionNotes:     p.Instructions != nil && p.Instructions.FrictionNotes,
	}
	if cfg.FrictionNotes {
		cfg.Recorder = newFrictionRecorder(p, driver.Harness())
	}
	// One host per authored language starts once and stays alive for the
	// whole session. A project without tools opens no runtime at all.
	if len(p.Tools) > 0 {
		toolCfg, err := toolConfig(p, workspace, "")
		if err != nil {
			fmt.Fprintln(stderr, "tenon mcp serve:", err)
			return 1
		}
		rt, err := toolruntime.Open(toolCfg)
		if err != nil {
			fmt.Fprintln(stderr, "tenon mcp serve:", err)
			return 1
		}
		defer rt.Close()
		for _, d := range rt.Definitions() {
			cfg.Definitions = append(cfg.Definitions, mcp.Definition{
				Name:         d.Name,
				Description:  d.Description,
				InputSchema:  d.InputSchema,
				OutputSchema: d.OutputSchema,
			})
		}
		cfg.Tools = toolCaller{runtime: rt}
	}
	if err := mcp.Serve(context.Background(), stdin, stdout, stderr, cfg); err != nil {
		fmt.Fprintln(stderr, "tenon mcp serve:", err)
		return 1
	}
	return 0
}

// maxRunTimeout bounds the whole-process deadline a caller may request.
const maxRunTimeout = 30 * time.Minute

// newHarnessDriver resolves the headless driver for a harness: the real Claude
// Code and Codex protocol drivers, each launching its native executable behind
// the harness.Driver seam. Codex reports tenon's version to its app-server on
// initialize.
func newHarnessDriver(name string) (harness.Driver, error) {
	switch name {
	case "claude":
		return claudeharness.NewDriver("claude"), nil
	case "codex":
		return codexharness.NewDriver("codex", version.Version), nil
	default:
		return nil, fmt.Errorf("--harness must be exactly claude or codex")
	}
}

// runRun dispatches headless turns for one conversation: it reads bounded JSONL
// input on stdin and writes the ordered wire event stream to stdout. It bounds
// the whole process with --timeout and each task turn with --turn-timeout.
func runRun(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness: claude or codex")
	workspace := fs.String("workspace", "", "workspace directory (required)")
	conversation := fs.String("conversation", "", "conversation id (defaults to local)")
	input := fs.String("input", "jsonl", "input format: jsonl")
	timeout := fs.Duration("timeout", 2*time.Minute, "whole-process deadline")
	turnTimeout := fs.Duration("turn-timeout", 0, "per-turn deadline (task mode; 0 disables)")
	manifestPath := fs.String("manifest", "", "optional supplied agent manifest to verify")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon run: exactly one AGENT directory is required\n%s", usage)
		return 2
	}
	agent := positional[0]

	switch *harnessName {
	case "claude", "codex":
	default:
		fmt.Fprintln(stderr, "tenon run: --harness must be exactly claude or codex")
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "tenon run: --workspace is required")
		return 2
	}
	if *input != "jsonl" {
		fmt.Fprintln(stderr, "tenon run: --input must be jsonl")
		return 2
	}
	if *timeout <= 0 || *timeout > maxRunTimeout {
		fmt.Fprintf(stderr, "tenon run: --timeout must be greater than 0 and at most %s\n", maxRunTimeout)
		return 2
	}
	if *turnTimeout < 0 {
		fmt.Fprintln(stderr, "tenon run: --turn-timeout must not be negative")
		return 2
	}

	driver, err := newHarnessDriver(*harnessName)
	if err != nil {
		fmt.Fprintln(stderr, "tenon run:", err)
		return 1
	}

	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon run:", err)
		return 1
	}
	// Load the project and dispatch under the whole-process deadline.
	p, diags, err := agentproject.LoadWithManifest(agent, expectedFingerprint(supplied))
	if err != nil {
		fmt.Fprintln(stderr, "tenon run:", err)
		return 1
	}
	if p == nil || diags.HasErrors() {
		_ = diags.WriteProse(stderr)
		fmt.Fprintln(stderr, "tenon run: the agent project is invalid; run tenon apply")
		return 1
	}
	// A supplied manifest gates the process open: on drift, open nothing.
	if err := checkManifest(p, *harnessName, resolveIntegrationStoreBase(), supplied); err != nil {
		fmt.Fprintln(stderr, "tenon run:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := dispatch.Run(ctx, dispatch.Options{
		Project:      p,
		Driver:       driver,
		Workspace:    *workspace,
		Harness:      *harnessName,
		Conversation: *conversation,
		Mode:         dispatch.Interactive,
		In:           stdin,
		Out:          stdout,
		TurnTimeout:  *turnTimeout,
		Manifest:     manifestIdentity(supplied),
	}); err != nil {
		fmt.Fprintln(stderr, "tenon run:", err)
		return 1
	}
	return 0
}

// runSchedule dispatches the "tenon schedule" subcommands (ADR 0008, ADR 0011).
func runSchedule(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "tenon schedule: a subcommand is required (trigger, run)\n%s", usage)
		return 2
	}
	switch args[0] {
	case "trigger":
		return runScheduleTrigger(args[1:], stdout, stderr)
	case "run":
		return runScheduleRun(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tenon schedule: unknown subcommand %q\n%s", args[0], usage)
		return 2
	}
}

// loadScheduleProject loads and validates the agent project and finds the named
// schedule, or reports why it could not. Both schedule subcommands require a
// valid project; trigger additionally requires the schedule to exist.
func loadScheduleProject(agent, cmdName, expectedFingerprint string, stderr io.Writer) (*agentproject.Project, bool) {
	p, diags, err := agentproject.LoadWithManifest(agent, expectedFingerprint)
	if err != nil {
		fmt.Fprintf(stderr, "tenon %s: %v\n", cmdName, err)
		return nil, false
	}
	if p == nil || diags.HasErrors() {
		_ = diags.WriteProse(stderr)
		fmt.Fprintf(stderr, "tenon %s: the agent project is invalid; run tenon apply\n", cmdName)
		return nil, false
	}
	return p, true
}

// runScheduleTrigger dispatches one occurrence of a named schedule under a
// caller-owned stable occurrence id. It requires current generated setup, opens
// a fresh native session for a fresh occurrence, deduplicates a repeated id
// without opening a harness, and writes exactly one bounded lifecycle line that
// never contains model text. Any non-completed terminal status exits nonzero.
func runScheduleTrigger(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("schedule trigger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness: claude or codex")
	workspace := fs.String("workspace", "", "workspace directory (required)")
	inputID := fs.String("input-id", "", "caller-owned stable occurrence id (required)")
	turnTimeout := fs.Duration("turn-timeout", 90*time.Second, "per-turn deadline (0 disables)")
	timeout := fs.Duration("timeout", 2*time.Minute, "whole-process deadline")
	manifestPath := fs.String("manifest", "", "optional supplied agent manifest to verify")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 2 {
		fmt.Fprintf(stderr, "tenon schedule trigger: usage: tenon schedule trigger AGENT NAME --workspace DIR --harness <claude|codex> --input-id ID\n")
		return 2
	}
	agent, name := positional[0], positional[1]

	switch *harnessName {
	case "claude", "codex":
	default:
		fmt.Fprintln(stderr, "tenon schedule trigger: --harness must be exactly claude or codex")
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "tenon schedule trigger: --workspace is required")
		return 2
	}
	if *inputID == "" {
		fmt.Fprintln(stderr, "tenon schedule trigger: --input-id is required")
		return 2
	}
	if *turnTimeout < 0 {
		fmt.Fprintln(stderr, "tenon schedule trigger: --turn-timeout must not be negative")
		return 2
	}
	if *timeout <= 0 || *timeout > maxRunTimeout {
		fmt.Fprintf(stderr, "tenon schedule trigger: --timeout must be greater than 0 and at most %s\n", maxRunTimeout)
		return 2
	}

	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon schedule trigger:", err)
		return 1
	}
	p, ok := loadScheduleProject(agent, "schedule trigger", expectedFingerprint(supplied), stderr)
	if !ok {
		return 1
	}
	var target *agentproject.Schedule
	for i := range p.Schedules {
		if p.Schedules[i].Name == name {
			target = &p.Schedules[i]
			break
		}
	}
	if target == nil {
		fmt.Fprintf(stderr, "tenon schedule trigger: no schedule named %q in this agent\n", name)
		return 1
	}

	driver, err := newHarnessDriver(*harnessName)
	if err != nil {
		fmt.Fprintln(stderr, "tenon schedule trigger:", err)
		return 1
	}
	// Triggering requires the workspace to carry the applied setup: fail closed
	// on stale or missing generated setup rather than dispatch against drift.
	if err := apply.Verify(p, *workspace, *harnessName); err != nil {
		fmt.Fprintln(stderr, "tenon schedule trigger:", err)
		return 1
	}
	// Take the same exclusive lock the clock uses so a trigger never races a
	// running clock or a concurrent trigger for the same setup — both would
	// rewrite the single dispatch file under last-writer-wins. Fail closed if
	// held; the caller can retry.
	release, err := schedule.Lock(*workspace, p.Name, *harnessName)
	if err != nil {
		fmt.Fprintln(stderr, "tenon schedule trigger:", err)
		return 1
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	// A supplied manifest gates this process open before any harness
	// invocation, matching run and mcp serve: on drift, open nothing.
	if err := checkManifest(p, *harnessName, resolveIntegrationStoreBase(), supplied); err != nil {
		fmt.Fprintln(stderr, "tenon schedule trigger:", err)
		return 1
	}
	if err := driver.Verify(ctx); err != nil {
		fmt.Fprintf(stderr, "tenon schedule trigger: the %s harness could not be verified: %v\n", *harnessName, err)
		return 1
	}

	outcome, err := dispatch.RunTask(ctx, dispatch.Options{
		Project:      p,
		Driver:       driver,
		Workspace:    *workspace,
		Harness:      *harnessName,
		Conversation: schedule.ConversationID(name),
		Mode:         dispatch.Task,
		TurnTimeout:  *turnTimeout,
		Manifest:     manifestIdentity(supplied),
	}, *inputID, target.Prompt)
	if err != nil {
		fmt.Fprintln(stderr, "tenon schedule trigger:", err)
		return 1
	}

	line := fmt.Sprintf("schedule=%q input_id=%q status=%s duplicate=%t",
		name, *inputID, string(outcome.Status), outcome.Duplicate)
	if outcome.SessionID != "" {
		line += fmt.Sprintf(" session_id=%q", outcome.SessionID)
	}
	if outcome.Reason != "" {
		line += fmt.Sprintf(" reason=%q", outcome.Reason)
	}
	fmt.Fprintln(stdout, line)

	if outcome.Status != dispatchstate.Completed {
		return 1
	}
	return 0
}

// runScheduleRun runs the foreground UTC clock for an agent's schedules. It
// holds exclusive local ownership, requires current generated setup, and drains
// in-flight occurrences on a stop signal. Lifecycle output goes to stdout and
// never contains model text.
func runScheduleRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("schedule run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness: claude or codex")
	workspace := fs.String("workspace", "", "workspace directory (required)")
	turnTimeout := fs.Duration("turn-timeout", 90*time.Second, "per-turn deadline (0 disables)")
	maxActive := fs.Int("max-active-turns", schedule.DefaultMaxActive, "concurrent occurrences across distinct schedules")
	manifestPath := fs.String("manifest", "", "optional supplied agent manifest to verify")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon schedule run: exactly one AGENT directory is required\n%s", usage)
		return 2
	}
	agent := positional[0]

	switch *harnessName {
	case "claude", "codex":
	default:
		fmt.Fprintln(stderr, "tenon schedule run: --harness must be exactly claude or codex")
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "tenon schedule run: --workspace is required")
		return 2
	}
	if *turnTimeout <= 0 {
		// The clock drains in-flight occurrences on shutdown, and the turn
		// deadline is their only bound; require a positive one so a hung turn
		// cannot block shutdown forever.
		fmt.Fprintln(stderr, "tenon schedule run: --turn-timeout must be positive")
		return 2
	}
	if *maxActive < schedule.MinMaxActive || *maxActive > schedule.MaxMaxActive {
		fmt.Fprintf(stderr, "tenon schedule run: --max-active-turns must be between %d and %d\n", schedule.MinMaxActive, schedule.MaxMaxActive)
		return 2
	}

	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "tenon schedule run:", err)
		return 1
	}
	p, ok := loadScheduleProject(agent, "schedule run", expectedFingerprint(supplied), stderr)
	if !ok {
		return 1
	}
	driver, err := newHarnessDriver(*harnessName)
	if err != nil {
		fmt.Fprintln(stderr, "tenon schedule run:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	storeBase := resolveIntegrationStoreBase()
	opts := schedule.Options{
		Project:     p,
		Driver:      driver,
		Workspace:   *workspace,
		Harness:     *harnessName,
		TurnTimeout: *turnTimeout,
		MaxActive:   *maxActive,
		Out:         stdout,
		Manifest:    manifestIdentity(supplied),
	}
	// A supplied manifest is re-verified before each occurrence opens a harness
	// process; drift fails the occurrence closed and ends admission.
	if supplied != nil {
		opts.VerifyOccurrence = func() error {
			return checkManifest(p, *harnessName, storeBase, supplied)
		}
	}
	if err := schedule.Run(ctx, opts); err != nil {
		fmt.Fprintln(stderr, "tenon schedule run:", err)
		return 1
	}
	return 0
}

// frictionRecorder binds the served project's identity to the friction store
// so the managed boundary passes only the note.
type frictionRecorder struct {
	store *friction.Store
	note  friction.Note
}

// newFrictionRecorder resolves the private local inbox for this agent. An
// unresolvable state directory yields a recorder that stores nothing, which
// the boundary reports as an ordinary unretained note.
func newFrictionRecorder(p *agentproject.Project, harness string) frictionRecorder {
	base, err := stateBase()
	if err != nil {
		base = ""
	}
	return frictionRecorder{
		store: friction.NewStore(base),
		note: friction.Note{
			Agent:             p.Name,
			SourceFingerprint: p.Fingerprint,
			Harness:           harness,
			TenonVersion:      version.Version,
		},
	}
}

func (r frictionRecorder) Record(note string) bool {
	stored := r.note
	stored.Text = note
	return r.store.Record(stored)
}

// stateBase resolves tenon's private, owner-only local state directory. It is
// deliberately outside both the agent source and the workspace.
func stateBase() (string, error) {
	if runtime.GOOS == "darwin" {
		config, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(config, "tenon", "state"), nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "tenon"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "tenon"), nil
}

// runConnection dispatches the "tenon connection" subcommands (ADR 0016).
func runConnection(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "tenon connection: a subcommand is required (add, status, remove)\n%s", usage)
		return 2
	}
	switch args[0] {
	case "add":
		return runConnectionAdd(args[1:], stdout, stderr)
	case "status":
		return runConnectionStatus(args[1:], stdout, stderr)
	case "remove":
		return runConnectionRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tenon connection: unknown subcommand %q\n%s", args[0], usage)
		return 2
	}
}

// parsePositional parses fs against args, accepting positional arguments
// intermixed with flags in any order — the same convention commonFlags
// uses — and returns every positional argument in order.
func parsePositional(fs *flag.FlagSet, args []string) ([]string, bool) {
	var positional []string
	rest := args
	for len(rest) > 0 {
		if rest[0] != "" && rest[0][0] != '-' {
			positional = append(positional, rest[0])
			rest = rest[1:]
			continue
		}
		if err := fs.Parse(rest); err != nil {
			return nil, false
		}
		next := fs.Args()
		if len(next) == len(rest) {
			return nil, false
		}
		rest = next
	}
	return positional, true
}

// proveAgentRoot resolves agent to an absolute path and proves it an agent
// project the same way agentproject.Load does: instructions.md must be
// present as a real regular file. It never searches ancestors and never
// selects a workspace or harness (ADR 0016).
func proveAgentRoot(agent, cmdName string, stderr io.Writer) (string, bool) {
	root, err := filepath.Abs(agent)
	if err != nil {
		fmt.Fprintf(stderr, "tenon %s: %v\n", cmdName, err)
		return "", false
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "tenon %s: the agent root must be an existing directory: %s\n", cmdName, agent)
		return "", false
	}
	instr, err := os.Lstat(filepath.Join(root, "instructions.md"))
	if err != nil || instr.Mode()&os.ModeSymlink != 0 || !instr.Mode().IsRegular() {
		fmt.Fprintf(stderr, "tenon %s: %s is not a proven agent project; instructions.md must be present as a real regular file\n", cmdName, agent)
		return "", false
	}
	return root, true
}

// runConnectionAdd validates a new connection entirely offline — name, URL,
// context length, and every collision it can check — then creates the file
// atomically. It never overwrites an existing connection and never applies a
// workspace.
func runConnectionAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connection add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	urlFlag := fs.String("url", "", "absolute HTTPS remote endpoint URL")
	contextFlag := fs.String("context", "", "optional model-facing usage context")
	packageFlag := fs.String("package", "", "installed package identifier (not supported yet)")
	capabilityFlag := fs.String("capability", "", "installed capability identifier (not supported yet)")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 2 {
		fmt.Fprintf(stderr, "tenon connection add: usage: tenon connection add AGENT NAME --url HTTPS_URL [--context TEXT]\n")
		return 2
	}
	agent, name := positional[0], positional[1]

	if *packageFlag != "" || *capabilityFlag != "" {
		fmt.Fprintln(stderr, "tenon connection add: installed package targets are not supported yet; only remote streamable-http targets are available")
		return 1
	}
	if *urlFlag == "" {
		fmt.Fprintln(stderr, "tenon connection add: --url is required")
		return 2
	}

	// Load proves the root and supplies the exact offline collision space
	// (existing connections and accepted plugin MCP servers) add must check.
	p, diags, err := agentproject.Load(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon connection add:", err)
		return 1
	}
	if p == nil || diags.HasErrors() {
		_ = diags.WriteProse(stderr)
		fmt.Fprintln(stderr, "tenon connection add: the agent project is invalid; fix it before adding a connection")
		return 1
	}

	if !agentproject.ValidConnectionName(name) {
		fmt.Fprintf(stderr, "tenon connection add: %q is not a valid connection name: 1-64 characters, a leading lowercase letter, then lowercase letters, digits, underscores, or hyphens\n", name)
		return 1
	}
	if name == agentproject.ManagedConnectionName {
		fmt.Fprintf(stderr, "tenon connection add: the name %q is reserved for tenon's own managed server\n", name)
		return 1
	}
	for _, c := range p.Connections {
		if c.Name == name {
			fmt.Fprintf(stderr, "tenon connection add: the connection name %q already exists at %s\n", name, c.SourcePath)
			return 1
		}
	}
	for _, s := range p.PluginServers {
		if s.Name == name {
			fmt.Fprintf(stderr, "tenon connection add: the connection name %q collides with the accepted plugin MCP server declared at %s\n", name, s.SourcePath)
			return 1
		}
	}
	if err := agentproject.ValidateConnectionURL(*urlFlag); err != nil {
		fmt.Fprintln(stderr, "tenon connection add:", err)
		return 1
	}
	context := strings.TrimSpace(*contextFlag)
	if n := utf8.RuneCountInString(context); n > agentproject.MaxConnectionContextRunes {
		fmt.Fprintf(stderr, "tenon connection add: context may contain at most %d Unicode characters; found %d\n", agentproject.MaxConnectionContextRunes, n)
		return 1
	}

	root, ok := proveAgentRoot(agent, "connection add", stderr)
	if !ok {
		return 1
	}
	connectionsDir := filepath.Join(root, "connections")
	if err := os.MkdirAll(connectionsDir, 0o755); err != nil {
		fmt.Fprintln(stderr, "tenon connection add:", err)
		return 1
	}
	path := filepath.Join(connectionsDir, name+".md")
	if _, err := os.Lstat(path); err == nil {
		fmt.Fprintf(stderr, "tenon connection add: connections/%s.md already exists; there is no update command, edit it directly\n", name)
		return 1
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(stderr, "tenon connection add:", err)
		return 1
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: mcp\n")
	b.WriteString("transport: streamable-http\n")
	b.WriteString("url: " + *urlFlag + "\n")
	b.WriteString("---\n")
	if context != "" {
		b.WriteString("\n" + context + "\n")
	}
	if err := writeFileAtomic(path, []byte(b.String())); err != nil {
		fmt.Fprintln(stderr, "tenon connection add:", err)
		return 1
	}

	fmt.Fprintf(stdout, "added: connection %s at connections/%s.md\n", name, name)
	fmt.Fprintln(stdout, "run tenon apply for each intended workspace")
	return 0
}

// runConnectionStatus reports the declared target and context presence of
// every connection, or one named connection, without contacting anything.
// Any malformed connection is reported with its authored path and makes the
// result nonzero.
func runConnectionStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connection status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) < 1 || len(positional) > 2 {
		fmt.Fprintf(stderr, "tenon connection status: usage: tenon connection status AGENT [NAME]\n")
		return 2
	}
	agent := positional[0]
	filterName := ""
	if len(positional) == 2 {
		filterName = positional[1]
	}

	if _, ok := proveAgentRoot(agent, "connection status", stderr); !ok {
		return 1
	}

	connections, diags, err := agentproject.LoadConnectionsForStatus(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon connection status:", err)
		return 1
	}

	storeBase := resolveIntegrationStoreBase()
	var store *integration.Store
	if storeBase != "" {
		store = integration.NewStore(storeBase)
	}

	found := false
	reportedUnresolved := false
	for _, c := range connections {
		if filterName != "" && c.Name != filterName {
			continue
		}
		found = true
		contextState := "no context"
		if c.Context != "" {
			contextState = "context present"
		}
		if c.Kind == agentproject.ConnectionKindInstalled {
			resolved, detail := installedConnectionHealth(store, c)
			health := "unresolved"
			if resolved {
				health = "resolved " + detail
			}
			fmt.Fprintf(stdout, "%s: target=installed package=%s capability=%s %s %s (%s)\n",
				c.Name, c.Package, c.Capability, contextState, health, c.SourcePath)
			if !resolved {
				fmt.Fprintf(stderr, "tenon connection status: %s: %s\n", c.Name, detail)
				reportedUnresolved = true
			}
			continue
		}
		fmt.Fprintf(stdout, "%s: target=remote transport=streamable-http url=%s %s configured runtime=unchecked (%s)\n",
			c.Name, c.URL, contextState, c.SourcePath)
	}

	reportedMalformed := false
	for _, d := range diags.All() {
		if filterName != "" && d.Path != "connections/"+filterName+".md" {
			continue
		}
		fmt.Fprintln(stderr, d.String())
		if d.Severity == diagnostics.Error {
			reportedMalformed = true
			found = true
		}
	}

	if !found {
		fmt.Fprintf(stderr, "tenon connection status: no connection named %q\n", filterName)
		return 1
	}
	if reportedMalformed || reportedUnresolved {
		return 1
	}
	return 0
}

// installedConnectionHealth resolves one installed connection against store
// offline, without executing anything. On success it reports the supported
// harness targets; on failure it reports a bounded, credential-free
// diagnostic naming the failure category, never raw store internals.
func installedConnectionHealth(store *integration.Store, c agentproject.Connection) (resolved bool, detail string) {
	if store == nil {
		return false, "no integration store is configured"
	}
	desc, err := store.Resolve(c.Package, c.Capability, version.Version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return false, diagnostics.Bound(err.Error(), 256)
	}
	if desc.ServerName != c.Name {
		return false, fmt.Sprintf("the capability's declared server name %q does not equal the connection name", desc.ServerName)
	}
	targets := make([]string, 0, len(desc.Targets))
	for harness := range desc.Targets {
		targets = append(targets, harness)
	}
	sort.Strings(targets)
	return true, "targets=" + strings.Join(targets, ",")
}

// runConnectionRemove deletes exactly the named real connection file,
// without requiring it — or any other connection — to be healthy.
func runConnectionRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connection remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 2 {
		fmt.Fprintf(stderr, "tenon connection remove: usage: tenon connection remove AGENT NAME\n")
		return 2
	}
	agent, name := positional[0], positional[1]

	root, ok := proveAgentRoot(agent, "connection remove", stderr)
	if !ok {
		return 1
	}
	if !agentproject.ValidConnectionName(name) {
		fmt.Fprintf(stderr, "tenon connection remove: %q is not a valid connection name\n", name)
		return 1
	}

	path := filepath.Join(root, "connections", name+".md")
	info, err := os.Lstat(path)
	if err != nil {
		fmt.Fprintf(stderr, "tenon connection remove: no connection file at connections/%s.md\n", name)
		return 1
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		fmt.Fprintf(stderr, "tenon connection remove: connections/%s.md is not a real regular file; refusing to remove it\n", name)
		return 1
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintln(stderr, "tenon connection remove:", err)
		return 1
	}

	fmt.Fprintf(stdout, "removed: connection %s at connections/%s.md\n", name, name)
	fmt.Fprintln(stdout, "run tenon apply for each intended workspace")
	return 0
}

// writeFileAtomic writes content to a same-directory temporary file and
// renames it into place, so a concurrent reader never observes a partial
// connection file.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tenon-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
