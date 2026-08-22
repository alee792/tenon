// Package integration is the vendor-neutral integration-package store slice:
// operator-installed, metadata-first, process-isolated third-party
// integrations (ADR 0014). A bounded schema-versioned manifest describes a
// package's identity, provenance, a half-open tenon compatibility range, exact
// platform artifacts, and closed versioned capability declarations. Metadata
// validation opens no artifact, resolves no symlink, fetches no URL, inspects
// no archive, and starts no process; installation and content verification are
// a separate, still-offline phase. The store is owner-only, content-addressed,
// and shared across agents and workspaces, and it hands a later consumer a
// credential-free launch descriptor without ever resolving an ambient value or
// launching what it installs.
package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SchemaVersion is the only manifest schema this core recognizes.
const SchemaVersion = 1

// MaxManifestBytes bounds one manifest. The SHA-256 of these exact bytes is the
// package's immutable identity, so the envelope stays small and legible.
const MaxManifestBytes = 128 * 1024

const (
	maxShortField = 256
	maxDescField  = 1024
)

// stableID is the shared grammar for a stable identifier — package id,
// artifact id, capability id, and native server name. It doubles as a store
// directory component, so it is deliberately conservative.
var stableID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// platformToken is one exact GOOS or GOARCH token.
var platformToken = regexp.MustCompile(`^[a-z0-9]+$`)

// lowerHex64 is a lowercase hex SHA-256 digest.
var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// envName is the portable environment-variable-name grammar for a non-secret
// default and a required-ambient name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// proseDescription is the closed prose alphabet for a required-ambient
// description: ASCII letters, spaces, and a bounded punctuation set, starting
// with a letter. Underscores, equals signs, braces, and URI punctuation are
// deliberately absent so a description can never smuggle a value or reference.
var proseDescription = regexp.MustCompile(`^[A-Za-z][A-Za-z ,.;()'-]*$`)

// secretNameMarker flags an environment-variable name whose meaning is a
// credential. Such a variable must be required-ambient, never carry a default.
var secretNameMarker = regexp.MustCompile(`(?i)(secret|token|password|passwd|credential|api[_-]?key|access[_-]?key|private[_-]?key|\bpat\b)`)

// Manifest is a validated integration-package manifest. The exact source bytes
// are retained privately: the immutable identity and every capability
// selection derive from them, never from a caller-mutable copy.
type Manifest struct {
	raw    []byte
	shaHex string

	SchemaVersion int
	ID            string
	Version       string
	Name          string
	Description   string
	License       string
	Source        string
	Revision      string
	Compat        Compat
	Artifacts     []Artifact
	Capabilities  []Capability
}

// Compat is the half-open tenon-version compatibility range: a tenon version v
// is compatible when Minimum <= v < Before.
type Compat struct {
	Minimum string
	Before  string
}

// Artifact is one exact platform artifact, pinned by size and SHA-256 for both
// the raw payload and the prepared executable identity. Source is a closed
// union: exactly one of a package-relative payload path or one exact HTTPS URL.
type Artifact struct {
	ID         string
	OS         string
	Arch       string
	Format     string // "binary", "tar.gz", or "zip"
	Size       int64
	SHA256     string
	ExecPath   string
	ExecSize   int64
	ExecSHA256 string
	Package    string // package-relative payload path, mutually exclusive with HTTPS
	HTTPS      string // exact HTTPS URL, mutually exclusive with Package
}

// Capability is one closed, tagged, integer-versioned capability declaration.
// Schema 1 recognizes only type "native-mcp" version 1; NativeMCP is non-nil
// exactly then.
type Capability struct {
	ID        string
	Type      string
	Version   int
	NativeMCP *NativeMCP
}

// NativeMCP is a native stdio MCP server capability: a stable native server
// name, the artifact closure and executable identity, bounded literal launch
// data, required ambient environment-variable names (never values), and
// per-harness targets.
type NativeMCP struct {
	ServerName  string
	Artifacts   []string
	Executable  string
	Args        []string
	Workdir     string
	Env         map[string]string
	RequiredEnv []RequiredEnv
	Targets     Targets
}

