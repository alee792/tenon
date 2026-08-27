package toolruntime

// The catalog contract and the cache identity are proven here without
// starting a language toolchain: what a host reports is held to the same
// rules whoever produced it.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
)

func definition(name string) Definition {
	return Definition{
		Name:         name,
		Description:  "Do one bounded thing.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func expectation(names ...string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		out[name] = true
	}
	return out
}

// TestCatalogMustMatchDiscovery proves a host can neither hide an authored
// tool nor invent one: the directory remains the sole registry.
func TestCatalogMustMatchDiscovery(t *testing.T) {
	if err := validateCatalog(TypeScript, expectation("shout-text"), []Definition{definition("shout-text")}); err != nil {
		t.Fatalf("a matching catalog must be accepted: %v", err)
	}

	cases := map[string][]Definition{
		"missing tool":  {},
		"extra tool":    {definition("shout-text"), definition("invented")},
		"unknown name":  {definition("invented")},
		"duplicate":     {definition("shout-text"), definition("shout-text")},
		"invalid name":  {definition("Shout_Text")},
		"empty catalog": nil,
	}
	for name, reported := range cases {
		if err := validateCatalog(TypeScript, expectation("shout-text"), reported); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

// TestCatalogDefinitionsAreBounded proves every published field is held to the
// managed surface's own contract before a harness ever sees it.
func TestCatalogDefinitionsAreBounded(t *testing.T) {
	oversizedSchema := `{"type":"object","pad":"` + strings.Repeat("a", MaxSchemaBytes) + `"}`
	cases := map[string]Definition{
		"empty description": {Name: "t", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		"long description": {Name: "t", Description: strings.Repeat("d", MaxDescriptionBytes+1),
			InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		"missing schema": {Name: "t", Description: "One thing.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		"non-object schema": {Name: "t", Description: "One thing.",
			InputSchema: json.RawMessage(`{"type":"string"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		"unparseable schema": {Name: "t", Description: "One thing.",
			InputSchema: json.RawMessage(`{`), OutputSchema: json.RawMessage(`{"type":"object"}`)},
		"oversized schema": {Name: "t", Description: "One thing.",
			InputSchema: json.RawMessage(oversizedSchema), OutputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	for name, reported := range cases {
		if err := validateCatalog(Go, expectation("t"), []Definition{reported}); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

// TestCrossLanguageDuplicatesAreRefused proves one name means one tool: two
// hosts claiming it fails inspection rather than resolving by order.
func TestCrossLanguageDuplicatesAreRefused(t *testing.T) {
	rt := &Runtime{routes: map[string]*host{}}
	typescript := &host{language: TypeScript}
	golang := &host{language: Go}

	if err := rt.adopt(typescript, []Definition{definition("shout-text")}); err != nil {
		t.Fatal(err)
	}
	err := rt.adopt(golang, []Definition{definition("shout-text")})
	if err == nil || !strings.Contains(err.Error(), "both the typescript and go language hosts") {
		t.Fatalf("error = %v, want the cross-language duplicate refusal", err)
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Phase != "inspect" {
		t.Fatalf("the refusal must be an inspection failure: %v", err)
	}
}

// TestCacheDirTracksSourceAndHosts proves the cache is keyed by exactly what
// makes it stale: the agent's source fingerprint and tenon's own hosts.
func TestCacheDirTracksSourceAndHosts(t *testing.T) {
	cfg := Config{Workspace: "/ws", Fingerprint: "sha256:abc"}
	dir := cfg.CacheDir()
	if filepath.Dir(dir) != "/ws/.tenon/cache/tools" {
		t.Fatalf("cache dir = %s, want it under the workspace state directory", dir)
	}
	if !strings.HasPrefix(filepath.Base(dir), "abc-") {
		t.Fatalf("cache dir = %s, want the source fingerprint hex", dir)
	}
	if got := len(strings.TrimPrefix(filepath.Base(dir), "abc-")); got != hostCacheKeyChars {
		t.Fatalf("host key length = %d, want %d", got, hostCacheKeyChars)
	}

	other := Config{Workspace: "/ws", Fingerprint: "sha256:def"}
	if other.CacheDir() == dir {
		t.Fatal("different source must not share a tool cache")
	}
	throwaway := Config{Workspace: "/ws", Fingerprint: "sha256:abc", CacheRoot: "/tmp/scratch"}
	if filepath.Dir(throwaway.CacheDir()) != "/tmp/scratch" {
		t.Fatalf("an overridden cache root must be honored: %s", throwaway.CacheDir())
	}
}

// TestOpenRefusesAMissingOrChangedCache proves the one message a harness sees
// when it starts a server against setup nobody prepared.
func TestOpenRefusesAMissingOrChangedCache(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	cfg := Config{
		Source:      source,
		Workspace:   workspace,
		Fingerprint: "sha256:abc",
		Tools:       []agentproject.Tool{{Name: "shout-text", Language: TypeScript, SourcePath: "tools/shout_text.ts"}},
	}

	_, err := Open(cfg)
	if err == nil || err.Error() != "tool runtime is missing or changed; run tenon apply" {
		t.Fatalf("error = %v, want the run-apply message", err)
	}

	// A cache carrying someone else's host is exactly as stale as none.
	dir := cfg.CacheDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "typescript.ts"), []byte("console.log('not tenon')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cfg); err == nil || err.Error() != "tool runtime is missing or changed; run tenon apply" {
		t.Fatalf("error = %v, want the run-apply message", err)
	}
}

// TestEnvironmentIsAnAllowlist proves a toolchain never inherits the
// operator's whole environment.
func TestEnvironmentIsAnAllowlist(t *testing.T) {
	t.Setenv("CONSPICUOUS_SECRET", "value")
	t.Setenv("LC_ALL", "C")

	env := hostEnv("DENO_DIR=/cache/deno-dir")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "CONSPICUOUS_SECRET") {
		t.Fatalf("an unallowed variable reached a host: %v", env)
	}
	for _, want := range []string{"PATH=", "LC_ALL=C", "DENO_DIR=/cache/deno-dir"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment %v is missing %q", env, want)
		}
	}
}
