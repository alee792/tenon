// Package friction stores bounded, model-authored friction notes in a
// private, owner-only, per-agent local inbox outside both the agent source
// and the workspace. The store is write-only to models: it offers no read or
// list API, is never automatically read, transmitted, or applied, and is not
// telemetry, memory, or evidence. Every storage failure is reported as a
// plain false so a managed call never turns a full or broken inbox into a
// model-visible error.
package friction

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxNoteBytes bounds one stored note.
	MaxNoteBytes = 1024
	// MaxRecords bounds the notes retained per agent. At the cap the store
	// refuses the note: it never overwrites and never silently evicts.
	MaxRecords = 256
)

// agentName is the normalized agent-name grammar. It is revalidated here
// because the name becomes a directory component.
var agentName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Note is one note plus the identity recorded with it. The identity is for
// the human reading the inbox later; it never returns to a model.
type Note struct {
	// Agent is the normalized agent name and the inbox directory component.
	Agent string
	// SourceFingerprint is the agent project's source fingerprint.
	SourceFingerprint string
	// Harness is the harness the note was written under.
	Harness string
	// TenonVersion is the tenon version that served the managed call.
	TenonVersion string
	// Text is the bounded UTF-8 note itself.
	Text string
}

// Store is the per-agent friction inbox rooted at a caller-supplied base
// directory. The caller resolves the platform default; the store never
// guesses one.
type Store struct {
	base   string
	now    func() time.Time
	random io.Reader
}

// NewStore returns a store rooted at base, an absolute directory owned by the
// invoking user. An empty or relative base yields a store that records
// nothing rather than writing outside its inbox.
func NewStore(base string) *Store {
	return &Store{base: base, now: time.Now, random: rand.Reader}
}

// record is the stored JSON shape. Its schema version is the compatibility
// contract for whatever later reads the inbox.
type record struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
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

// Record stores one note and reports whether it was retained. False covers
// every validation, capacity, lock, and filesystem failure alike: the caller
// has nothing to retry and nothing to report.
func (s *Store) Record(n Note) bool {
	if s == nil || s.base == "" || !filepath.IsAbs(s.base) || !agentName.MatchString(n.Agent) {
		return false
	}
	if !utf8.ValidString(n.Text) || strings.TrimSpace(n.Text) == "" || len(n.Text) > MaxNoteBytes {
		return false
	}

	dir, err := s.agentDir(n.Agent)
	if err != nil {
		return false
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return false
	}
	defer unlock()

	// Capacity is checked under the lock and immediately before the write, so
	// concurrent servers cannot both pass the check at the cap.
	if !hasCapacity(dir) {
		return false
	}

	created := s.now().UTC()
	suffix := make([]byte, 4)
	if _, err := io.ReadFull(s.random, suffix); err != nil {
		return false
	}
	id := created.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix)

	var stored record
	stored.SchemaVersion = 1
	stored.ID = id
	stored.CreatedAt = created
	stored.Agent.Name = n.Agent
	stored.Agent.SourceFingerprint = n.SourceFingerprint
	stored.Runtime.TenonVersion = n.TenonVersion
	stored.Runtime.Harness = n.Harness
	stored.Note = n.Text
	content, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return false
	}
	return writeExclusive(filepath.Join(dir, id+".json"), append(content, '\n')) == nil
}

// agentDir creates and returns the owner-only inbox directory for one agent.
func (s *Store) agentDir(agent string) (string, error) {
	// The base itself may not exist yet; only tenon-owned levels below it are
	// mode-asserted, never an inherited ancestor.
	if err := os.MkdirAll(s.base, 0o700); err != nil {
		return "", err
	}
	dir := s.base
	for _, part := range []string{"friction", "agents", agent} {
		dir = filepath.Join(dir, part)
		if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		// The mode is asserted rather than assumed: Mkdir honors the umask,
		// and an inherited directory may predate this store.
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// hasCapacity reports whether the agent's inbox is below the retention cap.
// Only .json records count; the lock file is inbox machinery, not a note.
func hasCapacity(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	return count < MaxRecords
}

// writeExclusive writes content to a same-directory temporary file and links
// it into place. Link never overwrites, so a colliding name loses the note
// rather than replacing an earlier one.
func writeExclusive(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tenon-note-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}
