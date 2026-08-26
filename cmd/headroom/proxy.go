package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dobbo-ca/headroom-go/internal/config"
	"github.com/dobbo-ca/headroom-go/internal/proxy"
	"github.com/spf13/cobra"
)

// dryRunConfig records the configuration a --dry-run resolved, so tests can
// assert that each flag reached it.
var dryRunConfig proxy.Config

func newProxyCmd() *cobra.Command {
	var (
		ov     config.Overrides
		pov    proxy.Overrides
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run the compressing proxy in front of an LLM API",
		Long: "Forwards requests to HEADROOM_PROXY_UPSTREAM, compressing request\n" +
			"bodies through the live-zone dispatcher. Responses stream back verbatim\n" +
			"and are never compressed.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pcfg, err := proxy.Load(pov)
			if err != nil {
				return err
			}
			cfg, err := config.Load(ov)
			if err != nil {
				return err
			}
			if dryRun {
				dryRunConfig = pcfg
				return nil
			}

			srv, err := newProxyServer(pcfg, cfg)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			fmt.Fprintf(os.Stderr, "headroom proxy listening on %s -> %s\n", pcfg.Listen, pcfg.Upstream)
			return srv.ListenAndServe(ctx)
		},
	}

	cmd.Flags().StringVar(&pov.Upstream, "upstream", "", "upstream API base URL (env HEADROOM_PROXY_UPSTREAM)")
	cmd.Flags().StringVar(&pov.Listen, "listen", "", "listen address (env HEADROOM_PROXY_LISTEN)")
	cmd.Flags().StringVar(&ov.CCRBackend, "ccr-backend", "", "CCR store backend: sqlite or memory (env HEADROOM_CCR_BACKEND)")
	cmd.Flags().StringVar(&ov.CCRPath, "ccr-path", "", "SQLite CCR file path (env HEADROOM_CCR_PATH)")
	cmd.Flags().StringVar(&ov.Model, "model", "", "model name for token counting (env HEADROOM_MODEL)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "resolve and validate configuration, then exit")

	return cmd
}
