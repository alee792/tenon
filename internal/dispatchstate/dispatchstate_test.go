package dispatchstate

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func ref(conversation string) Ref {
	return Ref{Agent: "my-agent", Fingerprint: "sha256:deadbeef", Harness: "claude", Conversation: conversation}
}

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, ws
}

func mustAccept(t *testing.T, s *Store, r Ref, id, text string) AcceptResult {
	t.Helper()
	res, err := s.Accept(r, id, text)
	if err != nil {
		t.Fatalf("Accept(%s): %v", id, err)
	}
	if res.Rejected {
		t.Fatalf("Accept(%s): unexpectedly rejected: %s", id, res.Reason)
	}
	return res
}

// TestAcceptEnqueuesAndDedups proves the core FIFO-accept contract: a fresh
// id queues, a repeated id while queued deduplicates against the queue, and
// a repeated id after a recorded outcome deduplicates against that outcome
// with its retained status and reason.
func TestAcceptEnqueuesAndDedups(t *testing.T) {
	s, _ := openStore(t)
	r := ref("conv-1")

	res := mustAccept(t, s, r, "in-1", "hello")
	if res.Status != Queued || res.Duplicate {
		t.Fatalf("first accept = %+v, want fresh queued", res)
	}

	dup, err := s.Accept(r, "in-1", "hello again")
	if err != nil {
		t.Fatal(err)
	}
	if !dup.Duplicate || dup.Status != Queued {
		t.Fatalf("second accept = %+v, want duplicate queued", dup)
	}

	head, resumeID, ok, err := s.StartNext(r)
	if err != nil || !ok {
		t.Fatalf("StartNext: %v ok=%v", err, ok)
	}
	if head.InputID != "in-1" || resumeID != "" {
		t.Fatalf("StartNext head = %+v resumeID=%q", head, resumeID)
	}
	if err := s.Complete(r, "in-1", Failed, "boom", false); err != nil {
		t.Fatal(err)
	}

	afterOutcome, err := s.Accept(r, "in-1", "yet again")
	if err != nil {
		t.Fatal(err)
	}
	if !afterOutcome.Duplicate || afterOutcome.Status != Failed || afterOutcome.Reason != "boom" {
		t.Fatalf("post-outcome accept = %+v, want retained failed/boom", afterOutcome)
	}
}

// TestAcceptRejectsInvalidInput proves each validation rule returns a
// rejection with a reason rather than an error, and never mutates state.
func TestAcceptRejectsInvalidInput(t *testing.T) {
	s, ws := openStore(t)
	r := ref("conv-1")

	cases := []struct {
		name string
		id   string
		text string
	}{
		{"bad id leading dot", ".bad", "hello"},
		{"empty id", "", "hello"},
		{"empty text", "in-1", "   "},
		{"oversize text", "in-1", strings.Repeat("a", MaxInputBytes+1)},
		{"non-utf8 text", "in-1", string([]byte{0xff, 0xfe})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := s.Accept(r, c.id, c.text)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.Rejected || res.Reason == "" {
				t.Fatalf("Accept(%q) = %+v, want a rejection with a reason", c.id, res)
			}
		})
	}

	if _, err := os.Stat(StatePath(ws)); !os.IsNotExist(err) {
		t.Fatalf("rejections must not create state file, stat err = %v", err)
	}
}

// TestAcceptQueueFull proves the 32-entry queue cap.
func TestAcceptQueueFull(t *testing.T) {
	s, _ := openStore(t)
	r := ref("conv-1")
	for i := 0; i < MaxQueue; i++ {
		mustAccept(t, s, r, idAt(i), "hello")
	}
	res, err := s.Accept(r, idAt(MaxQueue), "one too many")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected || res.Reason == "" {
		t.Fatalf("Accept at cap = %+v, want rejected", res)
	}
}

func idAt(i int) string { return "in-" + string(rune('a'+i%26)) + string(rune('0'+i/26)) }

