// Command tenon compiles filesystem-authored agent projects into native
// configuration for coding-agent harnesses.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/diagnostics"
)

const usage = `usage:
  tenon apply AGENT --harness <claude|codex> [--workspace DIR] [--diagnostics <prose|jsonl>]
  tenon validate AGENT --harness <claude|codex> [--diagnostics <prose|jsonl>]
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "apply":
		return runApply(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
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

func runValidate(args []string, stdout, stderr io.Writer) int {
	agent, _, _, jsonl, ok := commonFlags("validate", args, stderr, false)
	if !ok {
		return 2
	}
	p, diags, err := agentproject.Load(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon validate:", err)
		return 1
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

	result, applyDiags, err := apply.Apply(p, workspace, driver)
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
	fmt.Fprintf(stdout, "start %s normally in %s\n", driver.Harness(), workspace)
	return 0
}
