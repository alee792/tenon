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
	mode := fs.String("format", "prose", "output rendering: prose or jsonl")

	positional, ok := parsePositional(fs, args)
	if !ok || len(positional) != 0 {
		fmt.Fprintf(stderr, "tenon clean: takes no positional arguments (there is no AGENT — clean acts on the workspace's own apply records)\n%s", usage)
		return 2
	}
	if *workspace == "" {
		fmt.Fprintln(stderr, "tenon clean: --workspace is required")
		return 2
	}
	// clean deliberately ignores TENON_HARNESS: an empty --harness here means
	// "every harness recorded in the workspace" (a full reset), and letting
	// the env var narrow that silently would clean less than an operator
	// with TENON_HARNESS set in their shell would expect from a bare clean.
	if *harnessName != "" && *harnessName != "claude" && *harnessName != "codex" {
		fmt.Fprintln(stderr, "tenon clean: --harness must be exactly claude or codex")
		return 2
	}
	jsonl, ok := parseFormat("clean", *mode, stderr)
	if !ok {
		return 2
	}

	ws, err := filepath.Abs(*workspace)
	if err != nil {
		return failEnv(jsonl, stdout, stderr, "clean", err)
	}
	if info, err := os.Stat(ws); err != nil || !info.IsDir() {
		return failEnvf(jsonl, stdout, stderr, "clean", "the workspace must be an existing directory: %s", *workspace)
	}

	harnesses, ignored, err := cleanHarnesses(ws, *harnessName)
	if err != nil {
		return failEnv(jsonl, stdout, stderr, "clean", err)
	}
	// A file in .tenon whose name merely looks like a record is reported and
	// then left alone. --harness is validated strictly, and a discovered name
	// gets the same standard: acting on an apply-anything.json would let a
	// stray file drive removals, and silently skipping it would hide a record
	// clean did not process.
	for _, name := range ignored {
		if jsonl {
			if err := writeResult(stdout, cleanIgnoredEvent{Ignored: name, Reason: "unknown-harness"}); err != nil {
				return failEnv(jsonl, stdout, stderr, "clean", err)
			}
			continue
		}
		fmt.Fprintf(stdout, "ignoring unrecognized record: .tenon/apply-%s.json\n", name)
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
			return failEnv(jsonl, stdout, stderr, "clean", err)
		}
		if record == nil {
			// A bare clean over a workspace holding no record for this
			// harness is the idempotent no-op: nothing was asked for by
			// name, and nothing is there. An explicit --harness is a
			// different statement — the operator named a harness this
			// workspace has never been applied for — so it is reported
			// rather than silently succeeding, which would read as "the
			// harness is now clean" when tenon never touched it. It is an
			// argument that does not match the environment, not a refusal
			// to remove something: the outcome is error, not blocked.
			if *harnessName != "" {
				return failEnvf(jsonl, stdout, stderr, "clean",
					"no %s record in %s; nothing to clean for that harness", h, filepath.Join(ws, ".tenon"))
			}
			continue
		}
		// A record owning zero files still gets a plan: clean's promise is
		// that it drops the record, and a record left behind by a clean that
		// reported nothing to clean is exactly the state ADR 0027 says clean
		// removes.
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
					return failEnv(jsonl, stdout, stderr, "clean", err)
				}
			}
			if err := writeResult(stdout, cleanOutcomeBlocked{Outcome: "blocked"}); err != nil {
				return failEnv(jsonl, stdout, stderr, "clean", err)
			}
		} else {
			ownership, containment := false, false
			for _, b := range blocked {
				fmt.Fprintln(stdout, b.prose)
				if b.reason == string(apply.ContainmentEscapes) || b.reason == string(apply.ContainmentSymlinkParent) ||
					b.reason == string(apply.ContainmentUnreadableParent) {
					containment = true
					continue
				}
				ownership = true
			}
			// The two refusals have different remedies, so a run blocked only
			// on containment is never told to rerun with --force, which would
			// not help and would read as an invitation to force a removal
			// outside the workspace.
			if ownership {
				fmt.Fprintln(stderr, "tenon clean: refusing to remove files modified or unowned since apply; rerun with --force to remove modified files (files without a record entry are never touched)")
			}
			if containment {
				fmt.Fprintln(stderr, "tenon clean: refusing to act on recorded paths that leave the workspace, are reached through a symlinked parent, or whose parent chain could not be read; --force does not override this, because it widens what tenon removes inside a workspace and never where it removes")
			}
		}
		return 1
	}

	if len(plans) == 0 {
		if jsonl {
			if err := writeResult(stdout, cleanOutcomeOK{Outcome: "ok", Removed: 0}); err != nil {
				return failEnv(jsonl, stdout, stderr, "clean", err)
			}
		} else {
			fmt.Fprintln(stdout, "nothing to clean")
		}
		return 0
	}

	total := 0
	for _, p := range plans {
		for _, path := range p.toRemove {
			// The plan pass is what gives clean its all-or-nothing refusal in
			// the common case, but it is a plan: the workspace can change
			// between classifying a path and removing it. Every path is
			// re-classified immediately before its own removal, and a path
			// that no longer classifies as removable stops the clean where it
			// stands. Files removed before that point are already gone and are
			// already reported, so the run ends "blocked" — partially
			// uninstalled is a state nobody asked for, and saying so is the
			// only honest report of it.
			if reason := removableNow(ws, path, p.record, *force); reason != "" {
				if jsonl {
					if err := writeResult(stdout, cleanBlockedEvent{Blocked: path, Reason: reason}); err != nil {
						return failEnv(jsonl, stdout, stderr, "clean", err)
					}
					if err := writeResult(stdout, cleanOutcomeBlocked{Outcome: "blocked"}); err != nil {
						return failEnv(jsonl, stdout, stderr, "clean", err)
					}
				} else {
					fmt.Fprintf(stderr, "tenon clean: %s changed underneath the clean (%s); stopping after %d removed file(s), the workspace is partially cleaned\n", path, reason, total)
				}
				return 1
			}
			if err := os.Remove(filepath.Join(ws, path)); err != nil && !os.IsNotExist(err) {
				return failEnv(jsonl, stdout, stderr, "clean", err)
			}
			apply.PruneEmptyParents(ws, path)
			total++
			if jsonl {
				if err := writeResult(stdout, cleanRemovedEvent{Removed: path, Harness: p.harness}); err != nil {
					return failEnv(jsonl, stdout, stderr, "clean", err)
				}
			} else {
				fmt.Fprintf(stdout, "removed %s\n", path)
			}
		}
		if err := os.Remove(apply.RecordPath(ws, p.harness)); err != nil && !os.IsNotExist(err) {
			return failEnv(jsonl, stdout, stderr, "clean", err)
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
			return failEnv(jsonl, stdout, stderr, "clean", err)
		}
	}
	return 0
}