// TestAcceptFIFOOrder proves StartNext always promotes the oldest
// still-queued entry.
func TestAcceptFIFOOrder(t *testing.T) {
	s, _ := openStore(t)
	r := ref("conv-1")
	mustAccept(t, s, r, "first", "a")
	mustAccept(t, s, r, "second", "b")
	mustAccept(t, s, r, "third", "c")

	head, _, ok, err := s.StartNext(r)
	if err != nil || !ok || head.InputID != "first" {
		t.Fatalf("StartNext = %+v ok=%v err=%v, want first", head, ok, err)
	}
	if err := s.Complete(r, "first", Completed, "", false); err != nil {
		t.Fatal(err)
	}
	head, _, ok, err = s.StartNext(r)
	if err != nil || !ok || head.InputID != "second" {
		t.Fatalf("StartNext = %+v ok=%v err=%v, want second", head, ok, err)
	}
}

// TestStartNextAlreadyActiveOrEmpty proves StartNext refuses to double-start
// the active head and refuses an empty queue.
func TestStartNextAlreadyActiveOrEmpty(t *testing.T) {
	s, _ := openStore(t)
	r := ref("conv-1")

	if _, _, ok, err := s.StartNext(r); err != nil || ok {
		t.Fatalf("StartNext on empty queue: ok=%v err=%v", ok, err)
	}

	mustAccept(t, s, r, "in-1", "hello")
	if _, _, ok, err := s.StartNext(r); err != nil || !ok {
		t.Fatalf("first StartNext: ok=%v err=%v", ok, err)
	}
	if _, _, ok, err := s.StartNext(r); err != nil || ok {
		t.Fatalf("second StartNext on already-active head: ok=%v err=%v", ok, err)
	}
}

// TestSetSessionIDPersistsForResume proves a persisted session id is
// returned by the next StartNext.
func TestSetSessionIDPersistsForResume(t *testing.T) {
	s, _ := openStore(t)
	r := ref("conv-1")
	if err := s.SetSessionID(r, "native-session-42"); err != nil {
		t.Fatal(err)
	}
	mustAccept(t, s, r, "in-1", "hello")
	_, resumeID, ok, err := s.StartNext(r)
	if err != nil || !ok {
		t.Fatalf("StartNext: ok=%v err=%v", ok, err)
	}
	if resumeID != "native-session-42" {
		t.Fatalf("resumeID = %q, want native-session-42", resumeID)
	}
}

// TestCompleteTaskClearsSessionInteractiveKeeps proves ADR 0008's fresh-
// session-per-task-occurrence rule, and that interactive mode is unaffected.
func TestCompleteTaskClearsSessionInteractiveKeeps(t *testing.T) {
	for _, task := range []bool{true, false} {
		s, _ := openStore(t)
		r := ref("conv-1")
		if err := s.SetSessionID(r, "sess-1"); err != nil {
			t.Fatal(err)
		}
		mustAccept(t, s, r, "in-1", "hello")
		if _, _, ok, err := s.StartNext(r); err != nil || !ok {
			t.Fatalf("StartNext: ok=%v err=%v", ok, err)
		}
		if err := s.Complete(r, "in-1", Completed, "", task); err != nil {
			t.Fatal(err)
		}
		mustAccept(t, s, r, "in-2", "hello again")
		_, resumeID, ok, err := s.StartNext(r)
		if err != nil || !ok {
			t.Fatalf("StartNext: ok=%v err=%v", ok, err)
		}
		if task && resumeID != "" {
			t.Fatalf("task=true: session id = %q, want cleared", resumeID)
		}
		if !task && resumeID != "sess-1" {
			t.Fatalf("task=false: session id = %q, want retained sess-1", resumeID)
		}
	}
}

// TestCompleteOnlyActiveHead proves Complete refuses a queued-but-not-active
// entry and a mismatched input id.
func TestCompleteOnlyActiveHead(t *testing.T) {
	s, _ := openStore(t)
	r := ref("conv-1")
	mustAccept(t, s, r, "in-1", "hello")

	if err := s.Complete(r, "in-1", Completed, "", false); err == nil {
		t.Fatal("Complete on a merely-queued (not active) head must error")
	}

	if _, _, ok, err := s.StartNext(r); err != nil || !ok {
		t.Fatalf("StartNext: ok=%v err=%v", ok, err)
	}
	if err := s.Complete(r, "wrong-id", Completed, "", false); err == nil {
		t.Fatal("Complete with a mismatched input id must error")
	}
	if err := s.Complete(r, "in-1", Queued, "", false); err == nil {
		t.Fatal("Complete with a non-terminal status must error")
	}
}

