package agentproject

// Connections author standalone native MCP servers (ADR 0016, re-shaped by
// issue #49 to the Agent Plugins 1.0 mcp.json server-entry vocabulary and
// widened by ADR 0026, which is now the governing record for the mcp/
// directory, the field vocabulary, and remote authentication). Each
// mcp/<name>.md carries one closed YAML frontmatter document selecting
// exactly one target form: remote streamable-http, repo-relative stdio
// (ADR 0026, issue #50), or installed — an exact operator-installed
// native-mcp capability (ADR 0016/0014) selected by package and capability
// id. Load validates the installed frontmatter shape only; resolving the
// selection against the operator's integration store happens offline at
// generation time (internal/claude, internal/codex), exactly like plugin
// ${PLUGIN_DATA}. A stdio command and working directory are resolved and
// proven to stay inside the agent root at Load time instead, because that
// resolution needs no target-specific store and behaves exactly like a
// plugin-relative stdio command (ADR 0010's mcp.json), only anchored at the
// agent root rather than a plugin root.
//
// A legacy connections/ directory is a hard migration failure, not a silent
// no-op: authors must move the content to mcp/ and re-shape its frontmatter.

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
	"github.com/alee792/tenon/internal/frontmatter"
)

// Connection bounds (ADR 0013): safety ceilings, not ordinary-use quotas.
// Violations fail before any workspace mutation.
const (
	// MaxConnections bounds the number of immediate mcp/ files.
	MaxConnections = 128
	// MaxConnectionBytes bounds one connection source file.
	MaxConnectionBytes = 8 * 1024
	// MaxConnectionContextRunes bounds the optional trimmed Markdown body.
	MaxConnectionContextRunes = 1024
	// MaxStdioServers bounds how many mcp/<name>.md files may declare
	// type: stdio for one agent. ADR 0026 left the tree-resident executable
	// budget as an open item explicitly assigned to issue #50; this is the
	// recorded count half of that bound.
	MaxStdioServers = 16
	// MaxStdioCommandAggregateBytes bounds the combined size, in bytes, of
	// every distinct declared stdio command file's bytes for one agent
	// (deduplicated by resolved path, matching how the same bytes join the
	// fingerprint only once). This is the recorded byte half of ADR 0026's
	// open item assigned to issue #50.
	MaxStdioCommandAggregateBytes = 64 << 20 // 64 MiB
)

// connectionNamePattern is the connection filename grammar: 1-64 characters,
// a leading lowercase letter, then lowercase letters, digits, underscores, or
// hyphens. Underscores are permitted here, unlike the skill grammar.
var connectionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// installedIDPattern is the shared grammar for an installed connection's
// package and capability identifiers: ADR 0014's stable identifier grammar
// (^[a-z][a-z0-9-]{0,62}$), reused here so authored source is validated
// against the exact same rule the store enforces.
var installedIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// managedConnectionName is reserved for tenon's own managed server.
const managedConnectionName = "managed"

// ManagedConnectionName exports managedConnectionName for authoring commands
// that must reject it offline, before ever touching the filesystem.
const ManagedConnectionName = managedConnectionName

