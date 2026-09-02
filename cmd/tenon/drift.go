package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
)

// driftResult is the jsonl-mode result summary for a clean drift check,
// shaped like checkResult/applyResult: agent, harness, workspace, and the
// source fingerprint, plus the unchanged file list. Outcome is always "ok"
// here — a drift result object is only ever emitted for a clean run; a
// failing one ends with the shared gate's own gate_failed object, or with
// writeDriftOutcome's drift object, instead.
type driftResult struct {
	Outcome     string   `json:"outcome"`
	Agent       string   `json:"agent"`
	Harness     string   `json:"harness"`
	Workspace   string   `json:"workspace"`
	Fingerprint string   `json:"fingerprint"`
	Unchanged   []string `json:"unchanged"`
}

// driftOutcomeResult is the final jsonl-mode line for a drift run whose
// source passed the gate but whose workspace no longer matches what a fresh
// apply would produce (a modified, missing, or stale file). A source that
// fails the gate ends instead with the gate's own gate_failed object — the
// same one check and apply emit, because it is the same gate.
type driftOutcomeResult struct {
	Outcome string `json:"outcome"`
	// SourceDigest is always empty (and omitted) here, and stays declared so
	// this object and check's gate_failed object share one shape: a drift
	// run's source passed, so it has a fingerprint and needs no digest.
	SourceDigest string `json:"source_digest,omitempty"`
}

// writeDriftOutcome terminates the jsonl stream with the final drift object
// for a run whose workspace no longer matches. A no-op in prose mode.
func writeDriftOutcome(jsonl bool, stdout, stderr io.Writer) {
	if !jsonl {
		return
	}
	if err := writeResult(stdout, driftOutcomeResult{Outcome: "drift"}); err != nil {
		fmt.Fprintln(stderr, "tenon drift:", err)
	}
}

// runDrift reports whether a workspace still carries exactly what a fresh
// apply of AGENT for --harness would produce, without writing anything: it
// regenerates every tenon-owned file in memory on apply's own generation
// path (the same driver, the same Target shape including a supplied
// manifest's pinned Model, exactly as runApply builds it), then classifies
// each owned path against BOTH the workspace and the apply record — reusing
// apply.ClassifyOwnership, the exact rule apply's own conflict check
// enforces — as unchanged, modified on disk since the previous apply,
// missing, or stale (recorded but no longer generated).
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
	mode := fs.String("format", "prose", "output rendering: prose or jsonl")
	manifestPath := fs.String("pins", "", "supplied pin set to verify against the current runtime closure; fails closed naming the first drifted pin")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 1 {
		fmt.Fprintf(stderr, "tenon drift: exactly one AGENT directory is required\n%s", usage)
		return 2
	}
	agent := positional[0]

	driver, _, ok := resolveDriver("drift", *harnessName, false, stderr)
	if !ok {
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "tenon drift: --workspace is required")
		return 2
	}
	jsonl, ok := parseFormat("drift", *mode, stderr)
	if !ok {
		return 2
	}

	ws, err := filepath.Abs(*workspace)
	if err != nil {
		return failEnv(jsonl, stdout, stderr, "drift", err)
	}
	supplied, err := readSuppliedManifest(*manifestPath)
	if err != nil {
		return failEnv(jsonl, stdout, stderr, "drift", err)
	}
	// Everything up to classification is the gate check and apply run, in
	// the same function: load, verify a supplied pin set before any
	// generation, prepare tools, and generate against the Target apply
	// builds — a supplied pin set's pinned Model included, so a model-pinned
	// project regenerates identically to how it was applied and reports no
	// false drift from the pin alone. A diagnostic error anywhere in it is a
	// gate_failed outcome: the source itself is invalid, not a difference
	// against the workspace.
	//
	// Tools are prepared against the SOURCE with a throwaway cache, exactly
	// as check prepares them: drift writes nothing to the workspace or a
	// persistent cache, and a tool host launched in a workspace that does
	// not exist yet cannot start at all — which would report a tool failure
	// and gate_failed for a workspace whose only problem is that it is
	// missing. The real --workspace is what generation targets and what
	// classification reads below.
	gate, code := runGate(gateInput{
		command:       "drift",
		agent:         agent,
		driver:        driver,
		supplied:      supplied,
		jsonl:         jsonl,
		stdout:        stdout,
		stderr:        stderr,
		prepCacheTemp: true,
		generate:      true,
		genWorkspace:  ws,
		beforeTools: func() int {
			// A workspace that does not exist is not a gate failure: the
			// source is fine, the environment is what is missing, and
			// gate_failed says the opposite of the truth about the source. A
			// nonexistent workspace holds no apply record and no owned file,
			// so it classifies exactly as what it is — every generated path
			// missing — and the run ends "drift", the same outcome as any
			// other workspace that no longer matches. Nothing below writes to
			// the workspace, so there is nothing to create either.
			//
			// A workspace that exists but is a regular file is a different
			// thing entirely: not drift, not a gate failure, but a usage
			// mistake, and it is reported as one — exit 2, no outcome object,
			// like every other usage error. It is settled here, before the
			// gate prepares a single tool.
			if info, err := os.Stat(ws); err == nil && !info.IsDir() {
				fmt.Fprintf(stderr, "tenon drift: --workspace must be a directory (found a file): %s\n", ws)
				return 2
			}
			return -1
		},
	})
	defer gate.cleanup()
	if code != 0 {
		return code
	}
	p, diags, files := gate.project, gate.diags, gate.files
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	// From here on, the source has already passed the gate above: any
	// failure is the workspace itself — an unreadable record or a
	// modified/missing/stale file — so it is reported as drift, not
	// gate_failed.
	record, err := apply.ReadRecord(ws, driver.Harness())
	if err != nil {
		diags.Errorf("drift.record.invalid", ".",
			"the existing apply record could not be read and drift fails closed rather than guess ownership: %s",
			diagnostics.Bound(err.Error(), 256))
		render(diags, jsonl, stdout, stderr)
		writeDriftOutcome(jsonl, stdout, stderr)
		return 1
	}

	generated := map[string]bool{}
	var unchanged []string
	var modifiedPaths []string
	diffs := map[string]string{}
	for _, f := range files {
		generated[f.Path] = true
		classifyAndReportFile(ws, f, record, diags, &unchanged, &modifiedPaths, diffs)
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
			if diff := diffs[path]; diff != "" {
				fmt.Fprintln(stdout, diff)
			}
		}
	}
	if diags.HasErrors() {
		writeDriftOutcome(jsonl, stdout, stderr)
		return 1
	}
	sort.Strings(unchanged)
	if jsonl {
		res := driftResult{Outcome: "ok", Agent: p.Name, Harness: driver.Harness(), Workspace: ws, Fingerprint: p.Fingerprint, Unchanged: unchanged}
		if err := writeResult(stdout, res); err != nil {
			return failEnv(jsonl, stdout, stderr, "drift", err)
		}
		return 0
	}
	fmt.Fprintf(stdout, "clean: agent %s for %s in %s (%d unchanged)\n", p.Name, driver.Harness(), ws, len(unchanged))
	for _, path := range unchanged {
		fmt.Fprintf(stdout, "  unchanged %s\n", path)
	}
	return 0
}

