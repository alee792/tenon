package main

import (
	"context"
	"flag"
	"fmt"
	"io"

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

	driver, harnessValue, ok := resolveDriver("stage", *harness, false, stderr)
	if !ok {
		return 2
	}
	jsonl, ok := parseFormat("stage", *mode, stderr)
	if !ok {
		return 2
	}
	if *output == "" {
		fmt.Fprintf(stderr, "tenon stage: --output DIR is required\n")
		return 2
	}

	executable, err := resolveExecutable()
	if err != nil {
		return failEnv(jsonl, stdout, stderr, "stage", err)
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
		return failEnv(jsonl, stdout, stderr, "stage", err)
	}
	if result == nil || diags.HasErrors() {
		// The rejected source is attributable exactly as check, drift, and
		// apply make it: the digest names the bytes that failed the gate.
		// stage has no loaded project in hand here, so the digest comes from
		// the authored-file walk, which is the same path those commands take
		// when the loader never got as far as an inventory.
		writeGateFailed(jsonl, stdout, stderr, "stage", sourceDigest(agent, nil))
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
			return failEnv(jsonl, stdout, stderr, "stage", err)
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
	mode := fs.String("format", "prose", "output rendering: prose or jsonl")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 0 {
		fmt.Fprintf(stderr, "tenon stage verify: usage: tenon stage verify --artifact PATH [--prefix DIR] [--format <prose|jsonl>]\n")
		return 2
	}
	jsonl, ok := parseFormat("stage verify", *mode, stderr)
	if !ok {
		return 2
	}
	if *artifact == "" {
		fmt.Fprintf(stderr, "tenon stage verify: --artifact PATH is required\n")
		return 2
	}
	// Verification failure is a gate failure like any other: the tree does
	// not match what it claims to be. The reason is prose on stderr either
	// way — jsonl mode adds the machine-readable outcome so a consumer
	// reading the stream never has to infer failure from silence.
	if err := stage.Verify(*artifact, *prefix); err != nil {
		fmt.Fprintln(stderr, "tenon stage verify:", err)
		writeGateFailed(jsonl, stdout, stderr, "stage verify", "")
		return 1
	}
	if jsonl {
		if err := writeResult(stdout, stageVerifyResult{Outcome: "ok", Artifact: *artifact}); err != nil {
			return failEnv(jsonl, stdout, stderr, "stage verify", err)
		}
		return 0
	}
	fmt.Fprintln(stdout, "verified: the staged tree matches its artifact manifest")
	return 0
}

// stageVerifyResult is the jsonl-mode result object for a passing
// verification: the outcome every command's final object carries, plus the
// artifact manifest that was verified — which is all verify itself reports.
type stageVerifyResult struct {
	Outcome  string `json:"outcome"`
	Artifact string `json:"artifact"`
}