// Connection is one validated standalone native MCP connection: a remote
// streamable-http endpoint, a repo-relative stdio server (ADR 0026, issue
// #50), or an installed package capability selection (ADR 0016, issue #49).
// Kind discriminates which fields apply.
type Connection struct {
	// Name is the filename-derived connection and native server name.
	Name string
	// Kind is "remote", "stdio", or "installed".
	Kind string
	// URL is the exact validated absolute HTTPS URL. Set only when
	// Kind == "remote".
	URL string
	// Headers carries the optional remote header map, copied literally.
	// Values may end with exactly one ${VAR} environment-variable name
	// reference; tenon never resolves it. Set only when Kind == "remote";
	// nil when the remote form declares no headers.
	Headers map[string]string
	// Command is the stdio executable's absolute resolved path, already
	// proven to stay inside the agent root (ADR 0026). Set only when
	// Kind == "stdio".
	Command string
	// Args are the declared stdio arguments, unexpanded and copied
	// literally: authored args carry no placeholder expansion of any kind.
	// Set only when Kind == "stdio"; nil when the declaration carries none.
	Args []string
	// Env carries the optional stdio environment map, copied literally.
	// Values follow the same ${VAR} value grammar as Headers. Set only when
	// Kind == "stdio"; nil when the declaration carries no env.
	Env map[string]string
	// Cwd is the stdio working directory's absolute resolved path, already
	// proven to stay inside the agent root. Set only when Kind == "stdio";
	// empty when the declaration carries no cwd, in which case rendering
	// defaults to the agent root (see internal/claude and internal/codex).
	Cwd string
	// commandBytes is the stdio command file's exact byte count, captured at
	// Load time so the aggregate stdio budget (MaxStdioCommandAggregateBytes)
	// never re-reads the file. Zero for every non-stdio connection.
	commandBytes int64
	// Package and Capability select one installed, operator-trusted
	// native-mcp capability (ADR 0014). Set only when Kind == "installed";
	// resolution against the integration store happens at generation time,
	// not here.
	Package    string
	Capability string
	// Context is the optional trimmed Markdown body, model-facing usage
	// guidance; empty when the body is absent or whitespace-only.
	Context string
	// SourcePath is the authored path relative to the agent root:
	// "mcp/<name>.md".
	SourcePath string
}

// ConnectionKindRemote, ConnectionKindStdio, and ConnectionKindInstalled are
// the three supported connection target kinds (ADR 0016, widened by
// ADR 0026 to add ConnectionKindStdio).
const (
	ConnectionKindRemote    = "remote"
	ConnectionKindStdio     = "stdio"
	ConnectionKindInstalled = "installed"
)

// mcpAuthoredDir is the current authored directory name (issue #49). The
// prior name, "connections", is a hard migration failure: see
// checkLegacyConnectionsDir.
const mcpAuthoredDir = "mcp"

// loadConnections discovers and validates the optional mcp/ directory,
// returning the connections sorted by name and every source file read as a
// fingerprint input. pluginServers supplies the accepted plugin MCP server
// names a connection may not collide with (ADR 0010/0016). A legacy
// connections/ directory fails closed with a migration diagnostic rather
// than being silently ignored or read as mcp/.
func loadConnections(root string, pluginServers []PluginServer, diags *diagnostics.List) ([]Connection, []sourceInput) {
	checkLegacyConnectionsDir(root, diags)

	dir := filepath.Join(root, mcpAuthoredDir)
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil // mcp/ is optional
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("mcp.entry.invalid", mcpAuthoredDir,
			"mcp must be a real directory; symlinks are never followed")
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("mcp.entry.invalid", mcpAuthoredDir, "mcp could not be read: %v", err)
		return nil, nil
	}

	pluginNames := make(map[string]string, len(pluginServers))
	for _, s := range pluginServers {
		if _, ok := pluginNames[s.Name]; !ok {
			pluginNames[s.Name] = s.SourcePath
		}
	}

	var connections []Connection
	var inputs []sourceInput
	seen := make(map[string]string, len(entries))
	count := 0
	for _, entry := range entries {
		entryPath := mcpAuthoredDir + "/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("mcp.entry.invalid", entryPath,
				"each mcp entry must be a real regular file; symlinks are never followed")
			continue
		}
		if entry.IsDir() {
			diags.Errorf("mcp.entry.invalid", entryPath,
				"each mcp entry must be a real regular file, not a directory")
			continue
		}
		if !entry.Type().IsRegular() {
			diags.Errorf("mcp.entry.invalid", entryPath,
				"each mcp entry must be a real regular file")
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			diags.Errorf("mcp.entry.invalid", entryPath,
				"each mcp entry must use the .md extension")
			continue
		}

		count++
		if count == MaxConnections+1 {
			diags.Errorf("mcp.bounds.exceeded", mcpAuthoredDir,
				"mcp may contain at most %d files", MaxConnections)
		}

		conn, fileInputs, ok := loadConnectionFile(root, entry.Name(), diags)
		inputs = append(inputs, fileInputs...)
		if !ok {
			continue
		}
		if earlier, collide := seen[conn.Name]; collide {
			diags.Errorf("mcp.name.collision", conn.SourcePath,
				"the connection name %q collides with the connection declared at %s; connection names must be unique",
				conn.Name, earlier)
			continue
		}
		if sourcePath, collide := pluginNames[conn.Name]; collide {
			diags.Errorf("mcp.name.collision", conn.SourcePath,
				"the connection name %q collides with the accepted plugin MCP server declared at %s; a connection may not share a name with a plugin server",
				conn.Name, sourcePath)
			continue
		}
		seen[conn.Name] = conn.SourcePath
		connections = append(connections, conn)
	}
	slices.SortFunc(connections, func(a, b Connection) int { return strings.Compare(a.Name, b.Name) })
	checkStdioBudget(connections, diags)
	return connections, inputs
}

