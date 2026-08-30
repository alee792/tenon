package agentproject

// An accepted plugin may declare MCP servers in a bounded mcp.json at its
// root (ADR 0010). Tenon validates and translates the package declaration
// into the selected harness's native project configuration; the harness
// alone starts, approves, authenticates, and operates those servers.
//
// The isolation contrast is the same one plugin skills use. A malformed
// top-level document disables only that plugin's MCP component; an invalid,
// unsupported, or duplicate server is skipped alone, never suppressing a
// valid sibling server or the plugin's skills.
//
// Everything a workspace cannot change is validated here: document shape,
// server names, transports, command containment inside the real plugin tree,
// and remote URL and header rules. ${PLUGIN_ROOT} and ${PLUGIN_DATA} expand
// exactly once at generation time, because ${PLUGIN_DATA} names a
// workspace-local directory only the apply target knows.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/alee792/tenon/internal/diagnostics"
)

// Plugin MCP bounds (ADR 0013): safety ceilings, not ordinary-use quotas.
const (
	// MaxPluginMCPBytes bounds one plugin's mcp.json.
	MaxPluginMCPBytes = 128 * 1024
	// MaxPluginServers bounds the accepted plugin MCP servers of one project.
	MaxPluginServers = 128
	// MaxPluginCommandBytes bounds one plugin-relative stdio command, whose
	// content and executable intent join the source fingerprint.
	MaxPluginCommandBytes = 16 << 20
)

// pluginMCPSchemaID is the exact canonical Agent Plugins v1.0.0 MCP schema
// identifier every mcp.json must target. Tenon implements this small schema
// locally and never fetches it.
const pluginMCPSchemaID = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"

// Supported plugin MCP transports. SSE is recognized only to be skipped.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "streamable-http"
)

// managedServerName is tenon's own managed server. It is reserved: a plugin
// server claiming it would shadow the managed boundary, so it is skipped and
// never renamed.
const managedServerName = "managed"

// Plugin-root and plugin-data variables are the only two expanded, and only
// in argument values, environment values, and working directories.
const (
	pluginRootVar = "${PLUGIN_ROOT}"
	pluginDataVar = "${PLUGIN_DATA}"
)

// Recognized server fields per transport. Every other field is unknown, and
// an unknown field skips the server rather than silently dropping declared
// behavior tenon cannot operationalize.
var (
	stdioServerFields = map[string]bool{"type": true, "command": true, "args": true, "env": true, "cwd": true}
	httpServerFields  = map[string]bool{"type": true, "url": true, "headers": true}
)

// PluginServer is one accepted plugin-declared MCP server. Argument,
// environment, and working-directory values keep their authored
// ${PLUGIN_ROOT} and ${PLUGIN_DATA} text: ${PLUGIN_DATA} names a
// workspace-local directory, so expansion happens once at generation time
// against the apply target.
type PluginServer struct {
	// Name is the portable server name, unique across the project.
	Name string
	// Plugin is the vendored plugin's storage directory name — the plugin
	// half of the data-directory identity.
	Plugin string
	// PluginRoot is the plugin's absolute real root: the ${PLUGIN_ROOT} value.
	PluginRoot string
	// SourcePath is the declaring document's authored path.
	SourcePath string
	// Transport is TransportStdio or TransportHTTP.
	Transport string
	// Command is the stdio executable: a bare name the harness resolves on
	// its own PATH, or the absolute real path of a plugin-relative command.
	Command string
	// Args, Env, and Cwd are the unexpanded stdio values; Cwd is empty when
	// the declaration carries no working directory.
	Args []string
	Env  map[string]string
	Cwd  string
	// URL and Headers are the remote values, copied literally.
	URL     string
	Headers map[string]string
	// Vendored reports whether this server's plugin root is a
	// plugins/<Plugin>/ directory inside the agent root — an authored
	// vendored plugin, or a plugin reference whose pinned content is
	// materialized beside it (issue #58) — as opposed to a plugin
	// reference resolved against the operator's plugin cache (ADR 0026
	// "plugin acquisition by pointer and pin"). Only an in-tree plugin's
	// root and any plugin-relative stdio command move when the agent source
	// is staged to a different absolute location (Blocker 2, post-review):
	// a cache tree lives at a fixed, non-staged location regardless of
	// where the agent source itself is staged, so ResolveServers only
	// re-anchors PluginRoot and RelCommand when Vendored is true.
	Vendored bool
	// RelCommand is the plugin-root-relative slash path of a plugin-relative
	// ("./...") stdio command, recorded for every such command whatever the
	// plugin's provenance, and empty for a bare PATH-resolved command name.
	// ResolveServers consults it only for a Vendored server, joining it
	// against the render-time plugin root instead of trusting Command's
	// Load-time absolute value, exactly like Connection.Command for authored
	// stdio connections — which is what lets staging re-anchor a
	// cache-resolved reference's command by marking its servers Vendored
	// (see internal/stage.reAnchorReferencedServers).
	RelCommand string
}