// RequiredEnv is one required ambient environment variable: its name is
// diagnostic metadata, not a credential channel, and its description is bounded
// closed prose. A value is never carried.
type RequiredEnv struct {
	Name        string
	Description string
}

// Targets is the per-harness support map; at least one harness is present.
type Targets struct {
	Claude *HarnessTarget
	Codex  *HarnessTarget
}

// HarnessTarget declares one harness's startup policy for the server.
type HarnessTarget struct {
	Startup string // "optional" or "required"
}

// Raw returns a defensive copy of the exact manifest bytes. Capability
// selection derives from the retained bytes, so callers never mutate identity.
func (m *Manifest) Raw() []byte {
	out := make([]byte, len(m.raw))
	copy(out, m.raw)
	return out
}

// SHA256 is the lowercase-hex SHA-256 of the exact manifest bytes: the
// package's immutable identity. Any change, including reformatting, is a
// different identity.
func (m *Manifest) SHA256() string { return m.shaHex }

type manifestWire struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license"`
	Source        string            `json:"source"`
	Revision      string            `json:"revision"`
	Compat        compatWire        `json:"compat"`
	Artifacts     []artifactWire    `json:"artifacts"`
	Capabilities  []json.RawMessage `json:"capabilities"`
}

type compatWire struct {
	Minimum string `json:"minimum"`
	Before  string `json:"before"`
}

type artifactWire struct {
	ID         string `json:"id"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Format     string `json:"format"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	ExecPath   string `json:"exec_path"`
	ExecSize   int64  `json:"exec_size"`
	ExecSHA256 string `json:"exec_sha256"`
	Package    string `json:"package"`
	HTTPS      string `json:"https"`
}

type capabilityTag struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Version int    `json:"version"`
}

type nativeMCPWire struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Version     int               `json:"version"`
	ServerName  string            `json:"server_name"`
	Artifacts   []string          `json:"artifacts"`
	Executable  string            `json:"executable"`
	Args        []string          `json:"args"`
	Workdir     string            `json:"workdir"`
	Env         map[string]string `json:"env"`
	RequiredEnv []requiredEnvWire `json:"required_env"`
	Targets     targetsWire       `json:"targets"`
}

type requiredEnvWire struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type targetsWire struct {
	Claude *harnessTargetWire `json:"claude"`
	Codex  *harnessTargetWire `json:"codex"`
}

type harnessTargetWire struct {
	Startup string `json:"startup"`
}

