package agentproject

// Connections author standalone native MCP servers (ADR 0016). Each
// connections/<name>.md carries one closed YAML frontmatter document
// selecting exactly one target form: remote streamable-http, or installed —
// an exact operator-installed native-mcp capability (ADR 0014) selected by
// package and capability id. Load validates the installed frontmatter shape
// only; resolving the selection against the operator's integration store
// happens offline at generation time (internal/claude, internal/codex),
// exactly like plugin ${PLUGIN_DATA}.

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
	// MaxConnections bounds the number of immediate connections/ files.
	MaxConnections = 128
	// MaxConnectionBytes bounds one connection source file.
	MaxConnectionBytes = 8 * 1024
	// MaxConnectionContextRunes bounds the optional trimmed Markdown body.
	MaxConnectionContextRunes = 1024
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

// Connection is one validated standalone native MCP connection: either a
// remote streamable-http endpoint or an installed package capability
// selection (ADR 0016). Kind discriminates which fields apply.
type Connection struct {
	// Name is the filename-derived connection and native server name.
	Name string
	// Kind is "remote" or "installed".
	Kind string
	// URL is the exact validated absolute HTTPS URL. Set only when
	// Kind == "remote".
	URL string
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
	// "connections/<name>.md".
	SourcePath string
}

// ConnectionKindRemote and ConnectionKindInstalled are the two supported
// connection target kinds (ADR 0016).
const (
	ConnectionKindRemote    = "remote"
	ConnectionKindInstalled = "installed"
)

// loadConnections discovers and validates the optional connections/
// directory, returning the connections sorted by name and every source file
// read as a fingerprint input. pluginServers supplies the accepted plugin MCP
// server names a connection may not collide with (ADR 0010/0016).
func loadConnections(root string, pluginServers []PluginServer, diags *diagnostics.List) ([]Connection, []sourceInput) {
	dir := filepath.Join(root, "connections")
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, nil // connections/ is optional
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		diags.Errorf("connection.entry.invalid", "connections",
			"connections must be a real directory; symlinks are never followed")
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		diags.Errorf("connection.entry.invalid", "connections", "connections could not be read: %v", err)
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
		entryPath := "connections/" + entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			diags.Errorf("connection.entry.invalid", entryPath,
				"each connections entry must be a real regular file; symlinks are never followed")
			continue
		}
		if entry.IsDir() {
			diags.Errorf("connection.entry.invalid", entryPath,
				"each connections entry must be a real regular file, not a directory")
			continue
		}
		if !entry.Type().IsRegular() {
			diags.Errorf("connection.entry.invalid", entryPath,
				"each connections entry must be a real regular file")
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			diags.Errorf("connection.entry.invalid", entryPath,
				"each connections entry must use the .md extension")
			continue
		}

		count++
		if count == MaxConnections+1 {
			diags.Errorf("connection.bounds.exceeded", "connections",
				"connections may contain at most %d files", MaxConnections)
		}

		conn, input, ok := loadConnectionFile(dir, entry.Name(), diags)
		if input != nil {
			inputs = append(inputs, *input)
		}
		if !ok {
			continue
		}
		if earlier, collide := seen[conn.Name]; collide {
			diags.Errorf("connection.name.collision", conn.SourcePath,
				"the connection name %q collides with the connection declared at %s; connection names must be unique",
				conn.Name, earlier)
			continue
		}
		if sourcePath, collide := pluginNames[conn.Name]; collide {
			diags.Errorf("connection.name.collision", conn.SourcePath,
				"the connection name %q collides with the accepted plugin MCP server declared at %s; a connection may not share a name with a plugin server",
				conn.Name, sourcePath)
			continue
		}
		seen[conn.Name] = conn.SourcePath
		connections = append(connections, conn)
	}
	slices.SortFunc(connections, func(a, b Connection) int { return strings.Compare(a.Name, b.Name) })
	return connections, inputs
}

// loadConnectionFile validates one connections/<name>.md entry: its filename
// grammar and reservation, size, encoding, and frontmatter/body contract. The
// exact source bytes are always returned as a fingerprint input when they
// could be read, regardless of validity.
func loadConnectionFile(dir, filename string, diags *diagnostics.List) (Connection, *sourceInput, bool) {
	sourcePath := "connections/" + filename
	name := strings.TrimSuffix(filename, ".md")
	valid := true

	if !connectionNamePattern.MatchString(name) {
		diags.Errorf("connection.name.invalid", sourcePath,
			"a connection filename must be 1-64 characters, starting with a lowercase letter and continuing with lowercase letters, digits, underscores, or hyphens: %q", name)
		valid = false
	}
	if name == managedConnectionName {
		diags.Errorf("connection.name.reserved", sourcePath,
			"the name %q is reserved for tenon's own managed server", managedConnectionName)
		valid = false
	}

	full := filepath.Join(dir, filename)
	info, err := os.Lstat(full)
	if err != nil {
		diags.Errorf("connection.entry.invalid", sourcePath, "the connection file could not be read: %v", err)
		return Connection{Name: name, SourcePath: sourcePath}, nil, false
	}
	if info.Size() > MaxConnectionBytes {
		diags.Errorf("connection.bounds.exceeded", sourcePath,
			"a connection file may contain at most %d bytes; found %d", MaxConnectionBytes, info.Size())
		return Connection{Name: name, SourcePath: sourcePath}, nil, false
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		diags.Errorf("connection.entry.invalid", sourcePath, "the connection file could not be read: %v", err)
		return Connection{Name: name, SourcePath: sourcePath}, nil, false
	}
	input := &sourceInput{Path: sourcePath, Content: raw, Executable: false}

	if !utf8.Valid(raw) {
		diags.Errorf("connection.entry.invalid", sourcePath, "the connection file must be valid UTF-8")
		return Connection{Name: name, SourcePath: sourcePath}, input, false
	}

	parsed, ok := parseConnection(string(raw), sourcePath, diags)
	if parsed == nil {
		return Connection{Name: name, SourcePath: sourcePath}, input, false
	}
	parsed.Name = name
	parsed.SourcePath = sourcePath
	return *parsed, input, ok && valid
}

