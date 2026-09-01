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
)

// runClean removes tenon-owned files from a workspace using the apply
// record(s) written there, then removes the record(s) themselves — the
// precise inverse of apply. Unlike apply/check/drift, clean takes no AGENT
// positional argument: it operates entirely on a workspace's own records, so
// it works even when the source that produced them is gone (uninstall) or
// when only a stale harness's files need removing (switching harnesses
// leaves the previous one's files behind otherwise).
func runClean(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness: claude or codex (omit to clean every harness recorded in the workspace)")
	workspace := fs.String("workspace", "", "workspace directory (required)")
	force := fs.Bool("force", false, "remove tenon-owned files even when modified since the previous apply; files without a record entry are never touched")
	mode := fs.String("diagnostics", "prose", "diagnostic rendering: prose or jsonl")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 0 {
		fmt.Fprintf(stderr, "tenon clean: takes no positional arguments (there is no AGENT — clean acts on the workspace's own apply records)\n%s", usage)
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "tenon clean: --workspace is required")
		return 2
	}
	if *harnessName != "" && *harnessName != "claude" && *harnessName != "codex" {
		fmt.Fprintln(stderr, "tenon clean: --harness must be exactly claude or codex")
		return 2
	}
	var jsonl bool
	switch *mode {
	case "prose":
	case "jsonl":
		jsonl = true
	default:
		fmt.Fprintln(stderr, "tenon clean: --diagnostics must be prose or jsonl")
		return 2
	}

	ws, err := filepath.Abs(*workspace)
	if err != nil {
		fmt.Fprintln(stderr, "tenon clean:", err)
		return 1
	}
	if info, err := os.Stat(ws); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "tenon clean: the workspace must be an existing directory: %s\n", *workspace)
		return 1
	}

	harnesses, err := cleanHarnesses(ws, *harnessName)
	if err != nil {
		fmt.Fprintln(stderr, "tenon clean:", err)
		return 1
	}

	// Every path in every harness named above is classified before anything
	// is removed (plans first, mutation second): a blocked path in one
	// harness's record refuses the whole clean, including harnesses that
	// classified clean, rather than leaving the workspace partially
	// uninstalled.
	var plans []harnessPlan
	for _, h := range harnesses {
		record, err := apply.ReadRecord(ws, h)
		if err != nil {
			fmt.Fprintln(stderr, "tenon clean:", err)
			return 1
		}
		if record == nil || len(record.Files) == 0 {
			continue // no record, or a record owning nothing: idempotent no-op for this harness
		}
		plans = append(plans, planClean(ws, h, record, *force))
	}

	var blocked []blockedPath
	for _, p := range plans {
		blocked = append(blocked, p.blocked...)
	}
	if len(blocked) > 0 {
		sort.Slice(blocked, func(i, j int) bool { return blocked[i].path < blocked[j].path })
		if jsonl {
			for _, b := range blocked {
				if err := writeResult(stdout, cleanBlockedEvent{Blocked: b.path, Reason: b.reason}); err != nil {
					fmt.Fprintln(stderr, "tenon clean:", err)
					return 1
				}
			}
			if err := writeResult(stdout, cleanOutcomeBlocked{Outcome: "blocked"}); err != nil {
				fmt.Fprintln(stderr, "tenon clean:", err)
				return 1
			}
		} else {
			for _, b := range blocked {
				fmt.Fprintln(stdout, b.prose)
			}
			fmt.Fprintln(stderr, "tenon clean: refusing to remove files modified or unowned since apply; rerun with --force to remove modified files (files without a record entry are never touched)")
		}
		return 1
	}

	if len(plans) == 0 {
		if jsonl {
			if err := writeResult(stdout, cleanOutcomeOK{Outcome: "ok", Removed: 0}); err != nil {
				fmt.Fprintln(stderr, "tenon clean:", err)
				return 1
			}
		} else {
			fmt.Fprintln(stdout, "nothing to clean")
		}
		return 0
	}

	total := 0
	for _, p := range plans {
		for _, path := range p.toRemove {
			if err := os.Remove(filepath.Join(ws, path)); err != nil && !os.IsNotExist(err) {
				fmt.Fprintln(stderr, "tenon clean:", err)
				return 1
			}
			removeEmptyParents(ws, path)
			total++
			if jsonl {
				if err := writeResult(stdout, cleanRemovedEvent{Removed: path, Harness: p.harness}); err != nil {
					fmt.Fprintln(stderr, "tenon clean:", err)
					return 1
				}
			} else {
				fmt.Fprintf(stdout, "removed %s\n", path)
			}
		}
		if err := os.Remove(apply.RecordPath(ws, p.harness)); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, "tenon clean:", err)
			return 1
		}
		if !jsonl {
			fmt.Fprintf(stdout, "cleaned %s: %d files\n", p.harness, len(p.toRemove))
		}
	}

	// .tenon is removed only once every record it held is gone: a clean
	// scoped to a single --harness leaves other harnesses' records — and so
	// the directory itself — untouched.
	if entries, err := os.ReadDir(filepath.Join(ws, ".tenon")); err == nil && len(entries) == 0 {
		_ = os.Remove(filepath.Join(ws, ".tenon"))
	}

	if jsonl {
		if err := writeResult(stdout, cleanOutcomeOK{Outcome: "ok", Removed: total}); err != nil {
			fmt.Fprintln(stderr, "tenon clean:", err)
			return 1
		}
	}
	return 0
}

