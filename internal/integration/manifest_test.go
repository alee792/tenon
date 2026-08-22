package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// hashHex is the lowercase-hex SHA-256 of b, matching the manifest pin format.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling fixture: %v", err)
	}
	return b
}

// fakePayload is the deterministic fake artifact payload. It is never executed;
// its only role is to be pinned by a real SHA-256 the tests compute.
func fakePayload() []byte { return []byte("#!/bin/sh\necho fake-native-mcp\n") }

// baseArtifact returns a fresh valid binary artifact map. For the binary
// format the raw payload is itself the executable, so the raw and exec pins
// coincide.
func baseArtifact() map[string]any {
	p := fakePayload()
	sha := hashHex(p)
	return map[string]any{
		"id":          "server-darwin-arm64",
		"os":          "darwin",
		"arch":        "arm64",
		"format":      "binary",
		"size":        len(p),
		"sha256":      sha,
		"exec_path":   "bin/server",
		"exec_size":   len(p),
		"exec_sha256": sha,
		"package":     "payload/server",
	}
}

func baseCapability() map[string]any {
	return map[string]any{
		"id":          "mcp",
		"type":        "native-mcp",
		"version":     1,
		"server_name": "fake-server",
		"artifacts":   []any{"server-darwin-arm64"},
		"executable":  "bin/server",
		"args":        []any{"--stdio"},
		"workdir":     "",
		"env":         map[string]any{"LOG_LEVEL": "info"},
		"required_env": []any{
			map[string]any{"name": "FAKE_API_TOKEN", "description": "The ambient token the fake server reads from its own environment."},
		},
		"targets": map[string]any{
			"claude": map[string]any{"startup": "optional"},
			"codex":  map[string]any{"startup": "required"},
		},
	}
}

func baseManifest() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"id":             "fake-integration",
		"version":        "1.2.0",
		"name":           "Fake Integration",
		"description":    "A credential-free fake native MCP server for tests.",
		"license":        "MIT",
		"source":         "https://example.test/fake",
		"revision":       "abc123",
		"compat":         map[string]any{"minimum": "0.1.0", "before": "2.0.0"},
		"artifacts":      []any{baseArtifact()},
		"capabilities":   []any{baseCapability()},
	}
}

func wantManifestCode(t *testing.T, err error, code string) {
	t.Helper()
	var me *ManifestError
	if !errors.As(err, &me) {
		t.Fatalf("want ManifestError, got %T: %v", err, err)
	}
	if me.Code != code {
		t.Fatalf("ManifestError code = %q, want %q", me.Code, code)
	}
}

func TestParseManifestHappyPath(t *testing.T) {
	m, err := ParseManifest(mustJSON(t, baseManifest()))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.ID != "fake-integration" || m.Version != "1.2.0" {
		t.Fatalf("identity = %s %s", m.ID, m.Version)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0].NativeMCP == nil {
		t.Fatalf("expected one native-mcp capability")
	}
	native := m.Capabilities[0].NativeMCP
	if native.ServerName != "fake-server" {
		t.Fatalf("server_name = %s", native.ServerName)
	}
	if len(native.RequiredEnv) != 1 || native.RequiredEnv[0].Name != "FAKE_API_TOKEN" {
		t.Fatalf("required_env = %+v", native.RequiredEnv)
	}
	if native.Env["LOG_LEVEL"] != "info" {
		t.Fatalf("env = %+v", native.Env)
	}
	// The immutable identity is the SHA-256 of the exact bytes, and Raw hands
	// out a defensive copy.
	raw := m.Raw()
	if hashHex(raw) != m.SHA256() {
		t.Fatalf("SHA256 does not match Raw bytes")
	}
	raw[0] = 'X'
	if hashHex(m.Raw()) != m.SHA256() {
		t.Fatalf("mutating a Raw copy changed the retained identity")
	}
}

func TestParseManifestRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m map[string]any)
		code   string
	}{
		{"bad schema_version", func(m map[string]any) { m["schema_version"] = 2 }, "manifest.schema_version.unsupported"},
		{"bad id grammar", func(m map[string]any) { m["id"] = "Bad_ID" }, "manifest.id.invalid"},
		{"bad version", func(m map[string]any) { m["version"] = "1.2" }, "manifest.version.invalid"},
		{"empty name", func(m map[string]any) { m["name"] = "" }, "manifest.name.invalid"},
		{"unknown top field", func(m map[string]any) { m["extra"] = 1 }, "manifest.decode"},
		{"compat inverted", func(m map[string]any) {
			m["compat"] = map[string]any{"minimum": "2.0.0", "before": "1.0.0"}
		}, "manifest.compat.invalid"},
		{"no artifacts", func(m map[string]any) { m["artifacts"] = []any{} }, "manifest.artifacts.empty"},
		{"bad artifact format", func(m map[string]any) {
			a := baseArtifact()
			a["format"] = "rar"
			m["artifacts"] = []any{a}
		}, "manifest.artifact.format.invalid"},
		{"artifact source both", func(m map[string]any) {
			a := baseArtifact()
			a["https"] = "https://example.test/server"
			m["artifacts"] = []any{a}
		}, "manifest.artifact.source.union"},
		{"artifact source neither", func(m map[string]any) {
			a := baseArtifact()
			delete(a, "package")
			m["artifacts"] = []any{a}
		}, "manifest.artifact.source.union"},
		{"artifact package traversal", func(m map[string]any) {
			a := baseArtifact()
			a["package"] = "../escape"
			m["artifacts"] = []any{a}
		}, "manifest.artifact.package.invalid"},
		{"artifact https with query", func(m map[string]any) {
			a := baseArtifact()
			delete(a, "package")
			a["https"] = "https://example.test/server?token=x"
			m["artifacts"] = []any{a}
		}, "manifest.artifact.https.invalid"},
		{"no capabilities", func(m map[string]any) { m["capabilities"] = []any{} }, "manifest.capabilities.empty"},
		{"executable not in closure", func(m map[string]any) {
			c := baseCapability()
			c["executable"] = "bin/other"
			m["capabilities"] = []any{c}
		}, "native-mcp.executable.invalid"},
		{"placeholder in args", func(m map[string]any) {
			c := baseCapability()
			c["args"] = []any{"--token", "${SECRET}"}
			m["capabilities"] = []any{c}
		}, "native-mcp.args.placeholder"},
		{"placeholder in env", func(m map[string]any) {
			c := baseCapability()
			c["env"] = map[string]any{"LOG_LEVEL": "$LEVEL"}
			m["capabilities"] = []any{c}
		}, "native-mcp.env.placeholder"},
		{"required ambient with default", func(m map[string]any) {
			c := baseCapability()
			c["env"] = map[string]any{"FAKE_API_TOKEN": "x"}
			m["capabilities"] = []any{c}
		}, "native-mcp.env.required_conflict"},
		{"secret value in env default", func(m map[string]any) {
			c := baseCapability()
			c["env"] = map[string]any{"GREETING": "ghp_FAKEfakefakefakefake"}
			m["capabilities"] = []any{c}
		}, "native-mcp.env.secret"},
		{"bad prose alphabet", func(m map[string]any) {
			c := baseCapability()
			c["required_env"] = []any{map[string]any{"name": "FAKE_API_TOKEN", "description": "has_underscore = bad"}}
			m["capabilities"] = []any{c}
		}, "native-mcp.required_env.description.invalid"},
		{"empty prose", func(m map[string]any) {
			c := baseCapability()
			c["required_env"] = []any{map[string]any{"name": "FAKE_API_TOKEN", "description": ""}}
			m["capabilities"] = []any{c}
		}, "native-mcp.required_env.description.invalid"},
		{"long prose", func(m map[string]any) {
			c := baseCapability()
			c["required_env"] = []any{map[string]any{"name": "FAKE_API_TOKEN", "description": "A" + strings.Repeat("a", 512)}}
			m["capabilities"] = []any{c}
		}, "native-mcp.required_env.description.invalid"},
		{"no targets", func(m map[string]any) {
			c := baseCapability()
			c["targets"] = map[string]any{}
			m["capabilities"] = []any{c}
		}, "native-mcp.targets.empty"},
		{"bad startup", func(m map[string]any) {
			c := baseCapability()
			c["targets"] = map[string]any{"claude": map[string]any{"startup": "eager"}}
			m["capabilities"] = []any{c}
		}, "native-mcp.targets.startup.invalid"},
		{"unknown native field", func(m map[string]any) {
			c := baseCapability()
			c["extra"] = true
			m["capabilities"] = []any{c}
		}, "native-mcp.decode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := baseManifest()
			tt.mutate(m)
			_, err := ParseManifest(mustJSON(t, m))
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			wantManifestCode(t, err, tt.code)
		})
	}
}

func TestParseManifestUnsupportedCapability(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(c map[string]any)
	}{
		{"unknown type", func(c map[string]any) { c["type"] = "channel-adapter" }},
		{"unknown version", func(c map[string]any) { c["version"] = 2 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := baseManifest()
			c := baseCapability()
			tt.mutate(c)
			m["capabilities"] = []any{c}
			_, err := ParseManifest(mustJSON(t, m))
			var ue *UnsupportedCapabilityError
			if !errors.As(err, &ue) {
				t.Fatalf("want UnsupportedCapabilityError, got %T: %v", err, err)
			}
		})
	}
}

func TestParseManifestBounds(t *testing.T) {
	if _, err := ParseManifest(nil); err == nil {
		t.Fatalf("empty manifest should be rejected")
	}
	big := make([]byte, MaxManifestBytes+1)
	if _, err := ParseManifest(big); err == nil {
		t.Fatalf("oversize manifest should be rejected")
	}
}