// checkStdioBudget enforces ADR 0026's open item, recorded by issue #50: at
// most MaxStdioServers declared type: stdio connections, and at most
// MaxStdioCommandAggregateBytes combined bytes across every distinct
// resolved stdio command file (deduplicated by resolved path, so one
// executable shared by name collision is impossible but a byte count is
// never double-charged). It is a pure function over already-validated
// connections so it can be unit tested directly, without writing any large
// fixture file.
func checkStdioBudget(connections []Connection, diags *diagnostics.List) {
	count := 0
	var total int64
	seen := make(map[string]bool, len(connections))
	for _, c := range connections {
		if c.Kind != ConnectionKindStdio {
			continue
		}
		count++
		if !seen[c.Command] {
			seen[c.Command] = true
			total += c.commandBytes
		}
	}
	if count > MaxStdioServers {
		diags.Errorf("mcp.stdio.bounds.exceeded", mcpAuthoredDir,
			"an agent may declare at most %d type: stdio servers; found %d", MaxStdioServers, count)
	}
	if total > MaxStdioCommandAggregateBytes {
		diags.Errorf("mcp.stdio.bounds.exceeded", mcpAuthoredDir,
			"the combined size of every declared stdio command file may be at most %d bytes; found %d",
			MaxStdioCommandAggregateBytes, total)
	}
}

// checkLegacyConnectionsDir fails closed when a legacy connections/
// directory is present: pre-release, the directory was renamed to mcp/ and
// the frontmatter re-shaped to the Agent Plugins 1.0 vocabulary (issue #49),
// so silently ignoring the old directory would leave authors with connections
// that quietly stopped generating.
func checkLegacyConnectionsDir(root string, diags *diagnostics.List) {
	if info, err := os.Lstat(filepath.Join(root, "connections")); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		diags.Errorf("mcp.migration.connections-dir", "connections",
			"the connections/ directory is no longer read; move its contents to mcp/ and re-shape each file's frontmatter to the current vocabulary (type: streamable-http or type: installed)")
	}
}

