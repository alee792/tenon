package agentproject

// Harness-specific files are intentionally nonportable native project files
// copied byte-for-byte to only their selected harness at the same
// workspace-relative paths. Tenon does not parse, merge, or validate their
// semantics: bytes are opaque, and validation is limited to structure,
// bounds, and refusing tenon-owned destinations.

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
)

// Harness-specific file bounds (ADR 0013): safety ceilings, not ordinary-use
// quotas. Violations fail before any workspace mutation. Each recognized
// harness's subtree is bounded independently.
const (
	// MaxHarnessFiles bounds the files under one harness's subtree.
	MaxHarnessFiles = 1024
	// MaxHarnessFileBytes bounds one harness-specific file.
	MaxHarnessFileBytes = 1 << 20
	// MaxHarnessSetBytes bounds one harness's subtree in aggregate.
	MaxHarnessSetBytes = 8 << 20
)

// harnessDotDirs maps each recognized immediate harnesses/ entry to the one
// native directory it may carry.
var harnessDotDirs = map[string]string{
	"claude": ".claude",
	"codex":  ".codex",
}

// HarnessFile is one validated harness-specific file.
type HarnessFile struct {
	// RelPath is workspace-relative, e.g. ".claude/settings.json".
	RelPath string
	// Content is the exact authored bytes.
	Content []byte
	// Executable is the authored executable intent.
	Executable bool
}

// loadHarnessFiles discovers and validates harnesses/, returning every
// recognized harness's files sorted by RelPath and keyed by harness name,
// plus every file as a fingerprint input. Invalid harness files reject the
// project: they are authored project source, not isolatable plugin
// components.
func loadHarnessFiles(root string, diags *diagnostics.List) (map[string][]HarnessFile, []sourceInput) {
	dir := filepath.Join(root, "harnesses")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("harnessfile.entry.invalid", "harnesses",
			"harnesses must be a real directory; symlinks are never followed")
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("harnessfile.entry.invalid", "harnesses", "harnesses could not be read: %v", err)
		return nil, nil
	}

	result := map[string][]HarnessFile{}
	var inputs []sourceInput
	for _, entry := range entries {
		entryPath := "harnesses/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("harnessfile.entry.invalid", entryPath,
				"each harnesses entry must be a real harness directory; symlinks are never followed")
			continue
		}
		dotDir, ok := harnessDotDirs[entry.Name()]
		if !ok {
			diags.Errorf("harnessfile.harness.unknown", entryPath,
				"harnesses recognizes only claude and codex; found %q", entry.Name())
			continue
		}
		if !entry.IsDir() {
			diags.Errorf("harnessfile.entry.invalid", entryPath,
				"%s must be a real directory", entryPath)
			continue
		}
		files, harnessInputs := loadHarnessDir(dir, entry.Name(), dotDir, diags)
		inputs = append(inputs, harnessInputs...)
		if len(files) > 0 {
			result[entry.Name()] = files
		}
	}
	if len(result) == 0 {
		return nil, inputs
	}
	return result, inputs
}

