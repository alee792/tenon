package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/stage"
)

// runStage dispatches the "tenon stage" command (ADR 0012): staging an agent
// filesystem tree, and the offline verification the staged entrypoint invokes.
// It is one dispatch case in main so a concurrent slice merges cleanly.
func runStage(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "verify" {
		return runStageVerify(args[1:], stdout, stderr)
	}
	return runStagePrepare(args, stdout, stderr)
}

// runStagePrepare prepares one complete runnable tree at DIR from the agent
// source, for a downstream OCI builder. DIR must not already exist.
func runStagePrepare(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harness := fs.String("harness", "", "target harness: claude or codex")
	output := fs.String("output", "", "output directory to publish (must not already exist)")
	mode := fs.String("format", "prose", "output rendering: prose or jsonl")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon stage: usage: tenon stage AGENT --harness <claude|codex> --output DIR [--format <prose|jsonl>]\n")
		return 2
	}
	agent := positional[0]

	harnessValue, harnessFromEnv := resolveHarness(*harness)
	var driver apply.Driver
	switch harnessValue {
	case "claude":
		driver = claude.Driver{}
	case "codex":
		driver = codex.Driver{}
	default:
		fmt.Fprint(stderr, harnessFlagError("stage", harnessValue, harnessFromEnv))
		return 2
	}
	jsonl := false
	switch *mode {
	case "prose":
	case "jsonl":
		jsonl = true
	default:
		fmt.Fprintf(stderr, "tenon stage: --format must be prose or jsonl\n")
		return 2
	}
	if *output == "" {
		fmt.Fprintf(stderr, "tenon stage: --output DIR is required\n")
		return 2
	}

	executable, err := resolveExecutable()
	if err != nil {
		fmt.Fprintln(stderr, "tenon stage:", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), prepareBudget)
	defer cancel()
	result, diags, err := stage.Stage(ctx, stage.Options{
		AgentDir:   agent,
		Harness:    harnessValue,
		Output:     *output,
		Executable: executable,
		Driver:     driver,
	})
	render(diags, jsonl, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "tenon stage:", err)
		return 1
	}
	if result == nil || diags.HasErrors() {
		writeGateFailed(jsonl, stdout, stderr, "stage")
		return 1
	}
	// --format governs all output, not only diagnostics: in jsonl mode the
	// run ends with one final object carrying the outcome, exactly as every
	// other command's does, and the prose lines below — which are prose, and
	// carry no outcome a consumer can read — are not printed at all.
	if jsonl {
		if err := writeResult(stdout, stageResult{
			Outcome: "ok", Agent: result.Agent, Fingerprint: result.Fingerprint, Output: result.Output,
		}); err != nil {
			fmt.Fprintln(stderr, "tenon stage:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "staged: agent %s for %s at %s (fingerprint %s)\n",
		result.Agent, harnessValue, result.Output, result.Fingerprint)
	if len(result.RuntimeLanguages) == 0 {
		fmt.Fprintln(stdout, "  runtime closure: none (tool-free agent)")
	} else {
		fmt.Fprintf(stdout, "  runtime closure staged for: %v\n", result.RuntimeLanguages)
	}
	fmt.Fprintln(stdout, "  the native harness runtime is not bundled; provide it on the base image PATH")
	return 0
}

// stageResult is the jsonl-mode result summary for a successful stage: the
// agent and the fingerprint every result object carries, plus the directory
// the tree was published to, which is what a downstream builder consumes.
type stageResult struct {
	Outcome     string `json:"outcome"`
	Agent       string `json:"agent"`
	Fingerprint string `json:"fingerprint"`
	Output      string `json:"output"`
}

// runStageVerify verifies a staged tree against its artifact manifest offline.
// It is the verification the staged entrypoint calls; --prefix supports
// verifying a tree that is not yet at its canonical runtime locations.
func runStageVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stage verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	artifact := fs.String("artifact", "", "path to the artifact manifest to verify")
	prefix := fs.String("prefix", "", "physical prefix prepended to canonical final paths")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 0 {
		fmt.Fprintf(stderr, "tenon stage verify: usage: tenon stage verify --artifact PATH [--prefix DIR]\n")
		return 2
	}
	if *artifact == "" {
		fmt.Fprintf(stderr, "tenon stage verify: --artifact PATH is required\n")
		return 2
	}
	if err := stage.Verify(*artifact, *prefix); err != nil {
		fmt.Fprintln(stderr, "tenon stage verify:", err)
		return 1
	}
	fmt.Fprintln(stdout, "verified: the staged tree matches its artifact manifest")
	return 0
}
