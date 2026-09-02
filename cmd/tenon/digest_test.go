package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alee792/tenon/internal/agentproject"
)

// brokenInstructions has no frontmatter, so the loader refuses the root
// before it inventories anything: the digest for this tree comes from the
// fallback walk rather than from the loader's own file list.
const brokenInstructions = "no frontmatter, so this root does not load\n"

// finalFingerprint reads the fingerprint out of a passing run's final
// result object.
func finalFingerprint(t *testing.T, stream string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stream), "\n")
	var final struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatalf("the stream's last line must be one JSON object, got %q: %v", lines[len(lines)-1], err)
	}
	return final.Fingerprint
}

// TestGateFailureCarriesASourceDigest proves a rejected candidate is
// attributable: the gate_failed object names the bytes that failed, so a
// loop that discards a mutation can still say which mutation it discarded
// without hashing the tree itself.
func TestGateFailureCarriesASourceDigest(t *testing.T) {
	// The digest cases run the portable gate; the suite must not inherit
	// the operator's TENON_HARNESS, which would select the harness gate and
	// fail resolution on a machine with no harness binary.
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", brokenInstructions)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", agent, "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("a failing gate must exit 1, got %d: %s", code, stdout.String())
	}
	final := finalOutcome(t, stdout.String())
	if final.Outcome != "gate_failed" {
		t.Fatalf("outcome = %q, want gate_failed: %s", final.Outcome, stdout.String())
	}
	if !strings.HasPrefix(final.SourceDigest, "sha256:") {
		t.Fatalf("a gate failure must carry a sha256 source digest, got %q", final.SourceDigest)
	}
}

// TestSourceDigestIsDeterministicForTheSameTree proves the digest is a
// content hash and nothing else: the same broken tree digests identically
// across runs and across two separately written copies of it, and changing
// one authored byte changes it.
func TestSourceDigestIsDeterministicForTheSameTree(t *testing.T) {
	// The digest cases run the portable gate; the suite must not inherit
	// the operator's TENON_HARNESS, which would select the harness gate and
	// fail resolution on a machine with no harness binary.
	t.Setenv("TENON_HARNESS", "")
	digestOf := func(agent string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := run([]string{"check", agent, "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
			t.Fatalf("a failing gate must exit 1, got %d: %s", code, stdout.String())
		}
		return finalOutcome(t, stdout.String()).SourceDigest
	}

	first := writeAgent(t, "my-agent", brokenInstructions)
	writeFile(t, first, "skills/echo/SKILL.md", []byte(echoSkillMD), 0o644)
	second := writeAgent(t, "my-agent", brokenInstructions)
	writeFile(t, second, "skills/echo/SKILL.md", []byte(echoSkillMD), 0o644)

	a, b := digestOf(first), digestOf(second)
	if a != b {
		t.Fatalf("two byte-identical broken trees must digest identically: %s vs %s", a, b)
	}
	if again := digestOf(first); again != a {
		t.Fatalf("the same tree must digest identically across runs: %s vs %s", a, again)
	}

	writeFile(t, second, "skills/echo/SKILL.md", []byte(echoSkillMD+"\none more line\n"), 0o644)
	if changed := digestOf(second); changed == a {
		t.Fatalf("an authored edit must change the digest, both were %s", a)
	}
}

// TestSourceDigestIsNeverAFingerprint proves the property that keeps the two
// identities apart. A digest names bytes; a fingerprint names a
// configuration a gate proved (ADR 0025). They are hashed under different
// domains by construction, so the digest of a tree can never equal that
// tree's own fingerprint even though both hash the same content — and a
// consumer that joined failures and successes on one field would otherwise
// be one collision away from treating an unproven source as a proven one.
func TestSourceDigestIsNeverAFingerprint(t *testing.T) {
	// The digest cases run the portable gate; the suite must not inherit
	// the operator's TENON_HARNESS, which would select the harness gate and
	// fail resolution on a machine with no harness binary.
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", validInstructions)

	// The tree is valid, so the gate passes and mints a fingerprint.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", agent, "--format", "jsonl"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("check exit %d: %s", code, stderr.String())
	}
	final := finalOutcome(t, stdout.String())
	if final.Outcome != "ok" {
		t.Fatalf("outcome = %q, want ok: %s", final.Outcome, stdout.String())
	}
	// A passing run carries no digest at all: it has the stronger name.
	if final.SourceDigest != "" {
		t.Fatalf("a passing gate must emit no source digest, got %q", final.SourceDigest)
	}

	fingerprint := finalFingerprint(t, stdout.String())
	if fingerprint == "" {
		t.Fatal("a passing check must report a fingerprint")
	}

	// The digest over the loader's OWN inventory — the same inputs, in the
	// same order, that the fingerprint was computed from — still differs.
	// Comparing the fallback walk's digest instead would prove nothing: it
	// hashes a different file set, so it would differ even with no domain
	// separation at all.
	p, diags, err := agentproject.Load(agent)
	if err != nil || p == nil || diags.HasErrors() {
		t.Fatalf("load agent: err=%v diags=%v", err, diags)
	}
	if len(p.FingerprintEntries) == 0 {
		t.Fatal("the loader must have inventoried the passing source")
	}
	if p.Fingerprint != fingerprint {
		t.Fatalf("the loaded fingerprint %s must be the one check reported %s", p.Fingerprint, fingerprint)
	}
	digest := sourceDigest(agent, p)
	if digest == "" {
		t.Fatal("a readable root must have a digest")
	}
	if digest == p.Fingerprint {
		t.Fatalf("a digest over the fingerprint's own inputs must still differ from it: %s", digest)
	}
}

