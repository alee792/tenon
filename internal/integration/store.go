package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
)

// Store is one owner-only, content-addressed integration-package store rooted
// at a caller-supplied absolute base directory. It is shared across agents and
// workspaces. Every mutation validates before it writes, holds an advisory
// lock, and is offline: no operation here ever fetches a URL or executes a
// package. The caller resolves the platform default; the store never guesses.
//
// Layout under base:
//
//	.lock                          advisory mutation lock
//	blobs/sha256/<hex>             raw artifact bytes, content-addressed
//	prepared/<content-key>/...     prepared executable tree
//	packages/<id>/manifest.json    the exact retained manifest bytes
//	packages/<id>/state.json       the installation-state record (schema 1)
//
// A prepared tree's content key folds the raw size and SHA-256, the format, and
// the expected exec path, size, and SHA-256, so distinct transformations never
// alias and a valid immutable entry is never replaced.
type Store struct {
	base string
}

// NewStore returns a store rooted at base, an absolute directory owned by the
// invoking user. A relative or empty base makes every operation fail rather
// than write outside the store.
func NewStore(base string) *Store { return &Store{base: base} }

// DefaultBase resolves the per-OS-user default store location: on darwin under
// the user config directory, otherwise under XDG_STATE_HOME or ~/.local/state.
func DefaultBase() (string, error) {
	if runtime.GOOS == "darwin" {
		config, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(config, "tenon", "integrations"), nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "tenon", "integrations"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "tenon", "integrations"), nil
}

// State is the operator-owned installation record (schema 1). Every field binds
// back to the immutable manifest: the manifest SHA-256, the verified artifact
// and executable identities, and each declared capability's id, type, and
// version. Trust is explicit and always "operator".
type State struct {
	SchemaVersion  int               `json:"schema_version"`
	ID             string            `json:"id"`
	Version        string            `json:"version"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	Trust          string            `json:"trust"`
	Enabled        bool              `json:"enabled"`
	Artifacts      []ArtifactState   `json:"artifacts"`
	Capabilities   []CapabilityState `json:"capabilities"`
}

// ArtifactState is one verified artifact identity and where its bytes live.
type ArtifactState struct {
	ID          string `json:"id"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Format      string `json:"format"`
	SHA256      string `json:"sha256"`
	ExecPath    string `json:"exec_path"`
	ExecSHA256  string `json:"exec_sha256"`
	BlobKey     string `json:"blob_key"`
	PreparedKey string `json:"prepared_key"`
}

// CapabilityState is one declared capability's identity.
type CapabilityState struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Version int    `json:"version"`
}

// Installed is an inspection view: the immutable manifest plus installation
// state. It carries no secret and opens no process.
type Installed struct {
	Manifest *Manifest
	State    State
}

// Summary is one entry in a store listing.
type Summary struct {
	ID      string
	Version string
	Enabled bool
}

// LaunchDescriptor is the credential-free selection evidence a later consumer
// derives a native launch from. It carries absolute prepared paths, literal
// launch data, non-secret environment defaults, and the required ambient
// NAMES only — never a resolved value. Resolution never starts a process,
// resolves an ambient value, or writes configuration.
type LaunchDescriptor struct {
	ServerName  string
	Executable  string // absolute prepared executable path
	Args        []string
	Workdir     string // absolute resolved working directory
	Env         map[string]string
	RequiredEnv []string // required ambient names; deliberately never values
	Targets     map[string]TargetPolicy
}

// TargetPolicy is one harness's startup policy and trust ownership. Trust is
// always "native-project": the harness owns the launched process.
type TargetPolicy struct {
	Startup string
	Trust   string
}

// InstallRequest describes one install or update. TrustOperator records the
// explicit operator trust decision; TenonVersion, OS, and Arch pin the host
// the package is validated against.
type InstallRequest struct {
	Source        string
	TrustOperator bool
	TenonVersion  string
	OS            string
	Arch          string
}