// parseConnection enforces the closed connection frontmatter contract: one
// plain field "type" set to exactly "mcp", then exactly one target form —
// remote (transport, url) or installed (package, capability). It validates
// installed frontmatter shape only: package and capability must each be
// present, non-empty, and match ADR 0014's stable identifier grammar. It
// never contacts the integration store; that happens at generation time.
func parseConnection(content, path string, diags *diagnostics.List) (*Connection, bool) {
	raw, bodyStart, err := frontmatter.Split([]byte(content))
	if err != nil {
		diags.Errorf("connection.frontmatter.missing", path,
			"connections/<name>.md must start with YAML frontmatter delimited by --- lines")
		return nil, false
	}
	doc, err := frontmatter.Parse(raw)
	if err != nil {
		diags.Errorf("connection.frontmatter.invalid", path, "%s", err)
		return nil, false
	}

	keys := doc.Keys()
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}

	if !doc.Has("type") {
		diags.Errorf("connection.frontmatter.missing", path,
			"frontmatter must carry the field type set to \"mcp\" plus one supported target form")
		return nil, false
	}
	typeVal, err := doc.String("type")
	if err != nil || typeVal != "mcp" {
		diags.Errorf("connection.frontmatter.invalid", path, "frontmatter field \"type\" must be exactly \"mcp\"")
		return nil, false
	}

	isRemote := keySet["transport"] || keySet["url"]
	isInstalled := keySet["package"] || keySet["capability"]

	switch {
	case isRemote && isInstalled:
		diags.Errorf("connection.frontmatter.unknown-field", path,
			"frontmatter must select exactly one target form: remote (transport, url) or installed (package, capability), not both")
		return nil, false
	case !isRemote && !isInstalled:
		diags.Errorf("connection.frontmatter.missing", path,
			"frontmatter must select one target form: transport and url, or package and capability")
		return nil, false
	}

	allowed := map[string]bool{"type": true}
	form := "remote"
	if isRemote {
		allowed["transport"] = true
		allowed["url"] = true
	} else {
		form = "installed"
		allowed["package"] = true
		allowed["capability"] = true
	}
	for _, k := range keys {
		if !allowed[k] {
			diags.Errorf("connection.frontmatter.unknown-field", path,
				"the field %q is not part of a %s target declaration", k, form)
			return nil, false
		}
	}

	var conn Connection
	if isInstalled {
		pkg, err := doc.String("package")
		if err != nil || pkg == "" || !installedIDPattern.MatchString(pkg) {
			diags.Errorf("connection.target.invalid", path,
				"frontmatter field \"package\" must be a non-empty string matching %s", installedIDPattern.String())
			return nil, false
		}
		capability, err := doc.String("capability")
		if err != nil || capability == "" || !installedIDPattern.MatchString(capability) {
			diags.Errorf("connection.target.invalid", path,
				"frontmatter field \"capability\" must be a non-empty string matching %s", installedIDPattern.String())
			return nil, false
		}
		conn = Connection{Kind: ConnectionKindInstalled, Package: pkg, Capability: capability}
	} else {
		transport, err := doc.String("transport")
		if err != nil || transport != "streamable-http" {
			diags.Errorf("connection.target.invalid", path,
				"frontmatter field \"transport\" must be exactly \"streamable-http\"")
			return nil, false
		}
		rawURL, err := doc.String("url")
		if err != nil || rawURL == "" {
			diags.Errorf("connection.target.invalid", path, "frontmatter field \"url\" must be a non-empty string")
			return nil, false
		}
		if err := validConnectionURL(rawURL); err != nil {
			diags.Errorf("connection.target.invalid", path, "%s", err)
			return nil, false
		}
		conn = Connection{Kind: ConnectionKindRemote, URL: rawURL}
	}

	body := content[bodyStart:]
	if after, ok := strings.CutPrefix(body, "\r\n"); ok {
		body = after
	} else {
		body = strings.TrimPrefix(body, "\n")
	}
	body = strings.TrimSpace(body)
	if runeLen := utf8.RuneCountInString(body); runeLen > MaxConnectionContextRunes {
		diags.Errorf("connection.context.too-long", path,
			"the optional Markdown body may contain at most %d Unicode characters; found %d", MaxConnectionContextRunes, runeLen)
		return nil, false
	}
	conn.Context = body

	return &conn, true
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

// LoadConnectionsForStatus validates every connections/<name>.md entry
// independent of the rest of the project's validity: unlike Load, one
// malformed connection or an unrelated project defect never suppresses
// reporting the others, which is exactly what "tenon connection status"
// needs. It does not load plugins, so it cannot detect a connection/plugin
// server name collision; Load remains the authority for that check at apply
// and validate time.
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

// validConnectionURL enforces ADR 0016's remote target rule: an absolute
// HTTPS URL with a nonempty host and no user information, query, or
// fragment. This is stricter than the plugin remote rule: no query is ever
// permitted and there is no loopback exception to HTTPS.
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