// ResolvedServer is one accepted server with ${PLUGIN_ROOT} and
// ${PLUGIN_DATA} expanded exactly once for one workspace and agent.
type ResolvedServer struct {
	Name       string
	SourcePath string
	Transport  string
	Command    string
	Args       []string
	Env        map[string]string
	Cwd        string
	URL        string
	Headers    map[string]string
	// PlaceholderField and Placeholder carry the first expanded value that
	// still contains placeholder-like ${...} text, in argument, environment,
	// then working-directory order. A harness that runs its own expansion
	// pass over project MCP values must skip such a server rather than risk
	// substituting an ambient value. Both are empty when no value does.
	PlaceholderField string
	Placeholder      string
}

// PluginDataDir is the private, persistent, workspace-local data directory
// for one agent-and-plugin identity: the ${PLUGIN_DATA} value. It survives
// reapply and plugin removal and is never a tenon-owned generated file.
func PluginDataDir(workspace, agent, plugin string) string {
	return filepath.Join(workspace, ".tenon", "plugin-data", agent, plugin)
}

// ResolveServers expands every accepted server for one agent root, workspace,
// and agent, supplying both variables to each stdio server's environment as
// well. root is the agent source directory generation is rendering
// against — the apply-time agent root during an ordinary apply, or a staged
// copy of it during staging (Blocker 2, post-review) — and is used only for
// a Vendored server: its PluginRoot and any plugin-relative stdio Command
// were captured once at Load time against whatever root Load happened to
// run against, so a vendored plugin's identity is recomputed here against
// root instead, exactly mirroring how an authored stdio Connection's Command
// and Cwd are absolutized at render time rather than trusted from Load. A
// non-vendored server (a plugin reference's resolved cache tree) keeps its
// Load-time PluginRoot and Command unchanged: the cache lives at a fixed
// location independent of where the agent source itself is staged.
func ResolveServers(servers []PluginServer, root, workspace, agent string) []ResolvedServer {
	out := make([]ResolvedServer, 0, len(servers))
	for _, s := range servers {
		pluginRoot := s.PluginRoot
		command := s.Command
		if s.Vendored {
			pluginRoot = filepath.Join(root, "plugins", s.Plugin)
			if s.RelCommand != "" {
				command = filepath.Join(pluginRoot, filepath.FromSlash(s.RelCommand))
			}
		}
		data := PluginDataDir(workspace, agent, s.Plugin)
		r := ResolvedServer{
			Name:       s.Name,
			SourcePath: s.SourcePath,
			Transport:  s.Transport,
			Command:    command,
			Cwd:        expandPluginVars(s.Cwd, pluginRoot, data),
			URL:        s.URL,
			Headers:    s.Headers,
		}
		for _, arg := range s.Args {
			r.Args = append(r.Args, expandPluginVars(arg, pluginRoot, data))
		}
		if s.Transport == TransportStdio {
			r.Env = map[string]string{}
			for _, key := range sortedStringKeys(s.Env) {
				r.Env[key] = expandPluginVars(s.Env[key], pluginRoot, data)
			}
			// Tenon's own two variables are always supplied, and always with
			// tenon's values: they are the contract the package is written
			// against, not author-overridable environment.
			r.Env["PLUGIN_ROOT"] = pluginRoot
			r.Env["PLUGIN_DATA"] = data
		}
		r.PlaceholderField, r.Placeholder = firstPlaceholder(r)
		out = append(out, r)
	}
	return out
}