// Install validates and installs a package from a local directory or archive,
// then atomically records installation state under the lock. It is offline: a
// `package` artifact is verified from the staged payload, while an `https`
// artifact only succeeds if its exact bytes are already present in the blob
// store by pin — this slice fetches nothing. A different manifest under an
// already-installed id is refused; changing it requires Update with a fresh
// operator trust decision.
func (s *Store) Install(req InstallRequest) (*Installed, error) {
	return s.install(req, false)
}

// Update installs a package under an id that may already carry a different
// manifest, requiring a fresh operator trust decision. It otherwise behaves
// exactly like Install.
func (s *Store) Update(req InstallRequest) (*Installed, error) {
	return s.install(req, true)
}

func (s *Store) install(req InstallRequest, allowManifestChange bool) (*Installed, error) {
	if err := s.checkBase(); err != nil {
		return nil, err
	}
	if !req.TrustOperator {
		return nil, storeErrorf("install.trust.required", "installing a package is an explicit trust decision")
	}
	tenon, ok := parseSemver(req.TenonVersion)
	if !ok {
		return nil, storeErrorf("install.tenon-version.invalid", "the tenon version %q is not an exact semantic version", req.TenonVersion)
	}
	if !platformToken.MatchString(req.OS) || !platformToken.MatchString(req.Arch) {
		return nil, storeErrorf("install.platform.invalid", "the host os and arch must be exact GOOS/GOARCH tokens")
	}

	staging, cleanup, err := stageSource(req.Source)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	raw, err := readManifestBytes(filepath.Join(staging, "integration.json"))
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}

	if err := checkCompat(m.Compat, tenon); err != nil {
		return nil, err
	}
	if !hostHasArtifact(m, req.OS, req.Arch) {
		return nil, storeErrorf("install.platform.unsupported",
			"the package declares no artifact for %s/%s", req.OS, req.Arch)
	}

	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	unlock, err := lockStore(s.base)
	if err != nil {
		return nil, storeErrorf("install.lock", "the store lock could not be acquired: %s", boundErr(err))
	}
	defer unlock()

	existing, err := s.readState(m.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.ManifestSHA256 != m.SHA256() && !allowManifestChange {
		return nil, storeErrorf("install.manifest.conflict",
			"a different manifest is already installed for id %q; use update to change it with a fresh operator trust decision", m.ID)
	}

	artifactStates, err := s.prepareHostArtifacts(m, staging, req.OS, req.Arch)
	if err != nil {
		return nil, err
	}

	state := State{
		SchemaVersion:  1,
		ID:             m.ID,
		Version:        m.Version,
		ManifestSHA256: m.SHA256(),
		Trust:          "operator",
		Enabled:        true,
		Artifacts:      artifactStates,
	}
	for _, c := range m.Capabilities {
		state.Capabilities = append(state.Capabilities, CapabilityState{ID: c.ID, Type: c.Type, Version: c.Version})
	}

	if err := s.writePackage(m, state); err != nil {
		return nil, err
	}
	return &Installed{Manifest: m, State: state}, nil
}