// TestDigestPreimageCarriesItsDomain proves the separation above is a
// property of the construction and not a coincidence of two hash functions
// disagreeing: the domain prefix is in the material actually hashed, so no
// source can produce one value under both names. Delete the prefix and this
// fails, which is what the property is for.
func TestDigestPreimageCarriesItsDomain(t *testing.T) {
	preimage := digestPreimage([]digestEntry{
		{path: "instructions.md", hash: "sha256:abc", executable: false},
		{path: "tools/hash_text.ts", hash: "sha256:def", executable: true},
	})
	if !strings.HasPrefix(preimage, sourceDigestDomain) {
		t.Fatalf("the hashed material must open with the domain prefix, got %q", preimage)
	}
	// The executable bit is hashed too: it is authored intent, and a tool
	// that gained or lost it is a different source. The fingerprint covers
	// it (agentproject.computeFingerprint), so a digest that ignored it
	// would name fewer bytes than the fingerprint names.
	if !strings.Contains(preimage, "tools/hash_text.ts\nsha256:def\nx\n") {
		t.Fatalf("each entry must contribute its path, hash, and executable bit: %q", preimage)
	}
	// The order is total: two entries differing only in the executable bit
	// still hash to one stable material.
	unsorted := digestPreimage([]digestEntry{
		{path: "tools/hash_text.ts", hash: "sha256:def", executable: true},
		{path: "instructions.md", hash: "sha256:abc"},
	})
	if unsorted != preimage {
		t.Fatalf("the material must not depend on entry order:\n%q\n%q", unsorted, preimage)
	}
}

// TestSourceDigestWalksOnlyAuthoredFiles proves the fallback walk is an
// allowlist of what the loader reads, not a denylist of what apply writes.
// The determinism the digest exists for depends on it: .git mutates on every
// fetch and checkout, and a vendored dependency tree is neither authored nor
// stable, so a digest that folded either in would change when nothing
// authored had.
func TestSourceDigestWalksOnlyAuthoredFiles(t *testing.T) {
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", brokenInstructions)
	writeFile(t, agent, "skills/echo/SKILL.md", []byte(echoSkillMD), 0o644)
	before := sourceDigest(agent, nil)
	if before == "" {
		t.Fatal("a readable root must have a digest")
	}

	writeFile(t, agent, ".git/HEAD", []byte("ref: refs/heads/main\n"), 0o644)
	writeFile(t, agent, ".git/objects/ab/cdef", []byte("object bytes"), 0o644)
	writeFile(t, agent, "node_modules/left-pad/index.js", []byte("module.exports = 1\n"), 0o644)
	writeFile(t, agent, ".venv/pyvenv.cfg", []byte("home = /usr\n"), 0o644)
	writeFile(t, agent, "dist/bundle.js", []byte("bundled\n"), 0o644)
	writeFile(t, agent, ".DS_Store", []byte("finder droppings"), 0o644)
	if after := sourceDigest(agent, nil); after != before {
		t.Fatalf("only authored files may move the digest: %s vs %s", before, after)
	}

	// A checkout that only moves .git is the case that matters most: the
	// same authored source must digest identically before and after.
	writeFile(t, agent, ".git/HEAD", []byte("ref: refs/heads/other\n"), 0o644)
	if after := sourceDigest(agent, nil); after != before {
		t.Fatalf("a git checkout must not change the digest: %s vs %s", before, after)
	}
}