// classifyAndReportFile classifies one generated file f against both the
// workspace and the apply record, appending its path to unchanged or
// modifiedPaths and recording any diff, and adds exactly one diagnostic
// naming the finding (drift.file.missing or drift.file.modified) unless the
// file is clean. It shares apply.ClassifyOwnership with checkOwnership so
// drift never diverges from what apply itself would refuse: an executable-
// bit-only change, a symlink replacing an owned file, and a workspace file
// that no longer matches the previous apply record (even if it happens to
// already match the fresh regeneration) are every one of them reported,
// exactly as they would cause apply.conflict.unowned or
// apply.conflict.modified.
func classifyAndReportFile(ws string, f apply.GeneratedFile, record *apply.Record, diags *diagnostics.List, unchanged, modifiedPaths *[]string, diffs map[string]string) {
	full := filepath.Join(ws, f.Path)
	kind, _ := apply.ClassifyOwnership(ws, f.Path, record)

	switch kind {
	case apply.OwnershipAbsent:
		diags.Errorf("drift.file.missing", f.Path,
			"the tenon-owned file no longer exists in the workspace; run tenon apply")
		return
	case apply.OwnershipNonRegular:
		diags.Errorf("drift.file.modified", f.Path,
			"the workspace entry is a symlink or other non-regular file; apply refuses to replace it (apply.conflict.unowned), which --discard-local does not override — move it aside and run tenon apply")
		*modifiedPaths = append(*modifiedPaths, f.Path)
		return
	}

	current, readErr := os.ReadFile(full)
	if readErr != nil {
		diags.Errorf("drift.file.missing", f.Path,
			"the tenon-owned file could not be read: %s", diagnostics.Bound(readErr.Error(), 256))
		return
	}
	info, statErr := os.Lstat(full)
	currentExecutable := statErr == nil && isExecutableMode(info.Mode())
	contentDiffers := string(current) != string(f.Content) || currentExecutable != f.Executable

	switch {
	case kind == apply.OwnershipUnowned:
		diff := unifiedDiff("generated/"+f.Path, "workspace/"+f.Path, f.Content, current)
		diags.Add(diagnostics.Diagnostic{
			ID: "drift.file.modified", Severity: diagnostics.Error, Path: f.Path,
			Rule:   "the workspace file exists without a recorded owned entry; apply refuses to overwrite it as hand-authored (apply.conflict.unowned), which --discard-local does not override",
			Detail: diff,
		})
		*modifiedPaths = append(*modifiedPaths, f.Path)
		diffs[f.Path] = diff
	case kind == apply.OwnershipModified || contentDiffers:
		diff := unifiedDiff("generated/"+f.Path, "workspace/"+f.Path, f.Content, current)
		diags.Add(diagnostics.Diagnostic{
			ID: "drift.file.modified", Severity: diagnostics.Error, Path: f.Path,
			Rule:   "the workspace file no longer matches the previous apply record and/or the freshly regenerated content; tenon never adopts a workspace edit back into source, run tenon apply --discard-local to overwrite it",
			Detail: diff,
		})
		*modifiedPaths = append(*modifiedPaths, f.Path)
		diffs[f.Path] = diff
	default:
		*unchanged = append(*unchanged, f.Path)
	}
}