// loadConnectionFile validates one mcp/<name>.md entry: its filename grammar
// and reservation, size, encoding, and frontmatter/body contract. The exact
// source bytes are always returned as a fingerprint input when they could be
// read, regardless of validity; a valid type: stdio declaration returns a
// second fingerprint input carrying the resolved command file's exact bytes
// and executable bit.
func loadConnectionFile(root, filename string, diags *diagnostics.List) (Connection, []sourceInput, bool) {
	sourcePath := mcpAuthoredDir + "/" + filename
	name := strings.TrimSuffix(filename, ".md")
	valid := true

	if !connectionNamePattern.MatchString(name) {
		diags.Errorf("mcp.name.invalid", sourcePath,
			"a connection filename must be 1-64 characters, starting with a lowercase letter and continuing with lowercase letters, digits, underscores, or hyphens: %q", name)
		valid = false
	}
	if name == managedConnectionName {
		diags.Errorf("mcp.name.reserved", sourcePath,
			"the name %q is reserved for tenon's own managed server", managedConnectionName)
		valid = false
	}

	full := filepath.Join(root, mcpAuthoredDir, filename)
	info, err := os.Lstat(full)
	if err != nil {
		diags.Errorf("mcp.entry.invalid", sourcePath, "the connection file could not be read: %v", err)
		return Connection{Name: name, SourcePath: sourcePath}, nil, false
	}
	if info.Size() > MaxConnectionBytes {
		diags.Errorf("mcp.bounds.exceeded", sourcePath,
			"a connection file may contain at most %d bytes; found %d", MaxConnectionBytes, info.Size())
		return Connection{Name: name, SourcePath: sourcePath}, nil, false
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		diags.Errorf("mcp.entry.invalid", sourcePath, "the connection file could not be read: %v", err)
		return Connection{Name: name, SourcePath: sourcePath}, nil, false
	}
	inputs := []sourceInput{{Path: sourcePath, Content: raw, Executable: false}}

	if !utf8.Valid(raw) {
		diags.Errorf("mcp.entry.invalid", sourcePath, "the connection file must be valid UTF-8")
		return Connection{Name: name, SourcePath: sourcePath}, inputs, false
	}

	parsed, cmdInput, ok := parseConnection(string(raw), root, sourcePath, diags)
	if cmdInput != nil {
		inputs = append(inputs, *cmdInput)
	}
	if parsed == nil {
		return Connection{Name: name, SourcePath: sourcePath}, inputs, false
	}
	parsed.Name = name
	parsed.SourcePath = sourcePath
	return *parsed, inputs, ok && valid
}

// connectionRemoteFields, connectionStdioFields, and connectionInstalledFields
// are the recognized frontmatter fields for each target form, keyed by the
// "type" discriminator (issue #49, widened by ADR 0026/issue #50): remote
// uses type: streamable-http, stdio uses type: stdio, and installed uses
// type: installed. Every other field is unknown for that form.
var (
	connectionRemoteFields    = map[string]bool{"type": true, "url": true, "headers": true}
	connectionStdioFields     = map[string]bool{"type": true, "command": true, "args": true, "env": true, "cwd": true}
	connectionInstalledFields = map[string]bool{"type": true, "package": true, "capability": true}
)

