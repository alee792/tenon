package stage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/apply"
	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/version"
)

// generateIntegration re-renders the native harness integration for the final
// runtime paths and writes it under <tmp>/workspace, alongside an apply record
// whose embedded identity is the final paths, never the physical build tree.
//
// It reuses the harness driver unchanged by handing it a shallow copy of the
// project whose Root is the final agent-source path and a Target whose
// Workspace and Executable are the final paths. The driver embeds exactly
// those in the generated MCP configuration. The physical files still land
// under <tmp>/workspace, so this deliberately does not call apply.Apply, which
// would require an existing workspace and would record the physical source
// path: internal/apply's workspace semantics are left intact.
//
// closureRootFinal is the final canonical directory the tool runtime closure
// is staged under (finalRuntimes+"/tools"), or "" for a tool-free agent. When
// set, it is recorded on the apply record as a path relative to the final
// workspace (ADR 0021): the staged apply record names the closure root it was
// published with, so serving can honor it instead of assuming the ordinary
// workspace-cache layout.
func generateIntegration(p *agentproject.Project, driver apply.Driver, finalAgentSource, closureRootFinal, tmp string, diags *diagnostics.List) error {
	staged := *p
	staged.Root = finalAgentSource

	target := apply.Target{
		Workspace:  finalWorkspace,
		Executable: finalTenonBin,
		// Staging does not resolve the operator's integration-package store:
		// an installed connection would embed operator paths that are not
		// portable into the staged tree. Passing no store makes any installed
		// connection fail to resolve with a clear diagnostic, and staging then
		// fails closed before writing.
		IntegrationStore: "",
		TenonVersion:     version.Version,
	}
	files := driver.Generate(&staged, target, diags)
	if diags.HasErrors() {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	record := apply.Record{
		Schema:      1,
		Agent:       p.Name,
		Source:      finalAgentSource,
		Harness:     driver.Harness(),
		Fingerprint: p.Fingerprint,
		GitCommit:   apply.CleanHeadCommit(p.Root),
		Files:       map[string]apply.OwnedFile{},
	}
	if closureRootFinal != "" {
		rel, err := filepath.Rel(finalWorkspace, closureRootFinal)
		if err != nil {
			return fmt.Errorf("relating the closure root to the workspace: %w", err)
		}
		record.ClosureRoot = filepath.ToSlash(rel)
	}
	for _, f := range files {
		mode := os.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		dst := filepath.Join(physical(tmp, finalWorkspace), filepath.FromSlash(f.Path))
		if err := writeFileMode(dst, f.Content, mode); err != nil {
			return fmt.Errorf("writing generated %s: %w", f.Path, err)
		}
		record.Files[f.Path] = apply.OwnedFile{Hash: sha256Prefixed(f.Content), Executable: f.Executable}
	}

	recordBytes, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the apply record: %w", err)
	}
	// apply.RecordPath places the record under <workspace>/.tenon; the final
	// workspace is /workspace, so the record is written at the physical mirror
	// of /workspace/.tenon/apply-<harness>.json.
	recordFinal := apply.RecordPath(finalWorkspace, driver.Harness())
	if err := writeFileMode(physical(tmp, recordFinal), append(recordBytes, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing the apply record: %w", err)
	}
	return nil
}
