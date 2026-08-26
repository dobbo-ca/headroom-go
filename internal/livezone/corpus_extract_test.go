//go:build corpus

// Corpus extractor for hr-wu5: wire-faithful tool_result blocks.
//
// Runbook:
//
//	go test -tags corpus ./internal/livezone/ -run 'TestCorpusExtract' -timeout 60m
//	go test -tags corpus ./internal/livezone/ -run 'TestCorpusClassify' -timeout 60m
//
// Produces:
//
//	/tmp/tio/blocks.jsonl - one block per line with wire field (raw JSON bytes)
//	/tmp/tio/meta.json    - denominators and counts
package livezone

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// extractedBlock is the wire-faithful corpus format. One field: wire.
type extractedBlock struct {
	Sha         string `json:"sha"`
	Origin      string `json:"origin"`
	Tool        string `json:"tool"`
	Cmd         string `json:"cmd"`
	Shape       string `json:"shape"`
	WireBytes   int    `json:"wire_bytes"`
	Occurrences int    `json:"occurrences"`
	Reshaped    string `json:"reshaped"`
	Wire        string `json:"wire"`
}

// extractMeta holds the corpus denominators.
type extractMeta struct {
	Files             int `json:"files"`
	BlocksSeen        int `json:"blocks_seen"`
	WireBytesSeen     int `json:"wire_bytes_seen"`
	UniqueBlocks      int `json:"unique_blocks"`
	UniqueWireBytes   int `json:"unique_wire_bytes"`
	SkippedUnparsable int `json:"skipped_unparsable"`
	MarkedReshaped    int `json:"marked_reshaped"`
}

func TestCorpusExtract(t *testing.T) {
	corpusRoot := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	if _, err := os.Stat(corpusRoot); os.IsNotExist(err) {
		t.Fatalf("corpus root does not exist: %s", corpusRoot)
	}

	outDir := "/tmp/tio"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pass 1: count occurrences
	hashCounts := make(map[string]int)
	var filesP1, blocksSeenP1, wireBytesSeen, skippedUnparsable int

	t.Logf("Pass 1: counting occurrences...")
	err := filepath.WalkDir(corpusRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}

		filesP1++
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
		for sc.Scan() {
			var msg map[string]interface{}
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				skippedUnparsable++
				continue
			}

			body := sc.Bytes()
			results := gjson.GetManyBytes(body, "role", "content")
			if results[0].String() != "user" {
				continue
			}
			if !results[1].IsArray() {
				continue
			}

			for _, block := range results[1].Array() {
				if block.Get("type").String() != "tool_result" {
					continue
				}

				// Extract wire-faithful content
				contentResult := gjson.Get(string(body), fmt.Sprintf("content.%d.content", block.Index-results[1].Index-1))
				wire := contentResult.Raw

				// Check UTF-8 validity
				if !utf8.Valid([]byte(wire)) {
					continue // Will be marked in pass 2
				}

				wireBytes := len(wire)
				if !keepBlock(wireBytes) {
					continue
				}

				blocksSeenP1++
				wireBytesSeen += wireBytes
				hash := wireHashBytes([]byte(wire))
				hashCounts[hash]++
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("pass 1 walk: %v", err)
	}

	if filesP1 == 0 {
		t.Fatal("pass 1: found 0 files - check corpus root")
	}

	t.Logf("Pass 1: %d files, %d blocks (>= 512B), %d unique", filesP1, blocksSeenP1, len(hashCounts))

	// Pass 2: write unique blocks with occurrence counts
	outPath := filepath.Join(outDir, "blocks.jsonl")
	of, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer of.Close()

	w := bufio.NewWriter(of)
	defer w.Flush()

	written := make(map[string]bool)
	var filesP2, blocksSeenP2, uniqueBlocks, uniqueWireBytes, markedReshaped int

	t.Logf("Pass 2: writing unique blocks...")
	err = filepath.WalkDir(corpusRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}

		filesP2++
		if filesP2%100 == 0 {
			t.Logf("Pass 2: %d files, %d unique written", filesP2, uniqueBlocks)
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
		for sc.Scan() {
			var msg map[string]interface{}
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				continue
			}

			body := sc.Bytes()
			results := gjson.GetManyBytes(body, "role", "content")
			if results[0].String() != "user" {
				continue
			}
			if !results[1].IsArray() {
				continue
			}

			for i, block := range results[1].Array() {
				if block.Get("type").String() != "tool_result" {
					continue
				}

				// Extract fields
				contentPath := fmt.Sprintf("content.%d.content", i)
				contentResult := gjson.GetBytes(body, contentPath)
				wire := contentResult.Raw

				// Check for reshaping conditions
				var reshaped string
				if !utf8.Valid([]byte(wire)) {
					reshaped = "invalid_utf8"
					markedReshaped++
				} else if wire == "" {
					reshaped = "no_content_key"
					markedReshaped++
				}

				wireBytes := len(wire)
				if !keepBlock(wireBytes) {
					continue
				}

				blocksSeenP2++
				hash := wireHashBytes([]byte(wire))
				if written[hash] {
					continue
				}
				written[hash] = true

				// Determine shape
				shape := "string"
				if len(wire) > 0 && wire[0] == '[' {
					shape = "array"
				}

				// Extract tool and cmd
				tool := block.Get("tool_use_id").String()
				// Find the tool_use in assistant messages
				toolName, cmd := findToolInfo(body, tool)

				uniqueBlocks++
				uniqueWireBytes += wireBytes

				eb := extractedBlock{
					Sha:         hash,
					Origin:      filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path),
					Tool:        toolName,
					Cmd:         cmd,
					Shape:       shape,
					WireBytes:   wireBytes,
					Occurrences: hashCounts[hash],
					Reshaped:    reshaped,
					Wire:        wire,
				}

				enc := json.NewEncoder(w)
				if err := enc.Encode(eb); err != nil {
					return err
				}
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("pass 2 walk: %v", err)
	}

	// Write meta
	meta := extractMeta{
		Files:             filesP2,
		BlocksSeen:        blocksSeenP2,
		WireBytesSeen:     wireBytesSeen,
		UniqueBlocks:      uniqueBlocks,
		UniqueWireBytes:   uniqueWireBytes,
		SkippedUnparsable: skippedUnparsable,
		MarkedReshaped:    markedReshaped,
	}

	metaPath := filepath.Join(outDir, "meta.json")
	mf, err := os.Create(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mf.Close()

	enc := json.NewEncoder(mf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		t.Fatal(err)
	}

	t.Logf("Extracted: %d unique blocks (%.1f MB wire bytes) from %d files",
		uniqueBlocks, float64(uniqueWireBytes)/1e6, filesP2)
	t.Logf("As-sent: %d blocks (%.1f MB wire bytes)",
		blocksSeenP2, float64(wireBytesSeen)/1e6)
	t.Logf("Marked reshaped: %d", markedReshaped)
}

func wireHashBytes(wire []byte) string {
	h := sha256.Sum256(wire)
	return hex.EncodeToString(h[:8])
}

func findToolInfo(body []byte, toolUseID string) (string, string) {
	// Walk backwards through messages looking for the tool_use
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return "", ""
	}

	for _, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			if block.Get("type").String() == "tool_use" && block.Get("id").String() == toolUseID {
				name := block.Get("name").String()
				cmd := block.Get("input.command").String()
				return name, cmd
			}
		}
	}
	return "", ""
}
