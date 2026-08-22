package stage

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// sha256Hex hashes bytes to a bare lowercase hex digest.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

// sha256Prefixed hashes bytes to the "sha256:<hex>" form the apply record
// uses, so a staged apply record is byte-identical to one apply would write.
func sha256Prefixed(b []byte) string {
	return "sha256:" + sha256Hex(b)
}

// hashFile hashes a regular file's contents to a bare hex digest.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// writeFileMode writes content at path, creating parents, at exactly mode
// (defeating the umask).
func writeFileMode(path string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// copyFile copies a regular file's contents to dst at exactly mode.
func copyFile(src, dst string, mode fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileMode(dst, data, mode)
}

// copyTree copies a directory tree byte-for-byte, preserving each regular
// file's executable bit and directory structure. Symlinks and other
// non-regular entries are rejected: the staged tree never carries one.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("staging refuses a non-regular entry: %s", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		return copyFile(path, target, mode)
	})
}

// copySource copies the immutable agent source byte-for-byte into dst. It
// rejects symlinks (as everywhere else in tenon) and skips a top-level
// .tenon directory, which is workspace state, not authored source (ADR 0003).
func copySource(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if rel == ".tenon" && d.IsDir() {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("staging refuses a symlink in agent source: %s", filepath.ToSlash(rel))
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("staging refuses a non-regular entry in agent source: %s", filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		return copyFile(path, target, mode)
	})
}