// parseConnection enforces the closed connection frontmatter contract
// (issue #49, widened by ADR 0026/issue #50): one plain field "type" that
// discriminates exactly one target form — "streamable-http" (url, optional
// headers), "stdio" (command, optional args/env/cwd, all agent-root-relative
// and resolved here), or "installed" (package, capability). "sse" fails as a
// deprecated, unsupported transport. It validates installed frontmatter shape
// only: package and capability must each be present, non-empty, and match
// ADR 0014's stable identifier grammar; it never contacts the integration
// store, which happens at generation time. A stdio declaration's command and
// cwd are, by contrast, resolved and containment-checked right here against
// root, because that resolution is target-independent (ADR 0026). On success
// for a stdio declaration it also returns the resolved command's fingerprint
// sourceInput; every other path returns a nil second value.
func parseConnection(content, root, path string, diags *diagnostics.List) (*Connection, *sourceInput, bool) {
	raw, bodyStart, err := frontmatter.Split([]byte(content))
	if err != nil {
		diags.Errorf("mcp.frontmatter.missing", path,
			"mcp/<name>.md must start with YAML frontmatter delimited by --- lines")
		return nil, nil, false
	}
	doc, err := frontmatter.Parse(raw)
	if err != nil {
		diags.Errorf("mcp.frontmatter.invalid", path, "%s", err)
		return nil, nil, false
	}

	keys := doc.Keys()

	if !doc.Has("type") {
		diags.Errorf("mcp.frontmatter.missing", path,
			"frontmatter must carry the field \"type\" set to \"streamable-http\", \"stdio\", or \"installed\"")
		return nil, nil, false
	}
	typeVal, err := doc.String("type")
	if err != nil {
		diags.Errorf("mcp.frontmatter.invalid", path, "frontmatter field \"type\" must be a string")
		return nil, nil, false
	}

	var form string
	var allowed map[string]bool
	switch typeVal {
	case "streamable-http":
		form = "remote"
		allowed = connectionRemoteFields
	case "stdio":
		form = "stdio"
		allowed = connectionStdioFields
	case "installed":
		form = "installed"
		allowed = connectionInstalledFields
	case "sse":
		diags.Errorf("mcp.transport.invalid", path,
			"the sse transport is deprecated and not supported; use type: streamable-http")
		return nil, nil, false
	default:
		diags.Errorf("mcp.frontmatter.invalid", path,
			"frontmatter field \"type\" must be \"streamable-http\", \"stdio\", or \"installed\"; found %q", typeVal)
		return nil, nil, false
	}

	for _, k := range keys {
		if !allowed[k] {
			diags.Errorf("mcp.frontmatter.unknown-field", path,
				"the field %q is not part of a %s target declaration", k, form)
			return nil, nil, false
		}
	}

	var conn Connection
	var cmdInput *sourceInput
	switch form {
	case "installed":
		pkg, err := doc.String("package")
		if err != nil || pkg == "" || !installedIDPattern.MatchString(pkg) {
			diags.Errorf("mcp.target.invalid", path,
				"frontmatter field \"package\" must be a non-empty string matching %s", installedIDPattern.String())
			return nil, nil, false
		}
		capability, err := doc.String("capability")
		if err != nil || capability == "" || !installedIDPattern.MatchString(capability) {
			diags.Errorf("mcp.target.invalid", path,
				"frontmatter field \"capability\" must be a non-empty string matching %s", installedIDPattern.String())
			return nil, nil, false
		}
		conn = Connection{Kind: ConnectionKindInstalled, Package: pkg, Capability: capability}
	case "stdio":
		stdioConn, sIn, ok := parseStdioConnection(doc, root, path, diags)
		if !ok {
			return nil, sIn, false
		}
		conn = *stdioConn
		cmdInput = sIn
	default:
		rawURL, err := doc.String("url")
		if err != nil || rawURL == "" {
			diags.Errorf("mcp.target.invalid", path, "frontmatter field \"url\" must be a non-empty string")
			return nil, nil, false
		}
		if err := validConnectionURL(rawURL); err != nil {
			diags.Errorf("mcp.target.invalid", path, "%s", err)
			return nil, nil, false
		}
		var headers map[string]string
		if doc.Has("headers") {
			headers, err = doc.StringMap("headers")
			if err != nil {
				diags.Errorf("mcp.header.invalid", path, "frontmatter field \"headers\" must be a mapping of strings to strings: %s", err)
				return nil, nil, false
			}
			if err := validAuthoredHeaders(headers); err != nil {
				diags.Errorf("mcp.header.invalid", path, "%s", err)
				return nil, nil, false
			}
		}
		conn = Connection{Kind: ConnectionKindRemote, URL: rawURL, Headers: headers}
	}

	body := content[bodyStart:]
	if after, ok := strings.CutPrefix(body, "\r\n"); ok {
		body = after
	} else {
		body = strings.TrimPrefix(body, "\n")
	}
	body = strings.TrimSpace(body)
	if runeLen := utf8.RuneCountInString(body); runeLen > MaxConnectionContextRunes {
		diags.Errorf("mcp.context.too-long", path,
			"the optional Markdown body may contain at most %d Unicode characters; found %d", MaxConnectionContextRunes, runeLen)
		return nil, cmdInput, false
	}
	conn.Context = body

	return &conn, cmdInput, true
}

