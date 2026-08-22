// Command tenon compiles filesystem-authored agent projects into native
// configuration for coding-agent harnesses.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/friction"
	"github.com/alee792/tenon/internal/mcp"
)

const usage = `usage:
  tenon apply AGENT --harness <claude|codex> [--workspace DIR] [--diagnostics <prose|jsonl>]
  tenon validate AGENT --harness <claude|codex> [--diagnostics <prose|jsonl>]
  tenon mcp serve AGENT --harness <claude|codex> [--workspace DIR]
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
	case "mcp":
		if len(args) < 2 || args[1] != "serve" {
			fmt.Fprintf(stderr, "tenon mcp: the only subcommand is serve\n%s", usage)
			return 2
		}
		return runMCPServe(args[2:], stdin, stdout, stderr)
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
		_ = driver.Generate(p, apply.Target{Workspace: workspace, Executable: executable}, diags)
	}
	render(diags, jsonl, stdout, stderr)
	if p == nil || diags.HasErrors() {
		return 1
	}
	fmt.Fprintf(stdout, "valid: agent %s (fingerprint %s)\n", p.Name, p.Fingerprint)
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

	result, applyDiags, err := apply.Apply(p, workspace, executable, driver)
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

// managedTools names the built-in tools the managed boundary will expose for
// the project. Authored tools join in a later slice.
func managedTools(p *agentproject.Project) []string {
	tools := []string{"echo"}
	if p.Instructions != nil && p.Instructions.FrictionNotes {
		tools = append(tools, "record-friction")
	}
	return tools
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