// ParseManifest validates raw manifest bytes and returns the parsed manifest.
// It reads no artifact: it resolves no symlink, fetches no URL, inspects no
// archive, and starts no process. An unrecognized capability type or version
// is a typed UnsupportedCapabilityError; every other rejection is a typed
// ManifestError carrying a stable code.
func ParseManifest(raw []byte) (*Manifest, error) {
	if len(raw) == 0 {
		return nil, manifestErrorf("manifest.empty", "the manifest is empty")
	}
	if len(raw) > MaxManifestBytes {
		return nil, manifestErrorf("manifest.too-large",
			"the manifest may contain at most %d bytes; found %d", MaxManifestBytes, len(raw))
	}
	if !utf8.Valid(raw) {
		return nil, manifestErrorf("manifest.encoding", "the manifest must be valid UTF-8")
	}

	// The schema version is checked before strict decoding so an old or future
	// schema is refused as such rather than as an unknown-field decode error.
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, manifestErrorf("manifest.decode", "the manifest is not valid JSON: %s", boundErr(err))
	}
	if probe.SchemaVersion != SchemaVersion {
		return nil, manifestErrorf("manifest.schema_version.unsupported",
			"schema_version must be %d; found %d", SchemaVersion, probe.SchemaVersion)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w manifestWire
	if err := dec.Decode(&w); err != nil {
		return nil, manifestErrorf("manifest.decode", "the manifest could not be decoded strictly: %s", boundErr(err))
	}
	if dec.More() {
		return nil, manifestErrorf("manifest.decode", "the manifest carries trailing data after the top-level object")
	}

	m := &Manifest{SchemaVersion: w.SchemaVersion}
	if !stableID.MatchString(w.ID) {
		return nil, manifestErrorf("manifest.id.invalid",
			"id must match %s; found %q", stableID.String(), w.ID)
	}
	m.ID = w.ID
	if _, ok := parseSemver(w.Version); !ok {
		return nil, manifestErrorf("manifest.version.invalid", "version must be an exact semantic version; found %q", w.Version)
	}
	m.Version = w.Version
	if err := requireShort("manifest.name.invalid", "name", w.Name); err != nil {
		return nil, err
	}
	m.Name = w.Name
	if w.Description == "" || len(w.Description) > maxDescField || !utf8.ValidString(w.Description) {
		return nil, manifestErrorf("manifest.description.invalid",
			"description must be non-empty UTF-8 of at most %d bytes", maxDescField)
	}
	m.Description = w.Description
	if err := requireShort("manifest.license.invalid", "license", w.License); err != nil {
		return nil, err
	}
	m.License = w.License
	if err := requireShort("manifest.source.invalid", "source", w.Source); err != nil {
		return nil, err
	}
	m.Source = w.Source
	if err := requireShort("manifest.revision.invalid", "revision", w.Revision); err != nil {
		return nil, err
	}
	m.Revision = w.Revision

	compat, err := validateCompat(w.Compat)
	if err != nil {
		return nil, err
	}
	m.Compat = compat

	artifacts, byID, err := validateArtifacts(w.Artifacts)
	if err != nil {
		return nil, err
	}
	m.Artifacts = artifacts

	caps, err := validateCapabilities(w.Capabilities, byID)
	if err != nil {
		return nil, err
	}
	m.Capabilities = caps

	sum := sha256.Sum256(raw)
	m.shaHex = hex.EncodeToString(sum[:])
	m.raw = make([]byte, len(raw))
	copy(m.raw, raw)
	return m, nil
}

func validateCompat(w compatWire) (Compat, error) {
	min, ok := parseSemver(w.Minimum)
	if !ok {
		return Compat{}, manifestErrorf("manifest.compat.invalid", "compat.minimum must be an exact semantic version; found %q", w.Minimum)
	}
	before, ok := parseSemver(w.Before)
	if !ok {
		return Compat{}, manifestErrorf("manifest.compat.invalid", "compat.before must be an exact semantic version; found %q", w.Before)
	}
	if compareSemver(min, before) >= 0 {
		return Compat{}, manifestErrorf("manifest.compat.invalid",
			"compat is the half-open range minimum <= tenon < before; found minimum %q not below before %q", w.Minimum, w.Before)
	}
	return Compat{Minimum: w.Minimum, Before: w.Before}, nil
}

