// Package version carries the single tenon version. Every version-bearing
// surface — the agent manifest's pinned tenon version, the managed MCP
// server's advertised identity, the staged artifact — reads it from here.
package version

// Version is the tenon version. Development builds keep the -dev default;
// release builds stamp the exact tag over it at link time:
//
//	go build -ldflags "-X github.com/alee792/tenon/internal/version.Version=0.1.0" ./cmd/tenon
//
// It is a var rather than a const for exactly that reason. The value is
// load-bearing: an agent manifest pins it and fails closed on drift, so a
// binary that misreports its version silently voids that pin.
var Version = "0.1.0-dev"