// parseStdioConnection validates and resolves a type: stdio declaration
// (ADR 0026, issue #50): command is required, agent-root-relative
// ("./..."), and containment-proven the way plugin-relative stdio commands
// are (internal/agentproject/pluginmcp.go's resolveInPlugin), but anchored at
// the agent root; cwd carries the identical rule when present. args are
// copied literally with no placeholder expansion of any kind — a value
// naming ${PLUGIN_ROOT} or ${PLUGIN_DATA} is rejected, because those expand
// only inside a plugin's own mcp.json. env values follow the exact same
// ${VAR} grammar as headers, reusing validPlaceholderValue so the two
// authored value shapes can never silently drift apart. On success it
// returns the resolved command file's fingerprint sourceInput; every
// rejection path returns whatever sourceInput was already captured (nil
// unless the command file itself was successfully read).
func parseStdioConnection(doc *frontmatter.Doc, root, path string, diags *diagnostics.List) (*Connection, *sourceInput, bool) {
	commandRaw, err := doc.String("command")
	if err != nil || commandRaw == "" {
		diags.Errorf("mcp.command.invalid", path, "frontmatter field \"command\" must be a non-empty string")
		return nil, nil, false
	}
	if !strings.HasPrefix(commandRaw, "./") {
		diags.Errorf("mcp.command.invalid", path,
			"the command %q must be an agent-root-relative path beginning with \"./\"; bare PATH-resolved names and absolute paths are rejected", commandRaw)
		return nil, nil, false
	}
	resolvedCommand, relCommand, err := resolveInRoot(root, commandRaw)
	if err != nil {
		diags.Errorf("mcp.command.invalid", path, "the command %q %s", commandRaw, err)
		return nil, nil, false
	}
	info, err := os.Lstat(resolvedCommand)
	if err != nil || !info.Mode().IsRegular() {
		diags.Errorf("mcp.command.invalid", path,
			"the command %q must name an existing regular file inside the agent root", commandRaw)
		return nil, nil, false
	}
	commandBytes, err := os.ReadFile(resolvedCommand)
	if err != nil {
		diags.Errorf("mcp.command.invalid", path, "the command %q could not be read: %v", commandRaw, err)
		return nil, nil, false
	}
	// The fingerprint input path is the agent-root-relative slash path, not
	// the absolute resolved one: the fingerprint must depend only on
	// declared source, never on where a checkout happens to live on disk.
	cmdInput := &sourceInput{
		Path:       relCommand,
		Content:    commandBytes,
		Executable: info.Mode().Perm()&0o111 != 0,
	}

	var args []string
	if doc.Has("args") {
		args, err = doc.StringList("args")
		if err != nil {
			diags.Errorf("mcp.args.invalid", path, "frontmatter field \"args\" must be a list of strings: %s", err)
			return nil, cmdInput, false
		}
		for _, a := range args {
			if strings.Contains(a, pluginRootVar) || strings.Contains(a, pluginDataVar) {
				diags.Errorf("mcp.args.invalid", path,
					"an arg may not reference %s or %s, which are plugin-scoped and never expanded in an authored stdio declaration", pluginRootVar, pluginDataVar)
				return nil, cmdInput, false
			}
		}
	}

	var env map[string]string
	if doc.Has("env") {
		env, err = doc.StringMap("env")
		if err != nil {
			diags.Errorf("mcp.env.invalid", path, "frontmatter field \"env\" must be a mapping of strings to strings: %s", err)
			return nil, cmdInput, false
		}
		if err := validAuthoredEnv(env); err != nil {
			diags.Errorf("mcp.env.invalid", path, "%s", err)
			return nil, cmdInput, false
		}
	}

	var cwd string
	if doc.Has("cwd") {
		cwdRaw, err := doc.String("cwd")
		if err != nil || cwdRaw == "" {
			diags.Errorf("mcp.cwd.invalid", path, "frontmatter field \"cwd\" must be a non-empty string when present")
			return nil, cmdInput, false
		}
		if !strings.HasPrefix(cwdRaw, "./") {
			diags.Errorf("mcp.cwd.invalid", path,
				"the working directory %q must be an agent-root-relative path beginning with \"./\"; absolute paths are rejected", cwdRaw)
			return nil, cmdInput, false
		}
		resolvedCwd, _, err := resolveInRoot(root, cwdRaw)
		if err != nil {
			diags.Errorf("mcp.cwd.invalid", path, "the working directory %q %s", cwdRaw, err)
			return nil, cmdInput, false
		}
		if info, err := os.Lstat(resolvedCwd); err != nil || !info.IsDir() {
			diags.Errorf("mcp.cwd.invalid", path,
				"the working directory %q must name an existing directory inside the agent root", cwdRaw)
			return nil, cmdInput, false
		}
		cwd = resolvedCwd
	}

	conn := &Connection{
		Kind:         ConnectionKindStdio,
		Command:      resolvedCommand,
		Args:         args,
		Env:          env,
		Cwd:          cwd,
		commandBytes: int64(len(commandBytes)),
	}
	return conn, cmdInput, true
}