func validateArtifacts(ws []artifactWire) ([]Artifact, map[string]Artifact, error) {
	if len(ws) == 0 {
		return nil, nil, manifestErrorf("manifest.artifacts.empty", "a manifest must declare at least one artifact")
	}
	byID := make(map[string]Artifact, len(ws))
	out := make([]Artifact, 0, len(ws))
	for _, w := range ws {
		if !stableID.MatchString(w.ID) {
			return nil, nil, manifestErrorf("manifest.artifact.id.invalid", "artifact id must match %s; found %q", stableID.String(), w.ID)
		}
		if _, dup := byID[w.ID]; dup {
			return nil, nil, manifestErrorf("manifest.artifact.id.duplicate", "artifact id %q is declared more than once", w.ID)
		}
		if !platformToken.MatchString(w.OS) {
			return nil, nil, manifestErrorf("manifest.artifact.os.invalid", "artifact %q os must be an exact GOOS token; found %q", w.ID, w.OS)
		}
		if !platformToken.MatchString(w.Arch) {
			return nil, nil, manifestErrorf("manifest.artifact.arch.invalid", "artifact %q arch must be an exact GOARCH token; found %q", w.ID, w.Arch)
		}
		switch w.Format {
		case "binary", "tar.gz", "zip":
		default:
			return nil, nil, manifestErrorf("manifest.artifact.format.invalid", "artifact %q format must be binary, tar.gz, or zip; found %q", w.ID, w.Format)
		}
		if w.Size <= 0 {
			return nil, nil, manifestErrorf("manifest.artifact.size.invalid", "artifact %q size must be a positive byte count; found %d", w.ID, w.Size)
		}
		if !lowerHex64.MatchString(w.SHA256) {
			return nil, nil, manifestErrorf("manifest.artifact.sha256.invalid", "artifact %q sha256 must be lowercase hex SHA-256", w.ID)
		}
		relExec, err := normalizeRelPath(w.ExecPath)
		if err != nil || relExec == "" {
			return nil, nil, manifestErrorf("manifest.artifact.exec_path.invalid", "artifact %q exec_path must be a normalized package-relative path; found %q", w.ID, w.ExecPath)
		}
		if w.ExecSize <= 0 {
			return nil, nil, manifestErrorf("manifest.artifact.exec_size.invalid", "artifact %q exec_size must be a positive byte count; found %d", w.ID, w.ExecSize)
		}
		if !lowerHex64.MatchString(w.ExecSHA256) {
			return nil, nil, manifestErrorf("manifest.artifact.exec_sha256.invalid", "artifact %q exec_sha256 must be lowercase hex SHA-256", w.ID)
		}
		if err := validateArtifactSource(w); err != nil {
			return nil, nil, err
		}
		a := Artifact{
			ID: w.ID, OS: w.OS, Arch: w.Arch, Format: w.Format,
			Size: w.Size, SHA256: w.SHA256,
			ExecPath: relExec, ExecSize: w.ExecSize, ExecSHA256: w.ExecSHA256,
			Package: w.Package, HTTPS: w.HTTPS,
		}
		byID[a.ID] = a
		out = append(out, a)
	}
	return out, byID, nil
}

// validateArtifactSource enforces the closed source union: exactly one of a
// normalized package-relative payload path or one exact HTTPS URL without
// userinfo, query, or fragment.
func validateArtifactSource(w artifactWire) error {
	switch {
	case w.Package != "" && w.HTTPS != "":
		return manifestErrorf("manifest.artifact.source.union", "artifact %q declares both a package path and an https URL; exactly one is required", w.ID)
	case w.Package == "" && w.HTTPS == "":
		return manifestErrorf("manifest.artifact.source.union", "artifact %q declares neither a package path nor an https URL; exactly one is required", w.ID)
	case w.Package != "":
		rel, err := normalizeRelPath(w.Package)
		if err != nil || rel == "" {
			return manifestErrorf("manifest.artifact.package.invalid", "artifact %q package must be a normalized package-relative path with no .. or absolute segment; found %q", w.ID, w.Package)
		}
	default:
		u, err := url.Parse(w.HTTPS)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return manifestErrorf("manifest.artifact.https.invalid", "artifact %q https must be an absolute HTTPS URL with no userinfo, query, or fragment; found %q", w.ID, w.HTTPS)
		}
	}
	return nil
}

func validateCapabilities(raws []json.RawMessage, byID map[string]Artifact) ([]Capability, error) {
	if len(raws) == 0 {
		return nil, manifestErrorf("manifest.capabilities.empty", "a manifest must declare at least one capability")
	}
	seen := map[string]bool{}
	out := make([]Capability, 0, len(raws))
	for _, raw := range raws {
		var tag capabilityTag
		if err := json.Unmarshal(raw, &tag); err != nil {
			return nil, manifestErrorf("manifest.capability.decode", "a capability could not be decoded: %s", boundErr(err))
		}
		if !stableID.MatchString(tag.ID) {
			return nil, manifestErrorf("manifest.capability.id.invalid", "capability id must match %s; found %q", stableID.String(), tag.ID)
		}
		if seen[tag.ID] {
			return nil, manifestErrorf("manifest.capability.id.duplicate", "capability id %q is declared more than once", tag.ID)
		}
		seen[tag.ID] = true

		if tag.Type != "native-mcp" || tag.Version != 1 {
			return nil, &UnsupportedCapabilityError{CapabilityID: tag.ID, Type: tag.Type, Version: tag.Version}
		}
		native, err := validateNativeMCP(raw, byID)
		if err != nil {
			return nil, err
		}
		out = append(out, Capability{ID: tag.ID, Type: tag.Type, Version: tag.Version, NativeMCP: native})
	}
	return out, nil
}

