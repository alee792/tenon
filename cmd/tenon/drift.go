package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/codex"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/version"
)

// driftResult is the jsonl-mode result summary for a clean drift check.
type driftResult struct {
	Agent     string   `json:"agent"`
	Harness   string   `json:"harness"`
	Workspace string   `json:"workspace"`
	Unchanged []string `json:"unchanged"`
}

// runDrift reports whether a workspace still carries exactly what a fresh
// generation of AGENT for --harness would produce, without writing anything:
// it regenerates every tenon-owned file in memory on the same generation
// path apply uses, then compares each against the workspace and the apply
// record (.tenon/apply-<harness>.json) as unchanged, modified on disk since
// the previous apply, missing, or stale (recorded but no longer generated).
//
// Drift deliberately never adopts a workspace edit back into source:
// generation is lossy in reverse (a rendered native file cannot be inverted
// into the authored form that produced it), so tenon never guesses author
// intent from a diff. Drift only shows the diff; the author edits source and
// reapplies, or reapplies with --discard-local to explicitly discard the
// workspace edit.
func runDrift(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness: claude or codex")
	workspace := fs.String("workspace", "", "workspace directory (required)")
	mode := fs.String("diagnostics", "prose", "diagnostic rendering: prose or jsonl")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon drift: exactly one AGENT directory is required\n%s", usage)
		return 2
	}
	agent := positional[0]

	var driver apply.Driver
	switch *harnessName {
	case "claude":
		driver = claude.Driver{}
	case "codex":
		driver = codex.Driver{}
	default:
		fmt.Fprintln(stderr, "tenon drift: --harness must be exactly claude or codex")
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "tenon drift: --workspace is required")
		return 2
	}
	var jsonl bool
	switch *mode {
	case "prose":
	case "jsonl":
		jsonl = true
	default:
		fmt.Fprintln(stderr, "tenon drift: --diagnostics must be prose or jsonl")
		return 2
	}

	p, diags, err := agentproject.Load(agent)
	if err != nil {
		fmt.Fprintln(stderr, "tenon drift:", err)
		return 1
	}
	if p == nil || diags.HasErrors() {
		render(diags, jsonl, stdout, stderr)
		return 1
	}

	executable, err := resolveExecutable()
	if err != nil {
		fmt.Fprintln(stderr, "tenon drift:", err)
		return 1
	}
	ws, err := filepath.Abs(*workspace)
	if err != nil {
		fmt.Fprintln(stderr, "tenon drift:", err)
		return 1
	}
	if info, err := os.Stat(ws); err != nil || !info.IsDir() {
		diags.Errorf("apply.workspace.missing", ".",
			"the workspace must be an existing directory: %s", *workspace)
		render(diags, jsonl, stdout, stderr)
		return 1
	}

	// Tool preparation runs against a throwaway cache exactly as validate
	// does: drift writes nothing to the workspace or a persistent cache.
	cache := ""
	if len(p.Tools) > 0 {
		cache, err = os.MkdirTemp("", "tenon-tools-")
		if err != nil {
			fmt.Fprintln(stderr, "tenon drift:", err)
			return 1
		}
		defer os.RemoveAll(cache)
	}
	if !prepareTools(p, ws, cache, diags) {
		render(diags, jsonl, stdout, stderr)
		return 1
	}

	// Regeneration reuses apply's exact generation path: the same driver,
	// the same target shape (no manifest-sourced Model here, since drift
	// takes no --manifest; a project applied with a pinned model is compared
	// against a regeneration without one, same as validate without a
	// supplied manifest).
	files := driver.Generate(p, apply.Target{
		Workspace:        ws,
		Executable:       executable,
		IntegrationStore: resolveIntegrationStoreBase(),
		TenonVersion:     version.Version,
	}, diags)
	if diags.HasErrors() {
		render(diags, jsonl, stdout, stderr)
		return 1
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	record, err := apply.ReadRecord(ws, driver.Harness())
	if err != nil {
		diags.Errorf("drift.record.invalid", ".",
			"the existing apply record could not be read and drift fails closed rather than guess ownership: %s",
			diagnostics.Bound(err.Error(), 256))
		render(diags, jsonl, stdout, stderr)
		return 1
	}

	generated := map[string]bool{}
	var unchanged []string
	var modifiedPaths []string
	diffs := map[string]string{}
	for _, f := range files {
		generated[f.Path] = true
		full := filepath.Join(ws, f.Path)
		current, readErr := os.ReadFile(full)
		switch {
		case os.IsNotExist(readErr):
			diags.Errorf("drift.file.missing", f.Path,
				"the tenon-owned file no longer exists in the workspace; run tenon apply")
		case readErr != nil:
			diags.Errorf("drift.file.missing", f.Path,
				"the tenon-owned file could not be read: %s", diagnostics.Bound(readErr.Error(), 256))
		case string(current) == string(f.Content):
			unchanged = append(unchanged, f.Path)
		default:
			diags.Errorf("drift.file.modified", f.Path,
				"the workspace file differs from the freshly regenerated content; tenon never adopts a workspace edit back into source, run tenon apply --discard-local to overwrite it")
			modifiedPaths = append(modifiedPaths, f.Path)
			diffs[f.Path] = unifiedDiff("generated/"+f.Path, "workspace/"+f.Path, f.Content, current)
		}
	}
	if record != nil {
		var stalePaths []string
		for path := range record.Files {
			if !generated[path] {
				stalePaths = append(stalePaths, path)
			}
		}
		sort.Strings(stalePaths)
		for _, path := range stalePaths {
			diags.Errorf("drift.file.stale", path,
				"the file is recorded from a previous apply but is no longer generated; run tenon apply to remove it")
		}
	}

	render(diags, jsonl, stdout, stderr)
	if !jsonl {
		sort.Strings(modifiedPaths)
		for _, path := range modifiedPaths {
			fmt.Fprintln(stdout, diffs[path])
		}
	}
	if diags.HasErrors() {
		return 1
	}
	sort.Strings(unchanged)
	if jsonl {
		res := driftResult{Agent: p.Name, Harness: driver.Harness(), Workspace: ws, Unchanged: unchanged}
		if err := writeResult(stdout, res); err != nil {
			fmt.Fprintln(stderr, "tenon drift:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "clean: agent %s for %s in %s (%d unchanged)\n", p.Name, driver.Harness(), ws, len(unchanged))
	for _, path := range unchanged {
		fmt.Fprintf(stdout, "  unchanged %s\n", path)
	}
	return 0
}

// --- unified diff (line-based, informational — not meant as a patch input) ---

// driftDiffLineLimit bounds the two-dimensional line-alignment table drift
// builds for one file's diff. A file pair beyond it is rendered as a coarse
// whole-file replacement instead of a line-aligned diff: correctness of the
// drift finding itself never depends on the diff rendering, and reporting
// must stay bounded regardless of how large a generated file is.
const driftDiffLineLimit = 4000

// diffContext is the number of unchanged lines kept around each change, the
// same convention `diff -u` uses.
const diffContext = 3

// unifiedDiff renders a bounded, informational unified diff between old and
// new file content, labeled oldLabel/newLabel. It is read-only reporting:
// nothing here writes to either side, and the result is for a human or an
// improvement loop to read, not to apply as a patch.
func unifiedDiff(oldLabel, newLabel string, oldContent, newContent []byte) string {
	oldLines := splitDiffLines(oldContent)
	newLines := splitDiffLines(newContent)
	var ops []diffOp
	if len(oldLines) > driftDiffLineLimit || len(newLines) > driftDiffLineLimit {
		for _, l := range oldLines {
			ops = append(ops, diffOp{kind: '-', line: l})
		}
		for _, l := range newLines {
			ops = append(ops, diffOp{kind: '+', line: l})
		}
	} else {
		ops = lcsLineDiff(oldLines, newLines)
	}
	return formatUnifiedDiff(oldLabel, newLabel, ops)
}

// splitDiffLines splits content into lines for diffing, dropping exactly one
// trailing newline the way most unified-diff tooling does.
func splitDiffLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.Split(s, "\n")
}

// diffOp is one aligned line in a diff: kind is ' ' (equal), '-' (only in
// old), or '+' (only in new).
type diffOp struct {
	kind byte
	line string
}

// lcsLineDiff aligns a and b by their longest common subsequence of lines,
// via the textbook dynamic-programming table. It is O(len(a)*len(b)) time
// and space, bounded by driftDiffLineLimit at the call site.
func lcsLineDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// formatUnifiedDiff renders aligned ops as unified-diff hunks with
// diffContext lines of surrounding context, merging hunks whose context
// overlaps. Empty when ops carries no changes.
func formatUnifiedDiff(oldLabel, newLabel string, ops []diffOp) string {
	type span struct{ lo, hi int }
	var spans []span
	for idx, op := range ops {
		if op.kind == ' ' {
			continue
		}
		lo, hi := idx-diffContext, idx+diffContext
		if lo < 0 {
			lo = 0
		}
		if hi >= len(ops) {
			hi = len(ops) - 1
		}
		if len(spans) > 0 && lo <= spans[len(spans)-1].hi+1 {
			if hi > spans[len(spans)-1].hi {
				spans[len(spans)-1].hi = hi
			}
			continue
		}
		spans = append(spans, span{lo, hi})
	}
	if len(spans) == 0 {
		return ""
	}

	// oldAt[idx]/newAt[idx] is the 1-based old/new line number of ops[idx].
	oldAt := make([]int, len(ops)+1)
	newAt := make([]int, len(ops)+1)
	oldAt[0], newAt[0] = 1, 1
	for idx, op := range ops {
		oldAt[idx+1], newAt[idx+1] = oldAt[idx], newAt[idx]
		switch op.kind {
		case ' ':
			oldAt[idx+1]++
			newAt[idx+1]++
		case '-':
			oldAt[idx+1]++
		case '+':
			newAt[idx+1]++
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n", oldLabel)
	fmt.Fprintf(&out, "+++ %s\n", newLabel)
	for _, sp := range spans {
		oldStart, newStart := oldAt[sp.lo], newAt[sp.lo]
		var oldCount, newCount int
		var body strings.Builder
		for idx := sp.lo; idx <= sp.hi; idx++ {
			op := ops[idx]
			switch op.kind {
			case ' ':
				oldCount++
				newCount++
				fmt.Fprintf(&body, " %s\n", op.line)
			case '-':
				oldCount++
				fmt.Fprintf(&body, "-%s\n", op.line)
			case '+':
				newCount++
				fmt.Fprintf(&body, "+%s\n", op.line)
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		out.WriteString(body.String())
	}
	return strings.TrimRight(out.String(), "\n")
}
