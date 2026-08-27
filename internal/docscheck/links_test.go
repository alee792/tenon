package docscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// linkPattern matches Markdown link targets: the text between "](" and ")".
var linkPattern = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// TestRelativeLinksResolve walks every Markdown file under docs/ plus the
// root README.md, AGENTS.md, CHANGELOG.md, CONTRIBUTING.md, SECURITY.md,
// and examples/README.md, and fails if any relative link target does not
// exist on disk.
func TestRelativeLinksResolve(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	files := []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(root, "CHANGELOG.md"),
		filepath.Join(root, "CONTRIBUTING.md"),
		filepath.Join(root, "SECURITY.md"),
		filepath.Join(root, "examples", "README.md"),
	}
	err = filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, match := range linkPattern.FindAllStringSubmatch(string(content), -1) {
			target := match[1]
			if strings.HasPrefix(target, "http") ||
				strings.HasPrefix(target, "mailto:") ||
				strings.HasPrefix(target, "#") {
				continue
			}
			if i := strings.Index(target, "#"); i >= 0 {
				target = target[:i]
			}
			if target == "" {
				continue
			}
			resolved := filepath.Join(filepath.Dir(file), filepath.FromSlash(target))
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s: link target %q does not exist (resolved %s)", file, match[1], resolved)
			}
		}
	}
}
