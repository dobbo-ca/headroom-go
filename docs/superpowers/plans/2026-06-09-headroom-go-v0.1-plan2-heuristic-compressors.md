# headroom-go v0.1 — Plan 2: Heuristic Compressors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the zero-dep heuristic compressors (reformats, log/diff/search engines, offloads, tag-protect, tool-pairs, adaptive sizer, importance signals, BM25 relevance) and wire them into the existing pipeline so `router.Compress` actually compresses each `ContentType` — replacing the Plan 1 passthrough spine.

**Architecture:** The Plan 1 foundation (`transform`, `ccr`, `tokenizer`, `detect`, `pipeline`, `router`) compiles and is faithful passthrough. Plan 2 adds: `reformats` (lossless `ReformatTransform`s), `compress` (standalone `LogCompressor`/`DiffCompressor`/`SearchCompressor` engines), `offloads` (`OffloadTransform` wrappers that call the engines + stash originals in CCR), plus the support libraries `signals`, `relevance`, `adaptive`, `tagprotect`, `toolpairs`. A new `router.NewDefault()` constructor registers the reformats + offloads into a `pipeline.Pipeline` so detection→compression works end-to-end. **SmartCrusher (JSON crush) is Plan 3** — `JsonOffload` is wired against a `Crusher` seam interface whose Plan-2 default is passthrough.

**Tech Stack:** Go 1.25, stdlib only for new code (`regexp`, `strings`, `crypto/md5`, `encoding/hex`, `encoding/json`, `hash/fnv`, `sort`, `math`). No new third-party deps. cgo-free. Deterministic (I4): no time/random on the compression output path.

**Spec:** `docs/superpowers/specs/2026-06-08-headroom-go-core-design.md` (§5 interfaces, §6 port plan, §8 v0.1, §10 parity).

**Authoritative behavior reference:** `docs/superpowers/research/2026-06-09-upstream-heuristic-compressors-behavior.txt` — a verbatim extraction of upstream `chopratejas/headroom` (Rust `crates/headroom-core`) behavior for every subsystem below (algorithms, exact regexes, constants, CCR marker formats, edge cases). **When this plan and the research file agree, follow them. When in doubt on a constant or pattern, the research file is the source of truth.** Faithfulness target: preserve upstream *behavior*; **byte-parity with Python/Rust is explicitly dropped** (Decision B).

---

## Shared Contract (read before any task)

These facts are fixed by the merged Plan 1 foundation. Every task depends on them; do not redefine them.

### Foundation interfaces (already exist — `internal/transform/transform.go`)

```go
type ContentType int
const ( JsonArray ContentType = iota; SourceCode; SearchResults; BuildOutput; GitDiff; Html; PlainText )
// String(): "json_array","source_code","search","build","diff","html","text"

type CompressionContext struct { Query string; TokenBudget *int }

var ( ErrInvalidInput = errors.New("invalid input"); ErrSkipped = errors.New("skipped"); ErrInternal = errors.New("internal") )

type ReformatOutput struct { Output string; BytesSaved int }
type OffloadOutput  struct { Output string; BytesSaved int; CacheKey string }

type ReformatTransform interface {
    Name() string
    AppliesTo() []ContentType
    Apply(content string) (ReformatOutput, error)
}
type OffloadTransform interface {
    Name() string
    AppliesTo() []ContentType
    EstimateBloat(content string) float32 // cheap, structural-only, safe on ""; NO full pass
    Apply(content string, ctx CompressionContext, store ccr.Store) (OffloadOutput, error)
    Confidence() float32
}
```

### CCR (already exists — `internal/ccr/`)

```go
type Store interface { Put(hash, payload string); Get(hash string) (string, bool); Len() int }
func ComputeKey(payload []byte) string // BLAKE3 -> 24 lowercase hex (canonical live-zone marker; v0.2)
func MarkerFor(hash string) string     // "<<ccr:HASH>>" (canonical; v0.2, not used by heuristic compressors)
```

### Decisions that govern ALL Plan-2 CCR usage

1. **Heuristic compressors use MD5, not BLAKE3.** Upstream keys originals with `hashlib.md5(content).hexdigest()[:24]`. **Task 1 adds `ccr.ComputeKeyMD5(payload []byte) string`** (md5 → 32 lowercase hex → first 24 chars). All log/diff/search/diff_noise/json_offload CCR keys use this. (The BLAKE3 `ComputeKey` is reserved for the v0.2 live-zone canonical marker.)
2. **Markers are inline, human-readable strings built by each compressor** — NOT `ccr.MarkerFor`. Exact formats are given per task. Do not unify them.
3. **The pipeline orchestrator has NO CCR ratio gate.** Confirmed against upstream: the only post-apply gate is `bytes_saved == 0`. The per-compressor "min compression ratio for CCR" (log 0.5, diff 0.8, search 0.8) lives **inside each engine** and only decides whether that engine appends its own inline marker / sets its cache key. The merged Go `pipeline.Run` is already correct — **do not modify pipeline gating.**
4. **Store side-channel.** An `OffloadTransform.Apply` (or the engine it wraps) must `store.Put(key, original)` for every cache key it returns, so `cache_key` resolves. Log/diff/search engines call `store.Put` internally; diff_noise/json_offload call it in their `Apply`.
5. **Determinism (I4).** No `time.*` or randomness on the compression output path. tag-protect uses a deterministic salt scan (not crypto/rand). search dedup uses FNV-1a (deterministic), only for in-call dedup keys that never appear in output.

### Line-counting semantics (get these exactly right per subsystem)

- **diff_compressor, search_compressor:** Python `str.split('\n')` semantics → use `strings.Split(s, "\n")`; `len` gives the count (`"a\n"`→2, `""`→1, trailing `\n` yields a trailing empty element). Do NOT use `bufio.Scanner`/`strings.Lines`.
- **log_template, log_compressor, the offload bloat estimators:** Rust `str::lines()` semantics → split on `\n`, drop one trailing empty element if the content ends with `\n`, and strip one trailing `\r` per line. Implement a small `splitLines(s string) []string` helper (per task) mirroring this.
- **All `.len()` comparisons** (never-inflate guards, bytes_saved) use **byte length** `len(s)`, never `utf8.RuneCountInString`.

### Where the wiring lives

`internal/router.NewDefault()` (Task 14) constructs the engines, wraps them in offloads, registers reformats + offloads into `pipeline.NewBuilder()`, and returns a `*Router`. `pipeline` must NOT import `offloads`/`compress` (keeps it a pure orchestrator); `router` is the integration seam and may import everything.

### Default registration (faithful to upstream `offloads/mod.rs` + reformat order)

- **Reformats** (registration order = run order): `JsonMinifier` → `[JsonArray]`; `LogTemplate` → `[BuildOutput]`.
- **Offloads** (registration order): `DiffNoise` → `[GitDiff]`; `DiffOffload` → `[GitDiff]`; `JsonOffload` → `[JsonArray]`; `LogOffload` → `[BuildOutput]`.
- **`SearchOffload` is implemented but NOT registered** (matches upstream — search results pass through the default pipeline).

---

## Task 0: Project tracking (beads)

**Files:** none. Run from the **main checkout** `/Users/christopherdobbyn/work/dobbo-ca/headroom-go` (the `.beads/` tracker lives there, gitignored, absent in this worktree). Use a subshell so the session CWD stays in the worktree: `(cd /Users/christopherdobbyn/work/dobbo-ca/headroom-go && bd ...)`.

- [ ] **Step 1: Create Plan-2 child tasks under epic `hr-47g`**

One `bd` issue per task below (Tasks 1–15), parented to `hr-47g`. Prefix `hr`. Example:
```bash
(cd /Users/christopherdobbyn/work/dobbo-ca/headroom-go && \
 bd create task "Plan2: ccr MD5 key + md5 helper" --parent hr-47g)
# ...repeat for each Plan-2 task...
```

- [ ] **Step 2: Verify**

`(cd /Users/christopherdobbyn/work/dobbo-ca/headroom-go && bd list)` — expect the new child tasks under `hr-47g`. Close each with `bd update <id> --status done` (or `bd close <id>`) as its task lands.

---

## Task 1: `ccr.ComputeKeyMD5` (shared CCR key for heuristic markers)

**Files:**
- Create: `internal/ccr/md5key.go`
- Test: `internal/ccr/md5key_test.go`

- [ ] **Step 1: Failing test**

Create `internal/ccr/md5key_test.go`:
```go
package ccr

import "testing"

func TestComputeKeyMD5(t *testing.T) {
	// Verified upstream vectors (hashlib.md5(s).hexdigest()[:24]):
	cases := map[string]string{
		"hello": "5d41402abc4b2a76b9719d91",
		"":      "d41d8cd98f00b204e9800998",
	}
	for in, want := range cases {
		if got := ComputeKeyMD5([]byte(in)); got != want {
			t.Errorf("ComputeKeyMD5(%q) = %q, want %q", in, got, want)
		}
	}
	if len(ComputeKeyMD5([]byte("anything"))) != 24 {
		t.Fatal("key must be 24 hex chars")
	}
}
```

- [ ] **Step 2: Run → fail** — `go test ./internal/ccr/ -run MD5` → `undefined: ComputeKeyMD5`.

- [ ] **Step 3: Implement** — Create `internal/ccr/md5key.go`:
```go
package ccr

import (
	"crypto/md5"
	"encoding/hex"
)

// ComputeKeyMD5 returns the first 24 lowercase hex chars of the MD5 of payload.
// The heuristic compressors (log/diff/search/diff_noise/json_offload) key their
// CCR originals this way to match upstream headroom (hashlib.md5(...).hexdigest()[:24]).
// The canonical live-zone marker uses ComputeKey (BLAKE3); these are intentionally distinct.
func ComputeKeyMD5(payload []byte) string {
	sum := md5.Sum(payload)
	return hex.EncodeToString(sum[:])[:24]
}
```

- [ ] **Step 4: Run → pass** — `go test ./internal/ccr/`.

- [ ] **Step 5: Commit**
```bash
git add internal/ccr/md5key.go internal/ccr/md5key_test.go
git commit -m "feat(ccr): MD5[:24] key helper for heuristic compressor CCR markers"
```

---

## Task 2: `internal/signals` (importance signals: KeywordDetector + Tiered)