func validateNativeMCP(raw json.RawMessage, byID map[string]Artifact) (*NativeMCP, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w nativeMCPWire
	if err := dec.Decode(&w); err != nil {
		return nil, manifestErrorf("native-mcp.decode", "the native-mcp capability could not be decoded strictly: %s", boundErr(err))
	}

	if !stableID.MatchString(w.ServerName) {
		return nil, manifestErrorf("native-mcp.server_name.invalid", "server_name must match %s; found %q", stableID.String(), w.ServerName)
	}
	if len(w.Artifacts) == 0 {
		return nil, manifestErrorf("native-mcp.artifacts.empty", "a native-mcp capability must reference at least one artifact")
	}
	var execArtifacts []Artifact
	for _, id := range w.Artifacts {
		a, ok := byID[id]
		if !ok {
			return nil, manifestErrorf("native-mcp.artifacts.unknown", "native-mcp references artifact %q, which is not declared", id)
		}
		execArtifacts = append(execArtifacts, a)
	}
	relExec, err := normalizeRelPath(w.Executable)
	if err != nil || relExec == "" {
		return nil, manifestErrorf("native-mcp.executable.invalid", "executable must be a normalized package-relative path; found %q", w.Executable)
	}
	matched := false
	for _, a := range execArtifacts {
		if a.ExecPath == relExec {
			matched = true
			break
		}
	}
	if !matched {
		return nil, manifestErrorf("native-mcp.executable.invalid", "executable %q must equal the exec_path of a referenced artifact", w.Executable)
	}

	for _, arg := range w.Args {
		if !utf8.ValidString(arg) || len(arg) > maxShortField {
			return nil, manifestErrorf("native-mcp.args.invalid", "each arg must be bounded UTF-8; found a %d-byte arg", len(arg))
		}
		if hasPlaceholder(arg) {
			return nil, manifestErrorf("native-mcp.args.placeholder", "args are literal; an environment or command placeholder is not permitted: %q", arg)
		}
	}

	var workdir string
	if w.Workdir != "" {
		workdir, err = normalizeRelPath(w.Workdir)
		if err != nil || workdir == "" {
			return nil, manifestErrorf("native-mcp.workdir.invalid", "workdir must be empty or a normalized package-relative path; found %q", w.Workdir)
		}
	}

	required := map[string]bool{}
	requiredEnv := make([]RequiredEnv, 0, len(w.RequiredEnv))
	for _, re := range w.RequiredEnv {
		if !envName.MatchString(re.Name) {
			return nil, manifestErrorf("native-mcp.required_env.name.invalid", "required_env name must match %s; found %q", envName.String(), re.Name)
		}
		if required[re.Name] {
			return nil, manifestErrorf("native-mcp.required_env.name.duplicate", "required_env name %q is declared more than once", re.Name)
		}
		if l := len(re.Description); l < 1 || l > 512 || !proseDescription.MatchString(re.Description) {
			return nil, manifestErrorf("native-mcp.required_env.description.invalid",
				"required_env %q description must be 1 to 512 characters of the closed prose alphabet (letters, spaces, and , . ; ( ) ' - only, starting with a letter)", re.Name)
		}
		required[re.Name] = true
		requiredEnv = append(requiredEnv, RequiredEnv{Name: re.Name, Description: re.Description})
	}

	env := map[string]string{}
	for name, value := range w.Env {
		if !envName.MatchString(name) {
			return nil, manifestErrorf("native-mcp.env.name.invalid", "env name must match %s; found %q", envName.String(), name)
		}
		if required[name] {
			return nil, manifestErrorf("native-mcp.env.required_conflict", "%q is a required ambient name and may not also carry a default", name)
		}
		if !utf8.ValidString(value) || len(value) > maxShortField {
			return nil, manifestErrorf("native-mcp.env.value.invalid", "env %q default must be bounded UTF-8", name)
		}
		if hasPlaceholder(value) {
			return nil, manifestErrorf("native-mcp.env.placeholder", "env defaults are literal; a placeholder is not permitted for %q", name)
		}
		// Defaults are non-secret by contract. A credential-shaped default is
		// rejected here so the store, receipts, and descriptor never carry a
		// value; a secret belongs in required_env as a name only.
		if looksLikeSecret(name, value) {
			return nil, manifestErrorf("native-mcp.env.secret", "env %q looks like a credential; env defaults are non-secret, so declare it in required_env instead", name)
		}
		env[name] = value
	}

	targets, err := validateTargets(w.Targets)
	if err != nil {
		return nil, err
	}

	return &NativeMCP{
		ServerName:  w.ServerName,
		Artifacts:   append([]string(nil), w.Artifacts...),
		Executable:  relExec,
		Args:        append([]string(nil), w.Args...),
		Workdir:     workdir,
		Env:         env,
		RequiredEnv: requiredEnv,
		Targets:     targets,
	}, nil
}