// cleanHarnesses lists the harnesses clean should process: just harnessName
// when given, otherwise every harness with an apply-*.json record present in
// ws/.tenon (matching apply.RecordPath's own naming, so a future harness
// needs no change here). A missing .tenon yields none rather than an error —
// that is the "nothing to clean" case, not a failure.
func cleanHarnesses(ws, harnessName string) (harnesses, ignored []string, err error) {
	if harnessName != "" {
		return []string{harnessName}, nil, nil
	}
	entries, err := os.ReadDir(filepath.Join(ws, ".tenon"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "apply-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		discovered := strings.TrimSuffix(strings.TrimPrefix(name, "apply-"), ".json")
		if discovered != "claude" && discovered != "codex" {
			ignored = append(ignored, discovered)
			continue
		}
		harnesses = append(harnesses, discovered)
	}
	sort.Strings(harnesses)
	sort.Strings(ignored)
	return harnesses, ignored, nil
}

// harnessPlan is one harness's classified clean plan: which recorded paths
// are safe to remove and which block the clean, decided before any mutation.
type harnessPlan struct {
	harness string
	// record is the record the plan was classified against, kept so the
	// removal pass can re-classify each path immediately before removing it.
	record   *apply.Record
	toRemove []string
	blocked  []blockedPath
}

// blockedPath is one recorded path clean refuses to remove: reason is the
// jsonl-mode value ("modified", "non-regular", "escapes-workspace", or
// "symlink-parent", or "unreadable-parent"); prose is the human line.
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

	plan := harnessPlan{harness: harnessName, record: record}
	for _, path := range paths {
		// Containment precedes ownership: ClassifyOwnership Lstats the leaf
		// only, so a recorded "../victim/x" or a path reached through a
		// symlinked parent directory would classify as an ordinary owned
		// file and be removed outside the workspace. A record is durable
		// state on disk — corruptible, hand-editable, written by an older
		// tenon — so clean never trusts the paths in it verbatim. Neither
		// issue is overridable by --force: --force widens what tenon
		// removes inside the workspace, never where it removes.
		if issue := apply.CheckContainment(ws, path); issue != apply.ContainmentOK {
			plan.blocked = append(plan.blocked, blockedPath{
				path: path, reason: string(issue),
				prose: containmentProse(issue, path),
			})
			continue
		}
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

// removableNow re-classifies path immediately before its removal and
// returns the blocking reason when it is no longer safe to remove, or ""
// when it is. It is the same rule planClean applied — containment, then
// apply.ClassifyOwnership, with force widening only OwnershipModified — run
// a second time against the workspace as it is now rather than as it was
// when the plan was made. A path that has since vanished is not a block:
// removal is what clean wanted, and it already happened.
func removableNow(ws, path string, record *apply.Record, force bool) string {
	if issue := apply.CheckContainment(ws, path); issue != apply.ContainmentOK {
		return string(issue)
	}
	switch kind, _ := apply.ClassifyOwnership(ws, path, record); kind {
	case apply.OwnershipAbsent, apply.OwnershipClean:
		return ""
	case apply.OwnershipModified:
		if force {
			return ""
		}
		return "modified"
	default:
		return "non-regular"
	}
}

// containmentProse renders the human line for a path clean refuses on
// containment grounds, naming what is wrong with the recorded path rather
// than only that it was refused.
func containmentProse(issue apply.ContainmentIssue, path string) string {
	switch issue {
	case apply.ContainmentEscapes:
		return fmt.Sprintf("escapes the workspace: %s", path)
	case apply.ContainmentUnreadableParent:
		return fmt.Sprintf("a parent directory could not be read, so containment could not be proven: %s", path)
	}
	return fmt.Sprintf("reached through a symlinked or non-directory parent: %s", path)
}

// cleanRemovedEvent is one jsonl-mode line clean emits per file it removes.
type cleanRemovedEvent struct {
	Removed string `json:"removed"`
	Harness string `json:"harness"`
}

// cleanIgnoredEvent is one jsonl-mode line clean emits per file in .tenon
// that names no harness clean knows.
type cleanIgnoredEvent struct {
	Ignored string `json:"ignored"`
	Reason  string `json:"reason"`
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