// loadHarnessDir validates one immediate harnesses/<harness> directory: that
// it carries only the harness's one native dot-directory, then walks that
// subtree for structure, reserved destinations, and bounds. Every regular
// file is returned as a fingerprint input regardless of validity so identity
// tracks exactly what was authored.
func loadHarnessDir(harnessesDir, harnessName, dotDir string, diags *diagnostics.List) ([]HarnessFile, []sourceInput) {
	sourcePrefix := "harnesses/" + harnessName
	full := filepath.Join(harnessesDir, harnessName)
	entries, err := os.ReadDir(full)
	if err != nil {
		diags.Errorf("harnessfile.entry.invalid", sourcePrefix, "%s could not be read: %v", sourcePrefix, err)
		return nil, nil
	}

	var dotEntry os.DirEntry
	for _, entry := range entries {
		if entry.Name() == dotDir {
			dotEntry = entry
			continue
		}
		diags.Errorf("harnessfile.entry.invalid", sourcePrefix+"/"+entry.Name(),
			"%s supports only %s; found %q", sourcePrefix, dotDir, entry.Name())
	}
	if dotEntry == nil {
		return nil, nil // optional: this harness carries no native files
	}
	if dotEntry.Type()&os.ModeSymlink != 0 || !dotEntry.IsDir() {
		diags.Errorf("harnessfile.entry.invalid", sourcePrefix+"/"+dotDir,
			"%s must be a real directory; symlinks are never followed", dotDir)
		return nil, nil
	}

	var files []HarnessFile
	var inputs []sourceInput
	fileCount := 0
	var aggregateBytes int64
	countExceeded, bytesExceeded := false, false

	dotRoot := filepath.Join(full, dotDir)
	walkErr := filepath.WalkDir(dotRoot, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if fp == dotRoot {
			return nil
		}
		rel, err := filepath.Rel(dotRoot, fp)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		relPath := dotDir + "/" + rel
		sourcePath := sourcePrefix + "/" + relPath

		if !utf8.ValidString(rel) {
			diags.Errorf("harnessfile.file.invalid", sourcePath,
				"every path inside harnesses/%s must be valid UTF-8; found %q", harnessName, rel)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if harnessReservedPath(harnessName, rel) {
			diags.Errorf("harnessfile.path.reserved", sourcePath,
				"%s is a tenon-owned destination and cannot be authored under harnesses/%s", relPath, harnessName)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.Type()&os.ModeSymlink != 0 {
			diags.Errorf("harnessfile.file.invalid", sourcePath,
				"every harness-file entry must be a real directory or regular file; symlinks are never followed")
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			diags.Errorf("harnessfile.file.invalid", sourcePath,
				"every harness-file entry must be a real directory or regular file")
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		// Size bounds come from file metadata so an out-of-bounds file is
		// rejected before it is read.
		readable := true
		if info.Size() > MaxHarnessFileBytes {
			diags.Errorf("harnessfile.bounds.exceeded", sourcePath,
				"a harness-specific file may contain at most %d bytes; found %d", MaxHarnessFileBytes, info.Size())
			readable = false
		}

		fileCount++
		if fileCount == MaxHarnessFiles+1 {
			diags.Errorf("harnessfile.bounds.exceeded", sourcePrefix,
				"harnesses/%s may contain at most %d selected files", harnessName, MaxHarnessFiles)
			countExceeded = true
		}

		aggregateBytes += info.Size()
		if !bytesExceeded && aggregateBytes > MaxHarnessSetBytes {
			diags.Errorf("harnessfile.bounds.exceeded", sourcePrefix,
				"harnesses/%s may contain at most %d bytes in aggregate", harnessName, MaxHarnessSetBytes)
			bytesExceeded = true
		}

		var content []byte
		if readable && !countExceeded && !bytesExceeded {
			content, err = os.ReadFile(fp)
			if err != nil {
				diags.Errorf("harnessfile.file.invalid", sourcePath,
					"the harness-specific file could not be read: %v", err)
			}
		}
		executable := info.Mode().Perm()&0o111 != 0
		files = append(files, HarnessFile{RelPath: relPath, Content: content, Executable: executable})
		inputs = append(inputs, sourceInput{Path: sourcePath, Content: content, Executable: executable})
		return nil
	})
	if walkErr != nil {
		diags.Errorf("harnessfile.file.invalid", sourcePrefix+"/"+dotDir,
			"the harness directory could not be read: %v", walkErr)
	}

	slices.SortFunc(files, func(a, b HarnessFile) int { return strings.Compare(a.RelPath, b.RelPath) })
	return files, inputs
}

// harnessReservedPath reports whether rel, a slash-separated path relative
// to the harness's native dot-directory (e.g. "skills/foo.md" under
// ".claude"), names a tenon-owned destination. Path segments compare
// case-insensitively so a case-folded alias cannot escape the reservation.
func harnessReservedPath(harnessName, rel string) bool {
	segs := strings.Split(rel, "/")
	switch harnessName {
	case "claude":
		return strings.EqualFold(segs[0], "skills") || strings.EqualFold(segs[0], "agents")
	case "codex":
		if strings.EqualFold(segs[0], "agents") {
			return true
		}
		return len(segs) == 1 && strings.EqualFold(segs[0], "config.toml")
	}
	return false
}