func validateTargets(w targetsWire) (Targets, error) {
	if w.Claude == nil && w.Codex == nil {
		return Targets{}, manifestErrorf("native-mcp.targets.empty", "at least one of the claude or codex targets is required")
	}
	var out Targets
	if w.Claude != nil {
		t, err := validateHarnessTarget("claude", w.Claude)
		if err != nil {
			return Targets{}, err
		}
		out.Claude = t
	}
	if w.Codex != nil {
		t, err := validateHarnessTarget("codex", w.Codex)
		if err != nil {
			return Targets{}, err
		}
		out.Codex = t
	}
	return out, nil
}

func validateHarnessTarget(harness string, w *harnessTargetWire) (*HarnessTarget, error) {
	switch w.Startup {
	case "optional", "required":
		return &HarnessTarget{Startup: w.Startup}, nil
	default:
		return nil, manifestErrorf("native-mcp.targets.startup.invalid", "the %s target startup must be optional or required; found %q", harness, w.Startup)
	}
}

// hasPlaceholder reports whether s carries an environment or command
// substitution. Launch data is literal, so any $NAME, ${NAME}, or $(cmd) form
// is refused rather than expanded.
func hasPlaceholder(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		c := s[i+1]
		if c == '{' || c == '(' || c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return true
		}
	}
	return false
}

// looksLikeSecret reports whether an environment default is credential-shaped,
// by its name's meaning or a conspicuous value marker.
func looksLikeSecret(name, value string) bool {
	if secretNameMarker.MatchString(name) {
		return true
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"secret", "token", "password", "api_key", "apikey", "-----begin"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, prefix := range []string{"ghp_", "gho_", "ghs_", "github_pat_", "sk-", "xoxb-", "akia"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// normalizeRelPath returns the cleaned slash form of a package-relative path,
// rejecting an absolute path or any parent-directory escape. It is a pure
// string check: metadata validation never touches the filesystem.
func normalizeRelPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", manifestErrorf("path.invalid", "path must be relative and slash-separated: %q", p)
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", manifestErrorf("path.invalid", "path must stay within the package: %q", p)
	}
	return clean, nil
}

func requireShort(code, field, value string) error {
	if value == "" || len(value) > maxShortField || !utf8.ValidString(value) {
		return manifestErrorf(code, "%s must be non-empty UTF-8 of at most %d bytes", field, maxShortField)
	}
	return nil
}

func boundErr(err error) string {
	s := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(s) > 256 {
		return s[:256] + "..."
	}
	return s
}