// TestRecentOutcomesEviction proves the 256-cap FIFO-evicts the oldest
// outcome and that the newest outcomes remain deduplicated.
func TestRecentOutcomesEviction(t *testing.T) {
	s, _ := openStore(t)
	r := ref("conv-1")

	for i := 0; i < MaxRecentOutcomes+10; i++ {
		id := "in-" + itoa(i)
		mustAccept(t, s, r, id, "hello")
		if _, _, ok, err := s.StartNext(r); err != nil || !ok {
			t.Fatalf("StartNext(%d): ok=%v err=%v", i, ok, err)
		}
		if err := s.Complete(r, id, Completed, "", false); err != nil {
			t.Fatalf("Complete(%d): %v", i, err)
		}
	}

	// The oldest outcome (in-0) was evicted: it is accepted as fresh, not a
	// duplicate.
	fresh, err := s.Accept(r, "in-0", "again")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Duplicate {
		t.Fatalf("in-0 should have been evicted, got duplicate = %+v", fresh)
	}
	// pop it back off so the newest-outcomes check below is unaffected
	if _, _, ok, err := s.StartNext(r); err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.Complete(r, "in-0", Completed, "", false); err != nil {
		t.Fatal(err)
	}

	// The newest outcome (in-<MaxRecentOutcomes+9>) is still retained.
	newestID := "in-" + itoa(MaxRecentOutcomes+9)
	dup, err := s.Accept(r, newestID, "again")
	if err != nil {
		t.Fatal(err)
	}
	if !dup.Duplicate || dup.Status != Completed {
		t.Fatalf("newest outcome %s should still dedup, got %+v", newestID, dup)
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// TestRecoverUncertainNeverReExecutes proves a persisted active entry is
// found uncertain after restart, with the stable reason, and is removed
// from the queue rather than left runnable.
func TestRecoverUncertainNeverReExecutes(t *testing.T) {
	s, ws := openStore(t)
	r := ref("conv-1")
	mustAccept(t, s, r, "in-1", "hello")
	if _, _, ok, err := s.StartNext(r); err != nil || !ok {
		t.Fatalf("StartNext: ok=%v err=%v", ok, err)
	}

	// Simulate a restart: reopen from disk.
	reopened, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.RecoverUncertain(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].InputID != "in-1" || recovered[0].Status != Uncertain || recovered[0].Reason != "dispatcher_restarted" {
		t.Fatalf("recovered = %+v, want one uncertain dispatcher_restarted entry", recovered)
	}

	if _, _, ok, err := reopened.StartNext(r); err != nil || ok {
		t.Fatalf("StartNext after recovery must find nothing runnable: ok=%v err=%v", ok, err)
	}
	dup, err := reopened.Accept(r, "in-1", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if !dup.Duplicate || dup.Status != Uncertain {
		t.Fatalf("re-accepting in-1 after recovery = %+v, want duplicate uncertain", dup)
	}

	// A second recovery call is a no-op: nothing left to recover.
	if recovered, err := reopened.RecoverUncertain(r); err != nil || len(recovered) != 0 {
		t.Fatalf("second RecoverUncertain = %+v err=%v, want none", recovered, err)
	}
}

// TestRecoverTaskUncertainTerminalizesQueuedAndActive proves task recovery
// terminalizes both a merely-queued entry and an active one, and clears the
// session id.
func TestRecoverTaskUncertainTerminalizesQueuedAndActive(t *testing.T) {
	s, _ := openStore(t)
	r := ref("task-1")
	if err := s.SetSessionID(r, "sess-should-be-cleared"); err != nil {
		t.Fatal(err)
	}
	mustAccept(t, s, r, "occ-1", "hello")
	if _, _, ok, err := s.StartNext(r); err != nil || !ok {
		t.Fatal(err)
	}
	mustAccept(t, s, r, "occ-2", "hello again") // stays merely queued

	recovered, err := s.RecoverTaskUncertain(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered = %+v, want both occ-1 and occ-2", recovered)
	}
	for _, rec := range recovered {
		if rec.Status != Uncertain || rec.Reason != "dispatcher_restarted" {
			t.Fatalf("recovered entry %+v, want uncertain dispatcher_restarted", rec)
		}
	}

	if _, _, ok, err := s.StartNext(r); err != nil || ok {
		t.Fatalf("StartNext after task recovery: ok=%v err=%v", ok, err)
	}
	_, resumeID, _, err := s.StartNext(r)
	if err != nil {
		t.Fatal(err)
	}
	if resumeID != "" {
		t.Fatalf("session id = %q, want cleared", resumeID)
	}
	for _, id := range []string{"occ-1", "occ-2"} {
		dup, err := s.Accept(r, id, "retry")
		if err != nil {
			t.Fatal(err)
		}
		if !dup.Duplicate || dup.Status != Uncertain {
			t.Fatalf("re-accepting %s = %+v, want duplicate uncertain", id, dup)
		}
	}
}

// TestPersistenceRoundTrips proves a fresh Open after a write reproduces
// equivalent state.
func TestPersistenceRoundTrips(t *testing.T) {
	s, ws := openStore(t)
	r := ref("conv-1")
	mustAccept(t, s, r, "in-1", "hello")
	mustAccept(t, s, r, "in-2", "world")
	if err := s.SetSessionID(r, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.StartNext(r); err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.Complete(r, "in-1", Completed, "done", false); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	head, resumeID, ok, err := reopened.StartNext(r)
	if err != nil || !ok || head.InputID != "in-2" {
		t.Fatalf("StartNext after reopen = %+v ok=%v err=%v", head, ok, err)
	}
	if resumeID != "sess-1" {
		t.Fatalf("resumeID after reopen = %q, want sess-1", resumeID)
	}
	dup, err := reopened.Accept(r, "in-1", "again")
	if err != nil {
		t.Fatal(err)
	}
	if !dup.Duplicate || dup.Status != Completed || dup.Reason != "done" {
		t.Fatalf("in-1 after reopen = %+v, want retained completed/done", dup)
	}
}

// TestOpenRejectsBroadPermissions proves a 0644 state file is refused.
func TestOpenRejectsBroadPermissions(t *testing.T) {
	ws := t.TempDir()
	path := StatePath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ws); err == nil {
		t.Fatal("Open must refuse a group/world-accessible state file")
	}
}

// TestOpenRejectsUnknownField proves strict decoding.
func TestOpenRejectsUnknownField(t *testing.T) {
	ws := t.TempDir()
	path := StatePath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ws); err == nil {
		t.Fatal("Open must refuse an unknown top-level field")
	}
}

// TestOpenRejectsDuplicateKey proves duplicate JSON keys are refused even
// though encoding/json alone would silently accept the last one.
func TestOpenRejectsDuplicateKey(t *testing.T) {
	ws := t.TempDir()
	path := StatePath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ws); err == nil {
		t.Fatal("Open must refuse a duplicate top-level key")
	}
}

// TestOpenRejectsOversizeFile proves the 1 MiB state-file bound.
func TestOpenRejectsOversizeFile(t *testing.T) {
	ws := t.TempDir()
	path := StatePath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, MaxStateBytes+1)
	for i := range big {
		big[i] = ' '
	}
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ws); err == nil {
		t.Fatal("Open must refuse a file over the byte bound")
	}
}

