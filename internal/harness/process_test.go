package harness

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestProcessRoundTrips proves a line written to stdin is read back from stdout,
// exercising the plumbing with cat rather than any harness protocol.
func TestProcessRoundTrips(t *testing.T) {
	p, err := StartProcess(context.Background(), "cat", "")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.WriteLine([]byte(`{"hello":"world"}`)); err != nil {
		t.Fatal(err)
	}
	line, err := p.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != `{"hello":"world"}` {
		t.Fatalf("read %q", line)
	}
}

// TestProcessBoundsLine proves an over-limit stdout line is a read error, not an
// unbounded allocation.
func TestProcessBoundsLine(t *testing.T) {
	p, err := StartProcess(context.Background(), "cat", "")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// A single line larger than the bound, with no newline, then EOF.
	huge := bytes.Repeat([]byte("x"), maxProcessLine+16)
	go func() {
		_, _ = p.stdin.Write(huge)
		_ = p.stdin.Close()
	}()
	if _, err := p.ReadLine(); err == nil {
		t.Fatal("want a bound error for an over-limit line")
	}
}

// TestProcessSwallowsStderr proves stderr is never surfaced as stdout: a process
// that writes to both yields only its stdout frame, then EOF.
func TestProcessSwallowsStderr(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "noisy.sh")
	body := "#!/bin/sh\necho out\necho SECRET-STDERR-LOG 1>&2\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := StartProcess(context.Background(), "/bin/sh", "", script)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	line, err := p.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != "out" {
		t.Fatalf("first stdout line %q", line)
	}
	if _, err := p.ReadLine(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF after the single stdout line, got %v", err)
	}
}

// TestVerifyExecutable proves Verify reports a clean exit and a failure.
func TestVerifyExecutable(t *testing.T) {
	if err := VerifyExecutable(context.Background(), "true"); err != nil {
		t.Fatalf("true --version should verify: %v", err)
	}
	if err := VerifyExecutable(context.Background(), "false"); err == nil {
		t.Fatal("false should fail verification")
	}
	if err := VerifyExecutable(context.Background(), "tenon-no-such-executable"); err == nil {
		t.Fatal("a missing executable should fail verification")
	}
}
