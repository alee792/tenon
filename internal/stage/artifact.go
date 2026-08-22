package stage

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Artifact is the staged tree's manifest: tenon's own schema-versioned
// bookkeeping, not author configuration. It records identity, the target
// platform, the final-path layout, the staged runtime set, and every staged
// file and directory with its content hash, mode, and intended runtime
// ownership, sufficient to verify the tree offline. It carries no timestamps
// and sorts its file and directory lists, so identical inputs produce
// identical bytes.
type Artifact struct {
	SchemaVersion     int               `json:"schema_version"`
	TenonVersion      string            `json:"tenon_version"`
	Harness           HarnessInfo       `json:"harness"`
	Agent             string            `json:"agent"`
	SourceFingerprint string            `json:"source_fingerprint"`
	Platform          Platform          `json:"platform"`
	Layout            map[string]string `json:"layout"`
	Runtime           RuntimeInfo       `json:"runtime"`
	Dirs              []DirEntry        `json:"dirs"`
	Files             []FileEntry       `json:"files"`
}

// HarnessInfo records the selected harness and whether its runtime is bundled.
// In this slice it is never bundled: see harnessPlaceholderNote.
type HarnessInfo struct {
	Name    string `json:"name"`
	Bundled bool   `json:"bundled"`
	Note    string `json:"note"`
}

// Platform is the target OS and architecture. Staging is platform-honest: it
// targets the building host's own platform and does not promise cross-builds.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// RuntimeInfo records the staged tool execution closure. Minimized is false in
// this slice: the prepared cache is staged whole (see Note).
type RuntimeInfo struct {
	Bundled     bool     `json:"bundled"`
	Languages   []string `json:"languages,omitempty"`
	ClosurePath string   `json:"closure_path,omitempty"`
	Minimized   bool     `json:"minimized"`
	Note        string   `json:"note"`
}

// Owner is the intended runtime ownership of a staged path. Staging records
// intent and never chowns; the downstream builder applies it.
type Owner struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

// FileEntry is one staged regular file: its final runtime path, content hash,
// mode, and intended ownership.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
	Owner  Owner  `json:"owner"`
}

// DirEntry is one staged directory: its final runtime path, mode, and intended
// ownership.
type DirEntry struct {
	Path  string `json:"path"`
	Mode  string `json:"mode"`
	Owner Owner  `json:"owner"`
}

// ownerFor returns the intended ownership of a final path: /opt is root-owned
// and read-only at runtime, /workspace and /home/tenon are owned by the
// non-root runtime identity.
func ownerFor(finalPath string) Owner {
	if strings.HasPrefix(finalPath, finalOptRoot+"/") || finalPath == finalOptRoot {
		return Owner{rootUID, rootGID}
	}
	return Owner{runtimeUID, runtimeGID}
}

// collectFiles walks the physical tree under root and records every staged
// regular file at its final path, except the manifest itself, which is the
// record and cannot list its own hash. It records directories not already
// listed. It rejects any non-regular entry.
func (a *Artifact) collectFiles(root string) error {
	seenDir := map[string]bool{}
	for _, d := range a.Dirs {
		seenDir[d.Path] = true
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		finalPath := "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			if !seenDir[finalPath] {
				info, ierr := d.Info()
				if ierr != nil {
					return ierr
				}
				a.Dirs = append(a.Dirs, DirEntry{Path: finalPath, Mode: octal(info.Mode()), Owner: ownerFor(finalPath)})
				seenDir[finalPath] = true
			}
			return nil
		}
		if finalPath == finalArtifact {
			return nil // the manifest is the record; it never lists itself
		}
		if !d.Type().IsRegular() {
			return errNonRegular(finalPath)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		a.Files = append(a.Files, FileEntry{
			Path:   finalPath,
			SHA256: hash,
			Mode:   octal(info.Mode()),
			Owner:  ownerFor(finalPath),
		})
		return nil
	})
}

func (a *Artifact) sort() {
	sort.Slice(a.Dirs, func(i, j int) bool { return a.Dirs[i].Path < a.Dirs[j].Path })
	sort.Slice(a.Files, func(i, j int) bool { return a.Files[i].Path < a.Files[j].Path })
}

func (a *Artifact) marshal() ([]byte, error) {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type nonRegularError struct{ path string }

func (e nonRegularError) Error() string {
	return "staging refuses a non-regular staged entry: " + e.path
}

func errNonRegular(path string) error { return nonRegularError{path: path} }