// TestOpenRejectsWrongSchemaVersion proves the schema_version gate.
func TestOpenRejectsWrongSchemaVersion(t *testing.T) {
	ws := t.TempDir()
	path := StatePath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ws); err == nil {
		t.Fatal("Open must refuse an unsupported schema_version")
	}
}

// TestOpenMissingFileIsEmptyState proves a missing file is not an error.
func TestOpenMissingFileIsEmptyState(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatalf("Open on a missing file must not error: %v", err)
	}
	if _, _, ok, err := s.StartNext(ref("conv-1")); err != nil || ok {
		t.Fatalf("fresh state must have nothing queued: ok=%v err=%v", ok, err)
	}
}

// TestAtomicWriteLeavesNoPartialFileOnMarshalError proves a marshal
// failure during save leaves the prior state on disk untouched (or absent,
// for a first write) and no temporary file behind.
func TestAtomicWriteLeavesNoPartialFileOnMarshalError(t *testing.T) {
	s, ws := openStore(t)
	r := ref("conv-1")

	prev := marshalIndent
	marshalIndent = func(v any, prefix, indent string) ([]byte, error) {
		return nil, errors.New("injected marshal failure")
	}

	if _, err := s.Accept(r, "in-1", "hello"); err == nil {
		marshalIndent = prev
		t.Fatal("Accept must surface the injected marshal failure")
	}
	marshalIndent = prev
	if _, err := os.Stat(StatePath(ws)); !os.IsNotExist(err) {
		t.Fatalf("state file must not exist after a failed first write, stat err = %v", err)
	}
	assertNoTempFiles(t, filepath.Dir(StatePath(ws)))

	// The store must have rolled back: a subsequent real write must
	// succeed and reflect a fresh accept, not a half-applied one.
	res, err := s.Accept(r, "in-1", "hello")
	if err != nil {
		t.Fatalf("Accept after rollback: %v", err)
	}
	if res.Duplicate {
		t.Fatalf("Accept after rollback = %+v, want fresh (rolled-back state had no in-1)", res)
	}
}

