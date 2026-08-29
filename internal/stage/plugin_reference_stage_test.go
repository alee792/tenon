package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
	"github.com/alee792/tenon/internal/claude"
	"github.com/alee792/tenon/internal/pluginref"
)

// withStagePluginCache installs cache for the duration of one test and
// restores the fail-closed default afterward (agentproject.ConfigurePluginCache
// is a package-level seam, exactly as internal/agentproject's own tests use
// it).
func withStagePluginCache(t *testing.T, cache agentproject.PluginCache) {
	t.Helper()
	agentproject.ConfigurePluginCache(cache)
	t.Cleanup(func() { agentproject.ConfigurePluginCache(nil) })
}

// writeStagePluginReference writes a plugins/<name>.md reference file under
// agentRoot.
func writeStagePluginReference(t *testing.T, agentRoot, name, source, rev string) {
	t.Helper()
	dir := filepath.Join(agentRoot, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nsource: " + source + "\nrev: " + rev + "\n---\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newLocalGitFixturePluginRepo creates a local git repo with one commit
// carrying a minimal valid plugin package (manifest, one skill, and an
// mcp.json declaring a plugin-relative stdio server whose command file keeps
// its executable bit through the commit), and returns the repo path and
// commit SHA. Fetch accepts a local path directly (the documented pluginref
// test seam); the CLI's own https-only authored grammar is bypassed here the
// same way cmd/tenon's plugin tests bypass it — by fetching from the local
// path and then recording a conspicuous https source in the cache state.
func newLocalGitFixturePluginRepo(t *testing.T) (repo, rev string) {
	t.Helper()
	repo = t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=tenon-test", "GIT_AUTHOR_EMAIL=tenon-test@example.invalid",
		"GIT_COMMITTER_NAME=tenon-test", "GIT_COMMITTER_EMAIL=tenon-test@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")

	write := func(rel, content string) {
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plugin.json", `{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "observability"}`)
	write("skills/telemetry/SKILL.md", "---\nname: telemetry\ndescription: Reports on telemetry pipelines.\n---\n\nUse telemetry tools.\n")
	write("mcp.json", `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {`+
		`"telemetry": {"command": "./bin/serve", "env": {"ROOT": "${PLUGIN_ROOT}"}}}}`)
	serveScript := filepath.Join(repo, "bin", "serve")
	if err := os.MkdirAll(filepath.Dir(serveScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serveScript, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	run("add", "-A")
	run("commit", "-m", "fixture")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return repo, strings.TrimSpace(string(out))
}

// rewriteStageCachedSource patches a cache entry's already-written state.json
// to record source in place, leaving its digest and every other field
// exactly as Fetch left them — the same test-only surgery cmd/tenon's plugin
// tests use, so a test can seed the cache from a local fixture path (the
// only source no test may reach a real network for) while the agent's own
// reference file still declares a conspicuous, grammar-valid https source.
func rewriteStageCachedSource(t *testing.T, base, rev, source string) {
	t.Helper()
	statePath := filepath.Join(base, rev, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state pluginref.State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.Source = source
	rewritten, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, append(rewritten, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStagePluginReferenceMaterializesResolvedContent proves the core of
// issue #58: a plugin reference's resolved cache tree is copied into the
// staged filesystem at plugins/<name>/ for both harnesses, the generated
// configuration renders PLUGIN_ROOT and the plugin-relative command under
// the staged path (never the cache base or a build directory), and the
// copied command file keeps its executable bit.
func TestStagePluginReferenceMaterializesResolvedContent(t *testing.T) {
	repo, rev := newLocalGitFixturePluginRepo(t)
	cacheBase := t.TempDir()
	cache := pluginref.NewCache(cacheBase)
	if _, err := cache.Fetch(repo, rev); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	const fakeSource = "https://example.com/fixture-observability-plugin.git"
	rewriteStageCachedSource(t, cacheBase, rev, fakeSource)
	withStagePluginCache(t, cache)

	agent := writeAgent(t, "ref-agent")
	writeStagePluginReference(t, agent, "obs", fakeSource, rev)
	exe := fakeExecutable(t)

	wantPluginRoot := finalAgentsRoot + "/ref-agent/plugins/obs"
	wantCommand := wantPluginRoot + "/bin/serve"

	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			out := stageWith(t, agent, harness, exe)

			// The resolved cache content landed under plugins/obs/, re-anchored
			// exactly like a vendored plugin.
			pluginRoot := filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(wantPluginRoot, "/")))
			for _, rel := range []string{"plugin.json", "mcp.json", "skills/telemetry/SKILL.md", "bin/serve"} {
				if _, err := os.Stat(filepath.Join(pluginRoot, filepath.FromSlash(rel))); err != nil {
					t.Fatalf("expected staged plugin file %s: %v", rel, err)
				}
			}
			info, err := os.Stat(filepath.Join(pluginRoot, "bin", "serve"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("staged plugin-relative command must keep its executable bit: mode %v", info.Mode())
			}

			// The reference file's own bytes still stage as authored source too
			// (harmless provenance).
			if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(finalAgentsRoot+"/ref-agent/plugins/obs.md", "/")))); err != nil {
				t.Fatalf("expected the reference file itself to stage as authored source: %v", err)
			}

			configPath := filepath.Join(out, "workspace", ".mcp.json")
			if harness == "codex" {
				configPath = filepath.Join(out, "workspace", ".codex", "config.toml")
			}
			config, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(config)
			if !strings.Contains(text, wantPluginRoot) {
				t.Fatalf("generated config must embed the staged plugin root %s: %s", wantPluginRoot, text)
			}
			if !strings.Contains(text, wantCommand) {
				t.Fatalf("generated config must embed the staged plugin-relative command %s: %s", wantCommand, text)
			}
			if strings.Contains(text, cacheBase) {
				t.Fatalf("generated config must never embed the plugin cache base path %s: %s", cacheBase, text)
			}
			if strings.Contains(text, out) {
				t.Fatalf("generated config must never embed the physical staging directory %s: %s", out, text)
			}

			// The negative assertion issue #58 calls for explicitly: no staged
			// file anywhere in the tree carries the cache base path.
			err = filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.Type().IsRegular() {
					return nil
				}
				data, rerr := os.ReadFile(path)
				if rerr != nil {
					return rerr
				}
				if strings.Contains(string(data), cacheBase) {
					t.Fatalf("staged file %s embeds the plugin cache base path %s", path, cacheBase)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// sequencedPluginCache resolves successfully exactly once (satisfying Load,
// inside Stage), then fails every later call, standing in for a cache
// tampered with or pruned between Load and staging's own re-verification
// copy step.
type sequencedPluginCache struct {
	calls int
	root  string
}

func (c *sequencedPluginCache) Resolve(source, rev string) (string, error) {
	c.calls++
	if c.calls > 1 {
		return "", fmt.Errorf("pluginref.digest.mismatch: the cached tree for rev %s no longer matches its recorded digest", rev)
	}
	return c.root, nil
}

// newMinimalCachedPluginTree materializes a minimal valid plugin package
// (manifest plus one skill, no MCP component) on disk, standing in for an
// already-resolved pluginref cache tree.
func newMinimalCachedPluginTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"),
		[]byte(`{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "observability"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "skills", "telemetry")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: telemetry\ndescription: Reports on telemetry pipelines.\n---\n\nUse telemetry tools.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestStagePluginReferenceTamperedCacheFailsClosed proves staging's own
// digest re-verification (issue #58): a cache that resolved fine at Load
// time but fails on the second Resolve call — the one staging performs
// immediately before copying — fails the stage closed with the same
// plugin.reference.unresolved diagnostic Load itself would raise, and
// publishes no output directory.
func TestStagePluginReferenceTamperedCacheFailsClosed(t *testing.T) {
	cachedRoot := newMinimalCachedPluginTree(t)
	withStagePluginCache(t, &sequencedPluginCache{root: cachedRoot})

	agent := writeAgent(t, "tampered-ref-agent")
	writeStagePluginReference(t, agent, "obs", "https://example.com/fixture-observability-plugin.git",
		"0123456789abcdef0123456789abcdef01234567")
	exe := fakeExecutable(t)

	out := filepath.Join(t.TempDir(), "staged")
	res, diags, err := Stage(context.Background(), Options{
		AgentDir: agent, Harness: "claude", Output: out, Executable: exe, Driver: claude.Driver{},
	})
	if err != nil {
		t.Fatalf("unexpected environment error: %v", err)
	}
	if res != nil {
		t.Fatal("staging must fail closed when the cache is tampered with between Load and copy")
	}
	found := false
	for _, d := range diags.All() {
		if d.ID == "plugin.reference.unresolved" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a plugin.reference.unresolved diagnostic, got %v", diags.All())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("no output directory should be published on stage failure: err=%v", err)
	}
}

// TestPlainApplyStillPointsAtCache proves the deliberate asymmetry (issue
// #58's design decision): an ordinary, non-staging Load-and-resolve keeps
// pointing a reference's stdio server at the operator's cache path, since
// only staging materializes and re-anchors the content.
func TestPlainApplyStillPointsAtCache(t *testing.T) {
	cachedRoot := newMinimalCachedPluginTreeWithMCP(t)
	withStagePluginCache(t, &sequencedPluginCache{root: cachedRoot})

	agent := writeAgent(t, "plain-ref-agent")
	writeStagePluginReference(t, agent, "obs", "https://example.com/fixture-observability-plugin.git",
		"0123456789abcdef0123456789abcdef01234567")

	p, diags, err := agentproject.Load(agent)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
	resolved := agentproject.ResolveServers(p.PluginServers, p.Root, "/workspace", p.Name)
	if len(resolved) != 1 {
		t.Fatalf("expected exactly one resolved server, got %d", len(resolved))
	}
	if resolved[0].Env["PLUGIN_ROOT"] != cachedRoot {
		t.Fatalf("a plain apply's PLUGIN_ROOT must still be the cache path %s, got %s", cachedRoot, resolved[0].Env["PLUGIN_ROOT"])
	}
}

// newMinimalCachedPluginTreeWithMCP is newMinimalCachedPluginTree plus an
// mcp.json declaring one stdio server, for tests that need a resolvable
// PluginServer rather than just skills.
func newMinimalCachedPluginTreeWithMCP(t *testing.T) string {
	t.Helper()
	dir := newMinimalCachedPluginTree(t)
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"),
		[]byte(`{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {`+
			`"telemetry": {"command": "server"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
