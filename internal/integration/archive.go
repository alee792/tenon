package integration

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extraction bounds. A malicious or corrupt source can neither exhaust the
// filesystem nor create an unbounded number of entries.
const (
	maxPayloadBytes = 256 * 1024 * 1024
	maxArchiveFiles = 8192
)

// stageSource materializes an install source into a fresh owner-only temporary
// directory and returns it with a cleanup. A directory is safely copied; a
// .tar.gz or .zip is safely extracted. Every path is contained, every entry is
// a regular file or directory (symlinks, devices, and other special files are
// refused), and the total byte count and entry count are bounded. The staging
// tree is where installation may finally read artifacts — metadata validation
// already ran against no filesystem.
func stageSource(source string) (string, func(), error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", nil, storeErrorf("install.source.invalid", "the install source could not be read: %s", boundErr(err))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, storeErrorf("install.source.invalid", "the install source must not be a symlink")
	}
	if !ownedByCaller(info) {
		return "", nil, storeErrorf("install.source.invalid", "the install source must be owned by the invoking user")
	}

	dst, err := os.MkdirTemp("", "tenon-integration-stage-")
	if err != nil {
		return "", nil, storeErrorf("install.source.invalid", "a staging directory could not be created: %s", boundErr(err))
	}
	if err := os.Chmod(dst, 0o700); err != nil {
		os.RemoveAll(dst)
		return "", nil, storeErrorf("install.source.invalid", "the staging directory could not be secured: %s", boundErr(err))
	}
	cleanup := func() { os.RemoveAll(dst) }

	switch {
	case info.IsDir():
		err = safeCopyDir(source, dst)
	case strings.HasSuffix(source, ".tar.gz") || strings.HasSuffix(source, ".tgz"):
		err = extractTarGz(source, dst)
	case strings.HasSuffix(source, ".zip"):
		err = extractZip(source, dst)
	default:
		err = storeErrorf("install.source.invalid", "the install source must be a directory, a .tar.gz, or a .zip")
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return dst, cleanup, nil
}

// safeCopyDir copies a directory tree into dst, refusing symlinks and special
// files and bounding the total size and entry count.
func safeCopyDir(src, dst string) error {
	var total int64
	var count int
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return storeErrorf("install.source.invalid", "the install source could not be walked: %s", boundErr(err))
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return storeErrorf("install.source.invalid", "a source path could not be made relative: %s", boundErr(err))
		}
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return storeErrorf("install.source.symlink", "the install source contains a symlink at %q, which is never followed", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		if !d.Type().IsRegular() {
			return storeErrorf("install.source.special", "the install source contains a non-regular file at %q", rel)
		}
		count++
		if count > maxArchiveFiles {
			return storeErrorf("install.source.too-large", "the install source contains more than %d files", maxArchiveFiles)
		}
		info, err := d.Info()
		if err != nil {
			return storeErrorf("install.source.invalid", "a source file could not be stat'd: %s", boundErr(err))
		}
		total += info.Size()
		if total > maxPayloadBytes {
			return storeErrorf("install.source.too-large", "the install source exceeds %d bytes", maxPayloadBytes)
		}
		return copyRegular(p, filepath.Join(dst, rel))
	})
}

func copyRegular(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return storeErrorf("install.source.invalid", "a source file could not be opened: %s", boundErr(err))
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func extractTarGz(source, dst string) error {
	f, err := os.Open(source)
	if err != nil {
		return storeErrorf("install.source.invalid", "the archive could not be opened: %s", boundErr(err))
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return storeErrorf("install.source.invalid", "the archive is not valid gzip: %s", boundErr(err))
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var total int64
	var count int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return storeErrorf("install.source.invalid", "the tar stream could not be read: %s", boundErr(err))
		}
		target, err := safeJoin(dst, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			count++
			if count > maxArchiveFiles {
				return storeErrorf("install.source.too-large", "the archive contains more than %d files", maxArchiveFiles)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			n, err := io.CopyN(out, tr, maxPayloadBytes-total+1)
			out.Close()
			if err != nil && err != io.EOF {
				return storeErrorf("install.source.invalid", "an archive entry could not be written: %s", boundErr(err))
			}
			total += n
			if total > maxPayloadBytes {
				return storeErrorf("install.source.too-large", "the archive expands beyond %d bytes", maxPayloadBytes)
			}
		default:
			return storeErrorf("install.source.special", "the archive contains a non-regular entry %q; symlinks, devices, and links are refused", hdr.Name)
		}
	}
}

func extractZip(source, dst string) error {
	r, err := zip.OpenReader(source)
	if err != nil {
		return storeErrorf("install.source.invalid", "the zip could not be opened: %s", boundErr(err))
	}
	defer r.Close()

	var total int64
	var count int
	for _, zf := range r.File {
		target, err := safeJoin(dst, zf.Name)
		if err != nil {
			return err
		}
		mode := zf.Mode()
		if mode&os.ModeSymlink != 0 {
			return storeErrorf("install.source.special", "the zip contains a symlink %q, which is never followed", zf.Name)
		}
		if strings.HasSuffix(zf.Name, "/") || mode.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return storeErrorf("install.source.special", "the zip contains a non-regular entry %q", zf.Name)
		}
		count++
		if count > maxArchiveFiles {
			return storeErrorf("install.source.too-large", "the zip contains more than %d files", maxArchiveFiles)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return storeErrorf("install.source.invalid", "a zip entry could not be opened: %s", boundErr(err))
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			rc.Close()
			return err
		}
		n, err := io.CopyN(out, rc, maxPayloadBytes-total+1)
		out.Close()
		rc.Close()
		if err != nil && err != io.EOF {
			return storeErrorf("install.source.invalid", "a zip entry could not be written: %s", boundErr(err))
		}
		total += n
		if total > maxPayloadBytes {
			return storeErrorf("install.source.too-large", "the zip expands beyond %d bytes", maxPayloadBytes)
		}
	}
	return nil
}

// safeJoin resolves an archive entry name under dst, refusing an absolute path
// or a parent-directory escape so no entry can be written outside the tree.
func safeJoin(dst, name string) (string, error) {
	if name == "" {
		return "", storeErrorf("install.source.traversal", "the archive contains an empty entry name")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return "", storeErrorf("install.source.traversal", "the archive contains an absolute path %q", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", storeErrorf("install.source.traversal", "the archive contains a parent-directory escape %q", name)
	}
	full := filepath.Join(dst, clean)
	rel, err := filepath.Rel(dst, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", storeErrorf("install.source.traversal", "the archive entry %q escapes the staging root", name)
	}
	return full, nil
}