// firstPlaceholder reports the first expanded value still carrying
// placeholder-like ${...} text, in a deterministic field order.
func firstPlaceholder(r ResolvedServer) (string, string) {
	for _, arg := range r.Args {
		if containsPlaceholder(arg) {
			return "argument", arg
		}
	}
	for _, key := range sortedStringKeys(r.Env) {
		if containsPlaceholder(r.Env[key]) {
			return "environment value " + key, r.Env[key]
		}
	}
	if containsPlaceholder(r.Cwd) {
		return "working directory", r.Cwd
	}
	return "", ""
}

// expandPluginVars replaces exactly ${PLUGIN_ROOT} and ${PLUGIN_DATA} in one
// left-to-right pass, so a substituted value is never rescanned.
func expandPluginVars(s, root, data string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '$' {
			if strings.HasPrefix(s[i:], pluginRootVar) {
				b.WriteString(root)
				i += len(pluginRootVar)
				continue
			}
			if strings.HasPrefix(s[i:], pluginDataVar) {
				b.WriteString(data)
				i += len(pluginDataVar)
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// containsPlaceholder reports whether s still carries ${...} text after
// portable expansion.
func containsPlaceholder(s string) bool {
	i := strings.Index(s, "${")
	return i >= 0 && strings.Contains(s[i+2:], "}")
}

// mergePluginServers keeps the first exact server name in acceptance order —
// plugin directories lexically, servers lexically within each document — and
// skips every later duplicate with a warning naming both authored paths
// (ADR 0010's first-wins-with-warning, unchanged). Names are never
// rewritten. The project-wide ceiling truncates the excess (ADR 0013)
// rather than failing an otherwise valid project. skipped carries every
// server that lost a naming collision (not one dropped by the ceiling): a
// masking declaration naming a plugin that did declare the server, but lost
// this collision, can then report exactly that instead of a generic
// dangling override (ADR 0026, issue #53 review).
func mergePluginServers(candidates []PluginServer, diags *diagnostics.List) (accepted, skipped []PluginServer) {
	seen := make(map[string]string, len(candidates))
	var out []PluginServer
	truncated := false
	for _, s := range candidates {
		if existing, collide := seen[s.Name]; collide {
			diags.Warnf("plugin.mcp.server.collision", s.SourcePath,
				"MCP server name %q declared at %s collides with the earlier server at %s; the later server is skipped and never renamed",
				s.Name, s.SourcePath, existing)
			skipped = append(skipped, s)
			continue
		}
		if len(out) >= MaxPluginServers {
			if !truncated {
				diags.Warnf("plugin.mcp.bounds.exceeded", "plugins",
					"a project may accept at most %d plugin MCP servers; later servers are ignored", MaxPluginServers)
				truncated = true
			}
			continue
		}
		seen[s.Name] = s.SourcePath
		out = append(out, s)
	}
	return out, skipped
}

// loadPluginMCP validates one accepted plugin's optional mcp.json: a
// bounded, regular, UTF-8 file holding a JSON object that targets the exact
// canonical MCP schema and maps server names to server declarations. A
// missing document is normal. Read bytes join the fingerprint even when the
// document turns out malformed, together with the content and executable
// intent of every accepted plugin-relative command.
func loadPluginMCP(pluginRoot, authoredRoot, pluginName string, vendored bool, diags *diagnostics.List) ([]PluginServer, []sourceInput) {
	path := authoredRoot + "/mcp.json"
	full := filepath.Join(pluginRoot, "mcp.json")

	info, err := os.Lstat(full)
	if err != nil {
		return nil, nil // missing mcp.json is normal
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		diags.Warnf("plugin.mcp.invalid", path,
			"mcp.json must be a regular file; symlinks are never followed; the plugin's MCP component is disabled")
		return nil, nil
	}
	if info.Size() > MaxPluginMCPBytes {
		diags.Warnf("plugin.mcp.invalid", path,
			"mcp.json may contain at most %d bytes; found %d; the plugin's MCP component is disabled",
			MaxPluginMCPBytes, info.Size())
		return nil, nil
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		diags.Warnf("plugin.mcp.invalid", path,
			"mcp.json could not be read: %v; the plugin's MCP component is disabled", err)
		return nil, nil
	}
	inputs := []sourceInput{{Path: path, Content: raw, Executable: info.Mode().Perm()&0o111 != 0}}

	if !utf8.Valid(raw) {
		diags.Warnf("plugin.mcp.invalid", path,
			"mcp.json must be valid UTF-8; the plugin's MCP component is disabled")
		return nil, inputs
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		diags.Warnf("plugin.mcp.invalid", path,
			"mcp.json is not valid JSON: %v; the plugin's MCP component is disabled", err)
		return nil, inputs
	}
	if schema, ok := doc["$schema"].(string); !ok || schema != pluginMCPSchemaID {
		diags.Warnf("plugin.mcp.invalid", path,
			"mcp.json must set \"$schema\" to the canonical Agent Plugins v1.0.0 MCP schema %q; the plugin's MCP component is disabled",
			pluginMCPSchemaID)
		return nil, inputs
	}
	declared, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		diags.Warnf("plugin.mcp.invalid", path,
			"mcp.json must map \"mcpServers\" to a JSON object of server declarations; found %s; the plugin's MCP component is disabled",
			jsonTypeName(doc["mcpServers"]))
		return nil, inputs
	}

	// Every plugin-relative path is proven against the plugin's real root, so
	// an unresolvable root disables the component rather than admitting an
	// unprovable declaration.
	realRoot, err := filepath.EvalSymlinks(pluginRoot)
	if err != nil {
		diags.Warnf("plugin.mcp.invalid", path,
			"the plugin directory could not be resolved to a real path: %v; the plugin's MCP component is disabled", err)
		return nil, inputs
	}

	var servers []PluginServer
	commands := map[string]bool{}
	for _, name := range sortedKeys(declared) {
		server, commandInput, ok := loadPluginServer(name, declared[name], path, pluginName, realRoot, vendored, diags)
		if !ok {
			continue
		}
		if commandInput != nil && !commands[commandInput.Path] {
			commands[commandInput.Path] = true
			inputs = append(inputs, *commandInput)
		}
		servers = append(servers, server)
	}
	return servers, inputs
}

// loadPluginServer validates one declared server. Every violation warns at
// the declaring document's authored path and skips exactly this server.
func loadPluginServer(name string, raw any, path, pluginName, realRoot string, vendored bool, diags *diagnostics.List) (PluginServer, *sourceInput, bool) {
	invalid := func(format string, args ...any) (PluginServer, *sourceInput, bool) {
		diags.Warnf("plugin.mcp.server.invalid", path,
			"MCP server %q is skipped: "+format, append([]any{name}, args...)...)
		return PluginServer{}, nil, false
	}

	if !validServerName(name) {
		return invalid("a server name must match the portable grammar ^[a-z][a-z0-9-]{0,62}$")
	}
	if name == managedServerName {
		diags.Warnf("plugin.mcp.server.collision", path,
			"MCP server name %q is reserved for tenon's own managed server; the plugin server is skipped and never renamed",
			managedServerName)
		return PluginServer{}, nil, false
	}
	declaration, ok := raw.(map[string]any)
	if !ok {
		return invalid("a server declaration must be a JSON object; found %s", jsonTypeName(raw))
	}

	transport := TransportStdio
	if value, present := declaration["type"]; present {
		text, isString := value.(string)
		if !isString {
			return invalid("\"type\" must be a string when present; found %s", jsonTypeName(value))
		}
		switch text {
		case TransportStdio, TransportHTTP:
			transport = text
		case "sse":
			return invalid("the sse transport is not supported")
		default:
			return invalid("\"type\" must be %q or %q; found %q", TransportStdio, TransportHTTP, text)
		}
	}
	known := stdioServerFields
	if transport == TransportHTTP {
		known = httpServerFields
	}
	for _, field := range sortedKeys(declaration) {
		if !known[field] {
			return invalid("the field %q is not part of a %s server declaration", field, transport)
		}
	}

	server := PluginServer{
		Name:       name,
		Plugin:     pluginName,
		PluginRoot: realRoot,
		SourcePath: path,
		Transport:  transport,
		Vendored:   vendored,
	}
	if transport == TransportHTTP {
		remote, ok := declaration["url"].(string)
		if !ok || remote == "" {
			return invalid("a %s server requires a non-empty \"url\" string", TransportHTTP)
		}
		if err := validRemoteURL(remote); err != nil {
			return invalid("%s", err)
		}
		headers, err := stringMap(declaration, "headers")
		if err != nil {
			return invalid("%s", err)
		}
		if err := validHeaders(headers); err != nil {
			return invalid("%s", err)
		}
		server.URL = remote
		server.Headers = headers
		return server, nil, true
	}

	command, ok := declaration["command"].(string)
	if !ok || command == "" {
		return invalid("a %s server requires a non-empty \"command\" string", TransportStdio)
	}
	var commandInput *sourceInput
	switch {
	case strings.HasPrefix(command, "./"):
		resolved, rel, err := resolveInPlugin(realRoot, command)
		if err != nil {
			return invalid("the plugin-relative command %q %s", command, err)
		}
		info, err := os.Lstat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return invalid("the plugin-relative command %q must name a regular file inside the plugin directory", command)
		}
		if info.Size() > MaxPluginCommandBytes {
			return invalid("the plugin-relative command %q may contain at most %d bytes; found %d",
				command, MaxPluginCommandBytes, info.Size())
		}
		content, err := os.ReadFile(resolved)
		if err != nil {
			return invalid("the plugin-relative command %q could not be read: %v", command, err)
		}
		commandInput = &sourceInput{
			Path:       strings.TrimSuffix(path, "/mcp.json") + "/" + rel,
			Content:    content,
			Executable: info.Mode().Perm()&0o111 != 0,
		}
		server.Command = resolved
		server.RelCommand = rel
	case strings.ContainsAny(command, `/\`) || command == "." || command == "..":
		return invalid("a command is either a bare executable name or a plugin-relative \"./\" path; found %q", command)
	case containsPlaceholder(command):
		// Expansion never applies to a bare executable name, so
		// placeholder-like text there is a name tenon would hand a harness
		// that might expand it into an ambient executable.
		return invalid("a bare executable name is never expanded and may not contain placeholder-like ${...} text; found %q", command)
	default:
		server.Command = command
	}

	if value, present := declaration["args"]; present {
		list, isList := value.([]any)
		if !isList {
			return invalid("\"args\" must be an array of strings when present; found %s", jsonTypeName(value))
		}
		for _, item := range list {
			text, isString := item.(string)
			if !isString {
				return invalid("every \"args\" entry must be a string; found %s", jsonTypeName(item))
			}
			server.Args = append(server.Args, text)
		}
	}
	env, err := stringMap(declaration, "env")
	if err != nil {
		return invalid("%s", err)
	}
	for _, key := range sortedStringKeys(env) {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return invalid("every \"env\" name must be a non-empty string without \"=\"; found %q", key)
		}
	}
	server.Env = env

	if value, present := declaration["cwd"]; present {
		cwd, isString := value.(string)
		if !isString || cwd == "" {
			return invalid("\"cwd\" must be a non-empty string when present")
		}
		// Containment is proven with ${PLUGIN_ROOT} expanded, because
		// addressing the plugin tree is exactly what that variable is for,
		// and with ${PLUGIN_DATA} left literal, because the workspace-local
		// data directory is never inside the plugin tree. The unexpanded text
		// is what the harness configuration renders.
		resolved, _, err := resolveInPlugin(realRoot, expandPluginVars(cwd, realRoot, pluginDataVar))
		if err != nil {
			return invalid("the working directory %q %s", cwd, err)
		}
		if info, err := os.Lstat(resolved); err != nil || !info.IsDir() {
			return invalid("the working directory %q must name a directory inside the plugin directory", cwd)
		}
		server.Cwd = cwd
	}
	return server, commandInput, true
}

// resolveInPlugin resolves candidate against the plugin's real root and
// proves it stays inside that tree without crossing a symlink. It returns the
// absolute real path and the plugin-relative slash path.
func resolveInPlugin(realRoot, candidate string) (string, string, error) {
	full := candidate
	if !filepath.IsAbs(full) {
		full = filepath.Join(realRoot, full)
	}
	full = filepath.Clean(full)
	rel, err := filepath.Rel(realRoot, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("must resolve inside the plugin directory")
	}
	walked := realRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		walked = filepath.Join(walked, part)
		info, err := os.Lstat(walked)
		if err != nil {
			return "", "", fmt.Errorf("must exist inside the plugin directory")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("crosses a symlink at %q; symlinks are never followed", part)
		}
	}
	return full, filepath.ToSlash(rel), nil
}

// validServerName reports whether name matches the portable server-name
// grammar ^[a-z][a-z0-9-]{0,62}$.
func validServerName(name string) bool {
	if len(name) == 0 || len(name) > 63 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// validRemoteURL enforces the portable remote endpoint rules: an absolute
// HTTP(S) URL carrying no user information and no fragment, where anything
// but a loopback host requires HTTPS.
func validRemoteURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the url %q is not a valid URL", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("the url %q must be an absolute http or https URL", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("the url %q must carry a host", raw)
	}
	if parsed.User != nil {
		return fmt.Errorf("the url %q must not carry user information", raw)
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("the url %q must not carry a fragment", raw)
	}
	if parsed.Scheme == "http" && !loopbackHost(parsed.Hostname()) {
		return fmt.Errorf("the url %q must use https; plain http is accepted only for loopback hosts", raw)
	}
	return nil
}

// loopbackHost reports whether host names the local machine.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validHeaders enforces valid HTTP header names and values with no
// case-insensitive collision. Headers are copied literally: they are visible
// source configuration, never a credential channel, so tenon neither
// inspects nor transforms their values.
func validHeaders(headers map[string]string) error {
	seen := make(map[string]string, len(headers))
	for _, name := range sortedStringKeys(headers) {
		if !validHeaderName(name) {
			return fmt.Errorf("the header name %q is not a valid HTTP header name", name)
		}
		if !validHeaderValue(headers[name]) {
			return fmt.Errorf("the header %q carries a value that is not a valid HTTP header value", name)
		}
		lower := strings.ToLower(name)
		if earlier, collide := seen[lower]; collide {
			return fmt.Errorf("the header name %q collides case-insensitively with %q", name, earlier)
		}
		seen[lower] = name
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	const tokenExtra = "!#$%&'*+-.^_`|~"
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte(tokenExtra, c) >= 0 {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\t' || c >= 0x20 && c != 0x7f {
			continue
		}
		return false
	}
	return !strings.HasPrefix(value, " ") && !strings.HasSuffix(value, " ")
}

// stringMap decodes an optional object of strings, naming the exact
// violation. A missing field yields a nil map.
func stringMap(declaration map[string]any, field string) (map[string]string, error) {
	value, present := declaration[field]
	if !present {
		return nil, nil
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return nil, fmt.Errorf("%q must be a JSON object of strings when present; found %s", field, jsonTypeName(value))
	}
	out := make(map[string]string, len(object))
	for _, key := range sortedKeys(object) {
		text, isString := object[key].(string)
		if !isString {
			return nil, fmt.Errorf("every %q value must be a string; %q is %s", field, key, jsonTypeName(object[key]))
		}
		out[key] = text
	}
	return out, nil
}

// sortedStringKeys returns m's keys sorted, so generated configuration and
// diagnostics render deterministically.
func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