// prepareHostArtifacts verifies and prepares every host-matching artifact in
// the capability closure: it verifies the raw payload's pinned size and SHA-256,
// stores the raw bytes as a content-addressed blob, prepares the executable
// tree (extracting archives safely), and verifies the prepared executable's
// pinned identity. An https artifact only succeeds if its bytes are already
// present by pin.
func (s *Store) prepareHostArtifacts(m *Manifest, staging, hostOS, hostArch string) ([]ArtifactState, error) {
	needed := closureArtifactIDs(m)
	var out []ArtifactState
	for _, a := range m.Artifacts {
		if a.OS != hostOS || a.Arch != hostArch || !needed[a.ID] {
			continue
		}
		st, err := s.prepareArtifact(a, staging)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, storeErrorf("install.platform.unsupported",
			"no capability artifact matches the host %s/%s", hostOS, hostArch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) prepareArtifact(a Artifact, staging string) (ArtifactState, error) {
	raw, err := s.artifactRawBytes(a, staging)
	if err != nil {
		return ArtifactState{}, err
	}
	if int64(len(raw)) != a.Size {
		return ArtifactState{}, storeErrorf("install.artifact.corrupt",
			"artifact %q raw size is %d, not the pinned %d", a.ID, len(raw), a.Size)
	}
	sum := sha256.Sum256(raw)
	gotSHA := hex.EncodeToString(sum[:])
	if gotSHA != a.SHA256 {
		return ArtifactState{}, storeErrorf("install.artifact.corrupt",
			"artifact %q raw SHA-256 does not match the pin", a.ID)
	}

	blobKey := a.SHA256
	if err := s.storeBlob(blobKey, raw); err != nil {
		return ArtifactState{}, err
	}

	preparedKey := contentKey(a)
	if err := s.preparePrepared(preparedKey, a, raw); err != nil {
		return ArtifactState{}, err
	}

	return ArtifactState{
		ID: a.ID, OS: a.OS, Arch: a.Arch, Format: a.Format,
		SHA256: a.SHA256, ExecPath: a.ExecPath, ExecSHA256: a.ExecSHA256,
		BlobKey: blobKey, PreparedKey: preparedKey,
	}, nil
}

// artifactRawBytes returns the raw artifact bytes: from the staged payload for
// a `package` source, or from the existing blob store for an `https` source.
// Since this slice is offline, an https artifact not already present by pin
// fails rather than being fetched.
func (s *Store) artifactRawBytes(a Artifact, staging string) ([]byte, error) {
	if a.Package != "" {
		full := filepath.Join(staging, filepath.FromSlash(a.Package))
		info, err := os.Lstat(full)
		if err != nil {
			return nil, storeErrorf("install.artifact.missing", "artifact %q payload %q is not present in the source", a.ID, a.Package)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, storeErrorf("install.artifact.special", "artifact %q payload %q is not a regular file", a.ID, a.Package)
		}
		if info.Size() > maxPayloadBytes {
			return nil, storeErrorf("install.source.too-large", "artifact %q payload exceeds %d bytes", a.ID, maxPayloadBytes)
		}
		return os.ReadFile(full)
	}
	// https source: offline, so only an already-present blob can satisfy it.
	blob := s.blobPath(a.SHA256)
	if _, err := os.Stat(blob); err != nil {
		return nil, storeErrorf("install.remote.unavailable",
			"artifact %q is a remote https source and remote artifact fetch is not available in this slice; its bytes are not already present by pin", a.ID)
	}
	return os.ReadFile(blob)
}

// preparePrepared materializes the prepared executable tree for one artifact
// under its content key, then verifies the prepared executable's pinned
// identity. A valid existing entry is never replaced.
func (s *Store) preparePrepared(key string, a Artifact, raw []byte) error {
	dir := s.preparedPath(key)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		// Immutable and content-addressed: an existing entry is reused, its
		// prepared executable re-verified below.
		return s.verifyPreparedExec(dir, a)
	}

	tmp, err := os.MkdirTemp(s.preparedRoot(), ".tmp-prepare-")
	if err != nil {
		return storeErrorf("install.prepared.failed", "a preparation directory could not be created: %s", boundErr(err))
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmp)
		}
	}()

	switch a.Format {
	case "binary":
		target := filepath.Join(tmp, filepath.FromSlash(a.ExecPath))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, raw, 0o700); err != nil {
			return storeErrorf("install.prepared.failed", "the prepared executable could not be written: %s", boundErr(err))
		}
	case "tar.gz":
		if err := extractTarGzBytes(raw, tmp); err != nil {
			return err
		}
	case "zip":
		if err := extractZipBytes(raw, tmp); err != nil {
			return err
		}
	default:
		return storeErrorf("install.prepared.failed", "artifact %q has an unpreparable format %q", a.ID, a.Format)
	}

	if err := s.verifyPreparedExec(tmp, a); err != nil {
		return err
	}
	// Make the executable runnable for a later native launch; the store stays
	// owner-only.
	_ = os.Chmod(filepath.Join(tmp, filepath.FromSlash(a.ExecPath)), 0o700)

	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			// A concurrent install committed the identical immutable entry.
			return s.verifyPreparedExec(dir, a)
		}
		return storeErrorf("install.prepared.failed", "the prepared tree could not be committed: %s", boundErr(err))
	}
	committed = true
	return nil
}

