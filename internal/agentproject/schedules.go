package agentproject

// Schedules are root-only Markdown cron tasks (ADR 0008): each nested file
// schedules/NESTED/NAME.md carries strict frontmatter with exactly one `cron`
// value and a non-empty body that is the task prompt. Apply validates and
// fingerprints them but starts no clock. Names come straight from the bounded
// UTF-8 relative path without the .md suffix; tenon imposes no model-tool
// identifier grammar on them. Schedules are root-only because subagents accept
// instructions.md only.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/cron"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/frontmatter"
)

// Schedule bounds (ADR 0013): safety ceilings, not ordinary-use quotas.
// Violations fail before any workspace mutation.
const (
	// MaxSchedules bounds the number of schedule sources discovered under
	// schedules/, at any nesting depth.
	MaxSchedules = 256
	// MaxScheduleSourceBytes bounds one schedule source file, prompt included.
	MaxScheduleSourceBytes = 128 * 1024
	// MaxSchedulePromptBytes bounds one schedule's prompt body.
	MaxSchedulePromptBytes = 32 * 1024
	// MaxSchedulesAggregateBytes bounds every schedule source combined.
	MaxSchedulesAggregateBytes = 16 << 20
)

// Schedule is one validated schedule source.
type Schedule struct {
	// Name is the relative path under schedules/ without the .md suffix,
	// slash-separated (e.g. "daily/digest").
	Name string
	// Cron is the validated standard five-field cron expression.
	Cron string
	// Prompt is the non-empty Markdown body, the task prompt.
	Prompt string
	// SourcePath is the authored path "schedules/NESTED/NAME.md".
	SourcePath string
}

// scheduleBudget tracks the shared count and aggregate byte budgets across
// every schedule source (ADR 0013), so the whole set stays bounded even when
// each source is individually small. Each ceiling reports once.
type scheduleBudget struct {
	count         int
	countExceeded bool
	bytes         int64
	bytesExceeded bool
}

// loadSchedules discovers and validates schedules/, returning the schedules
// sorted by name and every schedule source (exact bytes) as a fingerprint
// input. Invalid schedules reject the project: they are authored project
// source, not isolatable plugin components.
func loadSchedules(root string, diags *diagnostics.List) ([]Schedule, []sourceInput) {
	dir := filepath.Join(root, "schedules")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("schedule.entry.invalid", "schedules",
			"schedules must be a real directory; symlinks are never followed")
		return nil, nil
	}

	budget := &scheduleBudget{}
	var schedules []Schedule
	var inputs []sourceInput
	walkSchedules(dir, "schedules", "", budget, diags, &schedules, &inputs)
	slices.SortFunc(schedules, func(a, b Schedule) int { return strings.Compare(a.Name, b.Name) })
	return schedules, inputs
}

// walkSchedules traverses one directory under schedules/. relDir is the
// authored source-path prefix ("schedules" or "schedules/daily"); namePrefix is
// the schedule-name prefix relative to schedules/ ("" or "daily"). Directories
// are traversed; symlinks anywhere are rejected; a non-.md file entry is
// rejected rather than ignored.
func walkSchedules(fsDir, relDir, namePrefix string, budget *scheduleBudget, diags *diagnostics.List, schedules *[]Schedule, inputs *[]sourceInput) {
	entries, err := os.ReadDir(fsDir)
	if err != nil {
		diags.Errorf("schedule.entry.invalid", relDir, "the schedules directory could not be read: %v", err)
		return
	}
	for _, entry := range entries {
		sourcePath := relDir + "/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("schedule.entry.invalid", sourcePath,
				"each schedules entry must be a real file or directory; symlinks are never followed")
			continue
		}
		if entry.IsDir() {
			childName := entry.Name()
			if namePrefix != "" {
				childName = namePrefix + "/" + entry.Name()
			}
			walkSchedules(filepath.Join(fsDir, entry.Name()), sourcePath, childName, budget, diags, schedules, inputs)
			continue
		}
		if !entry.Type().IsRegular() {
			diags.Errorf("schedule.entry.invalid", sourcePath,
				"each schedules file entry must be a regular file")
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			diags.Errorf("schedule.entry.invalid", sourcePath,
				"every file under schedules/ must be a Markdown .md schedule; found %q", entry.Name())
			continue
		}

		budget.count++
		if budget.count == MaxSchedules+1 {
			diags.Errorf("schedule.bounds.exceeded", "schedules",
				"schedules may contain at most %d schedule sources", MaxSchedules)
			budget.countExceeded = true
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		if namePrefix != "" {
			name = namePrefix + "/" + name
		}
		sched, schedInputs, ok := loadScheduleFile(filepath.Join(fsDir, entry.Name()), sourcePath, name, budget, diags)
		*inputs = append(*inputs, schedInputs...)
		if ok {
			*schedules = append(*schedules, sched)
		}
	}
}

