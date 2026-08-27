package main

import (
	"flag"
	"fmt"
	"io"
	"runtime"
	"sort"

	"github.com/alee792/tenon/internal/integration"
	"github.com/alee792/tenon/internal/version"
)

const integrationUsage = `usage:
  tenon integration install SOURCE --trust operator
  tenon integration update ID SOURCE --trust operator
  tenon integration inspect|verify|list|enable|disable|remove [ID]
`

// resolveIntegrationStoreBase resolves the operator's integration-package
// store base directory (ADR 0014's per-OS-user default). It is the one
// resolution every command that opens the store shares — apply, validate,
// and connection status alike — so an installed connection resolves
// identically no matter which command asks. A resolution failure (e.g. an
// unreadable user config directory) yields an empty base, which callers that
// generate native configuration treat as "no store configured" rather than
// an environment failure.
func resolveIntegrationStoreBase() string {
	base, err := integration.DefaultBase()
	if err != nil {
		return ""
	}
	return base
}

// runIntegration is the operator CLI for the integration-package store. Trust
// is explicit: install and update refuse to proceed without --trust operator.
// Diagnostics are bounded and credential-free, and the store is offline: no
// command here fetches a URL or executes a package. The host tenon version is
// the single version constant from internal/mcp.
func runIntegration(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, integrationUsage)
		return 2
	}
	base, err := integration.DefaultBase()
	if err != nil {
		fmt.Fprintln(stderr, "tenon integration:", err)
		return 1
	}
	store := integration.NewStore(base)

	switch args[0] {
	case "install":
		return runIntegrationInstall(store, "install", args[1:], stdout, stderr)
	case "update":
		return runIntegrationInstall(store, "update", args[1:], stdout, stderr)
	case "inspect":
		return runIntegrationInspect(store, args[1:], stdout, stderr)
	case "verify":
		return runIntegrationVerify(store, args[1:], stdout, stderr)
	case "list":
		return runIntegrationList(store, stdout, stderr)
	case "enable":
		return runIntegrationSet(store, "enable", args[1:], stdout, stderr)
	case "disable":
		return runIntegrationSet(store, "disable", args[1:], stdout, stderr)
	case "remove":
		return runIntegrationSet(store, "remove", args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tenon integration: unknown subcommand %q\n%s", args[0], integrationUsage)
		return 2
	}
}

