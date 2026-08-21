// Command tenon compiles filesystem-authored agent projects into native
// configuration for coding-agent harnesses.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tenon:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("a command is required; see docs/product-spec.md")
	}
	return fmt.Errorf("unknown command %q", args[0])
}