// resolveInRoot resolves candidate — already required to begin with "./" by
// its caller — against the agent root and proves it stays inside that tree
// without crossing a symlink. It mirrors pluginmcp.go's resolveInPlugin
// exactly, anchored at the agent root instead of a plugin root; pluginmcp.go
// is out of scope for this slice's edits (it is under concurrent
// modification for the plugin-reference work), so the small amount of
// path-walking logic is intentionally duplicated here rather than factored
// into a shared helper across files that cannot both be touched right now.
func resolveInRoot(root, candidate string) (string, string, error) {
	full := candidate
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, full)
	}
	full = filepath.Clean(full)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("must resolve inside the agent root")
	}
	walked := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		walked = filepath.Join(walked, part)
		info, err := os.Lstat(walked)
		if err != nil {
			return "", "", fmt.Errorf("must exist inside the agent root")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("crosses a symlink at %q; symlinks are never followed", part)
		}
	}
	return full, filepath.ToSlash(rel), nil
}

// ValidConnectionName reports whether name matches the connection filename
// grammar (ADR 0016): 1-64 characters, a leading lowercase letter, then
// lowercase letters, digits, underscores, or hyphens. Authoring commands use
// it to validate a candidate name offline before creating a connection file.
func ValidConnectionName(name string) bool {
	return connectionNamePattern.MatchString(name)
}

// ValidateConnectionURL validates a candidate connection URL against the
// exact remote target rule (ADR 0016), so authoring commands enforce the
// same rule offline that Load enforces when reading the file back.
func ValidateConnectionURL(raw string) error {
	return validConnectionURL(raw)
}

// ValidateConnectionHeaders validates a candidate authored header map
// against the exact rule Load enforces when reading a remote connection
// back, so authoring commands can reject an invalid --header offline.
func ValidateConnectionHeaders(headers map[string]string) error {
	return validAuthoredHeaders(headers)
}

// LoadConnectionsForStatus validates every mcp/<name>.md entry independent
// of the rest of the project's validity: unlike Load, one malformed
// connection or an unrelated project defect never suppresses reporting the
// others, which is exactly what "tenon mcp status" needs. It does not load
// plugins, so it cannot detect a connection/plugin server name collision;
// Load remains the authority for that check at apply and validate time.
func LoadConnectionsForStatus(root string) ([]Connection, *diagnostics.List, error) {
	diags := &diagnostics.List{}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, diags, fmt.Errorf("resolving agent root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		diags.Errorf("project.root.missing", ".", "the agent root must be an existing directory: %s", root)
		return nil, diags, nil
	}
	connections, _ := loadConnections(abs, nil, diags)
	return connections, diags, nil
}