// isExecutableMode reports whether mode carries any executable bit, the
// same test apply.isExecutable makes (unexported there, so drift keeps its
// own copy rather than reaching across the package boundary for one line).
func isExecutableMode(mode os.FileMode) bool {
	return mode.Perm()&0o111 != 0
}

// --- unified diff (line-based, informational — not meant as a patch input) ---

// driftDiffLineLimit bounds the two-dimensional line-alignment table drift
// builds for one file's diff: at the limit, an int DP table is
// (limit+1)^2 * 8 bytes ~= 13 MB, comfortably bounded regardless of how
// large a generated file's line count grows. A pair beyond it skips
// line-alignment entirely and reports a short elided notice instead — never
// a partial, unbounded diff.
const driftDiffLineLimit = 1200

// diffContext is the number of unchanged lines kept around each change, the
// same convention `diff -u` uses.
const diffContext = 3

// driftDiffByteLimit bounds the total rendered diff text drift ever
// produces or embeds in a JSONL finding's Detail field, regardless of how
// many scattered changes an in-limit file pair has.
const driftDiffByteLimit = 8000

// unifiedDiff renders a bounded, informational unified diff between old and
// new file content, labeled oldLabel/newLabel. It is read-only reporting:
// nothing here writes to either side, and the result is for a human or an
// improvement loop to read, not to apply as a patch. Every path — the
// oversized-file elision, the ordinary line-aligned diff, and the
// trailing-newline-only case where the line-level content is identical — is
// bounded to driftDiffByteLimit bytes.
func unifiedDiff(oldLabel, newLabel string, oldContent, newContent []byte) string {
	if string(oldContent) == string(newContent) {
		return "" // nothing to show; callers only call this when they differ
	}
	oldLines := splitDiffLines(oldContent)
	newLines := splitDiffLines(newContent)
	if len(oldLines) > driftDiffLineLimit || len(newLines) > driftDiffLineLimit {
		return boundDiffText(fmt.Sprintf(
			"--- %s\n+++ %s\n@@ diff elided: %d old / %d new lines exceeds drift's %d-line diff limit @@\n",
			oldLabel, newLabel, len(oldLines), len(newLines), driftDiffLineLimit), driftDiffByteLimit)
	}
	ops := lcsLineDiff(oldLines, newLines)
	body := formatUnifiedDiff(oldLabel, newLabel, ops)
	if body == "" {
		// The two files' lines are identical; the byte-level difference
		// (already established above) is purely a trailing newline. diff -u
		// marks this with its own "\ No newline at end of file" convention;
		// drift says the same thing in its own words rather than emit a
		// hunk with no visible content.
		note := "content is identical except for a trailing newline"
		switch {
		case endsWithNewline(newContent) && !endsWithNewline(oldContent):
			note = "the workspace file ends with a newline the regenerated content does not"
		case endsWithNewline(oldContent) && !endsWithNewline(newContent):
			note = "the regenerated content ends with a newline the workspace file does not"
		}
		return fmt.Sprintf("--- %s\n+++ %s\n@@ %s @@", oldLabel, newLabel, note)
	}
	return boundDiffText(body, driftDiffByteLimit)
}

// endsWithNewline reports whether b's last byte is a newline; an empty b
// does not end with one.
func endsWithNewline(b []byte) bool {
	return len(b) > 0 && b[len(b)-1] == '\n'
}

// boundDiffText truncates s to at most limit bytes, cutting at the last
// line boundary within the limit so the truncated text stays readable, and
// appends a truncation marker. s at or under limit is returned unchanged.
func boundDiffText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := strings.LastIndexByte(s[:limit], '\n')
	if cut <= 0 {
		cut = limit
	}
	return s[:cut] + "\n... (diff truncated)"
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
