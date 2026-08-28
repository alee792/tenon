package agentproject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSchedule writes schedules/<rel> under an agent root, creating parents.
func writeSchedule(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, "schedules", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validSchedule = "---\ncron: \"*/5 * * * *\"\n---\n\nSummarize the day.\n"

func TestLoadNestedSchedulesParseSortFingerprint(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSchedule(t, root, "daily/digest.md", "---\ncron: 0 9 * * *\n---\n\nWrite the digest.\n")
	writeSchedule(t, root, "hourly.md", validSchedule)

	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("expected valid project: p=%v diags=%v", p, diags.All())
	}
	if len(p.Schedules) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(p.Schedules))
	}
	// Sorted by name: "daily/digest" before "hourly".
	if p.Schedules[0].Name != "daily/digest" || p.Schedules[1].Name != "hourly" {
		t.Fatalf("names/order = %q, %q", p.Schedules[0].Name, p.Schedules[1].Name)
	}
	if p.Schedules[0].Cron != "0 9 * * *" {
		t.Fatalf("cron = %q", p.Schedules[0].Cron)
	}
	if p.Schedules[0].Prompt != "Write the digest.\n" {
		t.Fatalf("prompt = %q", p.Schedules[0].Prompt)
	}
	if p.Schedules[0].SourcePath != "schedules/daily/digest.md" {
		t.Fatalf("source path = %q", p.Schedules[0].SourcePath)
	}
}

func TestScheduleFingerprintChangesWithEdit(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSchedule(t, root, "daily.md", validSchedule)
	p1, _, err := Load(root)
	if err != nil || p1 == nil {
		t.Fatalf("first load: %v", err)
	}

	writeSchedule(t, root, "daily.md", "---\ncron: 0 0 * * *\n---\n\nSummarize the day.\n")
	p2, _, err := Load(root)
	if err != nil || p2 == nil {
		t.Fatalf("second load: %v", err)
	}
	if p1.Fingerprint == p2.Fingerprint {
		t.Fatal("editing a schedule must change the fingerprint")
	}
}

func TestScheduleGoodCronAccepted(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSchedule(t, root, "ok.md", "---\ncron: 0,30 8-18 * * 1-5\n---\n\nDo work.\n")
	p, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("expected accepted: diags=%v", diags.All())
	}
}

func TestScheduleCronInvalidRejected(t *testing.T) {
	for _, expr := range []string{"not a cron", "* * * * * *", "@daily"} {
		root := writeAgent(t, "agent", validInstructions)
		writeSchedule(t, root, "bad.md", "---\ncron: \""+expr+"\"\n---\n\nBody.\n")
		_, diags, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		requireErrorID(t, diags, "schedule.cron.invalid")
	}
}

func TestScheduleUnknownFieldRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSchedule(t, root, "bad.md", "---\ncron: \"* * * * *\"\ntimezone: UTC\n---\n\nBody.\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "schedule.frontmatter.unknown-field")
}

func TestScheduleMissingFrontmatterRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSchedule(t, root, "bad.md", "just a body, no frontmatter\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "schedule.frontmatter.missing")
}

func TestScheduleEmptyBodyRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	writeSchedule(t, root, "bad.md", "---\ncron: \"* * * * *\"\n---\n\n   \n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "schedule.body.empty")
}

func TestScheduleNonMarkdownFileRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "schedules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "schedules", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "schedule.entry.invalid")
}

func TestScheduleSymlinkRejected(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	if err := os.MkdirAll(filepath.Join(root, "schedules"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(target, []byte(validSchedule), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "schedules", "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "schedule.entry.invalid")
}

func TestScheduleCountBoundExceeded(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	for i := 0; i < MaxSchedules+1; i++ {
		writeSchedule(t, root, filepath.FromSlash("s"+pad(i)+".md"), validSchedule)
	}
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "schedule.bounds.exceeded")
}

func TestSchedulePromptBoundExceeded(t *testing.T) {
	root := writeAgent(t, "agent", validInstructions)
	big := strings.Repeat("a", MaxSchedulePromptBytes+1)
	writeSchedule(t, root, "big.md", "---\ncron: \"* * * * *\"\n---\n\n"+big+"\n")
	_, diags, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorID(t, diags, "schedule.bounds.exceeded")
}

// pad zero-pads i to three digits so filenames sort stably and stay distinct.
func pad(i int) string {
	s := "000" + itoa(i)
	return s[len(s)-3:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