// verifyPreparedExec confirms the prepared executable exists as a regular file
// matching the artifact's pinned exec size and SHA-256.
func (s *Store) verifyPreparedExec(root string, a Artifact) error {
	full := filepath.Join(root, filepath.FromSlash(a.ExecPath))
	info, err := os.Lstat(full)
	if err != nil {
		return storeErrorf("install.prepared.corrupt", "artifact %q prepared executable %q is missing", a.ID, a.ExecPath)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return storeErrorf("install.prepared.corrupt", "artifact %q prepared executable %q is not a regular file", a.ID, a.ExecPath)
	}
	if info.Size() != a.ExecSize {
		return storeErrorf("install.prepared.corrupt", "artifact %q prepared executable size is %d, not the pinned %d", a.ID, info.Size(), a.ExecSize)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return storeErrorf("install.prepared.corrupt", "artifact %q prepared executable could not be read: %s", a.ID, boundErr(err))
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.ExecSHA256 {
		return storeErrorf("install.prepared.corrupt", "artifact %q prepared executable SHA-256 does not match the pin", a.ID)
	}
	return nil
}

// Inspect returns the immutable manifest view and installed identities for id
// without opening a process.
func (s *Store) Inspect(id string) (*Installed, error) {
	if err := s.checkBase(); err != nil {
		return nil, err
	}
	state, err := s.requireState(id)
	if err != nil {
		return nil, err
	}
	m, err := s.loadManifest(id, state)
	if err != nil {
		return nil, err
	}
	return &Installed{Manifest: m, State: *state}, nil
}

// List returns installed packages with id, version, and enablement.
func (s *Store) List() ([]Summary, error) {
	if err := s.checkBase(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.packagesRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		state, err := s.readState(e.Name())
		if err != nil || state == nil {
			continue
		}
		out = append(out, Summary{ID: state.ID, Version: state.Version, Enabled: state.Enabled})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Verify rehashes the raw artifact blobs and every prepared executable for id
// offline, reporting corruption precisely. It binds each state field back to
// the immutable manifest before trusting it.
func (s *Store) Verify(id string) error {
	if err := s.checkBase(); err != nil {
		return err
	}
	state, err := s.requireState(id)
	if err != nil {
		return err
	}
	m, err := s.loadManifest(id, state)
	if err != nil {
		return err
	}
	byID := map[string]Artifact{}
	for _, a := range m.Artifacts {
		byID[a.ID] = a
	}
	for _, as := range state.Artifacts {
		a, ok := byID[as.ID]
		if !ok || a.SHA256 != as.SHA256 || a.ExecSHA256 != as.ExecSHA256 {
			return storeErrorf("verify.state.drift", "artifact %q state no longer matches the manifest", as.ID)
		}
		blob := s.blobPath(as.BlobKey)
		data, err := os.ReadFile(blob)
		if err != nil {
			return storeErrorf("verify.blob.missing", "artifact %q raw blob is missing", as.ID)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != as.SHA256 {
			return storeErrorf("verify.blob.corrupt", "artifact %q raw blob SHA-256 does not match the pin", as.ID)
		}
		if err := s.verifyPreparedExec(s.preparedPath(as.PreparedKey), a); err != nil {
			return err
		}
	}
	return nil
}

// Enable verifies the package first, then records it enabled. Enablement never
// bypasses verification.
func (s *Store) Enable(id string) error {
	if err := s.checkBase(); err != nil {
		return err
	}
	if err := s.Verify(id); err != nil {
		return err
	}
	return s.setEnabled(id, true)
}

// Disable records the package disabled so future resolution is refused; the
// content-addressed bytes are retained.
func (s *Store) Disable(id string) error {
	if err := s.checkBase(); err != nil {
		return err
	}
	return s.setEnabled(id, false)
}

func (s *Store) setEnabled(id string, enabled bool) error {
	unlock, err := lockStore(s.base)
	if err != nil {
		return storeErrorf("store.lock", "the store lock could not be acquired: %s", boundErr(err))
	}
	defer unlock()
	state, err := s.requireState(id)
	if err != nil {
		return err
	}
	state.Enabled = enabled
	return s.writeState(id, *state)
}

// Remove deletes only the installation record for id, retaining the shared
// content-addressed blobs and prepared trees so a reinstall reuses them.
func (s *Store) Remove(id string) error {
	if err := s.checkBase(); err != nil {
		return err
	}
	if !stableID.MatchString(id) {
		return storeErrorf("store.id.invalid", "the package id %q is not a valid identifier", id)
	}
	unlock, err := lockStore(s.base)
	if err != nil {
		return storeErrorf("store.lock", "the store lock could not be acquired: %s", boundErr(err))
	}
	defer unlock()
	if _, err := s.requireState(id); err != nil {
		return err
	}
	return os.RemoveAll(s.packageDir(id))
}

// Resolve is the offline consumer API. It verifies enablement, compatibility,
// the artifact closure, and the executable identity, then returns a
// credential-free launch descriptor. It resolves no ambient value, starts
// nothing, and writes no configuration; the required ambient names it returns
// are diagnostic metadata, never a credential channel.
func (s *Store) Resolve(id, capabilityID, tenonVersion, osName, arch string) (*LaunchDescriptor, error) {
	if err := s.checkBase(); err != nil {
		return nil, err
	}
	state, err := s.requireState(id)
	if err != nil {
		return nil, err
	}
	if !state.Enabled {
		return nil, storeErrorf("resolve.disabled", "package %q is disabled; enable it before resolution", id)
	}
	tenon, ok := parseSemver(tenonVersion)
	if !ok {
		return nil, storeErrorf("resolve.tenon-version.invalid", "the tenon version %q is not an exact semantic version", tenonVersion)
	}
	m, err := s.loadManifest(id, state)
	if err != nil {
		return nil, err
	}
	if err := checkCompat(m.Compat, tenon); err != nil {
		return nil, err
	}

	var capability *Capability
	for i := range m.Capabilities {
		if m.Capabilities[i].ID == capabilityID {
			capability = &m.Capabilities[i]
			break
		}
	}
	if capability == nil {
		return nil, storeErrorf("resolve.capability.unknown", "package %q declares no capability %q", id, capabilityID)
	}
	if capability.NativeMCP == nil {
		return nil, storeErrorf("resolve.capability.type", "capability %q is not a native-mcp capability", capabilityID)
	}
	native := capability.NativeMCP

	// The executable belongs to exactly one host-matching artifact in the
	// closure whose exec_path equals the declared executable.
	var execArtifact *Artifact
	closure := map[string]bool{}
	for _, aid := range native.Artifacts {
		closure[aid] = true
	}
	for i := range m.Artifacts {
		a := &m.Artifacts[i]
		if !closure[a.ID] || a.OS != osName || a.Arch != arch {
			continue
		}
		if a.ExecPath == native.Executable {
			execArtifact = a
		}
	}
	if execArtifact == nil {
		return nil, storeErrorf("resolve.platform.unsupported",
			"capability %q has no %s/%s executable artifact", capabilityID, osName, arch)
	}

	as := s.artifactState(state, execArtifact.ID)
	if as == nil {
		return nil, storeErrorf("resolve.artifact.unprepared", "artifact %q was not prepared for this host", execArtifact.ID)
	}
	if err := s.verifyPreparedExec(s.preparedPath(as.PreparedKey), *execArtifact); err != nil {
		return nil, err
	}

	preparedRoot := s.preparedPath(as.PreparedKey)
	execAbs := filepath.Join(preparedRoot, filepath.FromSlash(native.Executable))
	workdir := preparedRoot
	if native.Workdir != "" {
		workdir = filepath.Join(preparedRoot, filepath.FromSlash(native.Workdir))
	}

	desc := &LaunchDescriptor{
		ServerName: native.ServerName,
		Executable: execAbs,
		Args:       append([]string(nil), native.Args...),
		Workdir:    workdir,
		Env:        map[string]string{},
		Targets:    map[string]TargetPolicy{},
	}
	for k, v := range native.Env {
		desc.Env[k] = v
	}
	// Required ambient variables travel as NAMES only. A resolved value must
	// never enter the descriptor, store state, or retained evidence.
	for _, re := range native.RequiredEnv {
		desc.RequiredEnv = append(desc.RequiredEnv, re.Name)
	}
	sort.Strings(desc.RequiredEnv)
	if native.Targets.Claude != nil {
		desc.Targets["claude"] = TargetPolicy{Startup: native.Targets.Claude.Startup, Trust: "native-project"}
	}
	if native.Targets.Codex != nil {
		desc.Targets["codex"] = TargetPolicy{Startup: native.Targets.Codex.Startup, Trust: "native-project"}
	}
	return desc, nil
}

// --- helpers ---

func (s *Store) checkBase() error {
	if s == nil || s.base == "" || !filepath.IsAbs(s.base) {
		return storeErrorf("store.base.invalid", "the store base must be an absolute directory")
	}
	return nil
}

func (s *Store) blobsRoot() string    { return filepath.Join(s.base, "blobs", "sha256") }
func (s *Store) preparedRoot() string { return filepath.Join(s.base, "prepared") }
func (s *Store) packagesRoot() string { return filepath.Join(s.base, "packages") }

func (s *Store) blobPath(key string) string     { return filepath.Join(s.blobsRoot(), key) }
func (s *Store) preparedPath(key string) string { return filepath.Join(s.preparedRoot(), key) }
func (s *Store) packageDir(id string) string    { return filepath.Join(s.packagesRoot(), id) }

func (s *Store) ensureDirs() error {
	for _, dir := range []string{s.base, s.blobsRoot(), s.preparedRoot(), s.packagesRoot()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return storeErrorf("store.init", "a store directory could not be created: %s", boundErr(err))
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return storeErrorf("store.init", "a store directory could not be secured: %s", boundErr(err))
		}
	}
	return nil
}

func (s *Store) storeBlob(key string, raw []byte) error {
	path := s.blobPath(key)
	if _, err := os.Stat(path); err == nil {
		return nil // immutable content already present
	}
	return writeFileAtomic(path, raw, 0o600)
}

func (s *Store) writePackage(m *Manifest, state State) error {
	dir := s.packageDir(m.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return storeErrorf("store.write", "the package directory could not be created: %s", boundErr(err))
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return storeErrorf("store.write", "the package directory could not be secured: %s", boundErr(err))
	}
	if err := writeFileAtomic(filepath.Join(dir, "manifest.json"), m.Raw(), 0o600); err != nil {
		return storeErrorf("store.write", "the manifest could not be written: %s", boundErr(err))
	}
	return s.writeState(m.ID, state)
}

func (s *Store) writeState(id string, state State) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return storeErrorf("store.write", "the installation state could not be encoded: %s", boundErr(err))
	}
	return writeFileAtomic(filepath.Join(s.packageDir(id), "state.json"), append(content, '\n'), 0o600)
}

func (s *Store) readState(id string) (*State, error) {
	if !stableID.MatchString(id) {
		return nil, storeErrorf("store.id.invalid", "the package id %q is not a valid identifier", id)
	}
	raw, err := os.ReadFile(filepath.Join(s.packageDir(id), "state.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, storeErrorf("store.read", "the installation state could not be read: %s", boundErr(err))
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, storeErrorf("store.read", "the installation state is not valid JSON: %s", boundErr(err))
	}
	if state.SchemaVersion != 1 {
		return nil, storeErrorf("store.read", "unsupported installation-state schema %d", state.SchemaVersion)
	}
	if state.Trust != "operator" {
		return nil, storeErrorf("store.read", "the installation state carries a non-operator trust %q", state.Trust)
	}
	return &state, nil
}

func (s *Store) requireState(id string) (*State, error) {
	state, err := s.readState(id)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, storeErrorf("store.not-found", "no package %q is installed", id)
	}
	return state, nil
}

// loadManifest reads the retained manifest bytes and binds them to the
// installation state: the manifest SHA-256 must equal the recorded identity,
// and capability selection always derives from these exact bytes.
func (s *Store) loadManifest(id string, state *State) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(s.packageDir(id), "manifest.json"))
	if err != nil {
		return nil, storeErrorf("store.read", "the retained manifest could not be read: %s", boundErr(err))
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, storeErrorf("store.manifest.corrupt", "the retained manifest is no longer valid: %s", boundErr(err))
	}
	if m.SHA256() != state.ManifestSHA256 {
		return nil, storeErrorf("store.manifest.corrupt", "the retained manifest SHA-256 does not match the installation state")
	}
	return m, nil
}

