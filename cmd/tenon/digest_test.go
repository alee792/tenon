package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	// The same tree's digest, computed directly, differs from it.
	digest := sourceDigest(agent, nil)
	if digest == "" {
		t.Fatal("a readable root must have a digest")
	}
	if digest == fingerprint {
		t.Fatalf("a digest must never equal a fingerprint of the same tree: %s", digest)
	}
}

// TestFixingASourceReplacesTheDigestWithAFingerprint walks the loop's own
// path: a rejected candidate is named by a digest, and once it gates it is
// named by a fingerprint and by nothing else. The two names never appear
// together, which is what stops a consumer from joining on whichever one is
// present.
func TestFixingASourceReplacesTheDigestWithAFingerprint(t *testing.T) {
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