// cleanHarnesses lists the harnesses clean should process: just harnessName
// when given, otherwise every harness with an apply-*.json record present in
// ws/.tenon (matching apply.RecordPath's own naming, so a future harness
// needs no change here). A missing .tenon yields none rather than an error —
// that is the "nothing to clean" case, not a failure.
func cleanHarnesses(ws, harnessName string) ([]string, error) {
	if harnessName != "" {
		return []string{harnessName}, nil
	}
	entries, err := os.ReadDir(filepath.Join(ws, ".tenon"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var harnesses []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "apply-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		harnesses = append(harnesses, strings.TrimSuffix(strings.TrimPrefix(name, "apply-"), ".json"))
	}
	sort.Strings(harnesses)
	return harnesses, nil
}

// harnessPlan is one harness's classified clean plan: which recorded paths
// are safe to remove and which block the clean, decided before any mutation.
type harnessPlan struct {
	harness  string
	toRemove []string
	blocked  []blockedPath
}

// blockedPath is one recorded path clean refuses to remove: reason is the
// jsonl-mode value ("modified" or "non-regular"); prose is the human line.
type blockedPath struct {
	path   string
	reason string
	prose  string
}

// planClean classifies every path record owns for harnessName against the
// workspace via apply.ClassifyOwnership — the identical rule apply's own
// conflict check enforces — deciding which paths clean will remove, which
// are already gone, and which block the clean. force lets a recorded,
// modified-since-apply file through; it never overrides OwnershipNonRegular
// or OwnershipUnowned, which the record's hash can never vouch for (a
// symlink, or a file that no longer reads back), so those always block
// regardless of force.
func planClean(ws, harnessName string, record *apply.Record, force bool) harnessPlan {
	paths := make([]string, 0, len(record.Files))
	for path := range record.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	plan := harnessPlan{harness: harnessName}
	for _, path := range paths {
		kind, _ := apply.ClassifyOwnership(ws, path, record)
		switch kind {
		case apply.OwnershipAbsent:
			// already gone; nothing to remove, nothing to block
		case apply.OwnershipClean:
			plan.toRemove = append(plan.toRemove, path)
		case apply.OwnershipModified:
			if force {
				plan.toRemove = append(plan.toRemove, path)
				continue
			}
			plan.blocked = append(plan.blocked, blockedPath{
				path: path, reason: "modified",
				prose: fmt.Sprintf("modified since apply: %s", path),
			})
		case apply.OwnershipNonRegular:
			plan.blocked = append(plan.blocked, blockedPath{
				path: path, reason: "non-regular",
				prose: fmt.Sprintf("not a regular file: %s", path),
			})
		case apply.OwnershipUnowned:
			// ClassifyOwnership only returns Unowned for a path present in
			// record.Files (every path planClean classifies) when the file
			// could not be read; an unreadable file is never a hash the
			// record can vouch for, so --force does not override it either.
			plan.blocked = append(plan.blocked, blockedPath{
				path: path, reason: "non-regular",
				prose: fmt.Sprintf("could not be read: %s", path),
			})
		}
	}
	return plan
}

// removeEmptyParents mirrors internal/apply's own empty-directory pruning
// after removing a stale generated file (apply.removeEmptyParents,
// unexported there — see drift.go's isExecutableMode for the same
// package-boundary reason cmd/tenon keeps its own copy): after clean
// removes an owned file it walks upward from the file's parent, removing
// each now-empty directory, stopping at the workspace root and never
// touching .tenon (removed separately, once every record in it is gone).
func removeEmptyParents(ws, path string) {
	for dir := filepath.Dir(path); dir != "." && dir != ".tenon" && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if os.Remove(filepath.Join(ws, dir)) != nil {
			return
		}
	}
}

// cleanRemovedEvent is one jsonl-mode line clean emits per file it removes.
type cleanRemovedEvent struct {
	Removed string `json:"removed"`
	Harness string `json:"harness"`
}

// cleanBlockedEvent is one jsonl-mode line clean emits per path it refuses
// to remove.
type cleanBlockedEvent struct {
	Blocked string `json:"blocked"`
	Reason  string `json:"reason"`
}

// cleanOutcomeOK is the final jsonl-mode line for a successful clean —
// Removed is always present, including the zero value for "nothing to
// clean", so it is not omitempty.
type cleanOutcomeOK struct {
	Outcome string `json:"outcome"`
	Removed int    `json:"removed"`
}

// cleanOutcomeBlocked is the final jsonl-mode line for a refused clean.
type cleanOutcomeBlocked struct {
	Outcome string `json:"outcome"`
}
