package stage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alee792/tenon/internal/agentproject"
)

// Verify checks a staged tree against its artifact manifest, entirely offline.
// artifactPath is the manifest to read; prefix is prepended to every final
// path, empty at real container runtime (where the tree lives at its canonical
// locations) and set to the physical staging directory in tests.
//
// It proves runtime identity, the generated integration, and the immutable
// source together: every recorded file is present at its final path as a
// regular file with the recorded mode and content hash, and the agent source
// reloaded from the staged tree still fingerprints to the recorded value. Any
// mismatch returns an error, so a container opened against a drifted tree
// fails closed before a turn.
func Verify(artifactPath, prefix string) error {
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("the artifact manifest could not be read: %w", err)
	}
	var a Artifact
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return fmt.Errorf("the artifact manifest is malformed: %w", err)
	}
	if a.SchemaVersion != SchemaVersion {
		return fmt.Errorf("the artifact manifest schema %d is not supported by this tenon", a.SchemaVersion)
	}
	if len(a.Files) == 0 {
		return fmt.Errorf("the artifact manifest records no staged files")
	}

	for _, f := range a.Files {
		phys := filepath.Join(prefix, filepath.FromSlash(f.Path))
		info, err := os.Lstat(phys)
		if err != nil {
			return fmt.Errorf("the staged file %s is missing", f.Path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("the staged file %s is no longer a regular file", f.Path)
		}
		if octal(info.Mode()) != f.Mode {
			return fmt.Errorf("the staged file %s has mode %s, expected %s", f.Path, octal(info.Mode()), f.Mode)
		}
		hash, err := hashFile(phys)
		if err != nil {
			return fmt.Errorf("the staged file %s could not be read: %w", f.Path, err)
		}
		if hash != f.SHA256 {
			return fmt.Errorf("the staged file %s was modified since staging", f.Path)
		}
	}

	source, ok := a.Layout["agent_source"]
	if !ok {
		return fmt.Errorf("the artifact manifest records no agent source path")
	}
	sourceDir := filepath.Join(prefix, filepath.FromSlash(source))
	p, diags, err := agentproject.Load(sourceDir)
	if err != nil {
		return fmt.Errorf("the staged agent source could not be read: %w", err)
	}
	if p == nil || diags.HasErrors() {
		return fmt.Errorf("the staged agent source no longer validates")
	}
	if p.Fingerprint != a.SourceFingerprint {
		return fmt.Errorf("the staged agent source fingerprint %s does not match the manifest %s", p.Fingerprint, a.SourceFingerprint)
	}
	return nil
}