**Behavior reference:** research §"signals (keyword_detector + mod)". Pure, no Plan-2 deps. Consumed by `search_compressor` and `log_offload`'s bloat estimator.

**Files:**
- Create: `internal/signals/signals.go` (types + `LineImportanceDetector` + `ImportanceSignal`), `internal/signals/keyword_detector.go`, `internal/signals/tiered.go`
- Test: `internal/signals/keyword_detector_test.go`, `internal/signals/tiered_test.go`

**Exact data (verbatim from research):**
- `ImportanceCategory`: `Error, Warning, Importance, Security, Markdown`.
- `ImportanceContext`: `Text, Search, Diff, Log` (default `Text`).
- `ImportanceSignal{ Category *ImportanceCategory (nil=no match); Priority float32; Confidence float32 }`. `Neutral()` = `{nil,0,0}`. `Matched(cat,prio,conf)`. `IsMatch()` = `Category != nil`.
- Keyword lists (case-insensitive, **word-boundary** match where boundary byte ∈ `[A-Za-z0-9_]`):
  - **Error** (universal, all contexts): `error, exception, fail, failed, failure, fatal, critical, crash, panic, abort, timeout, denied, rejected`
  - **Importance** (universal): `important, note, todo, fixme, hack, xxx, bug, fix`
  - **Warning** (only `Search`,`Log`,`Text`; NOT `Diff`): `warn, warning`
  - **Security** (only `Diff`): `security, auth, password, secret`  ← `token` deliberately excluded
  - **Markdown** (only `Text`, line-**prefix** match, no boundary): `"# ", "## ", "### ", "#### ", "**", "> "`
- Priorities: Error `0.95`, Security `0.85`, Warning `0.75`, Importance `0.6`, Markdown `0.45`. Confidence on any match `0.7` (`KEYWORD_CONFIDENCE`). No match → `Neutral()`.
- `score(line, ctx)`: collect all categories that matched **and are allowed in ctx**; return the one with the **highest priority** (max-by-priority, not sum), confidence `0.7`. If none, `Neutral()`.
- `contains_error_indicator(line)`: **substring-only** (no boundary), separate smaller list `error, fail, exception, traceback, fatal, panic, crash`.
- `Tiered`: ordered detectors; `score` = first tier whose `signal.Confidence >= 0.7` (`ESCALATE_THRESHOLD`) wins immediately; else the highest-confidence signal seen; else `Neutral()`. Escalation compares **confidence**, not priority. Builder: `NewTiered()`, `.With(d LineImportanceDetector)`.

**Zero-dep matching:** case-insensitive ASCII byte scan + word-boundary post-filter + longest-match-at-position (so `warning` beats `warn`; category is the same either way). No aho-corasick dependency.

- [ ] **Step 1: Failing test** — `internal/signals/keyword_detector_test.go`:
```go
package signals

import "testing"

func TestKeywordDetectorCategories(t *testing.T) {
	d := NewKeywordDetector()
	cases := []struct {
		line string
		ctx  ImportanceContext
		cat  ImportanceCategory
		prio float32
	}{
		{"FATAL: disk full", Text, Error, 0.95},
		{"a warning about config", Log, Warning, 0.75},
		{"warning suppressed in diff", Diff, Importance, 0.6}, // "warning" gated out in Diff; no other... see note
		{"added password = secret", Diff, Security, 0.85},
		{"TODO: refactor", Search, Importance, 0.6},
		{"# Heading", Text, Markdown, 0.45},
		{"nothing here", Text, 0, 0}, // no match
	}
	for _, c := range cases {
		got := d.Score(c.line, c.ctx)
		if c.prio == 0 {
			if got.IsMatch() {
				t.Errorf("%q: expected no match, got %+v", c.line, got)
			}
			continue
		}
		if !got.IsMatch() || *got.Category != c.cat || got.Priority != c.prio || got.Confidence != 0.7 {
			t.Errorf("%q ctx=%v: got %+v, want cat=%v prio=%v", c.line, c.ctx, got, c.cat, c.prio)
		}
	}
}

func TestWordBoundary(t *testing.T) {
	d := NewKeywordDetector()
	if d.Score("failover complete", Text).IsMatch() {
		t.Error("'failover' must not match 'fail' (word boundary)")
	}
}

func TestContainsErrorIndicator(t *testing.T) {
	d := NewKeywordDetector()
	if !d.ContainsErrorIndicator("Traceback (most recent call last)") {
		t.Error("substring 'traceback' should match")
	}
	if d.ContainsErrorIndicator("abort retry") { // 'abort' is NOT in the indicator list
		t.Error("'abort' is not an error indicator")
	}
}
```
> Note on the Diff "warning" case: in `Diff` context, Warning is gated out and Security/Error/Importance are checked. The example line has no Security/Error/Importance keyword either, so it should be **no match** — adjust the test line to one that genuinely yields the asserted category, or assert no-match. The implementer must make the asserted behavior true; pick lines that unambiguously exercise one category. (When writing the real test, prefer unambiguous lines like `"auth token rotated"` → Security in Diff.)

- [ ] **Step 2: Run → fail.**

- [ ] **Step 3: Implement** `signals.go`, `keyword_detector.go`, `tiered.go` per the exact data above. Key implementation points:
  - `wordByte(b) = (b|0x20) in 'a'..'z' || b in '0'..'9' || b=='_'`. A keyword hit at `[i,j)` is valid iff `(i==0 || !wordByte(line[i-1])) && (j==len || !wordByte(line[j]))`.
  - Case-insensitive compare on ASCII (lowercase both sides).
  - Markdown: `strings.HasPrefix(line, p)` for each prefix, only when `ctx==Text`.
  - `Score`: evaluate each allowed category; keep the max-priority match.

- [ ] **Step 4: Tiered test** — `internal/signals/tiered_test.go`:
```go
package signals

import "testing"

type fixed struct{ s ImportanceSignal }
func (f fixed) Score(string, ImportanceContext) ImportanceSignal { return f.s }

func TestTieredShortCircuitsOnConfidence(t *testing.T) {
	hi := fixed{Matched(Error, 0.95, 0.7)}
	lo := fixed{ImportanceSignal{Confidence: 0.4}}
	got := NewTiered().With(lo).With(hi).Score("x", Text)
	if !got.IsMatch() || got.Confidence != 0.7 {
		t.Fatalf("expected hi tier (conf 0.7) to win, got %+v", got)
	}
}

func TestTieredFallsToBest(t *testing.T) {
	a := fixed{ImportanceSignal{Confidence: 0.3}}
	b := fixed{ImportanceSignal{Confidence: 0.5}}
	got := NewTiered().With(a).With(b).Score("x", Text)
	if got.Confidence != 0.5 {
		t.Fatalf("expected best-confidence 0.5, got %v", got.Confidence)
	}
}
```

- [ ] **Step 5: Run → pass.**

- [ ] **Step 6: Commit**
```bash
git add internal/signals/
git commit -m "feat(signals): KeywordDetector (context-gated, word-boundary) + Tiered combinator"
```

---

## Task 3: `internal/relevance` (BM25 + Hybrid fallback)

**Behavior reference:** research §"relevance (bm25 + hybrid + base + mod)". Pure, no Plan-2 deps. Standalone scorer (wired by SmartCrusher planning in Plan 3; built+tested now per spec §8.9).

**Files:**
- Create: `internal/relevance/base.go` (`Score`, `Scorer`, `DefaultBatchScore`, `CreateScorer`), `internal/relevance/bm25.go`, `internal/relevance/hybrid.go`
- Test: `internal/relevance/bm25_test.go`, `internal/relevance/hybrid_test.go`

**Exact data:**
- `Score{ Score float64; Reason string; MatchedTerms []string }`; `NewScore(s,reason,terms)` clamps `s` to `[0,1]`; `EmptyScore(reason)` = `{0,reason,nil}`.
- `Scorer` interface: `Score(item, context string) Score`; `ScoreBatch(items []string, context string) []Score`; `IsAvailable() bool`. `DefaultBatchScore(s, items, context)` = per-item map.
- **BM25**: `k1=1.5`, `b=0.75`, `normalize=true`, `maxScore=10.0`. Tokenizer regex (lowercase text first, then `FindAllString`): `TOKEN_PATTERN = [0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|\b\d{4,}\b|[a-zA-Z0-9_]+` (UUID | 4+digit id | word; **order matters**; use Go `regexp` default Perl mode, NOT POSIX).
  - `idf = math.Log(2)` (fixed constant, NOT ln(N/df)).
  - `bm25Score(docTokens, queryFreq, avgDocLen) -> (float64, []string)`: empty doc or empty query → `(0,nil)`. `docFreq` map; `docLen=len(docTokens)`; `avgdl = avgDocLen if >0 else docLen if >0 else 1`. Iterate query terms **sorted**; for each present term `f`: `num=f*(k1+1)`, `den=f+k1*(1-b+b*docLen/avgdl)`, `score += idf*num/den * qf`; append term to matched.
  - finalize: `normalized = min(raw/maxScore, 1)` if normalize; then if any matched term has **byte-len ≥ 8**: `normalized = min(normalized+0.3, 1)`.
  - `Score(item, context)`: tokenize both; `queryFreq` over context; `bm25Score(itemTokens, queryFreq, 0.0)`; finalize. Reason: 0→`"BM25: no term matches"`; 1→`"BM25: matched '<t>'"`; n>1→`"BM25: matched <n> terms (<first3 joined ", "><"..." if n>3 else "">)"`. MatchedTerms capped to first 10.
  - `ScoreBatch`: empty context → per-item `EmptyScore("BM25: empty context")`. Else pre-tokenize all, `avgLen = totalTokens/max(1,len(items))`; per item `bm25Score(tokens, queryFreq, avgLen)` + finalize; reason 0→`"BM25: no matches"` else `"BM25: <n> terms"`; MatchedTerms capped to 5.