func (s *Store) artifactState(state *State, id string) *ArtifactState {
	for i := range state.Artifacts {
		if state.Artifacts[i].ID == id {
			return &state.Artifacts[i]
		}
	}
	return nil
}

func checkCompat(c Compat, tenon semver) error {
	min, _ := parseSemver(c.Minimum)
	before, _ := parseSemver(c.Before)
	if compareSemver(tenon, min) < 0 || compareSemver(tenon, before) >= 0 {
		return storeErrorf("compat.unsupported",
			"the tenon version is outside the package range [%s, %s)", c.Minimum, c.Before)
	}
	return nil
}

func hostHasArtifact(m *Manifest, osName, arch string) bool {
	for _, a := range m.Artifacts {
		if a.OS == osName && a.Arch == arch {
			return true
		}
	}
	return false
}

func closureArtifactIDs(m *Manifest) map[string]bool {
	out := map[string]bool{}
	for _, c := range m.Capabilities {
		if c.NativeMCP == nil {
			continue
		}
		for _, id := range c.NativeMCP.Artifacts {
			out[id] = true
		}
	}
	return out
}

// contentKey derives a prepared tree's content key, folding the raw size and
// SHA-256, the format, and the expected exec path, size, and SHA-256, so
// distinct transformations of the same or different bytes never alias.
func contentKey(a Artifact) string {
	h := sha256.New()
	io.WriteString(h, "integration-prepared-v1\n")
	io.WriteString(h, a.SHA256+"\n")
	io.WriteString(h, strconv.FormatInt(a.Size, 10)+"\n")
	io.WriteString(h, a.Format+"\n")
	io.WriteString(h, a.ExecPath+"\n")
	io.WriteString(h, strconv.FormatInt(a.ExecSize, 10)+"\n")
	io.WriteString(h, a.ExecSHA256+"\n")
	return hex.EncodeToString(h.Sum(nil))
}

