package main

import (
	"fmt"
	"path/filepath"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends" // registers the store backends
	"github.com/dobbo-ca/headroom-go/internal/config"
	"github.com/dobbo-ca/headroom-go/internal/mcp"
	"github.com/dobbo-ca/headroom-go/internal/paths"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Model Context Protocol server",
	}

	var ov config.Overrides
	serve := &cobra.Command{
		Use:           "serve",
		Short:         "Serve headroom_compress, headroom_retrieve, and headroom_stats over stdio",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load(ov)
			if err != nil {
				return err
			}
			if cfg.CCRPath != "" {
				if err := paths.EnsureDir(filepath.Dir(cfg.CCRPath)); err != nil {
					return fmt.Errorf("create CCR directory: %w", err)
				}
			}
			store, err := ccr.FromConfig(cfg.BackendConfig())
			if err != nil {
				return fmt.Errorf("open CCR store: %w", err)
			}

			srv := mcp.NewServer(mcp.Deps{
				Router:    router.NewDefault(),
				Store:     store,
				Tokenizer: tokenizer.GetTokenizer(cfg.Model),
				ProxyURL:  cfg.ProxyURL,
				Version:   version,
			})
			return srv.ServeStdio()
		},
	}
	serve.Flags().StringVar(&ov.CCRBackend, "ccr-backend", "", "CCR store backend: sqlite or memory (env HEADROOM_CCR_BACKEND)")
	serve.Flags().StringVar(&ov.CCRPath, "ccr-path", "", "SQLite CCR file path (env HEADROOM_CCR_PATH)")
	serve.Flags().StringVar(&ov.ProxyURL, "proxy-url", "", "headroom proxy base URL for retrieve fallback (env HEADROOM_PROXY_URL)")
	serve.Flags().StringVar(&ov.Model, "model", "", "model name for token counting (env HEADROOM_MODEL)")

	mcpCmd.AddCommand(serve)
	return mcpCmd
}
