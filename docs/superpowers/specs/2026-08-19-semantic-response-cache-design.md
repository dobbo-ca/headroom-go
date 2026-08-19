# Semantic response cache — design

**Bead:** `hr-47g.25` (child of `hr-47g`, headroom-go core)
**Status:** approved design, not yet implemented
**Depends on:** the v0.2 gateway/proxy (`livezone`), which does not exist yet

## 1. What this is

An opt-in cache that returns a previously stored provider response when a new
request is *semantically* close to one already seen. Closeness is measured by
embedding both requests with a local model and comparing the vectors.

Everything else in headroom-go shrinks a request. This does not — it removes
the request entirely on a hit. That makes it the highest-value transform and
the only unsafe one. Section 6 covers the safety rules that follow from that.

The idea comes from Lynkr, a Node.js LLM gateway. Its other three features were
evaluated and rejected; see section 8.

## 2. Placement

```
request
  |
  +-- carries tool results? --> yes --> skip cache entirely
  |
  +-- normalize --> embed (local model) --> nearest neighbour in store
  |                                          |
  |          hit, score >= threshold  <------+
  |            |                             |
  |            v                        miss |
  |      stored response                     v
  |      0 provider tokens          existing compression path
  |                                          |
  |                                          v
  |                                      provider
  |                                          |
  |                                 store (vector, response)
  v
```

The cache sits in front of the compression pipeline, not inside it. A hit
short-circuits before any transform runs. A miss costs one embedding call and
then behaves exactly as the gateway does today.

## 3. Why the model is reached over HTTP

The core is `CGO_ENABLED=0` and must stay that way: one static binary,
cross-compilation for every OS and architecture, and `go install` without a C
toolchain. This matches the existing choice of `modernc.org/sqlite`.

Running an embedding model in-process needs cgo — ONNX Runtime or a llama.cpp
binding. Running it in Ollama needs `net/http`. Ollama holds the cgo and the
Metal or CUDA acceleration in its own process; the gateway sends JSON to
`/api/embeddings` on localhost.

Node.js has the same constraint. `node-llama-cpp` and `onnxruntime-node` are
native addons; Lynkr's "local embeddings, no external calls" means a model in
its own process too, not a model compiled into V8.

## 4. Components

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/embed` | Turn text into a `[]float32`. One HTTP POST. | `net/http` |
| `internal/semcache` | Normalize, look up, decide hit or miss, store. | `embed`, `ccr` |
| `ccr` (existing) | Persist vectors and responses. | `modernc.org/sqlite` |

`embed` knows nothing about caching. `semcache` knows nothing about HTTP. The
gateway calls `semcache` and never calls `embed` directly.

### Normalization

Two requests that differ only in whitespace, message ordering metadata, or a
timestamp must embed to the same text. Normalization strips those before
embedding. It does *not* rewrite the request that goes to the provider — the
byte-surgery invariant (I1) is untouched, because on a miss the original bytes
are forwarded unmodified.

### Storage

Vectors go in the CCR SQLite store, which already holds blobs and already ships
in the binary. Lookup is a linear cosine scan over stored vectors.

> `ponytail:` linear scan, O(n) per request. Fine to a few thousand entries.
> Upgrade path: an ANN index, or SQLite `vec0` if a pure-Go build exists by then.

## 5. Configuration

Every value below is config with a documented default. None are constants.
The defaults are starting points chosen to be safe, not measured optima. Tune
`threshold` against real traffic before trusting it.

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Off unless explicitly turned on. |
| `endpoint` | `http://localhost:11434` | Where the embedding model listens. |
| `model` | `nomic-embed-text` | Changing this invalidates every stored vector. |
| `threshold` | `0.97` | Minimum cosine score for a hit. |
| `timeout` | `2s` | Embedding call budget; expiry means miss. |

## 6. Safety rules

These are requirements, not preferences.

- **Never serve a request that carries tool results.** Tool output is specific
  to a moment and a working tree. Two requests can read the same file and
  deserve different answers.
- **A missing model means a miss, never an error.** If Ollama is absent, the
  endpoint refuses, or the call times out, the lookup returns "no hit" and the
  gateway proceeds normally. The user loses cache benefit, never correctness.
- **The model tag is part of the cache key.** A model upgrade changes the
  embedding space. Stored vectors from a different tag must not be compared.
- **Off by default.** The operator opts in.

### The trade being made

A semantic cache returns a stored answer to a *different* question. At a 0.95
similarity score, two requests about two different files can match. No
threshold removes this; a higher threshold only makes it rarer. This is the
first transform in the project that is lossy by design, which is why it is
opt-in and why the tool-result rule above is absolute.

## 7. Interaction with the existing invariants

| Invariant | Effect |
|---|---|
| I1 byte surgery | Unaffected. A miss forwards the original bytes. |
| I4 determinism | Unaffected. Embedding is not on the compression path. |
| Frozen prefix (§2) | Unaffected. Nothing is injected or stripped. |

The cache is additive. If it is disabled, deleted, or fails, the compression
engine behaves exactly as specified today.

## 8. Rejected: the rest of Lynkr

| Feature | Reason |
|---|---|
| TOON JSON encode | Duplicates SmartCrusher and the JSON compressors. |
| Tool-schema stripping | Tool definitions live in the frozen prefix. Per-request stripping gives every request type a different prefix, so the provider's prompt cache misses. The saving on definitions is traded against the discount on the whole prefix. Which wins is a measurement we have not taken. |
| Tier routing | A routing concern, not a compression one. |
| Generative compression | Lynkr does not do this either. A generative model on the compression path breaks I4 and costs more latency than every heuristic transform combined. |

## 9. Testing

- Normalization: two requests differing only in whitespace produce one key.
- Miss path with no model: every lookup misses, response bytes unchanged.
- Hit path: a stored vector above threshold returns the stored response.
- Below threshold: no hit.
- Tool results present: no lookup happens at all.
- Model tag change: old vectors are not matched.
- Build gate: `CGO_ENABLED=0 go build` and a cross-compile both succeed.

## 10. Out of scope

Response streaming from cache, cache warming, eviction beyond the CCR store's
existing TTL, sharing a cache between machines, and embedding anything other
than the request.
