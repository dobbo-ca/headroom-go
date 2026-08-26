package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends" // registers the store backends
	"github.com/dobbo-ca/headroom-go/internal/config"
	"github.com/dobbo-ca/headroom-go/internal/ledger"
	"github.com/dobbo-ca/headroom-go/internal/paths"
	"github.com/dobbo-ca/headroom-go/internal/proxy"
	"github.com/dobbo-ca/headroom-go/internal/router"
	"github.com/dobbo-ca/headroom-go/internal/tokenizer"
)

// newProxyServer is the ONE place a proxy.Server is built.
//
// `headroom proxy` and `headroom wrap` both run one, and having two
// construction sites meant they could disagree about what a proxy is. They
// did: wrap's never opened a ledger, so the one command the quickstart tells
// people to run produced nothing for `headroom perf` to report on.
func newProxyServer(pcfg proxy.Config, cfg config.Config) (*proxy.Server, error) {
	if cfg.CCRPath != "" {
		if err := paths.EnsureDir(filepath.Dir(cfg.CCRPath)); err != nil {
			return nil, fmt.Errorf("create CCR directory: %w", err)
		}
	}
	store, err := ccr.FromConfig(cfg.BackendConfig())
	if err != nil {
		return nil, fmt.Errorf("open CCR store: %w", err)
	}

	return proxy.New(proxy.Deps{
		Config:    pcfg,
		Store:     store,
		Router:    router.NewDefault(),
		Tokenizer: tokenizer.GetTokenizer(cfg.Model),
		Version:   version,
		CCRPath:   cfg.CCRPath,
		Ledger:    openLedger(),
	}), nil
}

// openLedger returns the ledger writer, or nil with a warning.
//
// A ledger that will not open costs observability, never a session: `headroom
// perf` reports nothing rather than the proxy refusing to run.
func openLedger() *ledger.Writer {
	path, err := ledger.DefaultPath()
	if err == nil {
		var w *ledger.Writer
		if w, err = ledger.Open(path); err == nil {
			return w
		}
	}
	fmt.Fprintln(os.Stderr, "headroom: no ledger, `headroom perf` will see nothing:", err)
	return nil
}