func readManifestBytes(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, storeErrorf("install.manifest.missing", "the source contains no integration.json at its root")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, storeErrorf("install.manifest.invalid", "integration.json must be a regular file")
	}
	if info.Size() > MaxManifestBytes {
		return nil, storeErrorf("install.manifest.invalid", "integration.json exceeds %d bytes", MaxManifestBytes)
	}
	return os.ReadFile(path)
}

// writeFileAtomic writes content to a same-directory temporary file and renames
// it into place, creating parents as owner-only directories.
func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tenon-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// extractTarGzBytes and extractZipBytes prepare an archive artifact from its
// raw bytes into dst, reusing the safe extractors by staging the bytes to a
// temporary archive file first.
func extractTarGzBytes(raw []byte, dst string) error {
	return withTempArchive(raw, ".tar.gz", func(path string) error { return extractTarGz(path, dst) })
}

func extractZipBytes(raw []byte, dst string) error {
	return withTempArchive(raw, ".zip", func(path string) error { return extractZip(path, dst) })
}

func withTempArchive(raw []byte, suffix string, fn func(path string) error) error {
	tmp, err := os.CreateTemp("", "tenon-artifact-*"+suffix)
	if err != nil {
		return storeErrorf("install.prepared.failed", "an artifact could not be staged for extraction: %s", boundErr(err))
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return storeErrorf("install.prepared.failed", "an artifact could not be staged for extraction: %s", boundErr(err))
	}
	if err := tmp.Close(); err != nil {
		return storeErrorf("install.prepared.failed", "an artifact could not be staged for extraction: %s", boundErr(err))
	}
	return fn(tmp.Name())
}
