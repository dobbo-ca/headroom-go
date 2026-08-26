package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dobbo-ca/headroom-go/internal/ledger"
	"github.com/dobbo-ca/headroom-go/internal/perf"
	"github.com/spf13/cobra"
)

func newPerfCmd() *cobra.Command {
	var (
		ledgerPath  string
		transcripts string
		since       time.Duration
		asJSON      bool
	)

	cmd := &cobra.Command{
		Use:   "perf",
		Short: "Report whether headroom helped or hurt",
		Long: "Joins headroom's own ledger to the usage records Claude Code writes, and\n" +
			"reports both what headroom removed and what the prompt cache did about it.\n" +
			"Bytes saved with a busted cache is a loss, so the second half is the one\n" +
			"that decides the answer.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if ledgerPath == "" {
				if ledgerPath, err = ledger.DefaultPath(); err != nil {
					return err
				}
			}
			if transcripts == "" {
				if transcripts, err = perf.DefaultTranscriptRoot(); err != nil {
					return err
				}
			}

			entries, err := ledger.Read(ledgerPath)
			if err != nil {
				return fmt.Errorf("read the ledger: %w", err)
			}
			if since > 0 {
				entries = perf.Since(entries, time.Now().Add(-since))
			}
			sessions, err := perf.LoadSessions(transcripts)
			if err != nil {
				return fmt.Errorf("read %s: %w", transcripts, err)
			}

			r := perf.Build(entries, sessions)
			r.LedgerPath = ledgerPath
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			fmt.Fprint(cmd.OutOrStdout(), perf.Format(r))
			return nil
		},
	}

	cmd.Flags().StringVar(&ledgerPath, "ledger", "", "ledger file (default ~/.headroom/ledger.jsonl)")
	cmd.Flags().StringVar(&transcripts, "transcripts", "", "Claude Code project directory (default ~/.claude/projects)")
	cmd.Flags().DurationVar(&since, "since", 0, "only count turns newer than this, e.g. 24h")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON")
	return cmd
}
