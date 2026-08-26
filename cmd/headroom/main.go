// Command headroom is the single multi-command CLI for the headroom
// compression layer. v0.1 ships "mcp serve" and --version; proxy, wrap, perf,
// and learn arrive in v0.2.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is stamped at build time with -ldflags "-X main.version=<tag>".
var version = "0.1.0-dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "headroom",
		Short:         "Compress LLM context before it reaches the model",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Diagnostics and errors go to stderr; stdout belongs to whichever
	// command is producing output. "mcp serve" owns stdout for the protocol
	// stream and redirects its own writer (see newMCPCmd); "perf" writes its
	// report there so it can be piped.
	root.SetErr(os.Stderr)
	root.AddCommand(newMCPCmd())
	root.AddCommand(newProxyCmd())
	root.AddCommand(newWrapCmd())
	root.AddCommand(newPerfCmd())
	return root
}

func main() {
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "headroom:", err)
		os.Exit(1)
	}
}
