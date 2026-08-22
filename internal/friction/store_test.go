package friction

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func note(text string) Note {
	return Note{
		Agent:             "my-agent",
		SourceFingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Harness:           "claude",
		TenonVersion:      "0.1.0-dev",
		Text:              text,
	}
}

func agentDir(base string) string {
	return filepath.Join(base, "friction", "agents", "my-agent")
}

// jsonFiles returns the record file names in the agent inbox, ignoring the
// lock file and any other inbox machinery.
func jsonFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			names = append(names, entry.Name())
		}
	}
	return names
}

// TestRecordWritesOneOwnerOnlyRecord proves the inbox layout: owner-only
// directories, one owner-only JSON record per note, and the recorded schema.
func TestRecordWritesOneOwnerOnlyRecord(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base)

	if !store.Record(note("The managed tool contract needed rereading.")) {
		t.Fatal("a valid note must be retained")
	}

	dir := agentDir(base)
	for _, path := range []string{filepath.Join(base, "friction"), filepath.Join(base, "friction", "agents"), dir} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %v, want owner-only 0700", path, info.Mode().Perm())
		}
	}

	names := jsonFiles(t, dir)
	if len(names) != 1 {
		t.Fatalf("inbox = %v, want exactly one record", names)
	}
	full := filepath.Join(dir, names[0])
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %v, want owner-only 0600", info.Mode().Perm())
	}

	raw, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		SchemaVersion int    `json:"schema_version"`
		ID            string `json:"id"`
		CreatedAt     string `json:"created_at"`
		Agent         struct {
			Name              string `json:"name"`
			SourceFingerprint string `json:"source_fingerprint"`
		} `json:"agent"`
		Runtime struct {
			TenonVersion string `json:"tenon_version"`
			Harness      string `json:"harness"`
		} `json:"runtime"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("record %q is not JSON: %v", raw, err)
	}
	if stored.SchemaVersion != 1 || stored.ID == "" || stored.CreatedAt == "" {
		t.Fatalf("record identity = %+v", stored)
	}
	if names[0] != stored.ID+".json" {
		t.Fatalf("record file %q must be named for its id %q", names[0], stored.ID)
	}
	if !strings.HasSuffix(stored.CreatedAt, "Z") {
		t.Fatalf("created_at must be UTC: %q", stored.CreatedAt)
	}
	if stored.Agent.Name != "my-agent" || stored.Agent.SourceFingerprint != note("").SourceFingerprint {
		t.Fatalf("record agent = %+v", stored.Agent)
	}
	if stored.Runtime.TenonVersion != "0.1.0-dev" || stored.Runtime.Harness != "claude" {
		t.Fatalf("record runtime = %+v", stored.Runtime)
	}
	if stored.Note != "The managed tool contract needed rereading." {
		t.Fatalf("record note = %q", stored.Note)
	}
	if !strings.Contains(string(raw), "\n  \"schema_version\"") {
		t.Fatalf("records are indented for the human who reads them: %q", raw)
	}
}

// TestRecordAtCapacityRefusesWithoutEvicting proves the retention cap: at 256
// records the next note is refused, and nothing already stored is overwritten
// or evicted.
func TestRecordAtCapacityRefusesWithoutEvicting(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base)
	dir := agentDir(base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range MaxRecords {
		name := filepath.Join(dir, fmt.Sprintf("existing-%03d.json", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if store.Record(note("One note too many.")) {
		t.Fatal("a full inbox must refuse the note")
	}
	if got := len(jsonFiles(t, dir)); got != MaxRecords {
		t.Fatalf("inbox holds %d records, want the untouched %d", got, MaxRecords)
	}
	for i := range MaxRecords {
		content, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("existing-%03d.json", i)))
		if err != nil || len(content) != 0 {
			t.Fatalf("stored record %d was overwritten: %q, %v", i, content, err)
		}
	}
}

// TestLockFileIsNotARecord proves the lock guarding the capacity check is
// inbox machinery rather than a retained note.
func TestLockFileIsNotARecord(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base)
	dir := agentDir(base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range MaxRecords - 1 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("existing-%03d.json", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if !store.Record(note("The last note that fits.")) {
		t.Fatal("the lock file must not count toward the retention cap")
	}
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err != nil {
		t.Fatalf("the inbox lock must exist: %v", err)
	}
	if got := len(jsonFiles(t, dir)); got != MaxRecords {
		t.Fatalf("inbox holds %d records, want %d", got, MaxRecords)
	}
	if store.Record(note("One note too many.")) {
		t.Fatal("the inbox is now full and must refuse")
	}
}

// TestRecordRefusesUnboundedOrUnidentifiedNotes proves every store-side
// failure is a plain false rather than a partial write.
func TestRecordRefusesUnboundedOrUnidentifiedNotes(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base)
	cases := map[string]Note{
		"empty":           note(""),
		"blank":           note("   \n\t "),
		"oversize":        note(strings.Repeat("n", MaxNoteBytes+1)),
		"invalid UTF-8":   note("bad \xff byte"),
		"unnamed agent":   {Agent: "", Text: "A note."},
		"traversing name": {Agent: "../escape", Text: "A note."},
	}
	for name, candidate := range cases {
		if store.Record(candidate) {
			t.Fatalf("%s note was retained", name)
		}
	}
	if _, err := os.Stat(agentDir(base)); err == nil {
		if got := jsonFiles(t, agentDir(base)); len(got) != 0 {
			t.Fatalf("a refused note wrote %v", got)
		}
	}
}

// TestRecordRefusesWithoutAnAbsoluteBase proves the store never guesses a
// state directory when the caller could not resolve one.
func TestRecordRefusesWithoutAnAbsoluteBase(t *testing.T) {
	if NewStore("").Record(note("A note.")) {
		t.Fatal("an unrooted store must record nothing")
	}
	if NewStore("relative/state").Record(note("A note.")) {
		t.Fatal("a relative store base must record nothing")
	}
}

// TestRecordKeepsEveryNote proves two notes in the same inbox coexist rather
// than one replacing the other.
func TestRecordKeepsEveryNote(t *testing.T) {
	base := t.TempDir()
	store := NewStore(base)
	if !store.Record(note("First.")) || !store.Record(note("Second.")) {
		t.Fatal("both notes must be retained")
	}
	if got := jsonFiles(t, agentDir(base)); len(got) != 2 {
		t.Fatalf("inbox = %v, want two records", got)
	}
}
