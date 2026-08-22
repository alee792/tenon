// Package toolruntime prepares and serves an agent project's authored tools.
// Preparation happens once per apply: it materializes tenon's own language
// hosts into a workspace cache keyed by the source fingerprint, drives each
// language's native toolchain against the author's own lockfiles, and then
// inspects the result by starting each host once and reading its catalog.
// Serving reuses exactly that cache: one long-lived host per authored
// language, launched from recorded absolute executables, answering calls over
// tenon's bounded host protocol.
//
// Authors never write protocol code and never depend on tenon: a tool is a
// TypeScript, Python, or Go file that declares a description, two schemas, and
// a function. Everything between that file and the managed MCP boundary —
// bounds, deadlines, validation, process lifetime — is tenon's.
package toolruntime

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alee792/tenon/internal/agentproject"
)

// Language hosts are embedded, not vendored into the agent project: the
// author's tools directory stays exactly what the author wrote.
//
//go:embed hosts/typescript.ts
var typescriptHost []byte

//go:embed hosts/python.py
var pythonHost []byte

//go:embed hosts/go/main.go.tmpl
var goHostMain string

//go:embed hosts/go/go.mod.tmpl
var goHostMod string

// The three authored tool languages.
const (
	TypeScript = "typescript"
	Python     = "python"
	Go         = "go"
)

const (
	// InspectTimeout bounds the catalog request tenon issues when it starts
	// a host, during preparation and at every open.
	InspectTimeout = 10 * time.Second
	// CallDeadline bounds one authored tool call. A tool that overruns it
	// takes its whole language host down: tenon cannot interrupt authored
	// code, so it refuses to keep serving from a process it lost track of.
	CallDeadline = 30 * time.Second
	// MaxDescriptionBytes bounds one reported tool description.
	MaxDescriptionBytes = 1024
	// MaxSchemaBytes bounds one reported JSON Schema.
	MaxSchemaBytes = 64 << 10
	// hostCacheKeyChars is how much of the host-implementation digest names
	// the cache directory: enough to separate tenon versions, short enough
	// to keep the path readable.
	hostCacheKeyChars = 12
)

// Config identifies the project whose tools are prepared or served.
type Config struct {
	// Source is the absolute agent source root.
	Source string
	// Workspace is the absolute workspace directory. Hosts run with it as
	// their working directory, and the tool cache lives beneath it.
	Workspace string
	// Fingerprint is the agent project's source fingerprint. It names the
	// cache, so tools prepared for other source never serve this one.
	Fingerprint string
	// Tools are the statically discovered authored tools. The catalog a
	// host reports must match them exactly.
	Tools []agentproject.Tool
	// CacheRoot overrides the workspace cache location. Validate prepares
	// into a throwaway directory so it writes nothing into the workspace.
	CacheRoot string
}

// Failure is a bounded preparation or inspection failure. Its message names
// the language and the step, never a credential, an authored argument, or raw
// process output.
type Failure struct {
	// Phase is "prepare" or "inspect".
	Phase string
	// Language is the language whose host failed, or "" when the failure
	// belongs to no single language.
	Language string
	// Message is the bounded sentence to report.
	Message string
}

func (f *Failure) Error() string { return f.Message }

func prepareFailure(language, format string, args ...any) *Failure {
	return &Failure{Phase: "prepare", Language: language, Message: fmt.Sprintf(format, args...)}
}

func inspectFailure(language, format string, args ...any) *Failure {
	return &Failure{Phase: "inspect", Language: language, Message: fmt.Sprintf(format, args...)}
}

// languages returns the authored languages present in cfg, in a stable order.
func (c Config) languages() []string {
	present := map[string]bool{}
	for _, t := range c.Tools {
		present[t.Language] = true
	}
	var out []string
	for _, language := range []string{TypeScript, Python, Go} {
		if present[language] {
			out = append(out, language)
		}
	}
	return out
}

// expected returns the tool names discovery found, per language.
func (c Config) expected() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, t := range c.Tools {
		if out[t.Language] == nil {
			out[t.Language] = map[string]bool{}
		}
		out[t.Language][t.Name] = true
	}
	return out
}

// goToolDirs returns each Go tool's directory name (the authored directory,
// not the exposed tool name) paired with its exposed name, sorted.
func (c Config) goToolDirs() [][2]string {
	var out [][2]string
	for _, t := range c.Tools {
		if t.Language != Go {
			continue
		}
		out = append(out, [2]string{strings.TrimPrefix(t.SourcePath, "tools/"), t.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// validate checks the caller-supplied configuration. A relative path or a
// missing fingerprint is a caller bug, not an authored contract violation.
func (c Config) validate() error {
	if !filepath.IsAbs(c.Source) || !filepath.IsAbs(c.Workspace) {
		return fmt.Errorf("the tool runtime requires absolute source and workspace paths")
	}
	if c.Fingerprint == "" {
		return fmt.Errorf("the tool runtime requires the agent source fingerprint")
	}
	if len(c.Tools) == 0 {
		return fmt.Errorf("the tool runtime requires at least one discovered tool")
	}
	return nil
}

// CacheDir is where preparation writes and serving reads: one directory per
// source fingerprint and host implementation, so stale tools are never served
// and a tenon upgrade never reuses the previous release's hosts.
func (c Config) CacheDir() string {
	root := c.CacheRoot
	if root == "" {
		root = filepath.Join(c.Workspace, ".tenon", "cache", "tools")
	}
	return filepath.Join(root, hexFingerprint(c.Fingerprint)+"-"+hostsDigest())
}

// hexFingerprint strips the algorithm prefix so the cache directory name is
// one path element.
func hexFingerprint(fingerprint string) string {
	if _, hexPart, found := strings.Cut(fingerprint, ":"); found {
		return hexPart
	}
	return fingerprint
}

// hostsDigest identifies the embedded host implementations.
func hostsDigest() string {
	sum := sha256.New()
	for _, content := range [][]byte{typescriptHost, pythonHost, []byte(goHostMain), []byte(goHostMod)} {
		fmt.Fprintf(sum, "%d\n", len(content))
		sum.Write(content)
	}
	return hex.EncodeToString(sum.Sum(nil))[:hostCacheKeyChars]
}
