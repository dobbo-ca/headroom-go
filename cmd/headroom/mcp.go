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

	var backend, ccrPath, proxyURL, model string
	serve := &cobra.Command{
		Use:           "serve",
		Short:         "Serve headroom_compress, headroom_retrieve, and headroom_stats over stdio",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ov := config.Overrides{}
			if cmd.Flags().Changed("ccr-backend") {
				ov.CCRBackend = &backend
			}
			if cmd.Flags().Changed("ccr-path") {
				ov.CCRPath = &ccrPath
			}
			if cmd.Flags().Changed("proxy-url") {
				ov.ProxyURL = &proxyURL
			}
			if cmd.Flags().Changed("model") {
				ov.Model = &model
			}

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
	serve.Flags().StringVar(&backend, "ccr-backend", "", "CCR store backend: sqlite or memory (env HEADROOM_CCR_BACKEND)")
	serve.Flags().StringVar(&ccrPath, "ccr-path", "", "SQLite CCR file path (env HEADROOM_CCR_PATH)")
	serve.Flags().StringVar(&proxyURL, "proxy-url", "", "headroom proxy base URL for retrieve fallback (env HEADROOM_PROXY_URL)")
	serve.Flags().StringVar(&model, "model", "", "model name for token counting (env HEADROOM_MODEL)")

	mcpCmd.AddCommand(serve)
	return mcpCmd
}