// validConnectionURL enforces ADR 0016's remote target rule, carried forward
// unchanged by ADR 0026: an absolute HTTPS URL with a nonempty host and no
// user information, query, or fragment. This is stricter than the plugin
// remote rule: no query is ever permitted and there is no loopback exception
// to HTTPS.
func validConnectionURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the url %q is not a valid URL", raw)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("the url %q must be an absolute https URL", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("the url %q must carry a host", raw)
	}
	if parsed.User != nil {
		return fmt.Errorf("the url %q must not carry user information", raw)
	}
	if parsed.RawQuery != "" || strings.Contains(raw, "?") {
		return fmt.Errorf("the url %q must not carry a query", raw)
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("the url %q must not carry a fragment", raw)
	}
	return nil
}

// placeholderValuePattern is the exact, falsifiable grammar an authored
// header or stdio env value must satisfy when it carries a "$" (ADR 0026,
// which applies the identical grammar it inherits from ADR 0016's header
// rule to stdio env values): an optional literal prefix containing no "$",
// followed by exactly one ${VAR} environment-variable reference, with
// nothing after it. VAR is captured for the PLUGIN_ROOT/PLUGIN_DATA denylist
// below. Any other use of "$" — two references, a reference followed by a
// suffix, or a lone "$" — fails.
var placeholderValuePattern = regexp.MustCompile(`^[^$]*\$\{([A-Z_][A-Z0-9_]*)\}$`)

// deniedPlaceholderVars are the plugin-scoped variables an authored header or
// stdio env value may never reference: ${PLUGIN_ROOT} and ${PLUGIN_DATA}
// expand only inside a plugin's own mcp.json, against that plugin's own
// identity.
var deniedPlaceholderVars = map[string]bool{"PLUGIN_ROOT": true, "PLUGIN_DATA": true}

// validPlaceholderValue enforces the shared header/env value grammar (ADR
// 0026) against one value: a literal with no "$", or an optional literal
// prefix followed by exactly one ${VAR} reference and nothing after it,
// where VAR is never PLUGIN_ROOT or PLUGIN_DATA. It is the one place that
// grammar is checked, so validAuthoredHeaders and validAuthoredEnv can never
// silently drift apart.
func validPlaceholderValue(value string) error {
	if !strings.Contains(value, "$") {
		return nil
	}
	m := placeholderValuePattern.FindStringSubmatch(value)
	if m == nil {
		return fmt.Errorf("may use \"$\" only as an optional literal prefix followed by exactly one ${VAR} environment-variable reference at the end")
	}
	if deniedPlaceholderVars[m[1]] {
		return fmt.Errorf("may not reference ${%s}, which is reserved for plugin-scoped expansion", m[1])
	}
	return nil
}

// validAuthoredHeaders enforces valid HTTP header names and values with no
// case-insensitive collision (reusing the plugin mcp.json header rules), plus
// the shared placeholder-value grammar: tenon never resolves an authored
// header value, so any embedded literal is entirely the author's
// responsibility, and any use of "$" outside the single-reference grammar is
// rejected outright rather than guessed at.
func validAuthoredHeaders(headers map[string]string) error {
	if err := validHeaders(headers); err != nil {
		return err
	}
	for _, name := range sortedStringKeys(headers) {
		if err := validPlaceholderValue(headers[name]); err != nil {
			return fmt.Errorf("the header %q value %s", name, err)
		}
	}
	return nil
}

// validAuthoredEnv enforces valid stdio env variable names (non-empty, no
// "=" or NUL, mirroring the plugin mcp.json env-name check) plus the exact
// same placeholder-value grammar validAuthoredHeaders enforces (ADR 0026), so
// authored headers and authored stdio env values are validated by one shared
// rule rather than two that could drift apart.
func validAuthoredEnv(env map[string]string) error {
	for _, name := range sortedStringKeys(env) {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return fmt.Errorf("the env name %q must be a non-empty string without \"=\"", name)
		}
		if err := validPlaceholderValue(env[name]); err != nil {
			return fmt.Errorf("the env %q value %s", name, err)
		}
	}
	return nil
}
