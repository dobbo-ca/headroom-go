// Package perf answers one question: over a real day of use, did headroom
// help or hurt?
//
// Bytes saved is only half the answer. Anthropic bills a cached prefix read at
// 0.1x, a 5-minute cache write at 1.25x and a 1-hour cache write at 2.0x, so a
// proxy that removes tokens while busting the prefix loses money. This package
// therefore joins headroom's own ledger to the usage records Claude Code
// already writes, and reports both sides.
package perf

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dobbo-ca/headroom-go/internal/cachestab"
)

// Usage is one assistant turn's billing record, as Claude Code stores it.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	// CacheCreation splits the write by TTL. The two are billed
	// differently, so a report that merges them understates a 1-hour
	// session's cost by 60%.
	CacheCreation struct {
		Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// Session is one transcript's turns, keyed by the digest that also appears in
// the headroom ledger.
type Session struct {
	ID     string
	Digest string
	Turns  []Usage
}

// Total sums the usage across a session's turns.
func (s Session) Total() Usage {
	var t Usage
	for _, u := range s.Turns {
		t.InputTokens += u.InputTokens
		t.CacheCreationTokens += u.CacheCreationTokens
		t.CacheReadTokens += u.CacheReadTokens
		t.OutputTokens += u.OutputTokens
		t.CacheCreation.Ephemeral5m += u.CacheCreation.Ephemeral5m
		t.CacheCreation.Ephemeral1h += u.CacheCreation.Ephemeral1h
	}
	return t
}

// transcriptLine is the shape of one JSONL record this package reads. Every
// other field is ignored: the report needs the billing record and nothing
// else, and reading less means no conversation content is ever loaded.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Usage *Usage `json:"usage"`
	} `json:"message"`
}

// LoadSessions walks root for *.jsonl transcripts and returns one Session per
// file that carries at least one usage record.
//
// Subagent transcripts live at <project>/<session>/subagents/agent-*.jsonl and
// are counted too: they are 79% of real agent traffic, and a report that
// missed them would describe a fifth of the day.
func LoadSessions(root string) ([]Session, error) {
	var out []Session
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable project directory must not stop the report.
			return nil //nolint:nilerr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		s, ok := loadSession(path)
		if ok {
			out = append(out, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func loadSession(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()

	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	s := Session{ID: id, Digest: cachestab.ClaudeSessionDigest(id)}

	dec := json.NewDecoder(f)
	for {
		var line transcriptLine
		if err := dec.Decode(&line); err != nil {
			break
		}
		if line.Message.Usage != nil {
			s.Turns = append(s.Turns, *line.Message.Usage)
		}
	}
	return s, len(s.Turns) > 0
}

// DefaultTranscriptRoot is where Claude Code keeps its session records.
func DefaultTranscriptRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}