// TestAtomicWriteLeavesNoPartialFileOnRenameError proves a rename failure
// during save leaves any prior on-disk file untouched and no temp file
// behind.
func TestAtomicWriteLeavesNoPartialFileOnRenameError(t *testing.T) {
	s, ws := openStore(t)
	r := ref("conv-1")
	if err := s.SetSessionID(r, "sess-before"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(StatePath(ws))
	if err != nil {
		t.Fatal(err)
	}

	prev := renameFile
	renameFile = func(oldpath, newpath string) error {
		return errors.New("injected rename failure")
	}
	if err := s.SetSessionID(r, "sess-after"); err == nil {
		t.Fatal("SetSessionID must surface the injected rename failure")
	}
	renameFile = prev

	after, err := os.ReadFile(StatePath(ws))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("state file changed despite a failed rename:\nbefore=%s\nafter=%s", before, after)
	}
	assertNoTempFiles(t, filepath.Dir(StatePath(ws)))

	// Rolled back in memory too: the next real write must succeed cleanly.
	if err := s.SetSessionID(r, "sess-after"); err != nil {
		t.Fatalf("SetSessionID after rollback: %v", err)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tenon-dispatch-") {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

// TestIndependentRefsInOneFile proves two distinct Refs queue, dedup, and
// resume completely independently within one state file.
func TestIndependentRefsInOneFile(t *testing.T) {
	s, ws := openStore(t)
	a := ref("conv-a")
	b := ref("conv-b")

	mustAccept(t, s, a, "shared-id", "from a")
	fresh, err := s.Accept(b, "shared-id", "from b")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Duplicate {
		t.Fatalf("same input id in a different conversation must not dedup: %+v", fresh)
	}

	if err := s.SetSessionID(a, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionID(b, "sess-b"); err != nil {
		t.Fatal(err)
	}

	_, resumeA, ok, err := s.StartNext(a)
	if err != nil || !ok || resumeA != "sess-a" {
		t.Fatalf("StartNext(a) = resumeID=%q ok=%v err=%v", resumeA, ok, err)
	}
	if err := s.Complete(a, "shared-id", Completed, "", false); err != nil {
		t.Fatal(err)
	}

	// b is untouched by a's completion.
	_, resumeB, ok, err := s.StartNext(b)
	if err != nil || !ok || resumeB != "sess-b" {
		t.Fatalf("StartNext(b) = resumeID=%q ok=%v err=%v", resumeB, ok, err)
	}
	dupB, err := s.Accept(b, "shared-id", "again")
	if err != nil {
		t.Fatal(err)
	}
	if !dupB.Duplicate || dupB.Status != Active {
		t.Fatalf("b's shared-id should still be active, got %+v", dupB)
	}

	// Round-trip and re-check independence survives persistence too.
	reopened, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	dupA, err := reopened.Accept(a, "shared-id", "again")
	if err != nil {
		t.Fatal(err)
	}
	if !dupA.Duplicate || dupA.Status != Completed {
		t.Fatalf("a's shared-id should be completed after reopen, got %+v", dupA)
	}
}