// loadScheduleFile validates one schedule source. The exact bytes, once read,
// always join the fingerprint regardless of validity so identity tracks
// precisely what was authored.
func loadScheduleFile(fsPath, sourcePath, name string, budget *scheduleBudget, diags *diagnostics.List) (Schedule, []sourceInput, bool) {
	sched := Schedule{Name: name, SourcePath: sourcePath}
	valid := true

	if name == "" || !utf8.ValidString(name) {
		diags.Errorf("schedule.name.invalid", sourcePath,
			"a schedule name must be a non-empty UTF-8 relative path under schedules/ without the .md suffix")
		valid = false
	}

	info, err := os.Stat(fsPath)
	if err != nil {
		diags.Errorf("schedule.entry.invalid", sourcePath, "the schedule file could not be read: %v", err)
		return sched, nil, false
	}

	readable := true
	if info.Size() > MaxScheduleSourceBytes {
		diags.Errorf("schedule.bounds.exceeded", sourcePath,
			"a schedule source may contain at most %d bytes; found %d", MaxScheduleSourceBytes, info.Size())
		valid, readable = false, false
	}
	budget.bytes += info.Size()
	if !budget.bytesExceeded && budget.bytes > MaxSchedulesAggregateBytes {
		diags.Errorf("schedule.bounds.exceeded", "schedules",
			"every schedule source together may contain at most %d bytes", MaxSchedulesAggregateBytes)
		budget.bytesExceeded = true
		valid = false
	}

	var raw []byte
	if readable && !budget.bytesExceeded {
		raw, err = os.ReadFile(fsPath)
		if err != nil {
			diags.Errorf("schedule.entry.invalid", sourcePath, "the schedule file could not be read: %v", err)
			return sched, nil, false
		}
	}
	inputs := []sourceInput{{Path: sourcePath, Content: raw, Executable: false}}

	if raw == nil {
		return sched, inputs, false
	}
	if !utf8.Valid(raw) {
		diags.Errorf("schedule.entry.invalid", sourcePath, "a schedule source must be valid UTF-8")
		return sched, inputs, false
	}

	cronExpr, prompt, ok := parseSchedule(string(raw), sourcePath, diags)
	if !ok {
		valid = false
	}
	sched.Cron = cronExpr
	sched.Prompt = prompt
	if budget.countExceeded {
		valid = false
	}
	return sched, inputs, valid
}

// parseSchedule enforces the closed schedule frontmatter contract: exactly one
// `cron` field carrying a standard five-field expression, and a non-empty body
// that becomes the task prompt (matching instructions, at most one leading
// newline after the closing delimiter is removed).
func parseSchedule(content, path string, diags *diagnostics.List) (cronExpr, prompt string, ok bool) {
	raw, bodyStart, err := frontmatter.Split([]byte(content))
	if err != nil {
		diags.Errorf("schedule.frontmatter.missing", path,
			"a schedule must start with YAML frontmatter delimited by --- lines")
		return "", "", false
	}
	doc, err := frontmatter.Parse(raw)
	if err != nil {
		diags.Errorf("schedule.frontmatter.invalid", path, "%s", err)
		return "", "", false
	}

	valid := true
	for _, key := range doc.Keys() {
		if key != "cron" {
			diags.Errorf("schedule.frontmatter.unknown-field", path,
				"frontmatter permits only cron; found %q", key)
			valid = false
		}
	}
	if !doc.Has("cron") {
		diags.Errorf("schedule.cron.invalid", path,
			"frontmatter must carry exactly one cron field")
		valid = false
	} else if v, err := doc.String("cron"); err != nil {
		diags.Errorf("schedule.cron.invalid", path, "cron must be one plain string")
		valid = false
	} else if err := cron.Validate(v); err != nil {
		diags.Errorf("schedule.cron.invalid", path, "%s", err)
		valid = false
	} else {
		cronExpr = v
	}

	body := content[bodyStart:]
	if after, cut := strings.CutPrefix(body, "\r\n"); cut {
		body = after
	} else {
		body = strings.TrimPrefix(body, "\n")
	}
	if strings.TrimSpace(body) == "" {
		diags.Errorf("schedule.body.empty", path,
			"a schedule must have a non-empty Markdown body after the frontmatter")
		valid = false
	} else if len(body) > MaxSchedulePromptBytes {
		diags.Errorf("schedule.bounds.exceeded", path,
			"a schedule prompt may contain at most %d bytes; found %d", MaxSchedulePromptBytes, len(body))
		valid = false
	}
	prompt = body
	return cronExpr, prompt, valid
}