// TestSourceDigestCoversTheExecutableBit proves the digest names the same
// authored intent the fingerprint does: a tool that gained the executable
// bit is a different source, even though every byte of it is unchanged.
func TestSourceDigestCoversTheExecutableBit(t *testing.T) {
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", brokenInstructions)
	tool := filepath.Join(agent, "tools", "hash_text.ts")
	writeFile(t, agent, "tools/hash_text.ts", []byte("export function main() {}\n"), 0o644)
	before := sourceDigest(agent, nil)

	if err := os.Chmod(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if after := sourceDigest(agent, nil); after == before {
		t.Fatalf("gaining the executable bit must change the digest, both were %s", before)
	}
}

// TestSourceDigestCoversTheToolDependencyFiles proves the walk takes the
// native dependency files the fingerprint inventories at the agent root
// (agentproject.toolDependencySpecs), which are authored inputs even though
// they sit outside the component directories.
func TestSourceDigestCoversTheToolDependencyFiles(t *testing.T) {
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", brokenInstructions)
	writeFile(t, agent, "tools/hash_text.ts", []byte("export function main() {}\n"), 0o644)
	before := sourceDigest(agent, nil)

	for _, name := range []string{"deno.json", "deno.lock", "pyproject.toml", "uv.lock", "go.mod", "go.sum"} {
		writeFile(t, agent, name, []byte("# "+name+"\n"), 0o644)
		after := sourceDigest(agent, nil)
		if after == before {
			t.Fatalf("%s is a fingerprint input and must move the digest", name)
		}
		before = after
	}
}

// TestFixingASourceReplacesTheDigestWithAFingerprint walks the loop's own
// path: a rejected candidate is named by a digest, and once it gates it is
// named by a fingerprint and by nothing else. The two names never appear
// together, which is what stops a consumer from joining on whichever one is
// present.
func TestFixingASourceReplacesTheDigestWithAFingerprint(t *testing.T) {
	// The digest cases run the portable gate; the suite must not inherit
	// the operator's TENON_HARNESS, which would select the harness gate and
	// fail resolution on a machine with no harness binary.
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", brokenInstructions)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", agent, "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("a failing gate must exit 1, got %d: %s", code, stdout.String())
	}
	failed := finalOutcome(t, stdout.String())
	if failed.SourceDigest == "" {
		t.Fatalf("the rejected candidate must be attributable: %s", stdout.String())
	}

	if err := os.WriteFile(filepath.Join(agent, "instructions.md"), []byte(validInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"check", agent, "--format", "jsonl"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("check exit %d: %s", code, stderr.String())
	}
	passed := finalOutcome(t, stdout.String())
	if passed.Outcome != "ok" || passed.SourceDigest != "" {
		t.Fatalf("a fixed source must pass with no digest, got %+v", passed)
	}
	fingerprint := finalFingerprint(t, stdout.String())
	if fingerprint == "" {
		t.Fatal("a passing check must report a fingerprint")
	}
	if fingerprint == failed.SourceDigest {
		t.Fatal("the fingerprint of the fixed tree must not be the digest of the broken one")
	}
}

// TestSourceDigestIgnoresGeneratedOutput proves the fallback walk digests
// the authored source and not an apply's own output. The default workspace
// IS the agent directory, so a source that has been applied in place sits
// beside generated files; a digest that counted them would change when
// nothing authored changed.
func TestSourceDigestIgnoresGeneratedOutput(t *testing.T) {
	// The digest cases run the portable gate; the suite must not inherit
	// the operator's TENON_HARNESS, which would select the harness gate and
	// fail resolution on a machine with no harness binary.
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", validInstructions)
	before := sourceDigest(agent, nil)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", agent, "--harness", "claude"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("apply exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(agent, "CLAUDE.md")); err != nil {
		t.Fatalf("apply must have generated into the agent directory: %v", err)
	}
	if after := sourceDigest(agent, nil); after != before {
		t.Fatalf("generated output must not change the source digest: %s vs %s", before, after)
	}
}

// TestSourceDigestIsOmittedWhenTheRootCannotBeRead proves the one case the
// field is absent rather than wrong: a root that is not a readable directory
// has no bytes to name, and the gate_failed object simply carries no digest.
func TestSourceDigestIsOmittedWhenTheRootCannotBeRead(t *testing.T) {
	// The digest cases run the portable gate; the suite must not inherit
	// the operator's TENON_HARNESS, which would select the harness gate and
	// fail resolution on a machine with no harness binary.
	t.Setenv("TENON_HARNESS", "")
	missing := filepath.Join(t.TempDir(), "no-such-agent")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", missing, "--format", "jsonl"}, nil, &stdout, &stderr); code != 1 {
		t.Fatalf("a missing root must fail the gate, got %d: %s", code, stdout.String())
	}
	final := finalOutcome(t, stdout.String())
	if final.Outcome != "gate_failed" {
		t.Fatalf("outcome = %q, want gate_failed: %s", final.Outcome, stdout.String())
	}
	if final.SourceDigest != "" {
		t.Fatalf("an unreadable root must carry no digest, got %q", final.SourceDigest)
	}
}

// TestSourceDigestCoversTheLegacyConnectionsDir proves the one directory the
// loader reads only to reject is still named by the digest: a legacy
// connections/ tree is exactly the bytes behind mcp.migration.connections-dir,
// so two sources that differ only there must not digest as one.
func TestSourceDigestCoversTheLegacyConnectionsDir(t *testing.T) {
	t.Setenv("TENON_HARNESS", "")
	agent := writeAgent(t, "my-agent", validInstructions)
	writeFile(t, agent, "connections/github.md", []byte("---\nurl: https://a.example/mcp\n---\n"), 0o644)
	before := sourceDigest(agent, nil)
	if before == "" {
		t.Fatal("a readable root must have a digest")
	}
	writeFile(t, agent, "connections/github.md", []byte("---\nurl: https://b.example/mcp\n---\n"), 0o644)
	if after := sourceDigest(agent, nil); after == before {
		t.Fatalf("the rejected connections/ bytes must move the digest: %s", before)
	}
}