// parseTrust parses the shared --trust flag and the positional arguments after
// it. Trust is a required explicit operator decision.
func parseTrust(name string, args []string, stderr io.Writer) (positional []string, ok bool) {
	fs := flag.NewFlagSet("integration "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	trust := fs.String("trust", "", "explicit trust decision: operator")

	rest := args
	for len(rest) > 0 {
		if rest[0] != "" && rest[0][0] != '-' {
			positional = append(positional, rest[0])
			rest = rest[1:]
			continue
		}
		if err := fs.Parse(rest); err != nil {
			return nil, false
		}
		next := fs.Args()
		if len(next) == len(rest) {
			fmt.Fprintf(stderr, "tenon integration %s: unexpected argument %q\n", name, rest[0])
			return nil, false
		}
		rest = next
	}
	if *trust != "operator" {
		fmt.Fprintf(stderr, "tenon integration %s: installing a package is an explicit trust decision; pass --trust operator\n", name)
		return nil, false
	}
	return positional, true
}

func runIntegrationInstall(store *integration.Store, name string, args []string, stdout, stderr io.Writer) int {
	positional, ok := parseTrust(name, args, stderr)
	if !ok {
		return 2
	}
	req := integration.InstallRequest{
		TrustOperator: true,
		TenonVersion:  version.Version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
	var installed *integration.Installed
	var err error
	switch name {
	case "install":
		if len(positional) != 1 {
			fmt.Fprintf(stderr, "tenon integration install: exactly one SOURCE is required\n%s", integrationUsage)
			return 2
		}
		req.Source = positional[0]
		installed, err = store.Install(req)
	default: // update
		if len(positional) != 2 {
			fmt.Fprintf(stderr, "tenon integration update: exactly one ID and one SOURCE are required\n%s", integrationUsage)
			return 2
		}
		req.Source = positional[1]
		installed, err = store.Update(req)
		if err == nil && installed.Manifest.ID != positional[0] {
			fmt.Fprintf(stderr, "tenon integration update: the source manifest id %q does not match the requested id %q\n",
				installed.Manifest.ID, positional[0])
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "tenon integration "+name+":", err)
		return 1
	}
	fmt.Fprintf(stdout, "%sed: package %s version %s (manifest %s), trust operator\n",
		name, installed.Manifest.ID, installed.Manifest.Version, installed.Manifest.SHA256())
	for _, c := range installed.State.Capabilities {
		fmt.Fprintf(stdout, "  capability %s: %s v%d\n", c.ID, c.Type, c.Version)
	}
	return 0
}

func runIntegrationInspect(store *integration.Store, args []string, stdout, stderr io.Writer) int {
	id, ok := singleID("inspect", args, stderr)
	if !ok {
		return 2
	}
	installed, err := store.Inspect(id)
	if err != nil {
		fmt.Fprintln(stderr, "tenon integration inspect:", err)
		return 1
	}
	m := installed.Manifest
	fmt.Fprintf(stdout, "package %s version %s\n", m.ID, m.Version)
	fmt.Fprintf(stdout, "  name: %s\n", m.Name)
	fmt.Fprintf(stdout, "  license: %s\n", m.License)
	fmt.Fprintf(stdout, "  source: %s\n", m.Source)
	fmt.Fprintf(stdout, "  revision: %s\n", m.Revision)
	fmt.Fprintf(stdout, "  manifest sha256: %s\n", m.SHA256())
	fmt.Fprintf(stdout, "  compat: [%s, %s)\n", m.Compat.Minimum, m.Compat.Before)
	fmt.Fprintf(stdout, "  trust: %s\n", installed.State.Trust)
	fmt.Fprintf(stdout, "  enabled: %t\n", installed.State.Enabled)
	for _, as := range installed.State.Artifacts {
		fmt.Fprintf(stdout, "  artifact %s (%s/%s, %s): verified\n", as.ID, as.OS, as.Arch, as.Format)
	}
	for _, c := range m.Capabilities {
		if c.NativeMCP == nil {
			continue
		}
		fmt.Fprintf(stdout, "  capability %s: native-mcp v%d, server %s\n", c.ID, c.Version, c.NativeMCP.ServerName)
		names := make([]string, 0, len(c.NativeMCP.RequiredEnv))
		for _, re := range c.NativeMCP.RequiredEnv {
			names = append(names, re.Name)
		}
		sort.Strings(names)
		for _, n := range names {
			// Required ambient names are diagnostic metadata, never a value.
			fmt.Fprintf(stdout, "    requires ambient env: %s\n", n)
		}
	}
	return 0
}

func runIntegrationVerify(store *integration.Store, args []string, stdout, stderr io.Writer) int {
	id, ok := singleID("verify", args, stderr)
	if !ok {
		return 2
	}
	if err := store.Verify(id); err != nil {
		fmt.Fprintln(stderr, "tenon integration verify:", err)
		return 1
	}
	fmt.Fprintf(stdout, "verified: package %s is intact\n", id)
	return 0
}

func runIntegrationList(store *integration.Store, stdout, stderr io.Writer) int {
	summaries, err := store.List()
	if err != nil {
		fmt.Fprintln(stderr, "tenon integration list:", err)
		return 1
	}
	if len(summaries) == 0 {
		fmt.Fprintln(stdout, "no packages installed")
		return 0
	}
	for _, s := range summaries {
		state := "disabled"
		if s.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", s.ID, s.Version, state)
	}
	return 0
}

func runIntegrationSet(store *integration.Store, name string, args []string, stdout, stderr io.Writer) int {
	id, ok := singleID(name, args, stderr)
	if !ok {
		return 2
	}
	var err error
	switch name {
	case "enable":
		err = store.Enable(id)
	case "disable":
		err = store.Disable(id)
	case "remove":
		err = store.Remove(id)
	}
	if err != nil {
		fmt.Fprintln(stderr, "tenon integration "+name+":", err)
		return 1
	}
	fmt.Fprintf(stdout, "%sd: package %s\n", name, id)
	return 0
}

func singleID(name string, args []string, stderr io.Writer) (string, bool) {
	if len(args) != 1 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintf(stderr, "tenon integration %s: exactly one ID is required\n%s", name, integrationUsage)
		return "", false
	}
	return args[0], true
}
