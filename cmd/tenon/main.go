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
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/friction"
	"github.com/alee792/tenon/internal/integration"
	"github.com/alee792/tenon/internal/mcp"
	"github.com/alee792/tenon/internal/toolruntime"
)

// prepareBudget bounds one tool preparation: installing locked dependencies
// and building a Go host is slow, but it is not unbounded.
const prepareBudget = 5 * time.Minute

const usage = `usage:
  tenon apply AGENT --harness <claude|codex> [--workspace DIR] [--diagnostics <prose|jsonl>]
  tenon validate AGENT --harness <claude|codex> [--diagnostics <prose|jsonl>]
  tenon fingerprint show AGENT [--diagnostics <prose|jsonl>]
  tenon mcp serve AGENT --harness <claude|codex> [--workspace DIR]
  tenon stage AGENT --harness <claude|codex> --output DIR
  tenon stage verify --artifact PATH [--prefix DIR]
  tenon connection add AGENT NAME --url HTTPS_URL [--context TEXT]
  tenon connection status AGENT [NAME]
  tenon connection remove AGENT NAME
  tenon integration install SOURCE --trust operator
  tenon integration inspect|verify|list|enable|disable|remove [ID]
  tenon integration update ID SOURCE --trust operator
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
	case "fingerprint":
		if len(args) < 2 || args[1] != "show" {
			fmt.Fprintf(stderr, "tenon fingerprint: the only subcommand is show\n%s", usage)
			return 2
		}
		return runFingerprintShow(args[2:], stdout, stderr)
	case "mcp":
		if len(args) < 2 || args[1] != "serve" {
			fmt.Fprintf(stderr, "tenon mcp: the only subcommand is serve\n%s", usage)
			return 2
		}
		return runMCPServe(args[2:], stdin, stdout, stderr)
	case "stage":
		return runStage(args[1:], stdout, stderr)
	case "connection":
		return runConnection(args[1:], stdout, stderr)
	case "integration":
		return runIntegration(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tenon: unknown command %q\n%s", args[0], usage)
		return 2
	}
}

// commonFlags parses the shared AGENT positional and flag set. It returns
// the agent path, the selected driver, and the diagnostics mode.
func commonFlags(name string, args []string, stderr io.Writer, withWorkspace bool) (agent string, workspace string, driver apply.Driver, jsonl bool, ok bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	harness := fs.String("harness", "", "target harness: claude or codex")
	mode := fs.String("diagnostics", "prose", "diagnostic rendering: prose or jsonl")
	var ws *string
	if withWorkspace {
		ws = fs.String("workspace", "", "workspace directory (defaults to the agent directory)")
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
			return "", "", nil, false, false
		}
		next := fs.Args()
		if len(next) == len(rest) {
			fmt.Fprintf(stderr, "tenon %s: unexpected argument %q\n", name, rest[0])
			return "", "", nil, false, false
		}
		rest = next
	}
	if len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon %s: exactly one AGENT directory is required\n%s", name, usage)
		return "", "", nil, false, false
	}
	agent = positional[0]

	switch *harness {
	case "claude":
		driver = claude.Driver{}
	case "codex":
		driver = codex.Driver{}
	default:
		fmt.Fprintf(stderr, "tenon %s: --harness must be exactly claude or codex\n", name)
		return "", "", nil, false, false
	}
	switch *mode {
	case "prose":
	case "jsonl":
		jsonl = true
	default:
		fmt.Fprintf(stderr, "tenon %s: --diagnostics must be prose or jsonl\n", name)
		return "", "", nil, false, false
	}
	workspace = agent
	if withWorkspace && *ws != "" {
		workspace = *ws
	}
	return agent, workspace, driver, jsonl, true
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
	agent, _, driver, jsonl, ok := commonFlags("validate", args, stderr, false)
	if !ok {
		return 2
	}
	p, diags, err := agentproject.Load(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon validate:", err)
		return 1
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
				TenonVersion:     mcp.Version,
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
	agent, workspace, driver, jsonl, ok := commonFlags("apply", args, stderr, true)
	if !ok {
		return 2
	}
	p, diags, err := agentproject.Load(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon apply:", err)
		return 1
	}
	if p == nil || diags.HasErrors() {
		render(diags, jsonl, stdout, stderr)
		return 1
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
		IntegrationStore: resolveIntegrationStoreBase(),
		TenonVersion:     mcp.Version,
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
	agent, workspace, driver, _, ok := commonFlags("mcp serve", args, stderr, true)
	if !ok {
		return 2
	}
	p, diags, err := agentproject.Load(agent)
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
			TenonVersion:      mcp.Version,
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
	desc, err := store.Resolve(c.Package, c.Capability, mcp.Version, runtime.GOOS, runtime.GOARCH)
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