- **Hybrid**: `baseAlpha=0.5`, `adaptive=true`, holds `bm25` + a stub `EmbeddingScorer` (`IsAvailable()=false`), caches `embeddingAvailable=false`. `IsAvailable()` returns `true` unconditionally. v0 path always `boostBM25Only`:
  - `boostBM25Only(r)`: `b=r.Score`; if `len(r.MatchedTerms)>0`: `b=max(b,0.3)`; if `len>=2`: `b=min(b+0.2,1)`. Reason `"Hybrid (BM25 only, boosted): " + r.Reason`; terms = r.MatchedTerms.
  - `computeAlpha(context)` (for the future fused path; implement now, unreachable in v0): if `!adaptive` return `baseAlpha` (unclamped). Counts: UUID + numeric-id on **original** context; hostname + email on **lowercased** context (patterns below). Ladder (one fires): uuid>0→`max(α,0.85)`; elif idCount≥2→`0.75`; elif idCount==1→`0.65`; elif host>0||email>0→`0.6`. Then `clamp(α,0.3,0.9)`. Patterns: `UUID_PATTERN` (= UUID alt above), `NUMERIC_ID_PATTERN = \b\d{4,}\b`, `HOSTNAME_PATTERN = \b[a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9]*(?:\.[a-zA-Z]{2,})?\b`, `EMAIL_PATTERN = \b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b` (**keep the literal `|` in `[A-Z|a-z]` verbatim** — upstream quirk).
- `CreateScorer(tier)`: lowercase tier; `"bm25"`→BM25 default; `"hybrid"`→Hybrid default; `"embedding"`→`error("EmbeddingScorer requires the ONNX backend (not yet implemented in Rust)")`; else `error("Unknown scorer tier: <tier>. Valid tiers: bm25, embedding, hybrid")`.

- [ ] **Step 1: Failing BM25 test** — `internal/relevance/bm25_test.go`:
```go
package relevance

import "testing"

func TestBM25MatchesAndClamps(t *testing.T) {
	s := NewBM25Scorer()
	r := s.Score("the quick brown fox", "quick fox")
	if r.Score <= 0 || r.Score > 1 {
		t.Fatalf("score out of (0,1]: %v", r.Score)
	}
	if len(r.MatchedTerms) != 2 { // "fox","quick" (sorted)
		t.Fatalf("matched = %v, want fox+quick", r.MatchedTerms)
	}
	if r.MatchedTerms[0] != "fox" || r.MatchedTerms[1] != "quick" {
		t.Fatalf("matched not sorted: %v", r.MatchedTerms)
	}
}

func TestBM25NoMatch(t *testing.T) {
	s := NewBM25Scorer()
	r := s.Score("alpha beta", "gamma")
	if r.Score != 0 || r.Reason != "BM25: no term matches" {
		t.Fatalf("got %+v", r)
	}
}

func TestBM25LongTokenBonusUsesByteLen(t *testing.T) {
	s := NewBM25Scorer()
	// a matched token of byte-len >= 8 triggers +0.3
	r := s.Score("authentication subsystem", "authentication")
	if r.Score < 0.3 {
		t.Fatalf("expected long-token bonus, got %v", r.Score)
	}
}
```

- [ ] **Step 2–5:** Run→fail; implement `base.go`, `bm25.go`, `hybrid.go`; run→pass. Hybrid test asserts the fallback path:
```go
func TestHybridV0AlwaysBoostsBM25(t *testing.T) {
	h := NewHybridScorer()
	if !h.IsAvailable() { t.Fatal("hybrid is always available") }
	r := h.Score("the quick brown fox", "quick fox")
	// two matched terms -> floor 0.3 then +0.2
	if r.Score < 0.5 { t.Fatalf("expected boosted >=0.5, got %v", r.Score) }
	if r.Reason[:6] != "Hybrid" { t.Fatalf("reason = %q", r.Reason) }
}
```

- [ ] **Step 6: Commit**
```bash
git add internal/relevance/
git commit -m "feat(relevance): BM25 scorer + Hybrid BM25-only fallback + scorer factory"
```

---

## Task 4: `internal/adaptive` (simplified `ComputeOptimalK`)

**Behavior reference:** research §"adaptive_sizer (compute_optimal_k)". **Simplified MVP** per spec §6: keep the fast path + diversity-scaled clamp; **drop SimHash/Kneedle/zlib** (follow-ups). Consumed by log/search compressors.

**Files:**
- Create: `internal/adaptive/adaptive.go`
- Test: `internal/adaptive/adaptive_test.go`

**Simplified algorithm (`ComputeOptimalK(items []string, bias float64, minK int, maxK int) int`):**
- `n = len(items)`; `effMax = maxK if maxK > 0 else n`.
- **Fast path:** `if n <= 8 { return n }` (return raw `n`, NOT clamped — faithful).
- **Diversity** (cheap, replaces SimHash clustering): `unique = number of distinct strings in items`; `div = float64(unique)/float64(n)`.
- **Near-total redundancy:** `if unique <= 3 { k := max(minK, unique); return min(k, effMax) }`.
- **Keep fraction** (upstream None-knee branch, simplified — no Kneedle): `keepFraction = 0.3 + 0.7*div`; `knee = max(minK, int(float64(n)*keepFraction))`.
- **Bias:** treat `bias` as a keep-multiplier where `bias <= 0` means neutral: `k := knee; if bias > 0 { k = max(minK, int(float64(knee)*bias)) }`. (Upstream multiplies knee by bias; callers pass `bias=0.0` by default → neutral here. Documented divergence: we treat ≤0 as neutral so default behavior keeps the diversity-scaled fraction rather than collapsing to `minK`.)
- **Final clamp:** `return max(minK, min(k, effMax))`.

> Document at the top of the file: this is the simplified sizer (spec §6). Dropped vs upstream: SimHash fingerprinting + Hamming clustering, bigram-coverage Kneedle knee detection, zlib ratio validation/boost. Diversity is approximated by exact-string uniqueness instead of SimHash clusters. Tracked as a follow-up (full `compute_optimal_k`).

- [ ] **Step 1: Failing test** — `internal/adaptive/adaptive_test.go`:
```go
package adaptive

import "testing"

func mk(n int) []string {
	out := make([]string, n)
	for i := range out { out[i] = string(rune('a' + i%26)) + "-line" }
	return out
}

func TestFastPathKeepsAll(t *testing.T) {
	if got := ComputeOptimalK(mk(8), 0, 5, 30); got != 8 {
		t.Fatalf("n<=8 must return n unclamped, got %d", got)
	}
}

func TestNearTotalRedundancy(t *testing.T) {
	items := []string{"a", "a", "a", "a", "a", "a", "a", "a", "a", "a"} // unique=1
	if got := ComputeOptimalK(items, 0, 5, 30); got != 5 { // max(minK,1)=5, min(5,30)=5
		t.Fatalf("redundant -> minK, got %d", got)
	}
}

func TestClampToMax(t *testing.T) {
	got := ComputeOptimalK(mk(100), 0, 5, 30)
	if got < 5 || got > 30 {
		t.Fatalf("k out of [5,30]: %d", got)
	}
}
```

- [ ] **Step 2–5:** Run→fail; implement; run→pass.

- [ ] **Step 6: Commit**
```bash
git add internal/adaptive/
git commit -m "feat(adaptive): simplified compute_optimal_k (fast path + diversity-scaled clamp)"
```

---

## Task 5: `internal/tagprotect` (protect/restore custom-tag regions)

**Behavior reference:** research §"tag_protector". Pure, no Plan-2 deps. Standalone (used by v0.2 pipeline; built+tested now per spec §10). **Deterministic salt scan (no crypto/rand)** — satisfies I4.

**Files:**
- Create: `internal/tagprotect/tagprotect.go`, `internal/tagprotect/html_tags.go` (the HTML5 allowlist)
- Test: `internal/tagprotect/tagprotect_test.go`

**API:**
```go
type ProtectStats struct { TagsSeen, HTMLTagsSkipped, CustomBlocksProtected, SelfClosingProtected, OrphanCloses int; PlaceholderCollisionAvoided bool }
func ProtectTags(text string, compressTaggedContent bool) (protected string, blocks [][2]string, stats ProtectStats)
func RestoreTags(text string, blocks [][2]string) string
func IsKnownHTMLTag(name string) bool
```

