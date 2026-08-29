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
			results := gjson.GetManyBytes(body, "message.role", "message.content")
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

				// Extract wire-faithful content
				contentResult := gjson.Get(string(body), fmt.Sprintf("message.content.%d.content", i))
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
	if blocksSeenP1 == 0 {
		t.Fatal("pass 1: found 0 blocks - extractor is blind to the corpus schema")
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

		// Build tool_use_id map for this file
		toolMap := make(map[string]struct{ name, cmd string })
		sc1 := bufio.NewScanner(f)
		sc1.Buffer(make([]byte, 0, 1<<20), 256<<20)
		for sc1.Scan() {
			body := sc1.Bytes()
			if gjson.GetBytes(body, "message.role").String() == "assistant" {
				content := gjson.GetBytes(body, "message.content")
				if content.IsArray() {
					for _, blk := range content.Array() {
						if blk.Get("type").String() == "tool_use" {
							id := blk.Get("id").String()
							toolMap[id] = struct{ name, cmd string }{
								name: blk.Get("name").String(),
								cmd:  blk.Get("input.command").String(),
							}
						}
					}
				}
			}
		}
		if err := sc1.Err(); err != nil {
			return err
		}

		// Rewind for main pass
		if _, err := f.Seek(0, 0); err != nil {
			return err
		}

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
		for sc.Scan() {
			var msg map[string]interface{}
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				continue
			}

			body := sc.Bytes()
			results := gjson.GetManyBytes(body, "message.role", "message.content")
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
				contentPath := fmt.Sprintf("message.content.%d.content", i)
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

				// Extract tool and cmd from map
				toolUseID := block.Get("tool_use_id").String()
				toolName, cmd := "", ""
				if info, ok := toolMap[toolUseID]; ok {
					toolName, cmd = info.name, info.cmd
				}

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
	// JSONL format: each line is {message: {...}}
	// We need the tool_use block from the assistant message in the same transcript
	// For now, extract from the current message's metadata if available
	// The full solution would require multi-line context, but toolUseID lookup
	// is sufficient for the read-protection gate.

	// Check if this line has assistant content with matching tool_use
	if msgRole := gjson.GetBytes(body, "message.role").String(); msgRole == "assistant" {
		content := gjson.GetBytes(body, "message.content")
		if content.IsArray() {
			for _, block := range content.Array() {
				if block.Get("type").String() == "tool_use" && block.Get("id").String() == toolUseID {
					name := block.Get("name").String()
					cmd := block.Get("input.command").String()
					return name, cmd
				}
			}
		}
	}
	return "", ""
}