**Algorithm (exact):**
- Early return: `if text == "" || !strings.Contains(text, "<") { return text, nil, ProtectStats{} }`.
- Prefix/collision: `DEFAULT_PREFIX = "{{HEADROOM_TAG_"`, `SUFFIX = "}}"`. If text contains `DEFAULT_PREFIX`: for salt 0..15 try `candidate = "{{HEADROOM_TAG_" + salt + "_"`; first candidate not in text wins; set `PlaceholderCollisionAvoided=true`. If all 16 collide, use `FALLBACK_PREFIX = "{{HEADROOM_TAG_FALLBACK_a4f1c7e2_"` (still `=true`). Else use `DEFAULT_PREFIX`.
- Single-pass byte walk collecting non-overlapping `spans{start,end,kind}` (kinds: `Block, SelfClosing, OpenMarker, CloseMarker`). Tag lexer `parseTagAt(bytes, i)`:
  - `isNameStart(b) = isASCIILetter(b) || b=='_'`; `isNameCont(b) = isASCIIAlnum(b) || b in {'_','-','.',':'}`.
  - `<!-- -->`, `<!DOCTYPE>`, `<?xml?>`, CDATA → NotTag (since `!`,`?` aren't name-start) → emit verbatim, advance 1.
  - Open tags lex attributes honoring `"`/`'` quotes (`>` inside a quote doesn't close; EOF in quote → NotTag); detect `/>` → self-closing.
  - Close `</name>`: skip whitespace, require `>`.
  - `nameLower = lowercase(bytes[nameStart:nameEnd])`; `TagsSeen++`. If `IsKnownHTMLTag(nameLower)` → HTML, emit verbatim, `HTMLTagsSkipped++`, no span.
  - Custom self-closing → `Span{kind:SelfClosing}` immediately, `SelfClosingProtected++` (both modes).
  - Custom open: marker-mode (`compressTaggedContent==true`) → push `Span{kind:OpenMarker}` now + push open onto stack; block-mode → push open onto stack only.
  - Custom close: search stack from TOP for matching `nameLower`. No match → orphan: emit verbatim, `OrphanCloses++`. Match at k: block-mode → truncate stack to k & pop, remove any spans with `start >= matched.openStart`, emit one `Span{kind:Block, start:matched.openStart, end:close.tagEnd}`, `CustomBlocksProtected++`; marker-mode → truncate & pop, emit `Span{kind:CloseMarker}`.
  - EOF: unmatched opens → block-mode content NOT protected; marker-mode OpenMarker spans kept.
- Emit via **offset slicing** (NOT `strings.Replace`): spans are already sorted ascending & non-overlapping. For `(counter, span)`: `out += text[cursor:span.start]`; `ph = prefix + strconv.Itoa(counter) + "}}"`; append `(ph, text[span.start:span.end])` to blocks; `out += ph`; `cursor = span.end`. After: `out += text[cursor:]`.
- `RestoreTags`: forward-iterate blocks; for each `(ph, orig)`: if `strings.Contains(working, ph)` → `working = strings.ReplaceAll(working, ph, orig)`; else log via `slog.Error` and skip (do NOT inject). Return working.
- `IsKnownHTMLTag(name)`: lowercase, membership in the HTML5 allowlist set (`html_tags.go`, ~120 names — copy the exact list from the research file's `html5_allowlist` pattern).

- [ ] **Step 1: Failing test** — `internal/tagprotect/tagprotect_test.go`:
```go
package tagprotect

import "testing"

func TestProtectRestoreRoundTripCustomBlock(t *testing.T) {
	in := `pre <thinking>secret plan</thinking> post`
	prot, blocks, stats := ProtectTags(in, false)
	if stats.CustomBlocksProtected != 1 {
		t.Fatalf("blocks protected = %d", stats.CustomBlocksProtected)
	}
	if prot == in {
		t.Fatal("custom tag should have been replaced by a placeholder")
	}
	if got := RestoreTags(prot, blocks); got != in {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, in)
	}
}

func TestHTMLTagsNotProtected(t *testing.T) {
	in := `<div>hello <span>world</span></div>`
	prot, _, stats := ProtectTags(in, false)
	if prot != in || stats.HTMLTagsSkipped == 0 {
		t.Fatalf("HTML5 tags must be emitted verbatim; got %q skipped=%d", prot, stats.HTMLTagsSkipped)
	}
}

func TestCommentsAndDoctypeVerbatim(t *testing.T) {
	in := "<!-- note --><!DOCTYPE html><?xml v?>"
	prot, blocks, _ := ProtectTags(in, false)
	if prot != in || len(blocks) != 0 {
		t.Fatalf("non-tags must pass through, got %q blocks=%v", prot, blocks)
	}
}

func TestCollisionAvoidance(t *testing.T) {
	in := `{{HEADROOM_TAG_0}} <custom>x</custom>`
	_, blocks, stats := ProtectTags(in, false)
	if !stats.PlaceholderCollisionAvoided || len(blocks) != 1 {
		t.Fatalf("expected salted prefix; stats=%+v blocks=%v", stats, blocks)
	}
}
```

- [ ] **Step 2–5:** Run→fail; implement; run→pass. Verify the HTML5 allowlist against the research `html5_allowlist` (note modern entries: `search, portal, hgroup, slot, template, math, svg`).

- [ ] **Step 6: Commit**
```bash
git add internal/tagprotect/
git commit -m "feat(tagprotect): offset-slicing custom-tag protect/restore with HTML5 allowlist + deterministic salt"
```

---

## Task 6: `internal/toolpairs` (tool_use/tool_result atomicity helper)

**Behavior reference:** research §"toolpairs (from live_zone)". Pure, no Plan-2 deps. **Core finding: atomicity is structural, not key-based — there is no id matcher upstream.** The deliverable is the hot-zone exclusion list + the inner-content extraction contract for the future (v0.2) live-zone dispatcher.

**Files:**
- Create: `internal/toolpairs/toolpairs.go`
- Test: `internal/toolpairs/toolpairs_test.go`

**API:**
```go
// HotZoneBlockTypes are block types that must never be rewritten (mutating them
// would split a tool pair or bust the prompt cache). Exact, ordered, no prefix match.
func HotZoneBlockTypes() []string // {"tool_use","thinking","redacted_thinking","compaction"}
func IsHotZoneBlockType(blockType string) bool // exact equality against the list

// InnerContentField returns the name of the single compressible inner field for a
// block type, and whether one exists: "tool_result"->("content",true);
// "text"->("text",true); anything else -> ("",false). Encodes the
// compress-inner-content-only contract so the dispatcher never rewrites the envelope/id.
func InnerContentField(blockType string) (field string, ok bool)
```

> Do NOT implement a `tool_use_id` matcher — that is not how upstream guarantees atomicity, and adding one would diverge. Atomicity is preserved because (a) `tool_use` is hot-zone-excluded (never rewritten) and (b) a `tool_result` is compressed only within its inner `content` byte range, leaving `type`/`tool_use_id`/siblings intact. This helper encodes exactly those two facts. (An optional `ExtractToolUseID` validator may be added in v0.2 and clearly marked not-present-upstream — out of scope here.)

- [ ] **Step 1: Failing test** — `internal/toolpairs/toolpairs_test.go`:
```go
package toolpairs

import "testing"

func TestHotZoneExactList(t *testing.T) {
	want := []string{"tool_use", "thinking", "redacted_thinking", "compaction"}
	got := HotZoneBlockTypes()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsHotZoneExactMatchOnly(t *testing.T) {
	if !IsHotZoneBlockType("tool_use") {
		t.Error("tool_use must be hot-zone")
	}
	if IsHotZoneBlockType("tool_used") || IsHotZoneBlockType("Tool_Use") {
		t.Error("must be exact-equality (no prefix/case folding)")
	}
}

func TestInnerContentField(t *testing.T) {
	if f, ok := InnerContentField("tool_result"); !ok || f != "content" {
		t.Errorf("tool_result -> %q,%v", f, ok)
	}
	if f, ok := InnerContentField("text"); !ok || f != "text" {
		t.Errorf("text -> %q,%v", f, ok)
	}
	if _, ok := InnerContentField("image"); ok {
		t.Error("other types have no compressible inner field")
	}
}
```

- [ ] **Step 2–5:** Run→fail; implement; run→pass.

- [ ] **Step 6: Commit**
```bash
git add internal/toolpairs/
git commit -m "feat(toolpairs): hot-zone block-type exclusion + inner-content contract (structural atomicity)"
```

---

## Task 7: `internal/reformats/json_minifier.go` (lossless `ReformatTransform`)

**Behavior reference:** research §"reformats/json_minifier". `AppliesTo = [JsonArray]` (umbrella for arrays AND objects).

**Files:**
- Create: `internal/reformats/json_minifier.go`
- Test: `internal/reformats/json_minifier_test.go`

**Algorithm:**
- `Name() = "json_minifier"`; `AppliesTo() = []transform.ContentType{transform.JsonArray}`.
- `Apply(content string) (transform.ReformatOutput, error)`:
  1. `trimmed = strings.TrimSpace(content)`; if `trimmed == ""` → `return ReformatOutput{}, fmt.Errorf("json_minifier skipped: empty input: %w", transform.ErrSkipped)`.
  2. Decode the **trimmed** string: `dec := json.NewDecoder(strings.NewReader(trimmed)); dec.UseNumber()`; decode into `any`. On error → `%w` of `transform.ErrInvalidInput`.
  3. Re-emit compact with HTML escaping OFF: `var buf bytes.Buffer; enc := json.NewEncoder(&buf); enc.SetEscapeHTML(false); enc.Encode(v)`; `min := strings.TrimRight(buf.String(), "\n")` (Encoder appends a newline). On error → `%w` of `transform.ErrInternal`.
  4. **Never-inflate guard vs RAW length:** `if len(min) >= len(content) { return ReformatOutput{Output: content, BytesSaved: 0}, nil }` (return RAW original, byte-len compared to RAW `content`, not trimmed).
  5. Else `return ReformatOutput{Output: min, BytesSaved: len(content) - len(min)}, nil`.

> Faithfulness notes: `UseNumber()` preserves numeric literal text (safer than upstream serde, no precision drift). `SetEscapeHTML(false)` matches serde (no `<`). Go `json.Marshal`/Encoder sorts object keys alphabetically — matches upstream's default serde (BTreeMap). Compare against RAW `content` length and fall back to RAW `content` on inflate.

- [ ] **Step 1: Failing test** — `internal/reformats/json_minifier_test.go`:
```go
package reformats

import (
	"errors"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestJSONMinifierStripsWhitespace(t *testing.T) {
	var m JsonMinifier
	in := "[ {\n  \"a\": 1,\n  \"b\": 2\n} ]"
	out, err := m.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != `[{"a":1,"b":2}]` {
		t.Fatalf("got %q", out.Output)
	}
	if out.BytesSaved != len(in)-len(out.Output) {
		t.Fatalf("bytes_saved = %d", out.BytesSaved)
	}
}

func TestJSONMinifierNeverInflates(t *testing.T) {
	var m JsonMinifier
	for _, in := range []string{`{}`, `[]`, `null`, `42`, `{"a":1,"b":2}`} {
		out, err := m.Apply(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Output) > len(in) {
			t.Fatalf("inflated %q -> %q", in, out.Output)
		}
	}
}

func TestJSONMinifierEmptySkipped(t *testing.T) {
	var m JsonMinifier
	if _, err := m.Apply("   \n\t "); !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("want ErrSkipped, got %v", err)
	}
}

func TestJSONMinifierInvalid(t *testing.T) {
	var m JsonMinifier
	if _, err := m.Apply(`{not valid`); !errors.Is(err, transform.ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestJSONMinifierHTMLNotEscaped(t *testing.T) {
	var m JsonMinifier
	out, err := m.Apply(`["<a> & <b>"]`)
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != `["<a> & <b>"]` {
		t.Fatalf("HTML must not be escaped: %q", out.Output)
	}
}

var _ transform.ReformatTransform = JsonMinifier{}
```

- [ ] **Step 2–5:** Run→fail; implement; run→pass.

- [ ] **Step 6: Commit**
```bash
git add internal/reformats/json_minifier.go internal/reformats/json_minifier_test.go
git commit -m "feat(reformats): lossless JSON minifier (UseNumber, HTML-escape off, never-inflate)"
```

---

## Task 8: `internal/reformats/log_template.go` (lossless log-templating `ReformatTransform`)

**Behavior reference:** research §"reformats/log_template". `AppliesTo = [BuildOutput]`. **No regex masking** — variability is discovered positionally (a token position becomes `<*>` only because it differed across a run). Lossless: original line = template with wildcards replaced by the row's variant tokens.

**Files:**
- Create: `internal/reformats/log_template.go`
- Test: `internal/reformats/log_template_test.go`

**Config + defaults:** `min_lines=20`, `min_run=3`, `similarity_threshold=0.4` (float32), `min_constant_tokens=2`.

**Algorithm (exact):**
- `Name()="log_template"`; `AppliesTo()=[BuildOutput]`.
- Guard: empty content → `ErrSkipped("empty input")`.
- `lines = splitLinesRust(content)` (split `\n`, strip a trailing `\r` per line, drop the final empty element if content ends with `\n`). Track `endsWithNewline = strings.HasSuffix(content, "\n")`.
- Guard: `if len(lines) < 20` → `ErrSkipped("input below min_lines")` (line count, not bytes).
- `tokenize(line) = strings.Fields(line)` (== Rust `split_whitespace`: Unicode-ws split, no empty tokens). Blank/ws-only line → empty token slice.
- A `run` = `{indices []int; template []slot}` where `slot` is `{tok string; wild bool}`.
- Main loop over `(i, toks)`:
  - empty toks → flush active run, emit `lines[i] + "\n"` verbatim, continue (blank lines always break runs).
  - active run AND `extends(run, toks)` → append `i`, `mergeTemplate(run, toks)`.
  - else → flush active run, start fresh `run` (template = each token as `{tok, wild:false}`).
- After loop: flush active run.
- Trailing-newline fixup: if `!endsWithNewline && strings.HasSuffix(out, "\n")` → drop the final `\n`.
- Never-inflate: `if len(out) >= len(content) { return ReformatOutput{Output: content, BytesSaved: 0}, nil }`; else `BytesSaved = len(content)-len(out)`.
- `extends(run, toks)`: `if len(toks) != len(run.template) return false`; `matches=0`; for each pos: if `template[pos].wild` → `matches++`; elif `template[pos].tok == toks[pos]` → `matches++`. Return `float32(matches)/float32(len(toks)) >= 0.4`.
- `mergeTemplate(run, toks)`: for each pos: if `!wild && template[pos].tok != toks[pos]` → set `wild=true`.
- `flush(run)`: `constant = count(!wild)`, `varying = len(template)-constant`. **Collapse iff** `len(run.indices) >= 3 && constant >= 2 && varying > 0`.
  - Not collapse → emit each `lines[i] + "\n"`.
  - Collapse → header: `"[Template T" + id + ": " + <space-joined slots, slot=tok or "<*>"> + "] (" + len(indices) + " occurrences)\n"`; then for each index, a variant row = the wildcard-position tokens (ascending pos, space-joined) + `"\n"`. `id` starts at 1, increments **only on collapse**.

- [ ] **Step 1: Failing test** — `internal/reformats/log_template_test.go`:
```go
package reformats

import (
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func TestLogTemplateCollapsesRun(t *testing.T) {
	var lt LogTemplate
	var b strings.Builder
	for i := 0; i < 25; i++ {
		b.WriteString("INFO worker processing job ")
		b.WriteString(strings.Repeat("x", 1)) // varying token
		b.WriteByte('\n')
	}
	out, err := lt.Apply(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "[Template T1:") {
		t.Fatalf("expected a template header, got:\n%s", out.Output)
	}
	if out.BytesSaved <= 0 {
		t.Fatalf("expected savings, got %d", out.BytesSaved)
	}
}

func TestLogTemplateBelowMinLinesSkipped(t *testing.T) {
	var lt LogTemplate
	if _, err := lt.Apply("a\nb\nc\n"); !errors.Is(err, transform.ErrSkipped) {
		t.Fatalf("want skip, got %v", err)
	}
}

func TestLogTemplateLosslessReconstruct(t *testing.T) {
	// Build distinct-varying lines, collapse, then assert reconstruction matches.
	// (Implementer: write a helper that splices variant rows back into the header
	//  template at wildcard positions and compares to the original lines.)
}

var _ transform.ReformatTransform = LogTemplate{}
```
> The implementer must flesh out `TestLogTemplateLosslessReconstruct` into a real round-trip assertion (parse the header's slots, replace each `<*>` with successive variant-row tokens, compare to original input lines). Losslessness is the core contract — do not skip this test.

- [ ] **Step 2–5:** Run→fail; implement (incl. the shared `splitLinesRust` helper — put it in a small `internal/reformats/lines.go` or inline); run→pass.

- [ ] **Step 6: Commit**
```bash
git add internal/reformats/log_template.go internal/reformats/log_template_test.go
git commit -m "feat(reformats): lossless positional log-template run collapsing"
```

---

## Task 9: `internal/compress/log_compressor.go` (6-stage log engine)

**Behavior reference:** research §"log_compressor (6-stage)". Standalone engine (NOT a `Transform`). Deps: `signals` (level/indicator via its own keyword sets — but log uses its OWN level keyword sets, see below), `adaptive`, `ccr` (MD5). **No "repeated N times" literal** — dedup is silent.

**Files:**
- Create: `internal/compress/log_compressor.go`, `internal/compress/lines.go` (shared `splitLinesRust` if not already shared)
- Test: `internal/compress/log_compressor_test.go`

**Types:**
```go
type LogResult struct {
	Compressed          string
	OriginalLineCount   int
	CompressedLineCount int
	Ratio               float32 // compressedLineCount/originalLineCount
	CacheKey            string  // "" if no CCR marker emitted
}
type LogCompressor struct { /* config + precompiled regexes */ }
func NewLogCompressor() *LogCompressor
func (c *LogCompressor) Compress(content string, store ccr.Store) LogResult
```

**Config defaults:** `maxErrors=10, errorContextLines=3, keepFirstError=true, keepLastError=true, maxStackTraces=3, stackTraceMaxLines=20, maxWarnings=5, dedupeWarnings=true, keepSummaryLines=true, maxTotalLines=100, enableCCR=true, minLinesForCCR=50, minCompressionRatioForCCR=0.5`.

**Normalization regexes (for warning dedup key — applied in this strict order):** `\d+`→`N`, then `0x[0-9a-fA-F]+`→`ADDR`, then `/[\w/]+/`→`/PATH/`.

**Stages (condensed; full detail in research):**
1. Split lines. `originalLineCount = len(lines)`.
2. Format detect: scan first 100 lines, count substring markers per format (Pytest/Npm/Cargo/Jest/Make markers — see research `fmt.*_markers`), highest count wins; tie/none → Generic. (Format influences stack/summary heuristics; for the MVP it may be kept minimal but the field must exist.)
3. Classify each line into `LogLine{content, level, isStackTrace, isSummary}`: level by **word-boundary** keyword match in precedence Error>Fail>Warn>Info>Debug>Trace (log's own sets — see research `level.*_keywords`); `isStackTrace` flavor-aware (python/js/java/rust/go — see research); `isSummary` (`===`/`---` prefixes, `<digits> passed/failed/...`, `Test/Tests/Suite`, `TOTAL/Summary`, `Build/Compile/Test ... succeeded/failed/complete`).
4. Score: `level_score{Error|Fail=1.0, Warn=0.5, Info|Unknown=0.1, Debug=0.05, Trace=0.02} + (isStackTrace?0.3:0) + (isSummary?0.4:0)`, capped at 1.0.
5. Adaptive budget: `cap = min(adaptive.ComputeOptimalK(lineStrings, 0.0, /*minK*/?, maxTotalLines), maxTotalLines)`. (Use `minK` small, e.g. 5; clamp by `maxTotalLines=100`.)
6. Category selection + context windows + final cap: errors (≤10, keep first+last+top, ±3 context), fails similar, warnings (≤5, deduped via normalized-key set), stack traces (≤3 blocks, each ≤20 lines), summaries (kept). Collect selected indices; if over cap, sort selected by score desc and truncate; **emit in original line order**.
7. Format output: join selected (source order) with `\n`; if lines omitted, append a footer `[<N> lines omitted: <comma-joined nonzero level counts>]` (wording verify-against-fixtures; keep simple).
8. CCR: `ratio = compressedLineCount/originalLineCount`. If `enableCCR && originalLineCount >= 50 && ratio < 0.5`: `key = ccr.ComputeKeyMD5([]byte(content))`; `store.Put(key, content)`; append `"\n[" + originalLineCount + " lines compressed to " + len(selected) + ". Retrieve more: hash=" + key + "]"`; set `CacheKey=key`. Else `CacheKey=""`.

> The dedup key: split content at the FIRST `:` or `=` into prefix+suffix; prefix verbatim; suffix normalized by the 3 regexes in order; the prefix+normalized-suffix string is the dedup key in a set. Silent drop of duplicates. **No "repeated N times" output.**

- [ ] **Step 1: Failing test** — `internal/compress/log_compressor_test.go`:
```go
package compress

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
)

func newStore(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 16})
	if err != nil { t.Fatal(err) }
	return s
}

func TestLogCompressorDropsLowValueAndStoresOriginal(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ { b.WriteString("DEBUG noisy heartbeat tick\n") }
	b.WriteString("ERROR database connection refused\n")
	in := b.String()
	st := newStore(t)
	r := NewLogCompressor().Compress(in, st)
	if r.CompressedLineCount >= r.OriginalLineCount {
		t.Fatalf("expected compression, got %d/%d", r.CompressedLineCount, r.OriginalLineCount)
	}
	if !strings.Contains(r.Compressed, "ERROR database connection refused") {
		t.Fatal("must keep the error line")
	}
	if r.CacheKey == "" {
		t.Fatal("expected CCR key (80+ lines, ratio < 0.5)")
	}
	if v, ok := st.Get(r.CacheKey); !ok || v != in {
		t.Fatal("original must be retrievable from the store under CacheKey")
	}
	if !strings.Contains(r.Compressed, "Retrieve more: hash="+r.CacheKey) {
		t.Fatal("inline CCR marker missing")
	}
}

func TestLogCompressorShortInputNoCCR(t *testing.T) {
	r := NewLogCompressor().Compress("INFO a\nINFO b\nERROR c\n", newStore(t))
	if r.CacheKey != "" {
		t.Fatal("inputs < 50 lines never emit a CCR marker")
	}
}

func TestLogCompressorWordBoundaryLevel(t *testing.T) {
	// 'errorless' must NOT classify as Error (word boundary). Behavior is exercised
	// indirectly via selection; implementer adds a focused classify test if helpful.
}
```

- [ ] **Step 2–5:** Run→fail; implement; run→pass. (Use `crypto/md5` via `ccr.ComputeKeyMD5`; `regexp` for the 3 normalization patterns; word-boundary level matching mirroring `signals` boundary rule.)

- [ ] **Step 6: Commit**
```bash
git add internal/compress/log_compressor.go internal/compress/lines.go internal/compress/log_compressor_test.go
git commit -m "feat(compress): 6-stage log compressor (classify/score/select/dedup/CCR marker)"
```

---

## Task 10: `internal/compress/diff_compressor.go` (diff engine + DiffNoise logic helpers)

**Behavior reference:** research §"diff_compressor". Standalone engine. Dep: `ccr` (MD5). **Line counts via `strings.Split(content,"\n")`.**

**Files:**
- Create: `internal/compress/diff_compressor.go`
- Test: `internal/compress/diff_compressor_test.go`

**Types:**
```go
type DiffResult struct {
	Compressed          string
	OriginalLineCount   int
	CompressedLineCount int
	FilesAffected       int
	Additions           int
	Deletions           int
	HunksKept           int
	HunksRemoved        int
	CacheKey            string
}
type DiffCompressor struct { /* config + regexes */ }
func NewDiffCompressor() *DiffCompressor
func (c *DiffCompressor) Compress(content, context string, store ccr.Store) DiffResult
```

**Config defaults:** `maxContextLines=2, maxHunksPerFile=10, maxFiles=20, enableCCR=true, minLinesForCCR=50 (gates WHOLE path), minCompressionRatioForCCR=0.8`.

**Regexes (RE2-compatible — Go `regexp` ports directly):**
- hunk header: `^(?:@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@|@@@ -\d+(?:,\d+)? -\d+(?:,\d+)? \+\d+(?:,\d+)? @@@|@@@@ -\d+(?:,\d+)? -\d+(?:,\d+)? -\d+(?:,\d+)? \+\d+(?:,\d+)? @@@@)(.*)$`
- new-range extract: `\+(\d+)`
- `^diff --git a/(.+) b/(.+)$`, `^diff --combined (.+)$`, `^diff --cc (.+)$`
- old: `^--- (a/(.+)|/dev/null)$`, new: `^\+\+\+ (b/(.+)|/dev/null)$`, binary: `^Binary files .+ differ$`
- priority (case-insensitive `(?i)`): `\b(error|exception|fail(?:ed|ure)?|fatal|critical|crash|panic)\b`, `\b(important|note|todo|fixme|hack|xxx|bug|fix)\b`, `\b(security|auth|password|secret|token)\b`

**Algorithm (condensed; full in research):** split lines; **short-circuit** `if originalLineCount < 50` → return content verbatim (pass-through). Parse into files/hunks (state machine over lines — file headers, mode/rename/binary markers, `---`/`+++`, hunk headers, body lines classified `+`(not `+++`)/`-`(not `---`)/context/other). If zero files → verbatim. Score hunks: `(adds+dels)*0.03` capped 0.3, `+0.2` per context-query word (len>2, substring) cumulative, `+0.3` once if any priority pattern matches. File cap (>20: stable-sort by `adds+dels` desc, keep 20). Per-file hunk selection (`select_hunks`): keep **first + last** always; middle competes on score for `remaining = maxHunksPerFile-2` slots; re-sort selected by new-range line number asc. Context trim (`reduce_context`): keep `±2` around each `+/-`, force-keep `\`-prefixed no-newline lines; recount. Format output (pre-diff lines, per file: header/renames/hardcoded `new file mode 100644`/`deleted file mode 100644`/`Binary files differ`+skip hunks, else old/new + hunks; footer `[<N> files changed, +<A> -<D> lines<, <H> hunks omitted>]`). `compressedLineCount = len(strings.Split(output,"\n"))` **captured BEFORE** appending the CCR marker. CCR gate: `if enableCCR && compressedLineCount < originalLineCount*0.8`: `key=ComputeKeyMD5(content)`; append `"\n[" + originalLineCount + " lines compressed to " + compressedLineCount + ". Retrieve full diff: hash=" + key + "]"`; `store.Put(key, content)`; `CacheKey=key`.

> Edge guards to preserve: pass-through returns content VERBATIM; `compressedLineCount` is the pre-marker count (the emitted string is 1 line longer when a marker is present — intentional); `total_additions/deletions` summed from ORIGINAL hunk counts; `first`+`last` hunks always kept.

- [ ] **Step 1: Failing test** — `internal/compress/diff_compressor_test.go`:
```go
package compress

import (
	"strings"
	"testing"
)

func TestDiffShortInputPassThrough(t *testing.T) {
	in := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	r := NewDiffCompressor().Compress(in, "", newStore(t))
	if r.Compressed != in || r.CacheKey != "" {
		t.Fatal("input < 50 lines must pass through verbatim with no CCR")
	}
}

func TestDiffCompressesManyHunksAndStoresOriginal(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n")
	for i := 0; i < 30; i++ { // 30 hunks -> capped to 10, with header lines pushes well over 50 lines
		b.WriteString("@@ -")
		b.WriteString(strings.Repeat("1", 1))
		b.WriteString(" +1 @@\n-old\n+new\n context\n")
	}
	in := b.String()
	st := newStore(t)
	r := NewDiffCompressor().Compress(in, "", st)
	if r.OriginalLineCount < 50 {
		t.Skip("fixture too small; enlarge")
	}
	if r.HunksRemoved == 0 {
		t.Fatal("expected hunks to be dropped (cap 10/file)")
	}
	if r.CacheKey != "" {
		if v, ok := st.Get(r.CacheKey); !ok || v != in {
			t.Fatal("original must be retrievable under CacheKey")
		}
		if !strings.Contains(r.Compressed, "Retrieve full diff: hash="+r.CacheKey) {
			t.Fatal("inline diff CCR marker missing")
		}
	}
}
```

- [ ] **Step 2–5:** Run→fail; implement; run→pass.

- [ ] **Step 6: Commit**
```bash
git add internal/compress/diff_compressor.go internal/compress/diff_compressor_test.go
git commit -m "feat(compress): diff compressor (parse/score/cap/trim + inline CCR marker)"
```

---

## Task 11: `internal/compress/search_compressor.go` (search engine)

**Behavior reference:** research §"search_compressor". Standalone engine. Deps: `signals` (KeywordDetector for error boosting), `adaptive`, `ccr` (MD5). Byte-scan parser with Windows-drive guard; FNV-1a dedup (deterministic).

**Files:**
- Create: `internal/compress/search_compressor.go`
- Test: `internal/compress/search_compressor_test.go`

**Types:**
```go
type SearchResult struct {
	Compressed          string
	OriginalMatchCount  int
	CompressedMatchCount int
	FilesAffected       int
	CompressionRatio    float64 // BYTE ratio: len(body)/max(1,len(content))
	CacheKey            string
}
type SearchCompressor struct { /* config + detector */ }
func NewSearchCompressor() *SearchCompressor
func (c *SearchCompressor) Compress(content, context string, bias float64, store ccr.Store) SearchResult
```

**Config defaults:** `maxMatchesPerFile=5, alwaysKeepFirst=true, alwaysKeepLast=true, maxTotalMatches=30, maxFiles=15, contextKeywords=[], boostErrors=true, enableCCR=true, minMatchesForCCR=10, minCompressionRatioForCCR=0.8`.

**Parse (`parseMatchLine`, byte-scan, NO regex):** `b=[]byte(line)`; `scanStart = 2` if `len>=3 && asciiAlpha(b[0]) && b[1]==':' && (b[2]=='\\'||b[2]=='/')` (Windows drive) else `0`. Walk `i` from `scanStart`: when `b[i] in {':','-'}`: if `i>0 && b[i-1] in {':','-'}` skip (`i++; continue`); `ds=i+1`; advance `j` over digits; if `j>ds && j<len && b[j] in {':','-'}`: if `i==0` return none (empty path); `file=line[:i]`, `lineNo=parse(b[ds:j])`, `content=line[j+1:]`, return. Else `i++`. Leftmost valid `<sep><digits><sep>` wins; separators are BOTH `:` and `-`.

**Algorithm (condensed):** group parsed matches into a map keyed by file path (emit in **sorted path order**). Skip empty (trimmed) lines without counting. If zero parsed → return content verbatim, ratio 1.0. Score each match: `+0.3` per context word (len>2, lowercased substring), `+bump` from `signals` category when `boostErrors` (`Error 0.5, Warning 0.4, Importance 0.3, Security|Markdown 0.0`, `ImportanceContext::Search`), `+0.4` per config keyword; clamp 1.0. Select: drop files beyond `maxFiles` (by total score desc); `adaptiveTotal = adaptive.ComputeOptimalK(allMatchStrings, bias, 5, maxTotalMatches)`; per file (score-desc order) keep first+last+top-scored up to `remaining = min(maxMatchesPerFile, adaptiveTotal-totalSelected)`, dedup via `seen` set of `(lineNo, fnv64(content))`; re-sort each file's kept matches by line number asc. Format: per file (sorted path), `"<file>:<line>:<content>"` lines; if a file had matches dropped, append `"[... and <n> more matches in <file>]"`. `ratio = len(body)/max(1,len(content))` (BYTE). CCR: if `enableCCR && originalCount >= 10 && ratio < 0.8 && store != nil`: `key=ComputeKeyMD5(content)`; `store.Put`; append `"\n[<originalCount> matches compressed to <compressedCount>. Retrieve more: hash=<key>]"`; `CacheKey=key`.

- [ ] **Step 1: Failing test** — `internal/compress/search_compressor_test.go`:
```go
package compress

import (
	"strings"
	"testing"
)

func TestSearchParseWindowsDriveGuard(t *testing.T) {
	// C:\foo:10:bar must parse path "C:\foo", line 10, content "bar"
	in := "C:\\foo:10:bar\n"
	r := NewSearchCompressor().Compress(in, "", 0, newStore(t))
	if r.OriginalMatchCount != 1 {
		t.Fatalf("expected 1 match (windows-drive guard), got %d", r.OriginalMatchCount)
	}
}

func TestSearchCompressesClusteredMatches(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ { // 40 matches in 1 file -> per-file cap + adaptive
		b.WriteString("src/main.go:")
		b.WriteString(strings.Repeat("1", 1))
		b.WriteString(":func foo() {}\n")
	}
	in := b.String()
	st := newStore(t)
	r := NewSearchCompressor().Compress(in, "", 0, st)
	if r.CompressedMatchCount >= r.OriginalMatchCount {
		t.Fatalf("expected fewer matches kept, got %d/%d", r.CompressedMatchCount, r.OriginalMatchCount)
	}
	if r.CacheKey != "" {
		if v, ok := st.Get(r.CacheKey); !ok || v != in {
			t.Fatal("original must be retrievable")
		}
	}
}

func TestSearchEmptyPassThrough(t *testing.T) {
	r := NewSearchCompressor().Compress("not a search result at all\n", "", 0, newStore(t))
	if r.CompressionRatio != 1.0 {
		t.Fatalf("no parsed matches -> ratio 1.0, got %v", r.CompressionRatio)
	}
}
```

- [ ] **Step 2–5:** Run→fail; implement (`hash/fnv` for dedup); run→pass.

- [ ] **Step 6: Commit**
```bash
git add internal/compress/search_compressor.go internal/compress/search_compressor_test.go
git commit -m "feat(compress): search compressor (byte-scan parse, dedup, adaptive select, CCR marker)"
```

---

## Task 12: `internal/offloads` — log + diff + diff_noise + search + json (the `OffloadTransform`s)

**Behavior reference:** research §"offloads: log + diff + diff_noise" and §"offloads: search + json + mod". These implement `transform.OffloadTransform`. **`SearchOffload` is implemented but NOT registered** by the default builder.

**Files:**
- Create: `internal/offloads/offloads.go` (shared helpers: `splitLinesRust`, `fromLengths`, the `Crusher` seam), `internal/offloads/log_offload.go`, `internal/offloads/diff_offload.go`, `internal/offloads/diff_noise.go`, `internal/offloads/search_offload.go`, `internal/offloads/json_offload.go`
- Test: `internal/offloads/log_offload_test.go`, `internal/offloads/diff_offload_test.go`, `internal/offloads/diff_noise_test.go`, `internal/offloads/json_offload_test.go`, `internal/offloads/search_offload_test.go`

**Shared:**
```go
// fromLengths builds an OffloadOutput with saturating bytes_saved.
func fromLengths(inputLen int, output, cacheKey string) transform.OffloadOutput {
	saved := inputLen - len(output)
	if saved < 0 { saved = 0 }
	return transform.OffloadOutput{Output: output, BytesSaved: saved, CacheKey: cacheKey}
}

// Crusher is the SmartCrusher seam (Plan 3). Plan-2 default is passthrough.
type Crusher interface { Crush(content, query string, bias float64) CrushResult }
type CrushResult struct { Compressed string; WasModified bool }
type passthroughCrusher struct{}
func (passthroughCrusher) Crush(content, _ string, _ float64) CrushResult { return CrushResult{Compressed: content, WasModified: false} }
```

### LogOffload — `Name="log_offload"`, `AppliesTo=[BuildOutput]`, `Confidence=0.85`
- `EstimateBloat(content)`: empty→0. Sample first 100 lines: track unique line set + `lowPriority` count (a line is low-priority if `detector.Score(line, Log).Priority <= 0.4`). `total = lineCount(content)` (full); `if total < 50 || sampled==0 → 0`. `repetition = 1 - unique/sampled`; `dilution = lowPriority/sampled`; `score = repetition*0.5 + dilution*0.5`; clamp `[0,1]`.
- `Apply(content, ctx, store)`: `r := logCompressor.Compress(content, store)`; if `r.CacheKey == ""` → `ErrSkipped` (no fabricated key); else `fromLengths(len(content), r.Compressed, r.CacheKey)`.

### DiffOffload — `Name="diff_offload"`, `AppliesTo=[GitDiff]`, `Confidence=0.85`
- `EstimateBloat`: empty→0. Single pass: track `in_hunk` (`@@`→true, `diff --git`→false, `+++`/`---`→skip); count change(`+`/`-` first byte) vs context(` `) only when `in_hunk`. `if total<50 → 0`; `denom=context+change; if denom==0 → 0`; `ratio=context/denom`; `normal=0.6`; `if ratio<=normal → 0`; `return clamp((ratio-normal)/(1-normal),0,1)`.
- `Apply`: `r := diffCompressor.Compress(content, ctx.Query, store)`; if `r.CacheKey=="" → ErrSkipped`; else `fromLengths(...)`.

### DiffNoise — `Name="diff_noise"`, `AppliesTo=[GitDiff]`, `Confidence=0.9`, self-contained
- Config: `minLines=30`, `dropWhitespaceOnlyHunks=true`, `lockfileSuffixes = ["Cargo.lock","package-lock.json","yarn.lock","pnpm-lock.yaml","poetry.lock","Pipfile.lock","Gemfile.lock","go.sum","composer.lock"]`.
- `EstimateBloat`: empty→0. `total=lineCount; if total<30 → 0`. Parse segments; for each: `bodyBytes = Σ(len(line)+1)`; `droppable = isLockfile(seg.newPath) || (dropWhitespaceOnly && seg.bodyIsWhitespaceOnly())`. `if totalBytes==0 → 0`; `return clamp(droppableBytes/totalBytes,0,1)`.
- `Apply`: `segments = parseSegments(content)`; if empty → `ErrSkipped("no diff sections")`. Emit each segment's header lines verbatim (`+"\n"`); per body: if droppable → append `"[diff_noise: " + reason + " hunks dropped (" + len(bodyLines) + " lines)]\n"` (reason `"lockfile"` precedence over `"whitespace-only"`), `droppedAny=true`; else emit body lines verbatim. Prepend pre-diff prelude. Gate: `if !droppedAny || len(output) >= len(content) → ErrSkipped("no droppable hunks")`. `key=ComputeKeyMD5(content)`; `store.Put`; `output += "\n[diff_noise CCR: hash=" + key + "]"`; return `fromLengths(len(content), output, key)`.
- Helpers: `parseSegments` (segment = header lines until first `@@`, then body lines incl the `@@`; new segment on `diff --git`); `parseNewPath` = substring after last `" b/"`; `isLockfile` = path-segment-aware `ends_with` (boundary byte `/` or `\` or whole name); `bodyIsWhitespaceOnly` = collect `+`(not `+++`)/`-`(not `---`) bodies stripped of ASCII whitespace; require at least one change; return `adds == subs` (order-aware slice equality). `stripWS` removes ASCII whitespace only (` \t\n\r\v\f`).

### SearchOffload — `Name="search_offload"`, `AppliesTo=[SearchResults]`, `Confidence=0.85` (implemented, NOT registered)
- `EstimateBloat`: empty→0. Scan lines; `extractFilePrefix(line)` (byte-scan: optional Windows-drive skip `len>=2 && b[1]==':' && asciiAlpha(b[0]) → start=2`; first `:`/`-` preceded by ≥1 ASCII digit → `line[:i]`). `total = count of file-prefixed lines`, `files = unique prefixes`. `if total < minMatches || files==0 → 0`; `avg=total/files; if avg<=1 → 0`; `return clamp((avg-1)/clusterThreshold,0,1)`. Defaults `minMatches=10, clusterThreshold=10.0`.
- `Apply`: `r := searchCompressor.Compress(content, ctx.Query, bias, store)`; if `r.CacheKey=="" → ErrSkipped`; else `fromLengths(len(content), r.Compressed, r.CacheKey)`.

### JsonOffload — `Name="json_offload"`, `AppliesTo=[JsonArray]`, `Confidence=0.85` (SmartCrusher seam)
- Holds a `Crusher` (default `passthroughCrusher`). Config `minArrayRows=5, saturationRows=50`.
- `EstimateBloat`: empty→0. `if !strings.HasPrefix(strings.TrimLeft(content," \t\r\n"), "[") → 0`. `seps = Count("},{") + Count("}, {") + Count("},\n")`. `if seps < minArrayRows-1 → 0`. `sat = max(saturationRows-1, 1)`; `return clamp(float32(seps)/float32(sat),0,1)`.
- `Apply`: `r := crusher.Crush(content, ctx.Query, 0.0)`; if `!r.WasModified → ErrSkipped("smart crusher returned passthrough")`; if `len(r.Compressed) >= len(content) → ErrSkipped("no savings after crush")`; `key=ComputeKeyMD5(content)`; `store.Put`; `out := r.Compressed + "\n[json_offload CCR: hash=" + key + "]"`; return `fromLengths(len(content), out, key)`.

> With the Plan-2 passthrough crusher, `JsonOffload.Apply` always skips cleanly (WasModified=false). JSON compression in Plan 2 is delivered by `JsonMinifier` (Task 7). Plan 3 swaps in the real SmartCrusher via the `Crusher` seam — no offload changes needed.

- [ ] **Step 1: Failing tests** (one per offload). Example `internal/offloads/diff_noise_test.go`:
```go
package offloads

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func store(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 16})
	if err != nil { t.Fatal(err) }
	return s
}

func TestDiffNoiseDropsLockfileHunk(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n")
	for i := 0; i < 40; i++ { b.WriteString("@@ -1 +1 @@\n+example.com/x v1.0.0 h1:abc\n") }
	in := b.String()
	st := store(t)
	dn := NewDiffNoise()
	if dn.EstimateBloat(in) <= 0 {
		t.Fatal("lockfile diff should look bloated")
	}
	out, err := dn.Apply(in, transform.CompressionContext{}, st)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(out.Output, "[diff_noise: lockfile hunks dropped") {
		t.Fatal("expected lockfile cell marker")
	}
	if v, ok := st.Get(out.CacheKey); !ok || v != in {
		t.Fatal("original retrievable under CacheKey")
	}
}

func TestIsLockfilePathBoundary(t *testing.T) {
	// MyCargo.lock must NOT match; crates/foo/Cargo.lock must.
}

var _ transform.OffloadTransform = (*DiffNoise)(nil)
```
Add analogous tests asserting `var _ transform.OffloadTransform = ...` for `LogOffload`, `DiffOffload`, `SearchOffload`, `JsonOffload`; that `LogOffload`/`DiffOffload` skip (return `ErrSkipped`) when the wrapped compressor emits no key (short input); that `JsonOffload` skips with the passthrough crusher; and `EstimateBloat` returns 0 on `""` for all five.

- [ ] **Step 2–5:** Run→fail; implement all five offloads + shared helpers; run→pass.

- [ ] **Step 6: Commit**
```bash
git add internal/offloads/
git commit -m "feat(offloads): log/diff/diff_noise/search/json OffloadTransforms (search unregistered; json crusher seam)"
```

---

## Task 13: `internal/compress` integration sanity (engine determinism)

**Files:**
- Test: `internal/compress/determinism_test.go`

A small cross-engine determinism guard (I4): each engine, run twice on the same input with fresh stores, yields byte-equal compressed output.

- [ ] **Step 1: Test**
```go
package compress

import "testing"

func TestEnginesDeterministic(t *testing.T) {
	logIn := genLog()    // helper: 80 mixed lines
	diffIn := genDiff()  // helper: >50-line multi-hunk diff
	searchIn := genSearch() // helper: clustered matches
	if a, b := NewLogCompressor().Compress(logIn, newStore(t)).Compressed, NewLogCompressor().Compress(logIn, newStore(t)).Compressed; a != b {
		t.Fatal("log compressor not deterministic")
	}
	if a, b := NewDiffCompressor().Compress(diffIn, "", newStore(t)).Compressed, NewDiffCompressor().Compress(diffIn, "", newStore(t)).Compressed; a != b {
		t.Fatal("diff compressor not deterministic")
	}
	if a, b := NewSearchCompressor().Compress(searchIn, "", 0, newStore(t)).Compressed, NewSearchCompressor().Compress(searchIn, "", 0, newStore(t)).Compressed; a != b {
		t.Fatal("search compressor not deterministic")
	}
}
```

- [ ] **Step 2–4:** Add the `gen*` helpers, run → pass.

- [ ] **Step 5: Commit**
```bash
git add internal/compress/determinism_test.go
git commit -m "test(compress): determinism (I4) guard across log/diff/search engines"
```

---

## Task 14: Pipeline wiring — `router.NewDefault()` + end-to-end compression

**Behavior reference:** Shared Contract "Default registration". This replaces the Plan-1 passthrough at the router level. `pipeline` stays a pure orchestrator (no new imports).

**Files:**
- Create: `internal/router/default.go`
- Test: `internal/router/default_test.go`

**Implementation:**
```go
package router

import (
	"github.com/dobbo-ca/headroom-go/internal/compress"
	"github.com/dobbo-ca/headroom-go/internal/offloads"
	"github.com/dobbo-ca/headroom-go/internal/pipeline"
	"github.com/dobbo-ca/headroom-go/internal/reformats"
)

// NewDefault wires the v0.1 heuristic compressors into a Router: JSON minify +
// log templating reformats, and the diff_noise/diff/json/log offloads. SearchOffload
// is intentionally NOT registered (matches upstream). JsonOffload uses the Plan-2
// passthrough crusher seam (real SmartCrusher arrives in Plan 3).
func NewDefault() *Router {
	p := pipeline.NewBuilder().
		WithReformat(reformats.JsonMinifier{}).
		WithReformat(reformats.NewLogTemplate()).
		WithOffload(offloads.NewDiffNoise()).
		WithOffload(offloads.NewDiffOffload(compress.NewDiffCompressor())).
		WithOffload(offloads.NewJsonOffload()).
		WithOffload(offloads.NewLogOffload(compress.NewLogCompressor())).
		Build()
	return New(p)
}
```
(Adjust constructor names to whatever Tasks 7–12 defined; the registration order and set must match the Shared Contract.)

- [ ] **Step 1: Failing end-to-end test** — `internal/router/default_test.go`:
```go
package router

import (
	"strings"
	"testing"

	"github.com/dobbo-ca/headroom-go/internal/ccr"
	_ "github.com/dobbo-ca/headroom-go/internal/ccr/backends"
	"github.com/dobbo-ca/headroom-go/internal/transform"
)

func st(t *testing.T) ccr.Store {
	t.Helper()
	s, err := ccr.FromConfig(ccr.BackendConfig{Kind: ccr.InMemory, Capacity: 64})
	if err != nil { t.Fatal(err) }
	return s
}

func TestDefaultCompressesJSONArray(t *testing.T) {
	r := NewDefault()
	in := "[ {\n \"a\": 1\n}, {\n \"a\": 2\n} ]"
	res := r.Compress(in, transform.CompressionContext{}, st(t))
	if len(res.Output) >= len(in) {
		t.Fatalf("JSON array should be minified: %q", res.Output)
	}
}

func TestDefaultCompressesLargeDiff(t *testing.T) {
	r := NewDefault()
	var b strings.Builder
	b.WriteString("diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n")
	for i := 0; i < 60; i++ { b.WriteString("@@ -1 +1 @@\n+x v1 h1:y\n") }
	in := b.String()
	res := r.Compress(in, transform.CompressionContext{}, st(t))
	if len(res.Output) >= len(in) || len(res.CacheKeys) == 0 {
		t.Fatalf("lockfile diff should offload; output=%d in=%d keys=%v", len(res.Output), len(in), res.CacheKeys)
	}
}

func TestDefaultPlainTextPassthrough(t *testing.T) {
	r := NewDefault()
	in := "just some prose with no structure at all, several words long here ok"
	if got := r.Compress(in, transform.CompressionContext{}, st(t)).Output; got != in {
		t.Fatalf("plain text must pass through: %q", got)
	}
}

func TestDefaultDeterministic(t *testing.T) {
	r := NewDefault()
	in := "[ {\n \"a\": 1\n}, {\n \"a\": 2\n} ]"
	a := r.Compress(in, transform.CompressionContext{}, st(t)).Output
	b := r.Compress(in, transform.CompressionContext{}, st(t)).Output
	if a != b {
		t.Fatalf("non-deterministic: %q != %q", a, b)
	}
}
```

- [ ] **Step 2–5:** Run→fail; implement `default.go`; run→pass.

- [ ] **Step 6: Full sweep**
```bash
go build ./... && go vet ./... && go test ./...
```
Expected: all packages PASS, no vet warnings.

- [ ] **Step 7: Commit**
```bash
git add internal/router/default.go internal/router/default_test.go
git commit -m "feat(router): NewDefault wires reformats+offloads; router.Compress now compresses each type"
```

---

## Task 15: GOALS update + plan/research docs

**Files:**
- Modify: `GOALS.md`
- (the plan + research docs are already in the worktree under `docs/superpowers/`)

- [ ] **Step 1: Update GOALS**

Add under "Done":
```markdown
- [x] (Plan 2) Heuristic compressors: reformats (json_minifier, log_template),
      compress engines (log 6-stage, diff, search), offloads
      (log/diff/diff_noise/json; search built-unregistered), signals,
      relevance (BM25+Hybrid), adaptive (simplified), tagprotect, toolpairs;
      wired into router.NewDefault. SmartCrusher (JSON crush) is Plan 3.
```

- [ ] **Step 2: Commit**
```bash
git add GOALS.md docs/superpowers/plans/2026-06-09-headroom-go-v0.1-plan2-heuristic-compressors.md docs/superpowers/research/2026-06-09-upstream-heuristic-compressors-behavior.txt
git commit -m "docs: Plan 2 (heuristic compressors) plan + upstream behavior research; mark GOALS"
```

---

## Self-Review (completed during planning)

**1. Spec coverage** (spec §8 v0.1 item 6 + §6 + §10):
- reformats json_minifier/log_template → Tasks 7, 8 ✓
- compress log(6-stage)/diff/search → Tasks 9, 10, 11 ✓
- offloads log/diff/diff_noise/search(unregistered)/json(seam) → Task 12 ✓ (parity row "SearchOffload skip — impl, unregistered" honored)
- tagprotect → Task 5 ✓; toolpairs → Task 6 ✓; adaptive (simplified) → Task 4 ✓
- signals (KeywordDetector+Tiered) → Task 2 ✓; relevance (BM25+Hybrid) → Task 3 ✓
- wiring into pipeline.Builder so router.Compress compresses each ContentType → Task 14 ✓
- SmartCrusher explicitly deferred to Plan 3 (JsonOffload crusher seam) ✓
- I4 determinism guards → Tasks 13, 14 ✓; never-inflate guards in every reformat/engine ✓

**2. Placeholder scan:** Two tests are intentionally left for the implementer to flesh out (`TestLogTemplateLosslessReconstruct`, `TestIsLockfilePathBoundary`) — flagged inline with exactly what they must assert; these are not vague TODOs but precisely-scoped round-trip/boundary checks. All constants, regexes, marker formats, and gating values are concrete (sourced from the research file). No "TBD/handle errors/etc."

**3. Type consistency:** `transform.{ReformatTransform,OffloadTransform,ReformatOutput,OffloadOutput,CompressionContext,ContentType}` and `ccr.{Store,ComputeKeyMD5}` are used identically across tasks. Engine results (`LogResult`/`DiffResult`/`SearchResult`) carry a `CacheKey string` ("" = no CCR) consumed uniformly by their offload wrappers. `offloads.fromLengths(inputLen, output, cacheKey)` and the `Crusher`/`CrushResult` seam are defined once (Task 12) and referenced by Task 14. Registration order in Task 14 matches the Shared Contract and upstream `offloads/mod.rs`.

**Key reconciliations baked in (vs. the original brief):**
- CCR keys for heuristic markers are **MD5[:24]** (`ccr.ComputeKeyMD5`), not BLAKE3; markers are **inline** strings, not `<<ccr:HASH>>`.
- **No pipeline-level `min_compression_ratio_for_ccr`** — it lives inside each engine (log 0.5 / diff 0.8 / search 0.8). The merged Go pipeline is already correct; Task 14 does not touch it.
- log_compressor has **no "repeated N times" literal** (silent normalized dedup). diff_noise `minLines=30` (not 50). tagprotect/toolpairs are **deterministic** (no crypto/rand) → I4-safe.

---

## Dependency note for executor

Independent (parallelizable first wave): Task 1 (ccr MD5), Task 2 (signals), Task 3 (relevance), Task 4 (adaptive), Task 5 (tagprotect), Task 6 (toolpairs), Task 7 (json_minifier), Task 8 (log_template).
Then: Task 9 (log) needs 1,2,4; Task 10 (diff) needs 1; Task 11 (search) needs 1,2,4. Then Task 12 (offloads) needs 9,10,11. Then Task 13 (determinism), Task 14 (wiring) needs 7,8,12. Task 15 (docs) last. Recommended order: 1 → {2,3,4,5,6,7,8 in parallel} → {9,10,11} → 12 → 13 → 14 → 15.
